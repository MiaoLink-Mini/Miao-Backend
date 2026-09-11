package gateway

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
	"weagent/backend/database"
)

type fixture struct {
	t    *testing.T
	app  *Server
	http *httptest.Server
	db   *pgxpool.Pool
	url  string
}

func newFixture(t *testing.T) *fixture {

	return newFixtureWithProvider(t, DevelopmentProvider{})
}

func newFixtureWithProvider(t *testing.T, provider IdentityProvider) *fixture {
	t.Helper()
	adminURL := os.Getenv("TEST_DATABASE_URL")
	if adminURL == "" {
		t.Skip("TEST_DATABASE_URL not set; use scripts/test.ps1 for native PostgreSQL integration")
	}
	ctx := context.Background()
	admin, e := pgx.Connect(ctx, adminURL)
	if e != nil {
		t.Fatal("fixture database unavailable:", e)
	}
	dbname := "weagent_test_" + strings.TrimPrefix(id("db"), "db_")
	if _, e = admin.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{dbname}.Sanitize()); e != nil {
		admin.Close(ctx)
		t.Fatal(e)
	}
	u, e := url.Parse(adminURL)
	if e != nil {
		t.Fatal(e)
	}
	u.Path = "/" + dbname
	dbURL := u.String()
	t.Cleanup(func() {
		ctx, c := context.WithTimeout(context.Background(), 10*time.Second)
		defer c()
		if !strings.HasPrefix(dbname, "weagent_test_") {
			t.Fatal("unsafe database cleanup")
		}
		_, _ = admin.Exec(ctx, `SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname=$1 AND pid<>pg_backend_pid()`, dbname)
		if _, e := admin.Exec(ctx, "DROP DATABASE "+pgx.Identifier{dbname}.Sanitize()); e != nil {
			t.Error("fixture cleanup:", e)
		}
		admin.Close(ctx)
	})
	if e = database.Up(ctx, dbURL); e != nil {
		t.Fatal(e)
	}
	pool, e := OpenPool(ctx, dbURL)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(pool.Close)
	cfg := Defaults()
	cfg.Key = bytes.Repeat([]byte{7}, 32)
	cfg.AuthMode = "development"
	if wechat, ok := provider.(WechatProvider); ok {
		cfg.AuthMode = "wechat"
		cfg.WechatAppID, cfg.WechatSecret = wechat.AppID, wechat.Secret
	}
	cfg.CommandTTL = 3 * time.Second
	app, e := New(ctx, cfg, pool, provider, slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if e != nil {
		t.Fatal(e)
	}
	h := httptest.NewServer(app.Handler())
	t.Cleanup(func() { h.Close(); app.Close() })
	return &fixture{t: t, app: app, http: h, db: pool, url: dbURL}
}
func (f *fixture) call(method, path, token, key string, body any, status int) M {
	f.t.Helper()
	var data io.Reader
	if body != nil {
		data = bytes.NewReader(canonical(body))
	}
	r, e := http.NewRequest(method, f.http.URL+path, data)
	if e != nil {
		f.t.Fatal(e)
	}
	if token != "" {
		r.Header.Set("Authorization", "Bearer "+token)
	}
	if key != "" {
		r.Header.Set("Idempotency-Key", key)
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	resp, e := http.DefaultClient.Do(r)
	if e != nil {
		f.t.Fatal(e)
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(resp.Body)
	if e != nil {
		f.t.Fatal(e)
	}
	var m M
	if e = json.Unmarshal(b, &m); e != nil {
		f.t.Fatalf("%s %s non JSON: %s", method, path, b)
	}
	if resp.StatusCode != status {
		f.t.Fatalf("%s %s status %d expected %d: %s", method, path, resp.StatusCode, status, b)
	}
	return m
}
func (f *fixture) login(identity string) string {
	return str(f.call("POST", "/v1/auth/wechat", "", "", M{"code": "dev:" + identity}, 200), "accessToken")
}

type testNode struct {
	f               *fixture
	id, token, user string
	key             ed25519.PrivateKey
	conn            *websocket.Conn
	epoch           int64
	seq             map[string]int64
	cap             M
	project, agent  string
}

func (f *fixture) pair(user string) *testNode {
	f.t.Helper()
	pub, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		f.t.Fatal(e)
	}
	en := f.call("POST", "/v1/node/enrollments", "", "", M{"publicKey": base64.StdEncoding.EncodeToString(pub), "name": "Fixture Node", "platform": "windows", "version": "0.1.0"}, 200)
	preview := f.call("POST", "/v1/pairings/preview", user, "", M{"code": str(en, "code")}, 200)
	confirm := f.call("POST", "/v1/pairings/confirm", user, id("operation"), M{"ticketId": str(preview, "ticketId")}, 200)
	nid := str(object(confirm, "result"), "nodeId")
	poll := f.call("GET", "/v1/node/enrollments/"+str(en, "id"), str(en, "pollToken"), "", nil, 200)
	if str(poll, "nodeId") != nid {
		f.t.Fatal("poll did not confirm")
	}
	n := &testNode{f: f, id: nid, key: key, user: user, seq: map[string]int64{}, cap: M{"send": true, "cancel": true, "approval": true, "question": true, "diff": true, "plan": true, "usage": true, "queue": true, "steer": false}, project: id("project"), agent: id("agent")}
	n.auth()
	return n
}
func (n *testNode) auth() {
	n.f.t.Helper()
	ch := n.f.call("POST", "/v1/node/auth/challenges", "", "", M{"nodeId": n.id}, 200)
	proof := M{"challengeId": str(ch, "id"), "signature": base64.StdEncoding.EncodeToString(ed25519.Sign(n.key, []byte(str(ch, "signingInput"))))}
	n.token = str(n.f.call("POST", "/v1/node/auth/prove", "", "", proof, 200), "accessToken")
}
func (n *testNode) connect() {
	n.f.t.Helper()
	ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	conn, _, e := websocket.Dial(ctx, strings.Replace(n.f.http.URL, "http", "ws", 1)+"/v1/ws/node", &websocket.DialOptions{HTTPHeader: http.Header{"Authorization": []string{"Bearer " + n.token}}})
	if e != nil {
		n.f.t.Fatal(e)
	}
	n.conn = conn
	n.f.t.Cleanup(func() { conn.CloseNow() })
	m := n.read("node.ready")
	n.epoch = number(object(m, "data"), "epoch")
}
func (n *testNode) read(kind string) M {
	n.f.t.Helper()
	ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	for {
		_, b, e := n.conn.Read(ctx)
		if e != nil {
			n.f.t.Fatalf("waiting %s: %v", kind, e)
		}
		var m M
		if json.Unmarshal(b, &m) != nil {
			n.f.t.Fatal("invalid frame")
		}
		if str(m, "type") == "error" {
			n.f.t.Fatalf("Node error: %s", b)
		}
		if str(m, "type") == kind {
			return m
		}
	}
}
func (n *testNode) send(kind string, b M) {
	n.f.t.Helper()
	b = clone(b)
	b["epoch"] = n.epoch
	ctx, c := context.WithTimeout(context.Background(), 5*time.Second)
	defer c()
	if e := n.conn.Write(ctx, websocket.MessageText, canonical(frame(kind, b))); e != nil {
		n.f.t.Fatal(e)
	}
}
func (n *testNode) inventory() {
	n.send("node.inventory", M{"inventoryId": id("inventory"), "projects": []any{M{"id": n.project, "name": "GoLink", "description": "fixture", "branch": "main", "valid": true}}, "agents": []any{M{"id": n.agent, "name": "Fake Agent", "state": "ready", "version": nil, "adapterVersion": "0.1.0", "capabilities": n.cap, "capabilityRevision": 1}}, "complete": true})
	n.send("node.heartbeat", M{"nonce": id("nonce")})
	n.read("node.heartbeat")
}
func (n *testNode) source(kind, sid string, b M) {
	n.f.t.Helper()
	n.seq[sid]++
	b = clone(b)
	b["sourceEventId"] = id("source")
	b["sourceSequence"] = n.seq[sid]
	if kind == "event" {
		n.send("node.events", M{"events": []any{b}})
	} else {
		n.send(kind, b)
	}
	ack := n.read("events.ack")
	if number(object(ack, "data"), "sourceSequence") != n.seq[sid] {
		n.f.t.Fatal("wrong source ACK")
	}
}
func (n *testNode) state(sid, turn, state string, queue []any) {
	if queue == nil {
		queue = []any{}
	}
	n.source("node.session", sid, M{"session": M{"sessionId": sid, "turnId": turn, "state": state, "capabilities": n.cap, "capabilityRevision": 1, "queue": queue}})
}
func (n *testNode) ack(cmd M, stage string, result any) {
	n.send("command.ack", M{"operationId": str(cmd, "operationId"), "state": stage, "result": result, "error": nil})
	n.send("node.heartbeat", M{"nonce": id("nonce")})
	n.read("node.heartbeat")
}
func (n *testNode) result(cmd M) M {
	r := emptyResult()
	r["sessionId"] = cmd["sessionId"]
	r["nodeId"] = n.id
	switch str(cmd, "kind") {
	case "create":
		r["turnId"] = cmd["turnId"]
	case "send":
		if str(object(cmd, "payload"), "mode") == "queue" {
			r["turnId"] = object(cmd, "payload")["expectedTurnId"]
			r["queueItemId"] = id("queue")
		} else {
			r["turnId"] = cmd["nextTurnId"]
		}
	case "respond":
		r["turnId"] = object(cmd, "payload")["expectedTurnId"]
		r["requestId"] = cmd["requestId"]
	case "cancel":
		r["turnId"] = object(cmd, "payload")["expectedTurnId"]
	}
	return r
}
func (n *testNode) create() (M, string, string) {
	n.f.t.Helper()
	body := M{"nodeId": n.id, "projectId": n.project, "agentId": n.agent, "prompt": "检查网关事件去重逻辑", "capabilityRevision": 1}
	op := n.f.call("POST", "/v1/sessions", n.user, id("operation"), body, 202)
	if str(op, "state") != "accepted" {
		n.f.t.Fatal("HTTP must not confirm")
	}
	cmd := object(n.read("command"), "data")
	n.ack(cmd, "delivered", nil)
	n.ack(cmd, "confirmed", n.result(cmd))
	sid, turn := str(cmd, "sessionId"), str(cmd, "turnId")
	n.state(sid, turn, "running", nil)
	return cmd, sid, turn
}

func TestGatewayEndToEnd(t *testing.T) {
	f := newFixture(t)
	alice, bob := f.login("alice"), f.login("bob")
	f.call("GET", "/v1/bootstrap", "", "", nil, 401)
	n := f.pair(alice)
	n.connect()
	n.inventory()
	bootstrap := f.call("GET", "/v1/bootstrap", alice, "", nil, 200)
	if number(object(bootstrap, "counts"), "onlineNodes") != 1 {
		t.Fatal("Node not online")
	}
	cmd, sid, turn := n.create()
	f.call("GET", "/v1/sessions/"+sid, bob, "", nil, 404)
	key := str(cmd, "operationId")
	same := f.call("POST", "/v1/sessions", alice, key, object(cmd, "payload"), 202)
	if str(same, "state") != "confirmed" {
		t.Fatal("idempotent receipt changed")
	}
	bad := clone(object(cmd, "payload"))
	bad["prompt"] = "other"
	f.call("POST", "/v1/sessions", alice, key, bad, 409)
	n.source("event", sid, M{"sessionId": sid, "turnId": turn, "type": "message.delta", "data": M{"itemId": "reply", "role": "assistant", "text": "检查"}})
	n.source("event", sid, M{"sessionId": sid, "turnId": turn, "type": "message.completed", "data": M{"itemId": "reply", "role": "assistant", "text": "检查完成", "truncated": false}})
	n.source("event", sid, M{"sessionId": sid, "turnId": turn, "type": "message.delta", "data": M{"itemId": "reply", "role": "assistant", "text": "迟到文本"}})
	n.state(sid, turn, "completed", nil)
	page := f.call("GET", "/v1/sessions/"+sid+"/events?limit=2", alice, "", nil, 200)
	if !truth(page, "hasMore") {
		t.Fatal("pagination missing")
	}
	last := number(page, "highWater")
	cursor := int64(0)
	for {
		path := "/v1/sessions/" + sid + "/events?limit=2&after=" + fmtInt(cursor) + "&until=" + fmtInt(last)
		p := f.call("GET", path, alice, "", nil, 200)
		for _, v := range array(p, "events") {
			if number(v.(M), "sequence") != cursor+1 {
				t.Fatal("sequence gap")
			}
			cursor++
		}
		if !truth(p, "hasMore") {
			break
		}
	}
	if cursor != last {
		t.Fatal("history incomplete")
	}
	cp := f.call("GET", "/v1/sessions/"+sid+"/checkpoint", alice, "", nil, 200)
	items := f.call("GET", "/v1/checkpoints/"+str(cp, "id")+"/items", alice, "", nil, 200)
	if len(array(items, "items")) != 1 || str(array(items, "items")[0].(M), "text") != "检查完成" {
		t.Fatalf("final projection wrong: %v", items)
	}
	f.call("GET", "/v1/checkpoints/"+str(cp, "id")+"/items", bob, "", nil, 404)
	f.call("POST", "/v1/sessions/"+sid+"/messages", alice, id("operation"), M{"expectedTurnId": turn, "text": "继续", "mode": "send", "capabilityRevision": 1}, 202)
	next := object(n.read("command"), "data")
	n.ack(next, "confirmed", n.result(next))
	n.state(sid, str(next, "nextTurnId"), "running", nil)
	query := f.call("GET", "/v1/sessions?q="+url.QueryEscape("去重"), alice, "", nil, 200)
	if len(array(query, "items")) != 1 {
		t.Fatal("title search failed")
	}
	rev := f.call("POST", "/v1/nodes/"+n.id+"/revoke", alice, id("operation"), M{}, 200)
	if str(rev, "state") != "confirmed" {
		t.Fatal("revoke not confirmed")
	}
	f.call("GET", "/v1/sessions/"+sid, alice, "", nil, 404)
	f.call("POST", "/v1/node/auth/challenges", "", "", M{"nodeId": n.id}, 404)
}
func fmtInt(n int64) string { b, _ := json.Marshal(n); return string(b) }

func TestQuestionValidationAndQueueCancel(t *testing.T) {
	f := newFixture(t)
	user := f.login("owner")
	n := f.pair(user)
	n.connect()
	n.inventory()
	_, sid, turn := n.create()
	now := time.Now().UTC()
	request := M{"id": id("request"), "sessionId": sid, "turnId": turn, "kind": "question", "title": "断线后保留什么？", "summary": "仅本次有效", "createdAt": timestamp(now), "expiresAt": timestamp(now.Add(10 * time.Minute)), "questions": []any{M{"id": "q1", "label": "策略", "required": true, "type": "single", "options": []any{M{"id": "history", "label": "历史"}, M{"id": "draft", "label": "草稿"}}}, M{"id": "q2", "label": "说明", "required": false, "type": "text", "maxLength": 20}}}
	n.source("node.request", sid, M{"request": request})
	n.state(sid, turn, "waiting_input", nil)
	rid := str(request, "id")
	body := M{"expectedTurnId": turn, "requestRevision": 1, "capabilityRevision": 1, "decision": M{"kind": "question", "answers": M{"q1": "foreign"}}}
	f.call("POST", "/v1/requests/"+rid+"/respond", user, id("operation"), body, 400)
	body["decision"] = M{"kind": "question", "answers": M{"q1": "history", "unknown": "x"}}
	f.call("POST", "/v1/requests/"+rid+"/respond", user, id("operation"), body, 400)
	body["decision"] = M{"kind": "question", "answers": M{"q1": "history", "q2": "保留"}}
	op := f.call("POST", "/v1/requests/"+rid+"/respond", user, id("operation"), body, 202)
	cmd := object(n.read("command"), "data")
	duplicate := f.call("POST", "/v1/requests/"+rid+"/respond", user, id("operation"), body, 202)
	if str(duplicate, "id") != str(op, "id") {
		t.Fatal("same answer changed winner")
	}
	other := clone(body)
	other["decision"] = M{"kind": "question", "answers": M{"q1": "draft"}}
	f.call("POST", "/v1/requests/"+rid+"/respond", user, id("operation"), other, 409)
	n.ack(cmd, "confirmed", n.result(cmd))
	resolved := f.call("GET", "/v1/requests/"+rid, user, "", nil, 200)
	if str(resolved, "state") != "resolved" {
		t.Fatal("request not resolved")
	}
	n.state(sid, turn, "running", nil)
	f.call("POST", "/v1/sessions/"+sid+"/messages", user, id("operation"), M{"expectedTurnId": turn, "text": "稍后执行", "mode": "queue", "capabilityRevision": 1}, 202)
	queued := object(n.read("command"), "data")
	qr := n.result(queued)
	n.ack(queued, "confirmed", qr)
	session := f.call("GET", "/v1/sessions/"+sid, user, "", nil, 200)
	queue := array(session, "queue")
	if len(queue) != 1 {
		t.Fatal("queue not persisted")
	}
	f.call("POST", "/v1/sessions/"+sid+"/cancel", user, id("operation"), M{"expectedTurnId": turn, "capabilityRevision": 1}, 202)
	cancel := object(n.read("command"), "data")
	n.ack(cancel, "confirmed", n.result(cancel))
	n.state(sid, turn, "cancelled", queue)
	session = f.call("GET", "/v1/sessions/"+sid, user, "", nil, 200)
	if len(array(session, "queue")) != 1 || str(session, "state") != "cancelled" {
		t.Fatal("cancel lost queue")
	}
}

func TestDatabaseNativeMigrationRollback(t *testing.T) {
	f := newFixture(t)
	f.app.Close()
	p, db, e := database.Provider(f.url)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	if _, e = f.db.Exec(ctx, "CREATE TABLE unrelated_fixture(id integer); INSERT INTO unrelated_fixture VALUES(7)"); e != nil {
		t.Fatal(e)
	}
	if _, e = p.Down(ctx); e != nil {
		t.Fatal(e)
	}
	var n int
	if e = f.db.QueryRow(ctx, "SELECT id FROM unrelated_fixture").Scan(&n); e != nil || n != 7 {
		t.Fatal("down touched unrelated data")
	}
	if _, e = p.Up(ctx); e != nil {
		t.Fatal(e)
	}
}
