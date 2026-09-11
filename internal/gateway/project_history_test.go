package gateway

import (
	"context"
	"testing"
)

func TestProjectHistoryHasNoSessionOrTurn(t *testing.T) {
	f := newFixture(t)
	alice, bob := f.login("alice"), f.login("bob")
	n := f.pair(alice)
	n.cap["workspace"], n.cap["historyImport"] = true, true
	n.connect()
	n.inventory()
	body := M{"nodeId": n.id, "projectId": n.project, "agentId": n.agent, "capabilityRevision": 1, "request": M{"kind": "history"}}
	path := "/v1/history/native"
	f.call("POST", path, bob, id("op"), body, 404)
	bad := clone(body)
	bad["request"] = M{"kind": "close"}
	f.call("POST", path, alice, id("op"), bad, 400)
	for i := 0; i < 2; i++ {
		key := id("op")
		f.call("POST", path, alice, key, body, 202)
		cmd := object(n.read("command"), "data")
		if str(cmd, "kind") != "history" || cmd["sessionId"] != nil || cmd["turnId"] != nil {
			t.Fatal("history allocated a session or turn")
		}
		result := emptyResult()
		result["nodeId"] = n.id
		result["native"] = M{"action": "workspace", "requestKind": "history", "view": M{"title": "History", "notice": "", "entries": []any{}, "controls": []any{}}}
		n.ack(cmd, "confirmed", result)
		receipt := f.call("POST", path, alice, key, body, 202)
		if str(receipt, "state") != "confirmed" {
			t.Fatal("history receipt not confirmed")
		}
	}
	for _, table := range []string{"sessions", "turns"} {
		var count int
		if err := f.db.QueryRow(context.Background(), "SELECT count(*) FROM "+table).Scan(&count); err != nil || count != 0 {
			t.Fatalf("%s count=%d error=%v", table, count, err)
		}
	}
	// Only an explicit import may allocate the first managed session.
	create := M{"nodeId": n.id, "projectId": n.project, "agentId": n.agent, "capabilityRevision": 1, "historyOnly": true, "origin": M{"projectHistory": true, "sessionId": "history_context", "resourceId": "resource_history", "mode": "import"}}
	f.call("POST", "/v1/sessions", alice, id("op"), create, 202)
	cmd := object(n.read("command"), "data")
	if str(cmd, "kind") != "create" {
		t.Fatal("explicit import was not dispatched")
	}
}
