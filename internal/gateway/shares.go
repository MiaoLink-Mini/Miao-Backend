package gateway

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

// The sharing API is deliberately separate from ownSession and owner resources.
// A grant never makes the recipient a node owner or unlocks arbitrary native actions.
const shareExpr = `jsonb_build_object('id',g.id,'sessionId',g.session_id,'title',ss.title,'permission',g.permission,'createdAt',g.created_at,'expiresAt',g.expires_at,'revokedAt',g.revoked_at,'acceptedAt',g.accepted_at,'recipientName',(SELECT display_name FROM users WHERE id=g.recipient_id))`
const shareAccessJoin = ` JOIN sessions ss ON (ss.id,ss.user_id)=(g.session_id,g.owner_id) JOIN node_access a ON (a.user_id,a.node_id)=(ss.user_id,ss.node_id) JOIN nodes n ON n.id=ss.node_id `
const shareLiveAccess = `g.revoked_at IS NULL AND g.expires_at>$3 AND a.revoked_at IS NULL AND n.revoked_at IS NULL AND ss.history_state='available'`

func unavailableShare() error {
	return apierr(404, "SHARE_UNAVAILABLE", "分享已失效或你没有访问权限")
}
func (s *Server) shareRow(ctx context.Context, db DB, gid string) (M, error) {
	return rowJSON(ctx, db, `SELECT `+shareExpr+` FROM session_shares g JOIN sessions ss ON ss.id=g.session_id WHERE g.id=$1`, gid)
}

