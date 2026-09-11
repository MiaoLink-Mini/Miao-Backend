package gateway

import (
	"bytes"
	"context"
	"testing"
)

func TestShareSecretEncryptionBinding(t *testing.T) {
	cfg := Defaults()
	cfg.Key = bytes.Repeat([]byte{19}, 32)
	s := &Server{cfg: cfg}
	plain := []byte(secret())
	a, e := s.sealShareSecret("owner", "share", plain)
	if e != nil {
		t.Fatal(e)
	}
	b, e := s.sealShareSecret("owner", "share", plain)
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Equal(a, b) || bytes.Contains(a, plain) {
		t.Fatal("ciphertext must not repeat or contain plaintext")
	}
	recovered, e := s.openShareSecret("owner", "share", a)
	if e != nil || recovered != string(plain) {
		t.Fatal("cannot recover identical creation receipt")
	}
	for _, binding := range [][2]string{{"other", "share"}, {"owner", "other"}} {
		if _, e = s.openShareSecret(binding[0], binding[1], a); e == nil {
			t.Fatal("binding substitution accepted")
		}
	}
	a[len(a)-1] ^= 1
	if _, e = s.openShareSecret("owner", "share", a); e == nil {
		t.Fatal("tampered invitation accepted")
	}
}

func TestReadOnlyShareHTTPIsolation(t *testing.T) {
	f := newFixture(t)
	owner, reader, other := f.login("share-owner"), f.login("share-reader"), f.login("share-other")
	n := f.pair(owner)
	n.connect()
	n.inventory()
	_, sid, _ := n.create()
	key := id("invite")
	body := M{"permission": "read", "ttlSeconds": 3600}
	invite := f.call("POST", "/v1/sessions/"+sid+"/shares", owner, key, body, 200)
	gid := str(object(invite, "share"), "id")
	token := str(invite, "token")
	replay := f.call("POST", "/v1/sessions/"+sid+"/shares", owner, key, body, 200)
	if str(replay, "token") != token {
		t.Fatal("creation retry changed secret")
	}
	f.call("POST", "/v1/sessions/"+sid+"/shares", owner, key, M{"permission": "control", "ttlSeconds": 3600}, 409)
	f.call("GET", "/v1/session-shares/"+gid, "", "", nil, 401)
	f.call("GET", "/v1/session-shares/"+gid, reader, "", nil, 404)
	f.call("POST", "/v1/session-shares/redeem", owner, "", M{"token": token}, 404)
	f.call("POST", "/v1/session-shares/redeem", reader, "", M{"token": token}, 200)
	f.call("POST", "/v1/session-shares/redeem", reader, "", M{"token": token}, 200)
	f.call("POST", "/v1/session-shares/redeem", other, "", M{"token": token}, 404)
	meta := f.call("GET", "/v1/session-shares/"+gid, reader, "", nil, 200)
	if str(object(meta, "session"), "id") != sid {
		t.Fatal("wrong shared session")
	}
	if bytes.Contains(canonical(meta), []byte(token)) {
		t.Fatal("secret leaked through metadata")
	}
	f.call("GET", "/v1/sessions/"+sid, reader, "", nil, 404)
	f.call("GET", "/v1/session-shares/"+gid+"/events?after=0", reader, "", nil, 200)
	f.call("GET", "/v1/session-shares/"+gid+"/checkpoint", reader, "", nil, 200)
	f.call("POST", "/v1/session-shares/"+gid+"/commands", reader, id("operation"), M{"action": "cancel", "payload": M{"expectedTurnId": object(meta, "session")["turnId"], "capabilityRevision": 1}}, 403)
	f.call("POST", "/v1/session-shares/"+gid+"/revoke", reader, "", nil, 404)
	f.call("POST", "/v1/session-shares/"+gid+"/revoke", owner, "", nil, 200)
	f.call("POST", "/v1/session-shares/"+gid+"/revoke", owner, "", nil, 200)
	f.call("GET", "/v1/session-shares/"+gid, reader, "", nil, 404)
	f.call("GET", "/v1/session-shares/"+gid+"/events?after=0", reader, "", nil, 404)
}

