package gateway

import (
	"context"
	"testing"
)

func TestNativeControlJournalAndCompactionTurn(t *testing.T) {
	f := newFixture(t)
	alice, bob := f.login("alice"), f.login("bob")
	n := f.pair(alice)
	n.cap["models"], n.cap["compact"] = true, true
	n.connect()
	n.inventory()
	_, sid, turn := n.create()
	path := "/v1/sessions/" + sid + "/native"
	body := M{"expectedTurnId": turn, "capabilityRevision": 1, "control": M{"action": "models"}}
	f.call("POST", path, alice, id("op"), body, 409) // Active turn.
	n.state(sid, turn, "completed", nil)
	f.call("POST", path, bob, id("op"), body, 404)
	stale := clone(body)
	stale["capabilityRevision"] = 2
	f.call("POST", path, alice, id("op"), stale, 409)
	key := id("op")
	f.call("POST", path, alice, key, body, 202)
	cmd := object(n.read("command"), "data")
	if str(cmd, "kind") != "native" || cmd["nextTurnId"] != nil {
		t.Fatal("invalid native catalog command")
	}
	f.call("POST", path, alice, id("op"), body, 409) // Unresolved command fences new controls.
	result := emptyResult()
	result["sessionId"], result["nodeId"], result["turnId"] = sid, n.id, turn
	result["native"] = M{"action": "models", "models": []any{M{"id": "native-model", "label": "Native model"}}, "selected": nil, "truncated": false}
	bad := clone(result)
	bad["native"] = M{"action": "compact", "status": "completed"}
	f.app.mu.Lock()
	err := f.app.acknowledge(context.Background(), f.app.nodes[n.id], M{"epoch": n.epoch, "operationId": key, "state": "confirmed", "result": bad, "error": nil}, false)
	f.app.mu.Unlock()
	if err == nil {
		t.Fatal("mismatched native receipt was accepted")
	}
	n.ack(cmd, "confirmed", result)
	receipt := f.call("POST", path, alice, key, body, 202)
	if str(receipt, "state") != "confirmed" || !equal(receipt["result"], result) {
		t.Fatal("native receipt did not survive dedupe")
	}
	compact := clone(body)
	compact["control"] = M{"action": "compact"}
	f.call("POST", path, alice, key, compact, 409) // Same key, different payload.
	f.call("POST", path, alice, id("op"), compact, 202)
	cmd = object(n.read("command"), "data")
	next := str(cmd, "nextTurnId")
	if next == "" || next == turn {
		t.Fatal("compaction must reserve a new turn")
	}
	n.state(sid, next, "running", nil)
	result["turnId"], result["native"] = next, M{"action": "compact", "status": "started"}
	n.ack(cmd, "confirmed", result)
	current := f.call("GET", "/v1/sessions/"+sid, alice, "", nil, 200)
	if str(current, "state") != "running" {
		t.Fatal("accepted compaction must not imply completion")
	}
	n.state(sid, next, "completed", nil)
	current = f.call("GET", "/v1/sessions/"+sid, alice, "", nil, 200)
	if str(current, "state") != "completed" || str(current, "turnId") != next {
		t.Fatal("compaction did not advance session")
	}
}

func TestNativeControlAbsentCapabilityFailsClosed(t *testing.T) {
	f := newFixture(t)
	alice := f.login("alice")
	n := f.pair(alice)
	n.connect()
	n.inventory()
	_, sid, turn := n.create()
	n.state(sid, turn, "completed", nil)
	for _, action := range []string{"models", "compact"} {
		response := f.call("POST", "/v1/sessions/"+sid+"/native", alice, id("op"), M{"expectedTurnId": turn, "capabilityRevision": 1, "control": M{"action": action}}, 409)
		if str(object(response, "error"), "code") != "CAPABILITY_UNSUPPORTED" {
			t.Fatal("missing native capability was not rejected")
		}
	}
}