// The random invitation is hashed for lookup. Its encrypted copy exists only to
// replay the exact create receipt after a lost response, and is erased on acceptance/revoke.
// Associated data binds ciphertext to its owner and share; no token is logged/listed.
func (s *Server) sealShareSecret(owner, gid string, plain []byte) ([]byte, error) {
	key := sha256.Sum256(s.cfg.Key)
	block, e := aes.NewCipher(key[:])
	if e != nil {
		return nil, e
	}
	gcm, e := cipher.NewGCM(block)
	if e != nil {
		return nil, e
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, e = io.ReadFull(rand.Reader, nonce); e != nil {
		return nil, e
	}
	return gcm.Seal(nonce, nonce, plain, []byte("weagent-share:"+owner+":"+gid)), nil
}
func (s *Server) openShareSecret(owner, gid string, sealed []byte) (string, error) {
	key := sha256.Sum256(s.cfg.Key)
	block, e := aes.NewCipher(key[:])
	if e != nil {
		return "", e
	}
	gcm, e := cipher.NewGCM(block)
	if e != nil {
		return "", e
	}
	if len(sealed) < gcm.NonceSize()+gcm.Overhead() {
		return "", errors.New("invalid invite ciphertext")
	}
	plain, e := gcm.Open(nil, sealed[:gcm.NonceSize()], sealed[gcm.NonceSize():], []byte("weagent-share:"+owner+":"+gid))
	return string(plain), e
}
func (s *Server) shareInvitation(ctx context.Context, uid, gid string) (M, error) {
	record, e := s.shareRow(ctx, s.db, gid)
	if e != nil {
		return nil, e
	}
	var token any
	if record["acceptedAt"] == nil && record["revokedAt"] == nil && parseTime(str(record, "expiresAt")).After(s.now()) {
		var encrypted []byte
		if e = s.db.QueryRow(ctx, `SELECT encrypted_invite FROM session_shares WHERE id=$1 AND owner_id=$2`, gid, uid).Scan(&encrypted); e != nil {
			return nil, e
		}
		if len(encrypted) > 0 {
			plain, e := s.openShareSecret(uid, gid, encrypted)
			if e != nil {
				return nil, apierr(503, "SERVICE_UNAVAILABLE", "邀请密钥已更新，请撤销后新建分享")
			}
			token = plain
		}
	}
	return M{"share": record, "token": token, "serverTime": timestamp(s.now())}, nil
}
func (s *Server) createShare(ctx context.Context, r *http.Request, uid string, b M) (M, error) {
	sid := r.PathValue("id")
	session, e := s.getResource(ctx, s.db, uid, "sessions", sid)
	if e != nil {
		return nil, e
	}
	if str(session, "historyState") == "purged" {
		return nil, unavailableShare()
	}

	op := r.Header.Get("Idempotency-Key")
	fp := hash(canonical(requestFingerprint(r, b)))
	var previous string
	var previousHash []byte
	e = s.db.QueryRow(ctx, `SELECT id,request_hash FROM session_shares WHERE owner_id=$1 AND create_operation_id=$2`, uid, op).Scan(&previous, &previousHash)
	if e == nil {
		if !bytes.Equal(previousHash, fp) {
			return nil, apierr(409, "IDEMPOTENCY_CONFLICT", "同一邀请请求不能更改内容")
		}
		return s.shareInvitation(ctx, uid, previous)
	}
	if e != pgx.ErrNoRows {
		return nil, e
	}
	if str(b, "permission") == "control" && (str(session, "mode") != "managed" || str(session, "state") == "closed") {
		return nil, apierr(409, "READ_ONLY", "此会话仅可分享阅读权限")
	}
	// Expired/revoked rows are retained for audit but don't consume the active quota.
	var count int
	if e = s.db.QueryRow(ctx, `SELECT count(*) FROM session_shares WHERE session_id=$1 AND revoked_at IS NULL AND expires_at>$2`, sid, s.now()).Scan(&count); e != nil {
		return nil, e
	}
	if count >= 20 {
		return nil, apierr(409, "CAPABILITY_UNSUPPORTED", "每个会话最多 20 个有效分享")
	}
	gid, token := id("share"), secret()
	now := s.now()
	expiry := now.Add(time.Duration(number(b, "ttlSeconds")) * time.Second)
	sealed, e := s.sealShareSecret(uid, gid, []byte(token))
	if e != nil {
		return nil, e
	}
	_, e = s.db.Exec(ctx, `INSERT INTO session_shares(id,session_id,owner_id,permission,token_hash,encrypted_invite,create_operation_id,request_hash,created_at,expires_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, gid, sid, uid, str(b, "permission"), hash([]byte(token)), sealed, op, fp, now, expiry)
	if e != nil {
		return nil, e
	}
	return s.shareInvitation(ctx, uid, gid)
}
func (s *Server) redeemShare(ctx context.Context, uid string, b M) (M, error) {
	if !s.rate("share-redeem:"+uid, 10) {
		return nil, apierr(429, "RATE_LIMITED", "请稍后重试")
	}
	var gid, owner, sid string
	e := s.db.QueryRow(ctx, `SELECT g.id,g.owner_id,g.session_id FROM session_shares g`+shareAccessJoin+`WHERE g.token_hash=$1 AND g.owner_id<>$2 AND (g.recipient_id IS NULL OR g.recipient_id=$2) AND `+shareLiveAccess, hash([]byte(str(b, "token"))), uid, s.now()).Scan(&gid, &owner, &sid)
	if e == pgx.ErrNoRows {
		return nil, unavailableShare()
	}
	if e != nil {
		return nil, e
	}
	// Single-instance sequencer and a conditional atomic UPDATE make first acceptance exclusive.
	result, e := s.db.Exec(ctx, `UPDATE session_shares SET recipient_id=$2,accepted_at=COALESCE(accepted_at,$3),encrypted_invite=NULL WHERE id=$1 AND revoked_at IS NULL AND expires_at>$3 AND (recipient_id IS NULL OR recipient_id=$2)`, gid, uid, s.now())
	if e != nil {
		return nil, e
	}
	if result.RowsAffected() != 1 {
		return nil, unavailableShare()
	}
	return s.shareRow(ctx, s.db, gid)
}
func (s *Server) revokeShare(ctx context.Context, uid, gid string) (M, error) {
	var sid string
	e := s.db.QueryRow(ctx, `SELECT session_id FROM session_shares WHERE id=$1 AND owner_id=$2`, gid, uid).Scan(&sid)
	if e == pgx.ErrNoRows {
		return nil, unavailableShare()
	}
	if e != nil {
		return nil, e
	}
	// Allow revocation even when the node itself was revoked; the owner still owns the share.
	_, e = s.db.Exec(ctx, `UPDATE session_shares SET revoked_at=COALESCE(revoked_at,$2),encrypted_invite=NULL WHERE id=$1`, gid, s.now())
	if e != nil {
		return nil, e
	}
	return s.shareRow(ctx, s.db, gid)
}

type shareGrant struct {
	ID, Owner, Session, Permission string
	Expires                        time.Time
}

func (s *Server) authorizeShare(ctx context.Context, uid, gid string, write bool) (shareGrant, error) {
	g := shareGrant{ID: gid}
	e := s.db.QueryRow(ctx, `SELECT g.owner_id,g.session_id,g.permission,g.expires_at FROM session_shares g`+shareAccessJoin+`WHERE g.id=$1 AND g.recipient_id=$2 AND `+shareLiveAccess, gid, uid, s.now()).Scan(&g.Owner, &g.Session, &g.Permission, &g.Expires)
	if e == pgx.ErrNoRows {
		return g, unavailableShare()
	}
	if e != nil {
		return g, e
	}
	if write && g.Permission != "control" {
		return g, apierr(403, "READ_ONLY", "此分享仅可阅读")
	}
	return g, nil
}
func (s *Server) sharedSession(ctx context.Context, uid, gid string) (M, error) {
	g, e := s.authorizeShare(ctx, uid, gid, false)
	if e != nil {
		return nil, e
	}
	session, e := s.getResource(ctx, s.db, g.Owner, "sessions", g.Session)
	if e != nil {
		return nil, e
	}
	// Origin metadata can reference other sessions. It is not part of this grant.
	delete(session, "parentSessionId")
	delete(session, "originPoint")
	delete(session, "tags")
	row, e := rowJSON(ctx, s.db, `SELECT jsonb_build_object('ownerName',u.display_name,'agentName',ag.name,'projectName',p.name,'nodeName',n.name) FROM sessions ss JOIN users u ON u.id=ss.user_id JOIN agents ag ON ag.id=ss.agent_id JOIN projects p ON p.id=ss.project_id JOIN nodes n ON n.id=ss.node_id WHERE ss.id=$1`, g.Session)
	if e != nil {
		return nil, e
	}
	requests, e := rowsJSON(ctx, s.db, `SELECT `+requestExpr+` FROM interaction_requests t WHERE t.session_id=$1 AND t.turn_id=$2 AND t.state IN ('pending','deciding') ORDER BY t.created_at DESC LIMIT 20`, g.Session, session["turnId"])
	if e != nil {
		return nil, e
	}
	share, e := s.shareRow(ctx, s.db, gid)
	if e != nil {
		return nil, e
	}
	row["share"] = share
	row["session"] = session
	row["nodeOnline"] = s.online(str(session, "nodeId"))
	row["requests"] = requests
	row["serverTime"] = timestamp(s.now())
	return row, nil
}

// All shared history subresources are bound to the grant BEFORE using owner-only helpers.
func (s *Server) sharedRead(ctx context.Context, r *http.Request, uid, name string) (any, error) {
	g, e := s.authorizeShare(ctx, uid, r.PathValue("id"), false)
	if e != nil {
		return nil, e
	}
	rr := r.Clone(ctx)
	switch name {
	case "listSharedEvents":
		rr.SetPathValue("id", g.Session)
		return s.eventsPage(ctx, rr, g.Owner)
	case "getSharedCheckpoint":
		return s.checkpoint(ctx, g.Owner, g.Session)
	case "getSharedCheckpointItems":
		cid := r.PathValue("checkpointId")
		var sid string
		if e = s.db.QueryRow(ctx, `SELECT session_id FROM checkpoints WHERE id=$1`, cid).Scan(&sid); e != nil {
			return nil, e
		}
		if sid != g.Session {
			return nil, unavailableShare()
		}
		rr.SetPathValue("id", cid)
		return s.checkpointItems(ctx, rr, g.Owner)
	case "getSharedDiffFile":
		fid := r.PathValue("fileId")
		var sid string
		if e = s.db.QueryRow(ctx, `SELECT session_id FROM diff_files WHERE id=$1`, fid).Scan(&sid); e != nil {
			return nil, e
		}
		if sid != g.Session {
			return nil, unavailableShare()
		}
		return s.diffFile(ctx, g.Owner, fid)
	case "getSharedRequest":
		req, e := s.getResource(ctx, s.db, g.Owner, "requests", r.PathValue("requestId"))
		if e != nil {
			return nil, e
		}
		if str(req, "sessionId") != g.Session {
			return nil, unavailableShare()
		}
		return req, nil
	}
	return nil, notFound()
}

type shareAdmissionKey struct{}
type shareAdmission struct {
	ShareID, ActorID, ClientOperationID, Action string
	Fingerprint                                 []byte
}

func (s *Server) sharedCommand(ctx context.Context, r *http.Request, uid string, b M) (M, error) {
	g, e := s.authorizeShare(ctx, uid, r.PathValue("id"), true)
	if e != nil {
		return nil, e
	}
	clientOp := r.Header.Get("Idempotency-Key")
	fp := hash(canonical(requestFingerprint(r, b)))
	var ownerOp, oldShare string
	var oldHash []byte
	e = s.db.QueryRow(ctx, `SELECT owner_operation_id,share_id,request_hash FROM session_share_commands WHERE actor_id=$1 AND client_operation_id=$2`, uid, clientOp).Scan(&ownerOp, &oldShare, &oldHash)
	if e == nil {
		if oldShare != g.ID || !bytes.Equal(oldHash, fp) {
			return nil, apierr(409, "IDEMPOTENCY_CONFLICT", "操作标识已被使用")
		}
		return s.sharedOperation(ctx, uid, g.ID, clientOp)
	}
	if e != pgx.ErrNoRows {
		return nil, e
	}
	action := str(b, "action")
	name := ""
	target := g.Session
	payload := object(b, "payload")
	switch action {
	case "send":
		name = "sendMessage"
	case "cancel":
		name = "cancelTurn"
	case "respond":
		name = "respondRequest"
		target = str(b, "requestId")
		request, e := s.getResource(ctx, s.db, g.Owner, "requests", target)
		if e != nil {
			return nil, e
		}
		if str(request, "sessionId") != g.Session {
			return nil, unavailableShare()
		}
		// Do not reuse an owner's or another recipient's decision operation receipt.
		if request["decision"] != nil {
			return nil, apierr(409, "REQUEST_ALREADY_DECIDED", "请求已处理")
		}
	default:
		return nil, apierr(403, "FORBIDDEN", "分享不允许此操作")
	}
	internalOp := "delegated_" + hex.EncodeToString(s.digest("session-share-command", uid+":"+g.ID+":"+clientOp))
	admission := shareAdmission{g.ID, uid, clientOp, action, fp}
	commandCtx := context.WithValue(ctx, shareAdmissionKey{}, admission)
	rr := r.Clone(commandCtx)
	rr.SetPathValue("id", target)
	rr.Header.Set("Idempotency-Key", internalOp)
	// Existing capability, turn, request revision, node epoch, operation journal and Node
	// deduplication checks remain authoritative. This is not a generic RPC passthrough.
	op, e := s.command(commandCtx, rr, g.Owner, name, payload)
	if e != nil {
		return nil, e
	}
	op["id"] = clientOp
	return op, nil
}
func (s *Server) recordShareAdmission(ctx context.Context, tx pgx.Tx, owner, op string) error {
	value, ok := ctx.Value(shareAdmissionKey{}).(shareAdmission)
	if !ok {
		return nil
	}
	// Recheck expiry/ownership at the actual admission transaction, not only at HTTP arrival.
	tag, e := tx.Exec(ctx, `INSERT INTO session_share_commands(share_id,actor_id,client_operation_id,owner_id,owner_operation_id,request_hash,action,created_at) SELECT g.id,$2,$3,$4,$5,$6,$7,$8 FROM session_shares g`+shareAccessJoin+`WHERE g.id=$1 AND g.recipient_id=$2 AND g.owner_id=$4 AND g.permission='control' AND g.revoked_at IS NULL AND g.expires_at>$8 AND a.revoked_at IS NULL AND n.revoked_at IS NULL AND ss.history_state='available'`, value.ShareID, value.ActorID, value.ClientOperationID, owner, op, value.Fingerprint, value.Action, s.now())
	if e != nil {
		return e
	}
	if tag.RowsAffected() != 1 {
		return unavailableShare()
	}
	return nil
}
func (s *Server) sharedOperation(ctx context.Context, uid, gid, clientOp string) (M, error) {
	g, e := s.authorizeShare(ctx, uid, gid, false)
	if e != nil {
		return nil, e
	}
	var ownerOp string
	if e = s.db.QueryRow(ctx, `SELECT owner_operation_id FROM session_share_commands WHERE share_id=$1 AND actor_id=$2 AND client_operation_id=$3 AND owner_id=$4`, gid, uid, clientOp, g.Owner).Scan(&ownerOp); e != nil {
		return nil, e
	}
	op, e := s.operation(ctx, s.db, g.Owner, ownerOp)
	if e != nil {
		return nil, e
	}
	op["id"] = clientOp
	return op, nil
}

func (s *Server) shareList(ctx context.Context, r *http.Request, uid string, received bool) (M, error) {
	sid := r.PathValue("id")
	kind := "owner-shares"
	where := `g.owner_id=$1 AND g.session_id=$2`
	if received {
		kind = "received-shares"
		where = `g.recipient_id=$1 AND $2::text='' AND g.revoked_at IS NULL AND g.expires_at>$5 AND a.revoked_at IS NULL AND n.revoked_at IS NULL AND ss.history_state='available'`
	} else {
		if _, e := ownSession(ctx, s.db, uid, sid); e != nil {
			return nil, e
		}
	}
	limit, e := parseLimit(r.URL.Query().Get("limit"), 30, 100)
	if e != nil {
		return nil, e
	}
	var before any
	lastID := ""
	if token := r.URL.Query().Get("pageToken"); token != "" {
		c, e := s.decodeCursor(token)
		if e != nil {
			return nil, e
		}
		if str(c, "user") != uid || str(c, "kind") != kind || str(c, "scope") != sid {
			return nil, invalid("分享游标不匹配")
		}
		before = parseTime(str(c, "before"))
		lastID = str(c, "after")
	}
	// Parameter 5 is used in both variants to retain one exact prepared-statement signature.
	rows, e := rowsJSON(ctx, s.db, `SELECT `+shareExpr+` FROM session_shares g`+shareAccessJoin+`WHERE `+where+` AND ($3::timestamptz IS NULL OR (g.created_at,g.id)<($3::timestamptz,$4)) AND g.created_at<=$5 ORDER BY g.created_at DESC,g.id DESC LIMIT $6`, uid, sid, before, lastID, s.now(), limit+1)
	if e != nil {
		return nil, e
	}
	var next any
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1].(M)
		next = s.cursor(M{"user": uid, "kind": kind, "scope": sid, "before": str(last, "createdAt"), "after": str(last, "id"), "expires": s.now().Add(5 * time.Minute).Unix()})
	}
	return M{"items": rows, "nextPageToken": next, "serverTime": timestamp(s.now())}, nil
}
func (s *Server) shareActivity(ctx context.Context, r *http.Request, uid string) (M, error) {
	sid := r.PathValue("id")
	if _, e := ownSession(ctx, s.db, uid, sid); e != nil {
		return nil, e
	}
	limit, e := parseLimit(r.URL.Query().Get("limit"), 30, 100)
	if e != nil {
		return nil, e
	}
	var before any
	after := ""
	if token := r.URL.Query().Get("pageToken"); token != "" {
		c, e := s.decodeCursor(token)
		if e != nil {
			return nil, e
		}
		if str(c, "user") != uid || str(c, "kind") != "share-activity" || str(c, "scope") != sid {
			return nil, invalid("活动游标不匹配")
		}
		before = parseTime(str(c, "before"))
		after = str(c, "after")
	}
	rows, e := rowsJSON(ctx, s.db, `SELECT jsonb_build_object('id',o.id,'shareId',g.id,'actorName',u.display_name,'action',c.action,'state',o.state,'createdAt',c.created_at) FROM session_share_commands c JOIN session_shares g ON g.id=c.share_id JOIN users u ON u.id=c.actor_id JOIN operations o ON (o.user_id,o.id)=(c.owner_id,c.owner_operation_id) WHERE g.owner_id=$1 AND g.session_id=$2 AND ($3::timestamptz IS NULL OR (c.created_at,o.id)<($3::timestamptz,$4)) ORDER BY c.created_at DESC,o.id DESC LIMIT $5`, uid, sid, before, after, limit+1)
	if e != nil {
		return nil, e
	}
	var next any
	if len(rows) > limit {
		rows = rows[:limit]
		last := rows[len(rows)-1].(M)
		next = s.cursor(M{"user": uid, "kind": "share-activity", "scope": sid, "before": str(last, "createdAt"), "after": str(last, "id"), "expires": s.now().Add(5 * time.Minute).Unix()})
	}
	return M{"items": rows, "nextPageToken": next}, nil
}
