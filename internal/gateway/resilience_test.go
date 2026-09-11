package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"github.com/coder/websocket"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func wsRead(t *testing.T, c *websocket.Conn) M {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, b, e := c.Read(ctx)
	if e != nil {
		t.Fatal(e)
	}
	var m M
	if json.Unmarshal(b, &m) != nil {
		t.Fatal("invalid WS JSON")
	}
	return m
}
func wsWrite(t *testing.T, c *websocket.Conn, m M) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if e := c.Write(ctx, websocket.MessageText, canonical(m)); e != nil {
		t.Fatal(e)
	}
}
func (f *fixture) clientSocket(token string) *websocket.Conn {
	f.t.Helper()
	ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	conn, _, e := websocket.Dial(ctx, strings.Replace(f.http.URL, "http", "ws", 1)+"/v1/ws/client", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer " + token}}})
	if e != nil {
		f.t.Fatal(e)
	}
	f.t.Cleanup(func() { conn.CloseNow() })
	return conn
}

func TestClientBarrierAndNodeEpochReplacement(t *testing.T) {
	f := newFixture(t)
	user := f.login("owner")
	n := f.pair(user)
	n.connect()
	n.inventory()
	_, sid, turn := n.create()
	bootstrap := f.call("GET", "/v1/bootstrap", user, "", nil, 200)
	session := f.call("GET", "/v1/sessions/"+sid, user, "", nil, 200)
	c := f.clientSocket(user)
	wsWrite(t, c, frame("watch", M{"revision": bootstrap["revision"], "sessions": []any{M{"sessionId": sid, "after": session["lastSequence"]}}}))
	ready := wsRead(t, c)
	if str(ready, "type") != "ready" {
		t.Fatal("ready barrier must precede live events")
	}
	barrier := number(array(object(ready, "data"), "sessions")[0].(M), "highWater")
	n.source("event", sid, M{"sessionId": sid, "turnId": turn, "type": "message.completed", "data": M{"itemId": "live", "role": "assistant", "text": "durable live", "truncated": false}})
	var event M
	for i := 0; i < 10; i++ {
		m := wsRead(t, c)
		if str(m, "type") == "event" {
			event = object(m, "data")
			break
		}
	}
	if event == nil || number(event, "sequence") != barrier+1 {
		t.Fatal("live cursor not contiguous")
	}
	persisted := f.call("GET", "/v1/sessions/"+sid+"/events?after="+fmtInt(barrier), user, "", nil, 200)
	if str(array(persisted, "events")[0].(M), "id") != str(event, "id") {
		t.Fatal("broadcast happened without durable event")
	}
	oldConn, oldEpoch := n.conn, n.epoch
	n.auth()
	n.connect()
	if n.epoch <= oldEpoch {
		t.Fatal("epoch did not advance")
	}
	n.inventory()
	n.state(sid, turn, "running", nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, _, e := oldConn.Read(ctx); e == nil {
		t.Fatal("replaced connection still open")
	}
	node := f.call("GET", "/v1/nodes/"+n.id, user, "", nil, 200)
	if !truth(node, "online") {
		t.Fatal("old close marked new connection offline")
	}
}

func TestSourceDedupConflictAndSnapshotExpiry(t *testing.T) {
	f := newFixture(t)
	user := f.login("owner")
	n := f.pair(user)
	n.connect()
	n.inventory()
	_, sid, turn := n.create()
	source := M{"sourceEventId": id("source"), "sourceSequence": 2, "sessionId": sid, "turnId": turn, "type": "message.completed", "data": M{"itemId": "same", "role": "assistant", "text": "one final", "truncated": false}}
	n.send("node.events", M{"events": []any{source}})
	ack1 := n.read("events.ack")
	n.send("node.events", M{"events": []any{source}})
	ack2 := n.read("events.ack")
	if !equal(ack1, ack2) {
		t.Fatal("duplicate source altered cursor")
	}
	n.seq[sid] = 2
	changed := clone(source)
	object(changed, "data")["text"] = "different"
	n.send("node.events", M{"events": []any{changed}})
	failure := wsRead(t, n.conn)
	if str(object(failure, "data"), "code") != "SOURCE_CONFLICT" {
		t.Fatalf("expected conflict: %v", failure)
	}
	page := f.call("GET", "/v1/sessions/"+sid+"/events", user, "", nil, 200)
	if len(array(page, "events")) != 2 {
		t.Fatal("duplicate event appended")
	}
	cp := f.call("GET", "/v1/sessions/"+sid+"/checkpoint", user, "", nil, 200)
	if _, e := f.db.Exec(context.Background(), `UPDATE checkpoints SET expires_at=now()-interval '1 second' WHERE id=$1`, str(cp, "id")); e != nil {
		t.Fatal(e)
	}
	f.call("GET", "/v1/checkpoints/"+str(cp, "id")+"/items", user, "", nil, 410)
	if _, e := f.db.Exec(context.Background(), `DELETE FROM session_events WHERE session_id=$1 AND sequence=1;`, sid); e != nil {
		t.Fatal(e)
	}
	if _, e := f.db.Exec(context.Background(), `UPDATE sessions SET earliest_sequence=2 WHERE id=$1`, sid); e != nil {
		t.Fatal(e)
	}
	f.call("GET", "/v1/sessions/"+sid+"/events?after=0", user, "", nil, 410)
	// Receipt is independent from compacted events and still accepts an identical replay.
	n.send("node.events", M{"events": []any{source}})
	n.read("events.ack")
}

func TestTimeoutAndRestartNeverReplayCommands(t *testing.T) {
	f := newFixture(t)
	user := f.login("owner")
	n := f.pair(user)
	n.connect()
	n.inventory()
	body := M{"nodeId": n.id, "projectId": n.project, "agentId": n.agent, "prompt": "ambiguous execution", "capabilityRevision": 1}
	op := f.call("POST", "/v1/sessions", user, id("operation"), body, 202)
	cmd := object(n.read("command"), "data")
	if _, e := f.db.Exec(context.Background(), `UPDATE operations SET deadline_at=now()-interval '1 second' WHERE id=$1`, str(op, "id")); e != nil {
		t.Fatal(e)
	}
	f.app.mu.Lock()
	e := f.app.expireOperations(context.Background())
	f.app.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	query := n.read("command.query")
	if str(object(query, "data"), "operationId") != str(op, "id") {
		t.Fatal("wrong reconciliation target")
	}
	receipt := f.call("GET", "/v1/operations/"+str(op, "id"), user, "", nil, 200)
	if str(receipt, "state") != "reconciling" {
		t.Fatal("timeout must remain ambiguous")
	}
	f.app.Close()
	f.http.Close()
	app, e := New(context.Background(), f.app.cfg, f.db, DevelopmentProvider{}, nil)
	if e != nil {
		t.Fatal(e)
	}
	h := httptest.NewServer(app.Handler())
	t.Cleanup(func() { h.Close(); app.Close() })
	f.app = app
	f.http = h
	n.auth()
	n.connect()
	query = n.read("command.query")
	if str(object(query, "data"), "operationId") != str(op, "id") {
		t.Fatal("restart failed to query journal")
	}
	n.send("command.status", M{"operationId": str(cmd, "operationId"), "state": "confirmed", "result": n.result(cmd), "error": nil})
	n.send("node.heartbeat", M{"nonce": id("nonce")})
	n.read("node.heartbeat")
	receipt = f.call("GET", "/v1/operations/"+str(op, "id"), user, "", nil, 200)
	if str(receipt, "state") != "confirmed" {
		t.Fatal("journal confirmation lost")
	}
}

func TestAuthorizationHandshakeAndPairingIsolation(t *testing.T) {
	f := newFixture(t)
	alice, bob := f.login("alice"), f.login("bob")
	n := f.pair(alice)
	f.call("POST", "/v1/nodes/"+n.id+"/revoke", bob, id("operation"), M{}, 404)
	for _, tc := range []struct {
		endpoint, token, origin string
		status                  int
	}{{"client", n.token, "", 401}, {"node", alice, "", 401}, {"client", alice, "https://hostile.invalid", 403}} {
		ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
		headers := http.Header{"Authorization": []string{"Bearer " + tc.token}}
		if tc.origin != "" {
			headers.Set("Origin", tc.origin)
		}
		conn, resp, e := websocket.Dial(ctx, strings.Replace(f.http.URL, "http", "ws", 1)+"/v1/ws/"+tc.endpoint, &websocket.DialOptions{HTTPHeader: headers})
		c()
		if conn != nil {
			conn.CloseNow()
		}
		if e == nil || resp == nil || resp.StatusCode != tc.status {
			t.Fatalf("handshake %s status mismatch: %v", tc.endpoint, e)
		}
	}
	challenge := f.call("POST", "/v1/node/auth/challenges", "", "", M{"nodeId": n.id}, 200)
	f.call("POST", "/v1/node/auth/prove", "", "", M{"challengeId": str(challenge, "id"), "signature": base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{1}, 64))}, 401)
	second, e := New(context.Background(), f.app.cfg, f.db, DevelopmentProvider{}, nil)
	if second != nil {
		second.Close()
	}
	if e == nil {
		t.Fatal("second Gateway must not own same DB")
	}
}