func TestControlledShareHTTPUsesNativeJournal(t *testing.T) {
	f := newFixture(t)
	owner, reader := f.login("controller-owner"), f.login("controller-reader")
	n := f.pair(owner)
	n.connect()
	n.inventory()
	_, sid, turn := n.create()
	invite := f.call("POST", "/v1/sessions/"+sid+"/shares", owner, id("invite"), M{"permission": "control", "ttlSeconds": 900}, 200)
	gid := str(object(invite, "share"), "id")
	f.call("POST", "/v1/session-shares/redeem", reader, "", M{"token": invite["token"]}, 200)
	f.call("POST", "/v1/session-shares/"+gid+"/commands", reader, id("operation"), M{"action": "native", "payload": M{}}, 400)
	clientOp := id("operation")
	body := M{"action": "send", "payload": M{"expectedTurnId": turn, "capabilityRevision": 1, "text": "queued collaboration", "mode": "queue"}}
	op := f.call("POST", "/v1/session-shares/"+gid+"/commands", reader, clientOp, body, 202)
	if str(op, "id") != clientOp || str(op, "state") != "accepted" {
		t.Fatal("HTTP must return caller receipt, not completion")
	}
	cmd := object(n.read("command"), "data")
	internalOp := str(cmd, "operationId")
	if internalOp == clientOp || str(cmd, "sessionId") != sid {
		t.Fatal("delegated operation not isolated")
	}
	n.ack(cmd, "delivered", nil)
	n.ack(cmd, "confirmed", n.result(cmd))
	receipt := f.call("GET", "/v1/session-shares/"+gid+"/operations/"+clientOp, reader, "", nil, 200)
	if str(receipt, "state") != "confirmed" {
		t.Fatal("native confirmation not preserved")
	}
	f.call("GET", "/v1/operations/"+internalOp, reader, "", nil, 404)
	repeat := f.call("POST", "/v1/session-shares/"+gid+"/commands", reader, clientOp, body, 202)
	if str(repeat, "state") != "confirmed" {
		t.Fatal("retry did not reuse the original receipt")
	}
	var count int
	if e := f.db.QueryRow(context.Background(), `SELECT count(*) FROM session_share_commands WHERE client_operation_id=$1`, clientOp).Scan(&count); e != nil || count != 1 {
		t.Fatal("duplicate command admission", e, count)
	}
	activity := f.call("GET", "/v1/sessions/"+sid+"/share-activity", owner, "", nil, 200)
	if len(array(activity, "items")) != 1 {
		t.Fatal("missing attributed activity")
	}
	f.call("POST", "/v1/session-shares/"+gid+"/revoke", owner, "", nil, 200)
	f.call("GET", "/v1/session-shares/"+gid+"/operations/"+clientOp, reader, "", nil, 404)
}

func TestExpiredShareHTTPDeniesAcceptedRecipient(t *testing.T) {
	f := newFixture(t)
	owner, reader := f.login("expiry-owner"), f.login("expiry-reader")
	n := f.pair(owner)
	n.connect()
	n.inventory()
	_, sid, turn := n.create()
	invite := f.call("POST", "/v1/sessions/"+sid+"/shares", owner, id("invite"), M{"permission": "control", "ttlSeconds": 900}, 200)
	gid := str(object(invite, "share"), "id")
	f.call("POST", "/v1/session-shares/redeem", reader, "", M{"token": invite["token"]}, 200)
	if _, e := f.db.Exec(context.Background(), `UPDATE session_shares SET created_at=now()-interval '2 hours',expires_at=now()-interval '1 hour' WHERE id=$1`, gid); e != nil {
		t.Fatal(e)
	}
	f.call("GET", "/v1/session-shares/"+gid, reader, "", nil, 404)
	f.call("POST", "/v1/session-shares/"+gid+"/commands", reader, id("operation"), M{"action": "cancel", "payload": M{"expectedTurnId": turn, "capabilityRevision": 1}}, 404)
}
