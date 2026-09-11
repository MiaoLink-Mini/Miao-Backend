package gateway

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"time"
)

type DB interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func OpenPool(ctx context.Context, url string) (*pgxpool.Pool, error) {
	c, e := pgxpool.ParseConfig(url)
	if e != nil {
		return nil, e
	}
	c.MaxConns = 12
	c.MinConns = 1
	c.ConnConfig.RuntimeParams["timezone"] = "UTC"
	c.ConnConfig.RuntimeParams["statement_timeout"] = "10000"
	c.ConnConfig.RuntimeParams["lock_timeout"] = "3000"
	p, e := pgxpool.NewWithConfig(ctx, c)
	if e != nil {
		return nil, e
	}
	if e = p.Ping(ctx); e != nil {
		p.Close()
		return nil, e
	}
	return p, nil
}
func rowJSON(ctx context.Context, db DB, query string, args ...any) (M, error) {
	var b []byte
	if e := db.QueryRow(ctx, query, args...).Scan(&b); e != nil {
		return nil, e
	}
	var m M
	e := json.Unmarshal(b, &m)
	return m, e
}
func rowsJSON(ctx context.Context, db DB, query string, args ...any) ([]any, error) {
	rows, e := db.Query(ctx, query, args...)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	out := []any{}
	for rows.Next() {
		var b []byte
		if e = rows.Scan(&b); e != nil {
			return nil, e
		}
		var m M
		if e = json.Unmarshal(b, &m); e != nil {
			return nil, e
		}
		out = append(out, m)
	}
	return out, rows.Err()
}
func userJSON(ctx context.Context, db DB, uid string) (M, error) {
	return rowJSON(ctx, db, `SELECT jsonb_build_object('id',id,'displayName',display_name,'createdAt',created_at) FROM users WHERE id=$1`, uid)
}
func (s *Server) transaction(ctx context.Context, uid string, fn func(pgx.Tx) error) error {
	tx, e := s.db.BeginTx(ctx, pgx.TxOptions{})
	if e != nil {
		return e
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	if uid != "" {
		var id string
		if e = tx.QueryRow(ctx, `SELECT id FROM users WHERE id=$1 FOR UPDATE`, uid).Scan(&id); e != nil {
			return e
		}
	}
	if e = fn(tx); e != nil {
		return e
	}
	return tx.Commit(ctx)
}
func ownNode(ctx context.Context, db DB, uid, nid string, includeRevoked bool) error {
	q := `SELECT n.id FROM nodes n JOIN node_access a ON a.node_id=n.id WHERE a.user_id=$1 AND n.id=$2`
	if !includeRevoked {
		q += ` AND a.revoked_at IS NULL AND n.revoked_at IS NULL`
	}
	var id string
	return db.QueryRow(ctx, q, uid, nid).Scan(&id)
}
func ownSession(ctx context.Context, db DB, uid, sid string) (string, error) {
	var nid string
	e := db.QueryRow(ctx, `SELECT s.node_id FROM sessions s JOIN node_access a ON (a.user_id,a.node_id)=(s.user_id,s.node_id) JOIN nodes n ON n.id=s.node_id WHERE s.id=$1 AND s.user_id=$2 AND a.revoked_at IS NULL AND n.revoked_at IS NULL`, sid, uid).Scan(&nid)
	return nid, e
}
func (s *Server) change(ctx context.Context, tx pgx.Tx, uid, kind, rid, action string, resourceRev int64) error {
	_, e := tx.Exec(ctx, `WITH r AS (UPDATE users SET last_revision=last_revision+1 WHERE id=$1 RETURNING last_revision) INSERT INTO user_changes(user_id,revision,resource_type,resource_id,action,resource_revision) SELECT $1,last_revision,$2,$3,$4,$5 FROM r`, uid, kind, rid, action, resourceRev)
	return e
}
func (s *Server) audit(ctx context.Context, tx pgx.Tx, uid, op, kind, state, nid, sid string) error {
	aid := id("audit")
	_, e := tx.Exec(ctx, `INSERT INTO audit_entries(id,user_id,operation_id,node_id,session_id,action,state,title) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`, aid, uid, nullString(op), nullString(nid), nullString(sid), kind, state, "操作 "+kind+" · "+state)
	if e != nil {
		return e
	}
	return s.change(ctx, tx, uid, "audit", aid, "upsert", 1)
}
func (s *Server) notify(ctx context.Context, tx pgx.Tx, uid, sid, kind, title string) error {
	nid := id("notice")
	_, e := tx.Exec(ctx, `INSERT INTO notifications(id,user_id,session_id,kind,title,summary) VALUES($1,$2,$3,$4,$5,'')`, nid, uid, nullString(sid), kind, title)
	if e != nil {
		return e
	}
	if e = s.queueSubscription(ctx, tx, uid, nid, sid, kind, title); e != nil {
		return e
	}
	return s.change(ctx, tx, uid, "notification", nid, "upsert", 1)
}
func (s *Server) operation(ctx context.Context, db DB, uid, op string) (M, error) {
	return rowJSON(ctx, db, `SELECT `+operationExpr+` FROM operations o WHERE o.user_id=$1 AND o.id=$2`, uid, op)
}

const operationExpr = `jsonb_build_object('id',o.id,'kind',o.kind,'state',o.state,'createdAt',o.created_at,'updatedAt',o.updated_at,'deadlineAt',o.deadline_at,'result',o.result,'error',o.error,'revision',o.revision)`

func (s *Server) finishOp(ctx context.Context, tx pgx.Tx, uid, op, state string, result any, reason *APIError) error {
	var eb any
	if reason != nil {
		eb = errorBody(reason, id("trace"))
	}
	var rev int64
	e := tx.QueryRow(ctx, `UPDATE operations SET state=$3,result=$4,error=$5,revision=revision+1,updated_at=$6 WHERE user_id=$1 AND id=$2 RETURNING revision`, uid, op, state, result, eb, s.now()).Scan(&rev)
	if e != nil {
		return e
	}
	return s.change(ctx, tx, uid, "operation", op, "upsert", rev)
}
func (s *Server) insertOp(ctx context.Context, tx pgx.Tx, uid, op, kind, nid, sid, turn string, epoch int64, body, command any, deadline time.Time) error {
	_, e := tx.Exec(ctx, `INSERT INTO operations(user_id,id,kind,request_hash,state,node_id,session_id,expected_turn_id,node_epoch,payload,deadline_at,created_at,updated_at) VALUES($1,$2,$3,$4,'accepted',$5,$6,$7,$8,$9,$10,$11,$11)`, uid, op, kind, hash(canonical(body)), nullString(nid), nullString(sid), nullString(turn), epoch, command, deadline, s.now())
	if e != nil {
		return e
	}
	return s.change(ctx, tx, uid, "operation", op, "upsert", 1)
}
func (s *Server) priorOp(ctx context.Context, uid, op string, fingerprint any) (M, error) {
	var h []byte
	e := s.db.QueryRow(ctx, `SELECT request_hash FROM operations WHERE user_id=$1 AND id=$2`, uid, op).Scan(&h)
	if e == pgx.ErrNoRows {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	if fmt.Sprintf("%x", h) != fmt.Sprintf("%x", hash(canonical(fingerprint))) {
		return nil, apierr(409, "IDEMPOTENCY_CONFLICT", "同一操作标识不能用于不同请求")
	}
	return s.operation(ctx, s.db, uid, op)
}