func TestRequestExpiryRetentionAndDiffAuthorization(t *testing.T) {
	f := newFixture(t)
	alice, bob := f.login("alice"), f.login("bob")
	n := f.pair(alice)
	n.connect()
	n.inventory()
	_, sid, turn := n.create()
	now := time.Now().UTC()
	rid := id("request")
	request := M{"id": rid, "sessionId": sid, "turnId": turn, "kind": "approval", "title": "Read project?", "summary": "local scope", "action": "read", "scope": "project only", "createdAt": timestamp(now), "expiresAt": timestamp(now.Add(time.Minute)), "choices": []any{M{"id": "allow", "label": "Allow once"}, M{"id": "deny", "label": "Deny"}}}
	n.source("node.request", sid, M{"request": request})
	if _, e := f.db.Exec(context.Background(), `UPDATE interaction_requests SET created_at=now()-interval '2 minutes',expires_at=now()-interval '1 minute' WHERE id=$1`, rid); e != nil {
		t.Fatal(e)
	}
	f.call("POST", "/v1/requests/"+rid+"/respond", alice, id("operation"), M{"expectedTurnId": turn, "requestRevision": 1, "capabilityRevision": 1, "decision": M{"kind": "approval", "choiceId": "allow"}}, 410)
	fileID := id("file")
	n.source("event", sid, M{"sessionId": sid, "turnId": turn, "type": "diff.file", "data": M{"id": fileID, "sessionId": sid, "itemId": "diff", "path": "src/main.go", "language": "go", "patch": "@@ -1 +1 @@\n-old\n+new", "truncated": false, "createdAt": timestamp(now)}})
	f.call("GET", "/v1/diff-files/"+fileID, bob, "", nil, 404)
	file := f.call("GET", "/v1/diff-files/"+fileID, alice, "", nil, 200)
	if str(file, "path") != "src/main.go" {
		t.Fatal("wrong diff")
	}
	n.state(sid, turn, "completed", nil)
	if _, e := f.db.Exec(context.Background(), `UPDATE turns SET ended_at=now()-interval '31 days' WHERE id=$1`, turn); e != nil {
		t.Fatal(e)
	}
	f.app.mu.Lock()
	e := f.app.maintenance(context.Background())
	f.app.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	f.call("GET", "/v1/sessions/"+sid+"/events", alice, "", nil, 410)
	f.call("GET", "/v1/diff-files/"+fileID, alice, "", nil, 404)
	n.send("node.events", M{"events": []any{M{"sourceEventId": id("source"), "sourceSequence": n.seq[sid] + 1, "sessionId": sid, "turnId": turn, "type": "message.completed", "data": M{"itemId": "late-after-purge", "role": "assistant", "text": "must not restore deleted content", "truncated": false}}}})
	if str(object(wsRead(t, n.conn), "data"), "code") != "HISTORY_PURGED" {
		t.Fatal("purged history accepted new content")
	}
	n.auth()
	n.connect()
	n.inventory()
	if !truth(f.call("GET", "/v1/nodes/"+n.id, alice, "", nil, 200), "online") {
		t.Fatal("purged session blocked Node readiness")
	}
	if _, e := f.db.Exec(context.Background(), `UPDATE turns SET ended_at=now()-interval '91 days' WHERE id=$1`, turn); e != nil {
		t.Fatal(e)
	}
	f.app.mu.Lock()
	e = f.app.maintenance(context.Background())
	f.app.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	f.call("GET", "/v1/sessions/"+sid, alice, "", nil, 404)
}

