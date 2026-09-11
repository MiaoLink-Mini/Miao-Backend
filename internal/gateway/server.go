package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
	"weagent/backend/contracts"
	"weagent/backend/internal/protocol"
)

type rateEntry struct {
	Count int
	Until time.Time
}
type Server struct {
	cfg       Config
	db        *pgxpool.Pool
	validator *protocol.Validator
	provider  IdentityProvider
	log       *slog.Logger
	mu        sync.Mutex // single-instance commit/subscribe/fencing sequencer; no native execution under it
	nodes     map[string]*peer
	clients   map[*peer]bool
	rates     map[string]rateEntry
	ctx       context.Context
	cancel    context.CancelFunc
	wg        sync.WaitGroup
	inflight  chan struct{}
	now       func() time.Time
	draining  bool
	mux       *http.ServeMux
	lease     *pgxpool.Conn
	closeOnce sync.Once
}

func New(ctx context.Context, cfg Config, db *pgxpool.Pool, provider IdentityProvider, logger *slog.Logger) (*Server, error) {
	if db == nil || len(cfg.Key) < 32 {
		return nil, errors.New("database and strong HMAC key required")
	}
	v, e := protocol.New()
	if e != nil {
		return nil, e
	}
	if logger == nil {
		logger = slog.Default()
	}
	if provider == nil {
		if cfg.AuthMode == "development" {
			provider = DevelopmentProvider{}
		} else {
			provider = WechatProvider{AppID: cfg.WechatAppID, Secret: cfg.WechatSecret}
		}
	}
	ctx, cancel := context.WithCancel(ctx)
	s := &Server{cfg: cfg, db: db, validator: v, provider: provider, log: logger, nodes: map[string]*peer{}, clients: map[*peer]bool{}, rates: map[string]rateEntry{}, ctx: ctx, cancel: cancel, inflight: make(chan struct{}, cfg.MaxInflight), now: time.Now, mux: http.NewServeMux()}
	lease, e := db.Acquire(ctx)
	if e != nil {
		cancel()
		return nil, e
	}
	var acquired bool
	if e = lease.QueryRow(ctx, `SELECT pg_try_advisory_lock(874235190001)`).Scan(&acquired); e != nil || !acquired {
		lease.Release()
		cancel()
		return nil, errors.New("another Gateway owns this database")
	}
	s.lease = lease
	if e = s.recoverOperations(ctx); e != nil {
		_, _ = lease.Exec(context.Background(), `SELECT pg_advisory_unlock(874235190001)`)
		lease.Release()
		cancel()
		return nil, e
	}
	if e = s.routes(); e != nil {
		_, _ = lease.Exec(context.Background(), `SELECT pg_advisory_unlock(874235190001)`)
		lease.Release()
		cancel()
		return nil, e
	}
	s.wg.Add(1)
	go s.loop()
	if truth(s.subscriptionConfig(), "enabled") {
		s.wg.Add(1)
		go s.subscriptionLoop()
	}
	return s, nil
}
func (s *Server) Handler() http.Handler { return s.mux }
func (s *Server) Close() {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.draining = true
		s.cancel()
		for p := range s.clients {
			p.stop()
		}
		for _, p := range s.nodes {
			p.stop()
		}
		s.mu.Unlock()
		s.wg.Wait()
		if s.lease != nil {
			ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
			defer c()
			_, _ = s.lease.Exec(ctx, `SELECT pg_advisory_unlock(874235190001)`)
			s.lease.Release()
		}
	})
}
func (s *Server) routes() error {
	var api struct {
		Paths map[string]map[string]struct {
			OperationID string `json:"operationId"`
			RequestBody *struct {
				Content map[string]struct {
					Schema struct {
						Ref string `json:"$ref"`
					}
				}
			} `json:"requestBody"`
			Responses map[string]struct {
				Content map[string]struct {
					Schema struct {
						Ref string `json:"$ref"`
					}
				}
			}
		}
	}
	if e := json.Unmarshal(contracts.OpenAPI, &api); e != nil {
		return e
	}
	for path, methods := range api.Paths {
		for method, op := range methods {
			name := op.OperationID
			input := ""
			if op.RequestBody != nil {
				input = refName(op.RequestBody.Content["application/json"].Schema.Ref)
			}
			status := 200
			response := refName(op.Responses["200"].Content["application/json"].Schema.Ref)
			if response == "" {
				status = 202
				response = refName(op.Responses["202"].Content["application/json"].Schema.Ref)
			}
			s.mux.HandleFunc(strings.ToUpper(method)+" "+path, s.wrap(name, input, response, status))
		}
	}
	s.mux.HandleFunc("GET /v1/ws/client", func(w http.ResponseWriter, r *http.Request) { s.socket(w, r, false) })
	s.mux.HandleFunc("GET /v1/ws/node", func(w http.ResponseWriter, r *http.Request) { s.socket(w, r, true) })
	return nil
}
func refName(ref string) string {
	i := strings.LastIndex(ref, "/")
	if i < 0 {
		return ""
	}
	return ref[i+1:]
}

var operationKey = regexp.MustCompile(`^[A-Za-z0-9_-]{16,128}$`)

