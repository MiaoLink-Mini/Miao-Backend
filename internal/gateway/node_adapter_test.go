package gateway

import "testing"

func TestUnstartedCreateDoesNotPermanentlyFenceNode(t *testing.T) {
	for _, stage := range []string{"not_seen", "rejected"} {
		t.Run(stage, func(t *testing.T) {
			f := newFixture(t)
			user := f.login("adapter-sync-" + stage)
			n := f.pair(user)
			n.connect()
			n.inventory()
			op := f.call("POST", "/v1/sessions", user, id("operation"), M{"nodeId": n.id, "projectId": n.project, "agentId": n.agent, "prompt": "never enter native adapter", "capabilityRevision": 1}, 202)
			cmd := object(n.read("command"), "data")
			// No delivered ACK and no source: simulate disconnect before the journal boundary.
			n.conn.CloseNow()
			n.auth()
			n.connect()
			query := object(n.read("command.query"), "data")
			if str(query, "operationId") != str(op, "id") {
				t.Fatal("wrong operation queried")
			}
			var errBody any
			if stage == "rejected" {
				errBody = M{"code": "SERVICE_UNAVAILABLE", "message": "Local preflight rejected", "retryable": false, "requestId": id("trace")}
			}
			n.send("command.status", M{"operationId": op["id"], "state": stage, "result": nil, "error": errBody})
			n.inventory()
			waitOperation(t, f, user, str(op, "id"), "failed")
			waitNodeOnline(t, f, user, n.id)
			session := f.call("GET", "/v1/sessions/"+str(cmd, "sessionId"), user, "", nil, 200)
			if str(session, "state") != "failed" {
				t.Fatal("unstarted turn should fail without pretending a native snapshot exists")
			}
			// A subsequent connection no longer queries a final operation. Its initial
			// needsSync set must exclude the same never-started reservation as well.
			n.conn.CloseNow()
			n.auth()
			n.connect()
			n.inventory()
			waitNodeOnline(t, f, user, n.id)
		})
	}
}