func TestHTTPStrictInputAndProvider(t *testing.T) {
	f := newFixture(t)
	for _, tc := range []struct {
		body, content string
		status        int
	}{{`{"code":"dev:a","code":"dev:b"}`, "application/json", 400}, {`{"code":"dev:a"} {}`, "application/json", 400}, {`{"code":"dev:a"}`, "text/plain", 400}, {`{"code":"dev:a"}`, "application/json-invalid", 400}, {`{"code":"` + strings.Repeat("x", 70000) + `"}`, "application/json", 413}} {
		r, _ := http.NewRequest("POST", f.http.URL+"/v1/auth/wechat", strings.NewReader(tc.body))
		r.Header.Set("Content-Type", tc.content)
		resp, e := http.DefaultClient.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != tc.status {
			t.Fatalf("strict input got %d", resp.StatusCode)
		}
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("appid") != "app" || q.Get("secret") != "server-only" || q.Get("js_code") != "fresh" || q.Get("grant_type") != "authorization_code" {
			t.Error("wrong identity exchange query")
		}
		io.WriteString(w, `{"openid":"stable-user","session_key":"NEVER_RETURN_THIS"}`)
	}))
	defer provider.Close()
	identity, e := (WechatProvider{AppID: "app", Secret: "server-only", Endpoint: provider.URL}).Exchange(context.Background(), "fresh")
	if e != nil || identity.Subject != "app:stable-user" {
		t.Fatal("provider port failed", e)
	}
}

