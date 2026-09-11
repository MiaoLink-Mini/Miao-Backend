package gateway

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
	"time"
)

func (s *Server) login(ctx context.Context, identity Identity) (M, error) {
	subject := s.digest("wechat-subject", identity.Subject)
	uid := ""
	e := s.db.QueryRow(ctx, `SELECT id FROM users WHERE wechat_subject_hash=$1`, subject).Scan(&uid)
	if e != nil && e != pgx.ErrNoRows {
		return nil, e
	}
	if uid == "" {
		uid = id("user")
	}
	token := secret()
	expiry := s.now().Add(s.cfg.UserTokenTTL)
	e = s.transaction(ctx, "", func(tx pgx.Tx) error {
		_, e := tx.Exec(ctx, `INSERT INTO users(id,wechat_subject_hash,display_name) VALUES($1,$2,$3) ON CONFLICT(wechat_subject_hash) DO NOTHING`, uid, subject, identity.Name)
		if e != nil {
			return e
		}
		if e = tx.QueryRow(ctx, `SELECT id FROM users WHERE wechat_subject_hash=$1 FOR UPDATE`, subject).Scan(&uid); e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `INSERT INTO user_tokens(token_hash,user_id,expires_at) VALUES($1,$2,$3)`, hash([]byte(token)), uid, expiry)
		if e != nil {
			return e
		}
		if e = s.retainSubscriptionIdentity(ctx, tx, uid, identity.Subject); e != nil {
			return e
		}
		return s.audit(ctx, tx, uid, "", "login", "confirmed", "", "")
	})
	if e != nil {
		return nil, e
	}
	user, e := userJSON(ctx, s.db, uid)
	return M{"accessToken": token, "expiresAt": timestamp(expiry), "user": user}, e
}
func fingerprint(key []byte) string {
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(hash(key))
}
func (s *Server) enroll(ctx context.Context, b M) (M, error) {
	key, e := base64.StdEncoding.DecodeString(str(b, "publicKey"))
	if e != nil || len(key) != ed25519.PublicKeySize {
		return nil, invalid("公钥无效")
	}
	const alphabet = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	random := make([]byte, 12)
	if _, e = rand.Read(random); e != nil {
		return nil, e
	}
	code := make([]byte, 12)
	for i, c := range random {
		code[i] = alphabet[int(c)%len(alphabet)]
	} // rejection sampling below avoids modulo bias
	for i := range code {
		for {
			var x [1]byte
			if _, e = rand.Read(x[:]); e != nil {
				return nil, e
			}
			if int(x[0]) < 256-256%len(alphabet) {
				code[i] = alphabet[int(x[0])%len(alphabet)]
				break
			}
		}
	}
	eid, poll := id("enrollment"), secret()
	expiry := s.now().Add(5 * time.Minute)
	_, e = s.db.Exec(ctx, `INSERT INTO node_enrollments(id,public_key,code_hash,poll_token_hash,name,platform,version,state,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,'pending',$8)`, eid, key, s.digest("pair-code", string(code)), hash([]byte(poll)), str(b, "name"), str(b, "platform"), str(b, "version"), expiry)
	if e != nil {
		return nil, e
	}
	return M{"id": eid, "code": string(code), "pollToken": poll, "keyFingerprint": fingerprint(key), "expiresAt": timestamp(expiry)}, nil
}
func (s *Server) pollEnrollment(ctx context.Context, eid, poll string) (M, error) {
	return rowJSON(ctx, s.db, `SELECT jsonb_build_object('state',CASE WHEN state='pending' AND expires_at<=now() THEN 'expired' ELSE state END,'nodeId',node_id,'expiresAt',expires_at) FROM node_enrollments WHERE id=$1 AND poll_token_hash=$2`, eid, hash([]byte(poll)))
}
func (s *Server) preview(ctx context.Context, uid, code string) (M, error) {
	if !s.rate("pair-preview:"+uid, 5) {
		return nil, apierr(429, "RATE_LIMITED", "配对尝试过于频繁")
	}
	var eid, name, platform string
	var key []byte
	var expiry time.Time
	e := s.db.QueryRow(ctx, `SELECT id,name,platform,public_key,expires_at FROM node_enrollments WHERE code_hash=$1 AND state='pending' AND expires_at>$2`, s.digest("pair-code", code), s.now()).Scan(&eid, &name, &platform, &key, &expiry)
	if e == pgx.ErrNoRows {
		return nil, apierr(410, "PAIRING_INVALID", "配对码无效、过期或已使用")
	}
	if e != nil {
		return nil, e
	}
	ticket := id("ticket")
	_, e = s.db.Exec(ctx, `INSERT INTO pairing_tickets(id,enrollment_id,user_id,expires_at) VALUES($1,$2,$3,$4)`, ticket, eid, uid, expiry)
	return M{"ticketId": ticket, "nodeName": name, "platform": platform, "keyFingerprint": fingerprint(key), "expiresAt": timestamp(expiry)}, e
}
func (s *Server) challenge(ctx context.Context, nid string) (M, error) {
	var found string
	e := s.db.QueryRow(ctx, `SELECT n.id FROM nodes n JOIN node_access a ON a.node_id=n.id WHERE n.id=$1 AND n.revoked_at IS NULL AND a.revoked_at IS NULL`, nid).Scan(&found)
	if e != nil {
		return nil, notFound()
	}
	cid, nonce := id("challenge"), secret()
	expiry := s.now().Add(time.Minute)
	input := strings.Join([]string{"weagent-node-auth/v1", nid, cid, nonce, timestamp(expiry)}, "\n")
	_, e = s.db.Exec(ctx, `INSERT INTO node_challenges(id,node_id,nonce_hash,signing_input,expires_at) VALUES($1,$2,$3,$4,$5)`, cid, nid, hash([]byte(nonce)), input, expiry)
	return M{"id": cid, "signingInput": input, "expiresAt": timestamp(expiry)}, e
}
func (s *Server) prove(ctx context.Context, b M) (M, error) {
	sig, e := base64.StdEncoding.DecodeString(str(b, "signature"))
	if e != nil {
		return nil, invalid("签名无效")
	}
	token := secret()
	expiry := s.now().Add(s.cfg.NodeTokenTTL)
	e = s.transaction(ctx, "", func(tx pgx.Tx) error {
		var key []byte
		var input, nid string
		var cv int64
		e := tx.QueryRow(ctx, `SELECT n.public_key,c.signing_input,n.id,n.credential_version FROM node_challenges c JOIN nodes n ON n.id=c.node_id JOIN node_access a ON a.node_id=n.id WHERE c.id=$1 AND c.consumed_at IS NULL AND c.expires_at>$2 AND n.revoked_at IS NULL AND a.revoked_at IS NULL FOR UPDATE OF c,n`, str(b, "challengeId"), s.now()).Scan(&key, &input, &nid, &cv)
		if e != nil || !ed25519.Verify(key, []byte(input), sig) {
			return apierr(401, "UNAUTHENTICATED", "设备身份验证失败")
		}
		if _, e = tx.Exec(ctx, `UPDATE node_challenges SET consumed_at=$2 WHERE id=$1`, str(b, "challengeId"), s.now()); e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `INSERT INTO node_tokens(token_hash,node_id,credential_version,expires_at) VALUES($1,$2,$3,$4)`, hash([]byte(token)), nid, cv, expiry)
		return e
	})
	return M{"accessToken": token, "expiresAt": timestamp(expiry)}, e
}
func requestFingerprint(r *http.Request, b M) M {
	return M{"method": r.Method, "path": r.URL.Path, "body": b}
}
func (s *Server) localCommand(ctx context.Context, r *http.Request, uid, name string, b M) (M, error) {
	op := r.Header.Get("Idempotency-Key")
	fp := requestFingerprint(r, b)
	prior, e := s.priorOp(ctx, uid, op, fp)
	if e != nil || prior != nil {
		return prior, e
	}
	kind := map[string]string{"confirmPairing": "pair", "revokeNode": "revoke", "markNotificationRead": "mark_read"}[name]
	target := r.PathValue("id")
	revoked := ""
	e = s.transaction(ctx, uid, func(tx pgx.Tx) error {
		result := emptyResult()
		nid := ""
		var e error
		switch name {
		case "confirmPairing":
			var eid string
			var key []byte
			var nodeName, platform, version, state string
			var expiry time.Time
			var consumed *time.Time
			e = tx.QueryRow(ctx, `SELECT e.id,e.public_key,e.name,e.platform,e.version,e.state,LEAST(e.expires_at,t.expires_at),t.consumed_at FROM pairing_tickets t JOIN node_enrollments e ON e.id=t.enrollment_id WHERE t.id=$1 AND t.user_id=$2 FOR UPDATE OF e,t`, str(b, "ticketId"), uid).Scan(&eid, &key, &nodeName, &platform, &version, &state, &expiry, &consumed)
			if e == pgx.ErrNoRows {
				return notFound()
			}
			if e != nil {
				return e
			}
			if !expiry.After(s.now()) {
				return apierr(410, "PAIRING_INVALID", "配对已过期")
			}
			if state != "pending" || consumed != nil {
				return apierr(409, "PAIRING_CONFLICT", "设备已被配对")
			}
			var exists bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM nodes WHERE public_key=$1)`, key).Scan(&exists); e != nil {
				return e
			}
			if exists {
				return apierr(409, "PAIRING_CONFLICT", "此密钥已有设备身份；撤销后请生成新密钥")
			}
			nid = id("node")
			_, e = tx.Exec(ctx, `INSERT INTO nodes(id,public_key,name,platform,version) VALUES($1,$2,$3,$4,$5)`, nid, key, nodeName, platform, version)
			if e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `INSERT INTO node_access(user_id,node_id) VALUES($1,$2)`, uid, nid); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE node_enrollments SET state='confirmed',node_id=$2 WHERE id=$1`, eid, nid); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE pairing_tickets SET consumed_at=$2 WHERE id=$1`, str(b, "ticketId"), s.now()); e != nil {
				return e
			}
			if e = s.change(ctx, tx, uid, "node", nid, "upsert", 1); e != nil {
				return e
			}
			result["nodeId"] = nid
		case "revokeNode":
			nid = target
			if e = ownNode(ctx, tx, uid, nid, true); e != nil {
				return e
			}
			var rev int64
			e = tx.QueryRow(ctx, `UPDATE nodes SET revoked_at=COALESCE(revoked_at,$2),credential_version=credential_version+1,connection_epoch=connection_epoch+1,revision=revision+1 WHERE id=$1 RETURNING revision`, nid, s.now()).Scan(&rev)
			if e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE node_access SET revoked_at=COALESCE(revoked_at,$2) WHERE node_id=$1`, nid, s.now()); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE subscription_outbox o SET state='failed',updated_at=now() FROM notifications n JOIN sessions s ON s.id=n.session_id WHERE o.notification_id=n.id AND s.node_id=$1 AND o.state='pending'`, nid); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE node_tokens SET revoked_at=$2 WHERE node_id=$1`, nid, s.now()); e != nil {
				return e
			}
			if e = s.change(ctx, tx, uid, "node", nid, "remove", rev); e != nil {
				return e
			}
			result["nodeId"] = nid
			revoked = nid
		case "markNotificationRead":
			if _, e = s.getResource(ctx, tx, uid, "notifications", target); e != nil {
				return e
			}
			var rev int64
			e = tx.QueryRow(ctx, `UPDATE notifications SET read_at=COALESCE(read_at,$2),revision=revision+1 WHERE id=$1 RETURNING revision`, target, s.now()).Scan(&rev)
			if e != nil {
				return e
			}
			if e = s.change(ctx, tx, uid, "notification", target, "upsert", rev); e != nil {
				return e
			}
		}
		if e = s.insertOp(ctx, tx, uid, op, kind, nid, "", "", 0, fp, nil, s.now()); e != nil {
			return e
		}
		if e = s.finishOp(ctx, tx, uid, op, "confirmed", result, nil); e != nil {
			return e
		}
		return s.audit(ctx, tx, uid, op, kind, "confirmed", nid, "")
	})
	if e != nil {
		return nil, e
	}
	if revoked != "" {
		if p := s.nodes[revoked]; p != nil {
			delete(s.nodes, revoked)
			p.stop()
		}
		for p := range s.clients {
			if p.user == uid {
				p.stop()
			}
		}
		if e = s.invalidateNodeOperations(ctx, uid, revoked); e != nil {
			return nil, e
		}
	}
	return s.operation(ctx, s.db, uid, op)
}