func (s *Server) wrap(name, input, output string, status int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		trace := id("trace")
		w.Header().Set("X-Request-ID", trace)
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		select {
		case s.inflight <- struct{}{}:
			defer func() { <-s.inflight }()
		default:
			s.writeError(w, apierr(503, "SERVICE_UNAVAILABLE", "请求过多"), trace)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		var body M
		if input != "" {
			mediaType, _, parseErr := mime.ParseMediaType(r.Header.Get("Content-Type"))
			if parseErr != nil || mediaType != "application/json" {
				s.writeError(w, invalid("Content-Type 必须为 application/json"), trace)
				return
			}
			b, e := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
			if e != nil {
				var large *http.MaxBytesError
				if errors.As(e, &large) {
					s.writeError(w, apierr(413, "PAYLOAD_TOO_LARGE", "请求过大"), trace)
				} else {
					s.writeError(w, invalid("无法读取请求"), trace)
				}
				return
			}
			v, e := protocol.Decode(b)
			if e != nil || s.validator.Validate(input, v) != nil {
				s.writeError(w, invalid("请求字段不符合协议"), trace)
				return
			}
			body, _ = v.(map[string]any)
		}
		if requiresKey(name) && !operationKey.MatchString(r.Header.Get("Idempotency-Key")) {
			s.writeError(w, invalid("需要有效 Idempotency-Key"), trace)
			return
		}
		// Rate limit identity provider calls before doing external network work.
		ip := s.clientIP(r)
		s.mu.Lock()
		transfer := workspaceTransfer(name, body)
		ipBucket, ipLimit := "ip:", 120
		if transfer {
			ipBucket, ipLimit = "transfer-ip:", 600
		}
		allowed := s.rate(ipBucket+ip, ipLimit)
		if isPublic(name) {
			allowed = allowed && s.rate("public:"+ip, 30)
		}
		if name == "createEnrollment" || name == "previewPairing" {
			allowed = allowed && s.rate("pairing-ip:"+ip, 5)
		}
		s.mu.Unlock()
		if !allowed {
			s.writeError(w, apierr(429, "RATE_LIMITED", "请求过于频繁"), trace)
			return
		}
		var identity Identity
		if name == "login" {
			var e error
			identity, e = s.provider.Exchange(ctx, str(body, "code"))
			if e != nil {
				s.writeError(w, apierr(401, "UNAUTHENTICATED", "登录兑换失败，请重新登录"), trace)
				return
			}
		}
		s.mu.Lock()
		defer s.mu.Unlock()
		if s.draining && name != "health" {
			s.writeError(w, apierr(503, "SERVICE_UNAVAILABLE", "服务正在停止"), trace)
			return
		}
		uid := ""
		var e error
		if !isPublic(name) && name != "pollEnrollment" {
			uid, _, e = s.authenticate(ctx, r)
			if e != nil {
				s.writeError(w, e, trace)
				return
			}
			userBucket, userLimit := "user:", 180
			if transfer {
				userBucket, userLimit = "transfer-user:", 400
			}
			if !s.rate(userBucket+uid, userLimit) {
				s.writeError(w, apierr(429, "RATE_LIMITED", "请求过于频繁"), trace)
				return
			}
			if requiresKey(name) && !transfer && !s.rate("commands:"+uid, 30) {
				s.writeError(w, apierr(429, "RATE_LIMITED", "操作过于频繁"), trace)
				return
			}
		}
		value, e := s.handle(ctx, r, name, uid, body, identity)
		if e != nil {
			s.writeError(w, e, trace)
			if normalizeError(e).Status == 500 {
				s.log.Error("request failed", "requestId", trace, "operation", name, "errorType", errorType(e))
			}
			return
		}
		if e = s.validator.Validate(output, value); e != nil {
			s.log.Error("response schema mismatch", "requestId", trace, "schema", output)
			s.writeError(w, apierr(500, "INTERNAL", "响应协议错误"), trace)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(value)
		if r.Method == http.MethodPost {
			s.pump(ctx)
		}
	}
}
func errorType(e error) string {
	if e == nil {
		return "nil"
	}
	return "database_or_internal"
} // never serialize SQL, arguments, provider URLs or schema payloads into logs
func isPublic(name string) bool {
	return name == "health" || name == "ready" || name == "login" || name == "createEnrollment" || name == "createNodeChallenge" || name == "proveNodeIdentity"
}
func requiresKey(name string) bool {
	switch name {
	case "createShare", "sharedCommand", "projectHistory", "createSession", "sendMessage", "cancelTurn", "respondRequest", "nativeControl", "confirmPairing", "revokeNode", "markNotificationRead", "organizeSession", "deleteHistory", "importHistory", "recordSubscriptionConsent":
		return true
	}
	return false
}
func (s *Server) rate(key string, limit int) bool {
	now := s.now()
	r := s.rates[key]
	if !r.Until.After(now) {
		r = rateEntry{Until: now.Add(time.Minute)}
	}
	if len(s.rates) >= 10000 {
		for k, v := range s.rates {
			if !v.Until.After(now) {
				delete(s.rates, k)
			}
		}
		if _, ok := s.rates[key]; !ok && len(s.rates) >= 10000 {
			return false
		}
	}
	r.Count++
	s.rates[key] = r
	return r.Count <= limit
}
func (s *Server) writeError(w http.ResponseWriter, err error, trace string) {
	e := normalizeError(err)
	if e.Status == 429 {
		w.Header().Set("Retry-After", "60")
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(M{"error": errorBody(e, trace)})
}
func bearer(r *http.Request) string {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") || len(h) > 1024 {
		return ""
	}
	return strings.TrimPrefix(h, "Bearer ")
}
func (s *Server) authenticate(ctx context.Context, r *http.Request) (string, time.Time, error) {
	var uid string
	var expiry time.Time
	token := bearer(r)
	if token == "" {
		return "", expiry, apierr(401, "UNAUTHENTICATED", "请先登录")
	}
	e := s.db.QueryRow(ctx, `SELECT user_id,expires_at FROM user_tokens WHERE token_hash=$1 AND revoked_at IS NULL AND expires_at>$2`, hash([]byte(token)), s.now()).Scan(&uid, &expiry)
	if e != nil {
		return "", expiry, apierr(401, "UNAUTHENTICATED", "登录已失效")
	}
	return uid, expiry, nil
}
func (s *Server) handle(ctx context.Context, r *http.Request, name, uid string, b M, identity Identity) (any, error) {
	switch name {
	case "health":
		return M{"status": "ok"}, nil
	case "ready":
		if e := s.db.Ping(ctx); e != nil {
			return nil, apierr(503, "SERVICE_UNAVAILABLE", "数据库暂不可用")
		}
		return M{"status": "ok"}, nil
	case "login":
		return s.login(ctx, identity)
	case "getUserProfile":
		return s.userProfile(ctx, uid)
	case "updateUserProfile":
		return s.updateUserProfile(ctx, uid, b)
	case "logout":
		_, e := s.db.Exec(ctx, `UPDATE user_tokens SET revoked_at=$2 WHERE token_hash=$1`, hash([]byte(bearer(r))), s.now())
		if e == nil {
			for p := range s.clients {
				if p.token == bearer(r) {
					p.stop()
				}
			}
		}
		return M{"status": "ok"}, e
	case "createEnrollment":
		return s.enroll(ctx, b)
	case "pollEnrollment":
		return s.pollEnrollment(ctx, r.PathValue("id"), bearer(r))
	case "createNodeChallenge":
		return s.challenge(ctx, str(b, "nodeId"))
	case "proveNodeIdentity":
		return s.prove(ctx, b)
	case "previewPairing":
		return s.preview(ctx, uid, str(b, "code"))
	case "confirmPairing", "revokeNode", "markNotificationRead":
		return s.localCommand(ctx, r, uid, name, b)
	case "organizeSession", "deleteHistory", "importHistory":
		return s.historyCommand(ctx, r, uid, name, b)
	case "exportHistory":
		return s.exportHistory(ctx, uid, r.PathValue("id"))
	case "getSubscriptionConfig":
		return s.subscriptionConfig(), nil
	case "recordSubscriptionConsent":
		return s.subscriptionConsent(ctx, r, uid, b)
	case "projectHistory", "createSession", "sendMessage", "cancelTurn", "respondRequest", "nativeControl":
		return s.command(ctx, r, uid, name, b)
	case "createShare":
		return s.createShare(ctx, r, uid, b)
	case "listSessionShares":
		return s.shareList(ctx, r, uid, false)
	case "listReceivedShares":
		return s.shareList(ctx, r, uid, true)
	case "redeemShare":
		return s.redeemShare(ctx, uid, b)
	case "revokeShare":
		return s.revokeShare(ctx, uid, r.PathValue("id"))
	case "getSharedSession":
		return s.sharedSession(ctx, uid, r.PathValue("id"))
	case "listSharedEvents", "getSharedCheckpoint", "getSharedCheckpointItems", "getSharedDiffFile", "getSharedRequest":
		return s.sharedRead(ctx, r, uid, name)
	case "sharedCommand":
		return s.sharedCommand(ctx, r, uid, b)
	case "getSharedOperation":
		return s.sharedOperation(ctx, uid, r.PathValue("id"), r.PathValue("operationId"))
	case "getShareActivity":
		return s.shareActivity(ctx, r, uid)
	case "getOperation":
		return s.operation(ctx, s.db, uid, r.PathValue("id"))
	case "bootstrap":
		return s.bootstrap(ctx, uid)
	case "listEvents":
		return s.eventsPage(ctx, r, uid)
	case "listChanges":
		return s.changesPage(ctx, r, uid)
	case "getCheckpoint":
		return s.checkpoint(ctx, uid, r.PathValue("id"))
	case "getCheckpointItems":
		return s.checkpointItems(ctx, r, uid)
	case "getDiffFile":
		return s.diffFile(ctx, uid, r.PathValue("id"))
	default:
		if strings.HasPrefix(name, "list") || strings.HasPrefix(name, "get") {
			return s.resource(ctx, r, uid, name)
		}
		return nil, notFound()
	}
}