// Local code2Session test service; not official WeChat acceptance.
func TestWechatAuthenticationThroughGateway(t *testing.T) {
	service := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		if q.Get("appid") != "test-app" || q.Get("secret") != "test-secret" || q.Get("grant_type") != "authorization_code" {
			t.Error("invalid server-side exchange parameters")
		}
		switch q.Get("js_code") {
		case "code-one", "code-two":
			io.WriteString(w, `{"openid":"test-openid","session_key":"private-session-key"}`)
		case "unavailable":
			w.WriteHeader(http.StatusServiceUnavailable)
		default:
			io.WriteString(w, `{"errcode":40029,"errmsg":"invalid code"}`)
		}
	}))
	defer service.Close()
	f := newFixtureWithProvider(t, WechatProvider{AppID: "test-app", Secret: "test-secret", Endpoint: service.URL})
	first := f.call("POST", "/v1/auth/wechat", "", "", M{"code": "code-one"}, 200)
	second := f.call("POST", "/v1/auth/wechat", "", "", M{"code": "code-two"}, 200)
	if str(object(first, "user"), "id") != str(object(second, "user"), "id") || str(first, "accessToken") == "" {
		t.Fatal("stable WeChat identity did not yield a session")
	}
	for _, secret := range []string{"test-secret", "test-openid", "private-session-key", "session_key"} {
		if strings.Contains(string(canonical(first)), secret) {
			t.Fatal("provider secrets leaked to client")
		}
	}
	f.call("GET", "/v1/bootstrap", str(first, "accessToken"), "", nil, 200)
	for _, code := range []string{"invalid", "unavailable", "dev:owner"} {
		rejected := f.call("POST", "/v1/auth/wechat", "", "", M{"code": code}, 401)
		if str(rejected, "accessToken") != "" {
			t.Fatal("failed exchange issued a token")
		}
	}
}

