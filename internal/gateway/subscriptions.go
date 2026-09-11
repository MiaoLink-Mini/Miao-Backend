package gateway

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// IDs and template data keys must be copied exactly from the app's WeChat console.
// No fabricated template IDs, field-name inference, or credentials in client responses.
type SubscriptionField struct {
	Source string `json:"source"`
	Value  string `json:"value"`
	Limit  int    `json:"limit"`
}
type SubscriptionTemplateConfig struct {
	ID    string                       `json:"id"`
	Title string                       `json:"title"`
	Kind  string                       `json:"kind"`
	State string                       `json:"miniprogramState"`
	Data  map[string]SubscriptionField `json:"data"`
}

func loadSubscriptions(path string) ([]SubscriptionTemplateConfig, error) {
	if path == "" {
		return nil, nil
	}
	file, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer file.Close()
	raw, e := io.ReadAll(io.LimitReader(file, 32769))
	if e != nil || len(raw) > 32768 {
		return nil, errors.New("subscription config exceeds 32 KiB")
	}
	var result []SubscriptionTemplateConfig
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if e = decoder.Decode(&result); e != nil {
		return nil, e
	}
	if e = decoder.Decode(new(any)); e != io.EOF {
		return nil, errors.New("trailing subscription JSON")
	}
	if len(result) > 5 {
		return nil, errors.New("at most five subscription templates")
	}
	ids, titles := map[string]bool{}, map[string]bool{}
	for _, t := range result {
		if t.ID == "" || len(t.ID) > 128 || t.Title == "" || len([]rune(t.Title)) > 100 || ids[t.ID] || titles[t.Title] {
			return nil, errors.New("subscription template IDs and titles must be unique")
		}
		ids[t.ID] = true
		titles[t.Title] = true
		if t.Kind != "approval" && t.Kind != "question" && t.Kind != "completed" && t.Kind != "failed" {
			return nil, errors.New("unsupported subscription event kind")
		}
		if t.State != "formal" && t.State != "trial" && t.State != "developer" {
			return nil, errors.New("explicit miniprogramState is required")
		}
		if len(t.Data) == 0 || len(t.Data) > 10 {
			return nil, errors.New("template fields must be explicitly configured")
		}
		for key, f := range t.Data {
			if key == "" || len(key) > 64 || f.Limit < 1 || f.Limit > 200 {
				return nil, errors.New("invalid template field key or limit")
			}
			if f.Source != "summary" && f.Source != "session_title" && f.Source != "state" && f.Source != "time" && f.Source != "fixed" {
				return nil, errors.New("unsupported subscription field source")
			}
			if f.Source == "fixed" && f.Value == "" {
				return nil, errors.New("fixed template field requires a value")
			}
		}
	}
	return result, nil
}
func (s *Server) subscriptionConfig() M {
	templates := []any{}
	enabled := s.cfg.AuthMode == "wechat" && len(s.cfg.Subscriptions) > 0
	if enabled {
		for _, t := range s.cfg.Subscriptions {
			templates = append(templates, M{"id": t.ID, "title": t.Title, "kind": t.Kind})
		}
	}
	notice := "Subscription delivery is disabled until real one-time templates and their exact fields are configured by the host."
	if enabled {
		notice = "Permission is requested only after a tap. A client callback is not proof of delivery; WeChat validates the actual subscription. Uncertain sends are never replayed automatically."
	}
	return M{"enabled": enabled, "templates": templates, "notice": notice}
}
func (s *Server) subscriptionConsent(ctx context.Context, r *http.Request, uid string, b M) (M, error) {
	op, fp := r.Header.Get("Idempotency-Key"), requestFingerprint(r, b)
	if prior, e := s.priorOp(ctx, uid, op, fp); e != nil || prior != nil {
		return prior, e
	}
	if !truth(s.subscriptionConfig(), "enabled") {
		return nil, apierr(409, "CAPABILITY_UNSUPPORTED", "Real subscription templates are not configured")
	}
	if str(b, "nonce") != op {
		return nil, invalid("Consent nonce must equal its idempotency key")
	}
	known := map[string]bool{}
	for _, t := range s.cfg.Subscriptions {
		known[t.ID] = true
	}
	e := s.transaction(ctx, uid, func(tx pgx.Tx) error {
		var identity bool
		if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM subscription_identities WHERE user_id=$1)`, uid).Scan(&identity); e != nil {
			return e
		}
		if !identity {
			return apierr(409, "READ_ONLY", "Sign in again with WeChat to register the encrypted delivery identity")
		}
		seen := map[string]bool{}
		for _, v := range array(b, "choices") {
			choice := v.(M)
			key, decision := str(choice, "templateId"), str(choice, "decision")
			if !known[key] || seen[key] {
				return invalid("Unknown or repeated subscription template")
			}
			seen[key] = true
			increment := 0
			if decision == "accept" {
				increment = 1
			}
			_, e := tx.Exec(ctx, `INSERT INTO subscription_consents(user_id,template_id,credits,decision) VALUES($1,$2,$3,$4) ON CONFLICT(user_id,template_id) DO UPDATE SET credits=CASE WHEN $4='accept' THEN LEAST(100,subscription_consents.credits+1) ELSE 0 END,decision=$4,updated_at=now()`, uid, key, increment, decision)
			if e != nil {
				return e
			}
			if decision != "accept" {
				if _, e = tx.Exec(ctx, `UPDATE subscription_outbox SET state='failed',updated_at=now() WHERE user_id=$1 AND template_id=$2 AND state='pending'`, uid, key); e != nil {
					return e
				}
			}
		}
		if e := s.insertOp(ctx, tx, uid, op, "subscribe", "", "", "", 0, fp, nil, s.now()); e != nil {
			return e
		}
		if e := s.finishOp(ctx, tx, uid, op, "confirmed", emptyResult(), nil); e != nil {
			return e
		}
		return s.audit(ctx, tx, uid, op, "subscribe", "confirmed", "", "")
	})
	if e != nil {
		return nil, e
	}
	return s.operation(ctx, s.db, uid, op)
}
func (s *Server) subjectCipher() (cipher.AEAD, error) {
	key := sha256.Sum256(append([]byte("weagent-subscription-identity/v1\x00"), s.cfg.Key...))
	block, e := aes.NewCipher(key[:])
	if e != nil {
		return nil, e
	}
	return cipher.NewGCM(block)
}
func (s *Server) retainSubscriptionIdentity(ctx context.Context, tx pgx.Tx, uid, subject string) error {
	if !truth(s.subscriptionConfig(), "enabled") {
		return nil
	}
	prefix := s.cfg.WechatAppID + ":"
	if !strings.HasPrefix(subject, prefix) || len(subject) <= len(prefix) {
		return errors.New("WeChat identity does not match configured app")
	}
	aead, e := s.subjectCipher()
	if e != nil {
		return e
	}
	nonce := make([]byte, aead.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return e
	}
	encrypted := aead.Seal(nonce, nonce, []byte(strings.TrimPrefix(subject, prefix)), []byte(uid))
	_, e = tx.Exec(ctx, `INSERT INTO subscription_identities(user_id,encrypted_subject) VALUES($1,$2) ON CONFLICT(user_id) DO UPDATE SET encrypted_subject=$2,updated_at=now()`, uid, encrypted)
	return e
}
func (s *Server) queueSubscription(ctx context.Context, tx pgx.Tx, uid, notification, sid, kind, summary string) error {
	if !truth(s.subscriptionConfig(), "enabled") {
		return nil
	}
	for _, t := range s.cfg.Subscriptions {
		if t.Kind != kind {
			continue
		}
		tag, e := tx.Exec(ctx, `UPDATE subscription_consents SET credits=credits-1 WHERE user_id=$1 AND template_id=$2 AND decision='accept' AND credits>0 AND EXISTS(SELECT 1 FROM subscription_identities WHERE user_id=$1)`, uid, t.ID)
		if e != nil {
			return e
		}
		if tag.RowsAffected() != 1 {
			continue
		}
		data := M{}
		var sessionTitle string
		for _, f := range t.Data {
			if f.Source == "session_title" {
				if e := tx.QueryRow(ctx, `SELECT title FROM sessions WHERE id=$1 AND user_id=$2`, sid, uid).Scan(&sessionTitle); e != nil {
					return e
				}
				break
			}
		}
		for key, f := range t.Data {
			value := f.Value
			switch f.Source {
			case "summary":
				value = summary
			case "session_title":
				value = sessionTitle
			case "state":
				value = kind
			case "time":
				value = subscriptionTime(s.now())
			}
			value, _ = truncate(value, f.Limit)
			data[key] = M{"value": value}
		}
		payload := M{"template_id": t.ID, "data": data, "miniprogram_state": t.State, "lang": "zh_CN", "page": "pages/session/index?id=" + url.QueryEscape(sid)}
		if _, e = tx.Exec(ctx, `INSERT INTO subscription_outbox(id,user_id,notification_id,template_id,payload,state) VALUES($1,$2,$3,$4,$5,'pending')`, id("push"), uid, notification, t.ID, payload); e != nil {
			return e
		}
	}
	return nil
}

func subscriptionTime(t time.Time) string {
	return t.In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format("2006-01-02 15:04")
}

// HTTP calls run outside the Gateway commit/WS mutex. Bodies and errors are never
// logged because access tokens and receiver identities are server-only data.
type subscriptionHTTP struct {
	client  *http.Client
	token   string
	expires time.Time
}

func (h *subscriptionHTTP) post(ctx context.Context, target string, body M) (M, error) {
	req, e := http.NewRequestWithContext(ctx, "POST", target, bytes.NewReader(canonical(body)))
	if e != nil {
		return nil, errors.New("invalid subscription request")
	}
	req.Header.Set("Content-Type", "application/json")
	response, e := h.client.Do(req)
	if e != nil {
		return nil, errors.New("subscription transport result unknown")
	}
	defer response.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(response.Body, 65537))
	if e != nil || len(raw) > 65536 || response.StatusCode != 200 {
		return nil, errors.New("subscription response result unknown")
	}
	var result M
	if e = json.Unmarshal(raw, &result); e != nil {
		return nil, errors.New("invalid subscription response")
	}
	return result, nil
}
func (h *subscriptionHTTP) access(ctx context.Context, cfg Config) (string, error) {
	if h.token != "" && time.Now().Before(h.expires) {
		return h.token, nil
	}
	result, e := h.post(ctx, "https://api.weixin.qq.com/cgi-bin/stable_token", M{"grant_type": "client_credential", "appid": cfg.WechatAppID, "secret": cfg.WechatSecret, "force_refresh": false})
	if e != nil {
		return "", e
	}
	token := str(result, "access_token")
	seconds := number(result, "expires_in")
	if token == "" || seconds < 1 || seconds > 7200 {
		return "", errors.New("WeChat access token was not issued")
	}
	h.token = token
	h.expires = time.Now().Add(time.Duration(seconds)*time.Second - time.Minute)
	return token, nil
}
func (s *Server) subscriptionLoop() {
	defer s.wg.Done()
	httpClient := &subscriptionHTTP{client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	// A crash after the HTTP POST began is ambiguous, never a reason to resend.
	_, _ = s.db.Exec(s.ctx, `UPDATE subscription_outbox SET state='unknown',updated_at=now() WHERE state='sending'`)
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(s.ctx, 20*time.Second)
			if e := s.deliverSubscription(ctx, httpClient); e != nil && ctx.Err() == nil {
				s.log.Warn("subscription delivery could not be completed; inspect outbox state")
			}
			cancel()
		}
	}
}
func (s *Server) deliverSubscription(ctx context.Context, h *subscriptionHTTP) error {
	// Obtain the token before reserving an item. A token failure has sent no message.
	var pending bool
	if e := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM subscription_outbox WHERE state='pending')`).Scan(&pending); e != nil || !pending {
		return e
	}
	token, e := h.access(ctx, s.cfg)
	if e != nil {
		return e
	}
	tx, e := s.db.Begin(ctx)
	if e != nil {
		return e
	}
	defer tx.Rollback(ctx)
	var key, uid, template string
	var payload M
	var encrypted []byte
	e = tx.QueryRow(ctx, `SELECT o.id,o.user_id,o.template_id,o.payload,i.encrypted_subject FROM subscription_outbox o JOIN subscription_identities i ON i.user_id=o.user_id JOIN subscription_consents c ON (c.user_id,c.template_id)=(o.user_id,o.template_id) JOIN notifications n ON (n.id,n.user_id)=(o.notification_id,o.user_id) JOIN sessions s ON (s.id,s.user_id)=(n.session_id,n.user_id) JOIN node_access a ON (a.node_id,a.user_id)=(s.node_id,s.user_id) WHERE o.state='pending' AND c.decision='accept' AND a.revoked_at IS NULL AND s.history_state<>'purged' ORDER BY o.created_at LIMIT 1 FOR UPDATE OF o SKIP LOCKED`).Scan(&key, &uid, &template, &payload, &encrypted)
	if e == pgx.ErrNoRows {
		return nil
	}
	if e != nil {
		return e
	}
	aead, e := s.subjectCipher()
	if e != nil {
		return e
	}
	if len(encrypted) < aead.NonceSize() {
		return errors.New("invalid encrypted subscription identity")
	}
	plain, e := aead.Open(nil, encrypted[:aead.NonceSize()], encrypted[aead.NonceSize():], []byte(uid))
	if e != nil {
		return errors.New("subscription identity decryption failed")
	}
	payload["touser"] = string(plain)
	if _, e = tx.Exec(ctx, `UPDATE subscription_outbox SET state='sending',updated_at=now() WHERE id=$1`, key); e != nil {
		return e
	}
	if e = tx.Commit(ctx); e != nil {
		return e
	}
	result, sendError := h.post(ctx, "https://api.weixin.qq.com/cgi-bin/message/subscribe/send?access_token="+url.QueryEscape(token), payload)
	state := "unknown"
	var code any
	if sendError == nil {
		if _, exists := result["errcode"]; exists {
			n := number(result, "errcode")
			code = n
			state = "failed"
			if n == 0 {
				state = "sent"
			}
			if n == 40001 || n == 40014 {
				h.token = ""
			}
			if n == 43101 {
				_, _ = s.db.Exec(ctx, `UPDATE subscription_consents SET credits=0 WHERE user_id=$1 AND template_id=$2`, uid, template)
			}
		}
	}
	// Even context cancellation after sending must leave an inspectable unknown fence.
	finish, stop := context.WithTimeout(context.Background(), 5*time.Second)
	defer stop()
	_, e = s.db.Exec(finish, `UPDATE subscription_outbox SET state=$2,error_code=$3,updated_at=now() WHERE id=$1 AND state='sending'`, key, state, code)
	if e != nil {
		return e
	}
	return sendError
}
