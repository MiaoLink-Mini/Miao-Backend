// Package fakenode implements an independent, persistent protocol fixture, not a real Agent adapter.
package fakenode

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/coder/websocket"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
	"weagent/backend/internal/protocol"
)

type M = map[string]any
type Config struct {
	URL, Dir, Name   string
	Output           io.Writer
	Once             bool
	FaultAfterAccept bool
}
type Journal struct {
	Hash, State string
	Result      M
	Error       M
	Command     M
}
type Session struct {
	ID, Turn, State string
	Sequence        int64
	Queue           []M
	Request         M
}
type State struct {
	PrivateKey                 string
	NodeID, ProjectID, AgentID string
	Enrollment                 M
	Journal                    map[string]*Journal
	Sessions                   map[string]*Session
	Spool                      []M
	NativeCalls                int
}
type Node struct {
	cfg       Config
	state     State
	validator *protocol.Validator
	conn      *websocket.Conn
	epoch     int64
	offset    time.Duration
	heartbeat time.Duration
	client    *http.Client
}

func uid(prefix string) string {
	var b [12]byte
	_, _ = rand.Read(b[:])
	return prefix + "_" + hex.EncodeToString(b[:])
}
func str(m M, k string) string { s, _ := m[k].(string); return s }
func obj(m M, k string) M      { x, _ := m[k].(map[string]any); return x }
func num(m M, k string) int64 {
	switch n := m[k].(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case json.Number:
		v, e := n.Int64()
		if e == nil {
			return v
		}
		f, _ := n.Float64()
		return int64(f)
	}
	return 0
}
func encode(v any) []byte { b, _ := json.Marshal(v); return b }
func now() string         { return time.Now().UTC().Format(time.RFC3339Nano) }
func caps() M {
	return M{"send": true, "cancel": true, "approval": true, "question": true, "diff": true, "plan": true, "usage": true, "queue": true, "steer": false}
}
func Run(ctx context.Context, cfg Config) error {
	if cfg.Output == nil {
		cfg.Output = io.Discard
	}
	if cfg.Name == "" {
		cfg.Name = "喵连 Fake Node"
	}
	u, e := url.Parse(cfg.URL)
	if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("valid Gateway URL required")
	}
	if u.Scheme == "http" && u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1" {
		return errors.New("HTTP Fake Node allowed only on loopback")
	}
	v, e := protocol.New()
	if e != nil {
		return e
	}
	n := &Node{cfg: cfg, validator: v, client: &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	if e = n.load(); e != nil {
		return e
	}
	for ctx.Err() == nil {
		if n.state.NodeID == "" {
			if e = n.provision(ctx); e != nil {
				if cfg.Once {
					return e
				}
				n.report(M{"event": "retry", "stage": "pairing"})
				if !sleep(ctx, time.Second) {
					break
				}
				continue
			}
		}
		e = n.connect(ctx)
		if e == nil {
			e = n.serve(ctx)
		}
		if n.conn != nil {
			n.conn.CloseNow()
			n.conn = nil
		}
		if ctx.Err() != nil {
			break
		}
		if cfg.Once {
			return e
		}
		n.report(M{"event": "retry", "stage": "connection"})
		if !sleep(ctx, time.Second) {
			break
		}
	}
	return ctx.Err()
}
func sleep(ctx context.Context, d time.Duration) bool {
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}
func (n *Node) report(m M) { _ = json.NewEncoder(n.cfg.Output).Encode(m) }
func (n *Node) load() error {
	if n.cfg.Dir == "" {
		return errors.New("private state directory required")
	}
	if e := os.MkdirAll(n.cfg.Dir, 0700); e != nil {
		return e
	}
	path := filepath.Join(n.cfg.Dir, "state.json")
	b, e := os.ReadFile(path)
	if e == nil {
		if len(b) > 64<<20 {
			return errors.New("journal quota exceeded")
		}
		if e = json.Unmarshal(b, &n.state); e != nil {
			return errors.New("corrupt journal; do not recreate identity automatically")
		}
		for _, entry := range n.state.Journal {
			if entry.State == "executing" {
				entry.State = "unknown"
			}
		}
		return n.save()
	}
	if !os.IsNotExist(e) {
		return e
	}
	_, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		return e
	}
	n.state = State{PrivateKey: base64.StdEncoding.EncodeToString(key), ProjectID: uid("project"), AgentID: uid("agent"), Journal: map[string]*Journal{}, Sessions: map[string]*Session{}, Spool: []M{}}
	return n.save()
}
func (n *Node) save() error {
	b := encode(n.state)
	if len(b) > 64<<20 {
		return errors.New("journal/spool quota exceeded; refusing new work")
	}
	tmp := filepath.Join(n.cfg.Dir, "state.pending")
	f, e := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if e != nil {
		return e
	}
	if _, e = f.Write(b); e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return e
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp, filepath.Join(n.cfg.Dir, "state.json"))
}
func (n *Node) api(ctx context.Context, method, path, token string, b M) (M, error) {
	var body io.Reader
	if b != nil {
		body = bytes.NewReader(encode(b))
	}
	r, e := http.NewRequestWithContext(ctx, method, strings.TrimSuffix(n.cfg.URL, "/")+path, body)
	if e != nil {
		return nil, e
	}
	if b != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	resp, e := n.client.Do(r)
	if e != nil {
		return nil, errors.New("Gateway unavailable")
	}
	defer resp.Body.Close()
	raw, e := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if e != nil {
		return nil, e
	}
	var out M
	if json.Unmarshal(raw, &out) != nil {
		return nil, errors.New("invalid Gateway response")
	}
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("Gateway rejected request: %s", str(obj(out, "error"), "code"))
	}
	return out, nil
}
func (n *Node) provision(ctx context.Context) error {
	if n.state.Enrollment == nil {
		key, e := base64.StdEncoding.DecodeString(n.state.PrivateKey)
		if e != nil || len(key) != ed25519.PrivateKeySize {
			return errors.New("invalid private identity")
		}
		en, e := n.api(ctx, "POST", "/v1/node/enrollments", "", M{"publicKey": base64.StdEncoding.EncodeToString(ed25519.PrivateKey(key).Public().(ed25519.PublicKey)), "name": n.cfg.Name, "platform": "other", "version": "fake/1"})
		if e != nil {
			return e
		}
		n.state.Enrollment = en
		if e = n.save(); e != nil {
			return e
		}
	}
	en := n.state.Enrollment
	n.report(M{"event": "pairing", "code": en["code"], "keyFingerprint": en["keyFingerprint"], "expiresAt": en["expiresAt"]})
	for ctx.Err() == nil {
		result, e := n.api(ctx, "GET", "/v1/node/enrollments/"+str(en, "id"), str(en, "pollToken"), nil)
		if e != nil {
			return e
		}
		switch str(result, "state") {
		case "confirmed":
			n.state.NodeID = str(result, "nodeId")
			n.state.Enrollment = nil
			return n.save()
		case "expired":
			n.state.Enrollment = nil
			return n.save()
		}
		if !sleep(ctx, time.Second) {
			return ctx.Err()
		}
	}
	return ctx.Err()
}
func (n *Node) connect(ctx context.Context) error {
	if n.state.NodeID == "" {
		return errors.New("pairing not confirmed")
	}
	challenge, e := n.api(ctx, "POST", "/v1/node/auth/challenges", "", M{"nodeId": n.state.NodeID})
	if e != nil {
		return e
	}
	key, e := base64.StdEncoding.DecodeString(n.state.PrivateKey)
	if e != nil || len(key) != 64 {
		return errors.New("identity key invalid")
	}
	proof, e := n.api(ctx, "POST", "/v1/node/auth/prove", "", M{"challengeId": challenge["id"], "signature": base64.StdEncoding.EncodeToString(ed25519.Sign(key, []byte(str(challenge, "signingInput"))))})
	if e != nil {
		return e
	}
	wsURL := strings.Replace(strings.TrimSuffix(n.cfg.URL, "/"), "http", "ws", 1) + "/v1/ws/node"
	dialCtx, c := context.WithTimeout(ctx, 10*time.Second)
	defer c()
	n.conn, _, e = websocket.Dial(dialCtx, wsURL, &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer " + str(proof, "accessToken")}}})
	if e != nil {
		return errors.New("Node socket failed")
	}
	n.conn.SetReadLimit(256 << 10)
	_, b, e := n.conn.Read(dialCtx)
	if e != nil {
		return e
	}
	v, e := protocol.Decode(b)
	if e != nil || n.validator.Validate("GatewayToNodeFrame", v) != nil {
		return errors.New("invalid Node handshake")
	}
	m := v.(M)
	if str(m, "type") != "node.ready" {
		return errors.New("missing node.ready")
	}
	data := obj(m, "data")
	n.epoch = num(data, "epoch")
	n.heartbeat = time.Duration(num(data, "heartbeatSeconds")) * time.Second
	serverTime, _ := time.Parse(time.RFC3339Nano, str(data, "serverTime"))
	n.offset = serverTime.Sub(time.Now())
	return nil
}
func (n *Node) send(ctx context.Context, kind string, b M) error {
	bcopy := M{}
	for k, v := range b {
		bcopy[k] = v
	}
	bcopy["epoch"] = n.epoch
	m := M{"version": "weagent/1", "type": kind, "data": bcopy}
	if e := n.validator.Validate("NodeToGatewayFrame", m); e != nil {
		return errors.New("fixture produced invalid protocol frame")
	}
	ctx, c := context.WithTimeout(ctx, 5*time.Second)
	defer c()
	return n.conn.Write(ctx, websocket.MessageText, encode(m))
}
func (n *Node) serve(ctx context.Context) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer n.conn.CloseNow()
	incoming := make(chan M, 128)
	errs := make(chan error, 1)
	done := make(chan struct{})
	conn := n.conn
	go func() {
		defer close(done)
		for {
			_, b, e := conn.Read(ctx)
			if e != nil {
				select {
				case errs <- e:
				default:
				}
				return
			}
			v, e := protocol.Decode(b)
			if e != nil || n.validator.Validate("GatewayToNodeFrame", v) != nil {
				select {
				case errs <- errors.New("invalid Gateway frame"):
				default:
				}
				return
			}
			select {
			case incoming <- v.(M):
			case <-ctx.Done():
				return
			}
		}
	}()
	defer func() { cancel(); conn.CloseNow(); <-done }()
	if e := n.send(ctx, "node.inventory", M{"inventoryId": uid("inventory"), "projects": []M{{"id": n.state.ProjectID, "name": "Fake workspace", "description": "Protocol fixture; no filesystem execution", "branch": "fixture", "valid": true}}, "agents": []M{{"id": n.state.AgentID, "name": "Fake Agent", "state": "ready", "version": "fake/1", "adapterVersion": "fake/1", "capabilities": caps(), "capabilityRevision": 1}}, "complete": true}); e != nil {
		return e
	}
	for _, event := range n.state.Spool {
		if e := n.transmitSource(ctx, event); e != nil {
			return e
		}
	}
	for _, session := range n.state.Sessions {
		if e := n.emitState(ctx, session); e != nil {
			return e
		}
	}
	n.report(M{"event": "connected", "nodeId": n.state.NodeID, "projectId": n.state.ProjectID, "agentId": n.state.AgentID, "epoch": n.epoch})
	tick := time.NewTicker(n.heartbeat)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case e := <-errs:
			return e
		case <-tick.C:
			if e := n.send(ctx, "node.heartbeat", M{"nonce": uid("nonce")}); e != nil {
				return e
			}
		case m := <-incoming:
			b := obj(m, "data")
			switch str(m, "type") {
			case "command":
				if e := n.command(ctx, b); e != nil {
					return e
				}
			case "command.query":
				op := str(b, "operationId")
				entry := n.state.Journal[op]
				if entry == nil {
					if e := n.send(ctx, "command.status", M{"operationId": op, "state": "not_seen", "result": nil, "error": nil}); e != nil {
						return e
					}
				} else {
					if e := n.receipt(ctx, "command.status", op, entry); e != nil {
						return e
					}
				}
			case "events.ack":
				sid, seq := str(b, "sessionId"), num(b, "sourceSequence")
				keep := []M{}
				for _, event := range n.state.Spool {
					data := obj(event, "data")
					if sourceSession(event) != sid || num(data, "sourceSequence") > seq {
						keep = append(keep, event)
					}
				}
				n.state.Spool = keep
				if e := n.save(); e != nil {
					return e
				}
			case "node.heartbeat":
				serverTime, _ := time.Parse(time.RFC3339Nano, str(b, "serverTime"))
				n.offset = serverTime.Sub(time.Now())
			case "error":
				return fmt.Errorf("Gateway protocol error: %s", str(b, "code"))
			}
		}
	}
}
func sourceSession(event M) string {
	data := obj(event, "data")
	switch str(event, "type") {
	case "node.session":
		return str(obj(data, "session"), "sessionId")
	case "node.request":
		return str(obj(data, "request"), "sessionId")
	default:
		return str(data, "sessionId")
	}
}
func (n *Node) emit(ctx context.Context, session *Session, kind string, b M) error {
	session.Sequence++
	b["sourceEventId"] = uid("source")
	b["sourceSequence"] = session.Sequence
	event := M{"type": kind, "data": b}
	n.state.Spool = append(n.state.Spool, event)
	if e := n.save(); e != nil {
		return e
	}
	return n.transmitSource(ctx, event)
}
func (n *Node) transmitSource(ctx context.Context, event M) error {
	if str(event, "type") == "event" {
		return n.send(ctx, "node.events", M{"events": []M{obj(event, "data")}})
	}
	return n.send(ctx, str(event, "type"), obj(event, "data"))
}
func (n *Node) emitState(ctx context.Context, session *Session) error {
	return n.emit(ctx, session, "node.session", M{"session": M{"sessionId": session.ID, "turnId": session.Turn, "state": session.State, "capabilities": caps(), "capabilityRevision": 1, "queue": session.Queue}})
}
func (n *Node) message(ctx context.Context, s *Session, role, text string) error {
	return n.emit(ctx, s, "event", M{"sessionId": s.ID, "turnId": s.Turn, "type": "message.completed", "data": M{"itemId": uid("message"), "role": role, "text": text, "truncated": false}})
}
func (n *Node) receipt(ctx context.Context, kind, op string, j *Journal) error {
	state := j.State
	if state == "executing" {
		state = "unknown"
	}
	var result, err any
	if j.Result != nil && state == "confirmed" {
		result = j.Result
	}
	if j.Error != nil {
		err = j.Error
	}
	return n.send(ctx, kind, M{"operationId": op, "state": state, "result": result, "error": err})
}
func (n *Node) reject(ctx context.Context, op string, j *Journal, code string) error {
	j.State = "rejected"
	j.Error = M{"code": code, "message": "Fake Node precondition rejected", "retryable": false, "requestId": uid("trace")}
	if e := n.save(); e != nil {
		return e
	}
	return n.receipt(ctx, "command.ack", op, j)
}
func (n *Node) command(ctx context.Context, cmd M) error {
	op := str(cmd, "operationId")
	digest := sha256.Sum256(encode(cmd))
	fingerprint := hex.EncodeToString(digest[:])
	if old := n.state.Journal[op]; old != nil {
		if old.Hash != fingerprint {
			return errors.New("operation ID content conflict")
		}
		return n.receipt(ctx, "command.ack", op, old)
	}
	j := &Journal{Hash: fingerprint, State: "delivered", Command: cmd}
	n.state.Journal[op] = j
	if e := n.save(); e != nil {
		return e
	}
	if e := n.receipt(ctx, "command.ack", op, j); e != nil {
		return e
	}
	deadline, _ := time.Parse(time.RFC3339Nano, str(cmd, "deadlineAt"))
	if num(cmd, "nodeEpoch") != n.epoch || !deadline.After(time.Now().Add(n.offset)) {
		return n.reject(ctx, op, j, "STALE_TURN")
	}
	payload := obj(cmd, "payload")
	sid := str(cmd, "sessionId")
	session := n.state.Sessions[sid]
	kind := str(cmd, "kind")
	if kind != "create" && (session == nil || session.Turn != str(payload, "expectedTurnId")) {
		return n.reject(ctx, op, j, "STALE_TURN")
	}
	if num(payload, "capabilityRevision") != 1 {
		return n.reject(ctx, op, j, "CAPABILITY_CHANGED")
	}
	result := M{"sessionId": sid, "nodeId": n.state.NodeID, "turnId": nil, "requestId": nil, "queueItemId": nil}
	var prompt string
	switch kind {
	case "create":
		if session != nil || str(payload, "projectId") != n.state.ProjectID || str(payload, "agentId") != n.state.AgentID {
			return n.reject(ctx, op, j, "VALIDATION_FAILED")
		}
		session = &Session{ID: sid, Turn: str(cmd, "turnId"), State: "running", Queue: []M{}}
		n.state.Sessions[sid] = session
		prompt = str(payload, "prompt")
		result["turnId"] = session.Turn
	case "send":
		if str(payload, "mode") == "queue" {
			if session.State != "running" && session.State != "waiting_approval" && session.State != "waiting_input" {
				return n.reject(ctx, op, j, "STALE_TURN")
			}
			if len(session.Queue) >= 20 {
				return n.reject(ctx, op, j, "CAPABILITY_UNSUPPORTED")
			}
			qid := uid("queue")
			session.Queue = append(session.Queue, M{"id": qid, "operationId": op, "text": str(payload, "text"), "state": "queued", "createdAt": now()})
			result["queueItemId"] = qid
			result["turnId"] = session.Turn
		} else {
			if session.State != "completed" && session.State != "cancelled" && session.State != "failed" {
				return n.reject(ctx, op, j, "STALE_TURN")
			}
			session.Turn = str(cmd, "nextTurnId")
			session.State = "running"
			prompt = str(payload, "text")
			result["turnId"] = session.Turn
		}
	case "cancel":
		if session.State == "completed" || session.State == "cancelled" || session.State == "closed" {
			return n.reject(ctx, op, j, "STALE_TURN")
		}
		result["turnId"] = session.Turn
		session.Request = nil
	case "respond":
		if session.Request == nil || str(session.Request, "id") != str(cmd, "requestId") {
			return n.reject(ctx, op, j, "REQUEST_ALREADY_DECIDED")
		}
		expires, _ := time.Parse(time.RFC3339Nano, str(session.Request, "expiresAt"))
		if !expires.After(time.Now().Add(n.offset)) {
			return n.reject(ctx, op, j, "REQUEST_EXPIRED")
		}
		result["requestId"] = cmd["requestId"]
		result["turnId"] = session.Turn
	default:
		return n.reject(ctx, op, j, "PROTOCOL_UNSUPPORTED")
	}
	j.State = "executing"
	n.state.NativeCalls++
	if e := n.save(); e != nil {
		return e
	}
	if n.cfg.FaultAfterAccept {
		n.report(M{"event": "fault", "stage": "after_native_accept"})
		return errors.New("injected crash window after native acceptance")
	}
	j.State = "confirmed"
	j.Result = result
	if e := n.save(); e != nil {
		return e
	}
	if e := n.receipt(ctx, "command.ack", op, j); e != nil {
		return e
	}
	switch kind {
	case "create":
		return n.runTurn(ctx, session, prompt)
	case "send":
		if str(payload, "mode") == "queue" {
			return n.emitState(ctx, session)
		}
		return n.runTurn(ctx, session, prompt)
	case "cancel":
		session.State = "cancelled"
		return n.emitState(ctx, session)
	case "respond":
		session.Request = nil
		session.State = "running"
		if e := n.emitState(ctx, session); e != nil {
			return e
		}
		if e := n.message(ctx, session, "assistant", "已收到本次回答（Fake Node 协议联调）。"); e != nil {
			return e
		}
		return n.complete(ctx, session)
	}
	return nil
}
func (n *Node) runTurn(ctx context.Context, s *Session, prompt string) error {
	s.State = "running"
	if e := n.emitState(ctx, s); e != nil {
		return e
	}
	if e := n.message(ctx, s, "user", prompt); e != nil {
		return e
	}
	if strings.Contains(prompt, "[hold]") {
		return n.message(ctx, s, "assistant", "Fake Node 保持运行，等待明确取消或排队。")
	}
	if strings.Contains(prompt, "[question]") || strings.Contains(prompt, "[approval]") {
		request := M{"id": uid("request"), "sessionId": s.ID, "turnId": s.Turn, "title": "确认本次测试操作", "summary": "这是 Fake Node 协议请求，不执行任何真实 Shell。", "createdAt": now(), "expiresAt": time.Now().Add(5 * time.Minute).UTC().Format(time.RFC3339Nano)}
		if strings.Contains(prompt, "[approval]") {
			request["kind"] = "approval"
			request["action"] = "fixture action"
			request["scope"] = "fixture only"
			request["choices"] = []M{{"id": "allow_once", "label": "允许一次"}, {"id": "deny", "label": "拒绝"}}
			s.State = "waiting_approval"
		} else {
			request["kind"] = "question"
			request["questions"] = []M{{"id": "strategy", "label": "恢复策略", "type": "single", "required": true, "options": []M{{"id": "history", "label": "保留历史与草稿"}, {"id": "draft", "label": "仅草稿"}}}, {"id": "note", "label": "补充说明", "type": "text", "required": false, "maxLength": 4000}}
			s.State = "waiting_input"
		}
		s.Request = request
		if e := n.emit(ctx, s, "node.request", M{"request": request}); e != nil {
			return e
		}
		return n.emitState(ctx, s)
	}
	if e := n.message(ctx, s, "assistant", "Fake Node 已完成协议联调。此输出不代表真实 Agent 执行结果。"); e != nil {
		return e
	}
	return n.complete(ctx, s)
}
func (n *Node) complete(ctx context.Context, s *Session) error {
	s.State = "completed"
	if e := n.emitState(ctx, s); e != nil {
		return e
	}
	n.report(M{"event": "turn.completed", "sessionId": s.ID, "turnId": s.Turn, "nativeCalls": n.state.NativeCalls})
	if len(s.Queue) > 0 {
		q := s.Queue[0]
		j := n.state.Journal[str(q, "operationId")]
		if j == nil || j.State != "confirmed" {
			return errors.New("queue journal lost; refusing execution")
		}
		s.Queue = s.Queue[1:]
		s.Turn = str(j.Command, "nextTurnId")
		return n.runTurn(ctx, s, str(q, "text"))
	}
	return nil
}