func TestQueueBackpressureAndDynamicAnswers(t *testing.T) {
	f := newFixture(t)
	user := f.login("queue-fixture")
	conn := f.clientSocket(user)
	cfg := Defaults()
	cfg.QueueFrames = 1
	cfg.QueueBytes = 1024
	ctx, c := context.WithCancel(context.Background())
	defer c()
	// A real socket with a deliberately paused writer: first frame fits, second trips bounded backpressure.
	p := &peer{s: &Server{cfg: cfg, validator: f.app.validator, log: f.app.log}, conn: conn, ctx: ctx, cancel: c, queue: make(chan outbound, 1)}
	if !p.enqueue(frame("heartbeat", M{"nonce": "first"})) {
		t.Fatal("first bounded frame rejected")
	}
	if p.enqueue(frame("heartbeat", M{"nonce": "second"})) {
		t.Fatal("full queue accepted another frame")
	}
	if p.ctx.Err() == nil {
		t.Fatal("slow socket was not disconnected")
	}
	request := M{"kind": "question", "questions": []any{M{"id": "q", "type": "multiple", "required": true, "max": 2, "options": []any{M{"id": "a"}, M{"id": "b"}}}}}
	for _, answer := range []any{[]any{}, []any{"a", "a"}, []any{"a", "foreign"}, "a"} {
		if validateDecision(request, M{"kind": "question", "answers": M{"q": answer}}) == nil {
			t.Fatal("accepted invalid dynamic choice")
		}
	}
	if e := validateDecision(request, M{"kind": "question", "answers": M{"q": []any{"a", "b"}}}); e != nil {
		t.Fatal(e)
	}
}

func TestDeliveredJournalCannotBecomeNotSeen(t *testing.T) {
	f := newFixture(t)
	user := f.login("journal-owner")
	n := f.pair(user)
	n.connect()
	n.inventory()
	op := f.call("POST", "/v1/sessions", user, id("operation"), M{"nodeId": n.id, "projectId": n.project, "agentId": n.agent, "prompt": "hold journal outcome", "capabilityRevision": 1}, 202)
	cmd := object(n.read("command"), "data")
	n.ack(cmd, "delivered", nil)
	if _, e := f.db.Exec(context.Background(), `UPDATE operations SET deadline_at=now()-interval '1 second' WHERE id=$1`, str(op, "id")); e != nil {
		t.Fatal(e)
	}
	f.app.mu.Lock()
	e := f.app.expireOperations(context.Background())
	f.app.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	n.read("command.query")
	n.send("command.status", M{"operationId": str(op, "id"), "state": "not_seen", "result": nil, "error": nil})
	if str(object(wsRead(t, n.conn), "data"), "code") != "SOURCE_CONFLICT" {
		t.Fatal("contradictory journal was trusted")
	}
	if str(f.call("GET", "/v1/operations/"+str(op, "id"), user, "", nil, 200), "state") != "reconciling" {
		t.Fatal("lost delivered journal must remain uncertain, not replayable")
	}
}

func TestQueuedPayloadSurvivesCleanupUntilConsumption(t *testing.T) {
	f := newFixture(t)
	user := f.login("retained-queue")
	n := f.pair(user)
	n.connect()
	n.inventory()
	_, sid, turn := n.create()
	op := f.call("POST", "/v1/sessions/"+sid+"/messages", user, id("operation"), M{"expectedTurnId": turn, "text": "later", "mode": "queue", "capabilityRevision": 1}, 202)
	cmd := object(n.read("command"), "data")
	n.ack(cmd, "confirmed", n.result(cmd))
	queue := array(f.call("GET", "/v1/sessions/"+sid, user, "", nil, 200), "queue")
	if _, e := f.db.Exec(context.Background(), `UPDATE operations SET updated_at=now()-interval '2 days' WHERE id=$1`, str(op, "id")); e != nil {
		t.Fatal(e)
	}
	f.app.mu.Lock()
	e := f.app.maintenance(context.Background())
	f.app.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	n.state(sid, turn, "completed", queue)
	n.state(sid, str(cmd, "nextTurnId"), "running", nil)
	current := f.call("GET", "/v1/sessions/"+sid, user, "", nil, 200)
	if str(current, "turnId") != str(cmd, "nextTurnId") || len(array(current, "queue")) != 0 {
		t.Fatal("retained queue could not be consumed exactly once")
	}
	// A confirmed send may not have reported its new turn yet; preserve its reserved ID until handoff.
	n.state(sid, str(current, "turnId"), "completed", nil)
	op = f.call("POST", "/v1/sessions/"+sid+"/messages", user, id("operation"), M{"expectedTurnId": str(current, "turnId"), "text": "next turn", "mode": "send", "capabilityRevision": 1}, 202)
	cmd = object(n.read("command"), "data")
	n.ack(cmd, "confirmed", n.result(cmd))
	if _, e = f.db.Exec(context.Background(), `UPDATE operations SET updated_at=now()-interval '2 days' WHERE id=$1`, str(op, "id")); e != nil {
		t.Fatal(e)
	}
	f.app.mu.Lock()
	e = f.app.maintenance(context.Background())
	f.app.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	n.state(sid, str(cmd, "nextTurnId"), "running", nil)
}

func TestDatabaseLeaseLossDrainsGateway(t *testing.T) {
	f := newFixture(t)
	user := f.login("lease-owner")
	client := f.clientSocket(user)
	f.app.mu.Lock()
	var pid int
	e := f.app.lease.QueryRow(context.Background(), `SELECT pg_backend_pid()`).Scan(&pid)
	if e == nil {
		_, e = f.db.Exec(context.Background(), `SELECT pg_terminate_backend($1)`, pid)
	}
	f.app.mu.Unlock()
	if e != nil {
		t.Fatal(e)
	}
	select {
	case <-f.app.ctx.Done():
	case <-time.After(5 * time.Second):
		t.Fatal("lost exclusive lease did not drain the Gateway")
	}
	f.call("GET", "/readyz", "", "", nil, 503)
	f.call("GET", "/v1/bootstrap", user, "", nil, 503)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, _, e = client.Read(ctx); e == nil {
		t.Fatal("lost lease kept client socket open")
	}
}

func TestWebSocketPingAndHeartbeatExpiry(t *testing.T) {
	f := newFixture(t)
	user := f.login("heartbeat-owner")
	n := f.pair(user)
	n.connect()
	n.inventory()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	readDone := make(chan error, 1)
	go func() { _, _, e := n.conn.Read(ctx); readDone <- e }()
	if e := n.conn.Ping(ctx); e != nil {
		t.Fatal("server must continuously read and answer WebSocket Ping", e)
	}
	f.app.mu.Lock()
	f.app.nodes[n.id].lastSeen = f.app.now().Add(-f.app.cfg.OfflineAfter - time.Second)
	f.app.mu.Unlock()
	select {
	case e := <-readDone:
		if e == nil {
			t.Fatal("expired heartbeat connection remained open")
		}
	case <-ctx.Done():
		t.Fatal("heartbeat expiration did not disconnect")
	}
	if truth(f.call("GET", "/v1/nodes/"+n.id, user, "", nil, 200), "online") {
		t.Fatal("expired Node still marked online")
	}
}
