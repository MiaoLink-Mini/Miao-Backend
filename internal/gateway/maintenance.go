package gateway

import (
	"context"
	"github.com/jackc/pgx/v5"
	"time"
)

func (s *Server) recoverOperations(ctx context.Context) error {
	// On restart no persisted outbox is automatically replayed. Untouched pending means definitely not written.
	rows, e := rowsJSON(ctx, s.db, `SELECT jsonb_build_object('user',o.user_id,'id',o.id,'outbox',c.state) FROM operations o LEFT JOIN command_outbox c ON(c.user_id,c.operation_id)=(o.user_id,o.id) WHERE o.state IN ('accepted','delivered','reconciling')`)
	if e != nil {
		return e
	}
	for _, v := range rows {
		m := v.(M)
		uid, op := str(m, "user"), str(m, "id")
		if str(m, "outbox") == "pending" {
			if e = s.failUnsent(ctx, uid, op, apierr(503, "SERVICE_UNAVAILABLE", "Gateway 重启前未发送此命令")); e != nil {
				return e
			}
		} else {
			if e = s.transaction(ctx, uid, func(tx pgx.Tx) error {
				if _, e := tx.Exec(ctx, `UPDATE command_outbox SET state='uncertain' WHERE user_id=$1 AND operation_id=$2 AND state<>'delivered'`, uid, op); e != nil {
					return e
				}
				return s.finishOp(ctx, tx, uid, op, "reconciling", nil, nil)
			}); e != nil {
				return e
			}
		}
	}
	return nil
}
func (s *Server) invalidateNodeOperations(ctx context.Context, uid, nid string) error {
	rows, e := rowsJSON(ctx, s.db, `SELECT jsonb_build_object('id',o.id,'outbox',c.state) FROM operations o JOIN command_outbox c ON(c.user_id,c.operation_id)=(o.user_id,o.id) WHERE o.user_id=$1 AND o.node_id=$2 AND o.state IN('accepted','delivered','reconciling')`, uid, nid)
	if e != nil {
		return e
	}
	for _, v := range rows {
		m := v.(M)
		if str(m, "outbox") == "pending" {
			if e = s.failUnsent(ctx, uid, str(m, "id"), apierr(409, "FORBIDDEN", "设备权限已撤销，命令未发送")); e != nil {
				return e
			}
		} else {
			if e = s.transaction(ctx, uid, func(tx pgx.Tx) error { return s.finishOp(ctx, tx, uid, str(m, "id"), "reconciling", nil, nil) }); e != nil {
				return e
			}
		}
	}
	return nil
}
func (s *Server) loop() {
	defer s.wg.Done()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	lastMaintenance := s.now()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-tick.C:
			s.mu.Lock()
			ctx, c := context.WithTimeout(s.ctx, 10*time.Second)
			if s.lease != nil {
				var one int
				if e := s.lease.QueryRow(ctx, `SELECT 1`).Scan(&one); e != nil {
					s.draining = true
					s.cancel()
					for p := range s.clients {
						p.stop()
					}
					for _, p := range s.nodes {
						p.stop()
					}
					c()
					s.mu.Unlock()
					return
				}
			}
			for p := range s.clients {
				if !p.expires.After(s.now()) || s.now().Sub(p.lastSeen) >= s.cfg.OfflineAfter {
					p.stop()
				}
			}
			for _, p := range s.nodes {
				if !p.expires.After(s.now()) || s.now().Sub(p.lastSeen) >= s.cfg.OfflineAfter {
					p.stop()
				}
			}
			if e := s.expireOperations(ctx); e != nil && ctx.Err() == nil {
				s.log.Error("operation reconciliation tick failed")
			}
			if s.now().Sub(lastMaintenance) >= s.cfg.MaintenanceInterval {
				if e := s.maintenance(ctx); e != nil && ctx.Err() == nil {
					s.log.Error("maintenance failed")
				}
				lastMaintenance = s.now()
			}
			s.pump(ctx)
			c()
			s.mu.Unlock()
		}
	}
}
func (s *Server) expireOperations(ctx context.Context) error {
	rows, e := rowsJSON(ctx, s.db, `SELECT jsonb_build_object('user',o.user_id,'id',o.id,'node',o.node_id,'outbox',c.state) FROM operations o JOIN command_outbox c ON(c.user_id,c.operation_id)=(o.user_id,o.id) WHERE o.state IN ('accepted','delivered') AND o.deadline_at<=$1 ORDER BY o.deadline_at LIMIT 100`, s.now())
	if e != nil {
		return e
	}
	for _, v := range rows {
		m := v.(M)
		uid, op := str(m, "user"), str(m, "id")
		if str(m, "outbox") == "pending" {
			if e = s.failUnsent(ctx, uid, op, apierr(503, "SERVICE_UNAVAILABLE", "命令在发送前到期")); e != nil {
				return e
			}
		} else {
			if e = s.transaction(ctx, uid, func(tx pgx.Tx) error {
				if _, e := tx.Exec(ctx, `UPDATE command_outbox SET state='uncertain' WHERE user_id=$1 AND operation_id=$2 AND state<>'delivered'`, uid, op); e != nil {
					return e
				}
				return s.finishOp(ctx, tx, uid, op, "reconciling", nil, nil)
			}); e != nil {
				return e
			}
			if p := s.nodes[str(m, "node")]; p != nil {
				p.enqueue(frame("command.query", M{"epoch": p.epoch, "operationId": op}))
			}
		}
	}
	return nil
}
func (s *Server) maintenance(ctx context.Context) error {
	requests, e := rowsJSON(ctx, s.db, `SELECT jsonb_build_object('id',id,'user',user_id,'session',session_id) FROM interaction_requests WHERE state='pending' AND expires_at<=$1 LIMIT 100`, s.now())
	if e != nil {
		return e
	}
	for _, v := range requests {
		r := v.(M)
		uid, rid := str(r, "user"), str(r, "id")
		if e = s.transaction(ctx, uid, func(tx pgx.Tx) error {
			var rev int64
			if e := tx.QueryRow(ctx, `UPDATE interaction_requests SET state='expired',revision=revision+1 WHERE id=$1 AND state='pending' RETURNING revision`, rid).Scan(&rev); e == pgx.ErrNoRows {
				return nil
			} else if e != nil {
				return e
			}
			if e := s.change(ctx, tx, uid, "request", rid, "upsert", rev); e != nil {
				return e
			}
			rr, e := s.getResource(ctx, tx, uid, "requests", rid)
			if e == pgx.ErrNoRows {
				return nil
			}
			if e != nil {
				return e
			}
			nid, e := ownSession(ctx, tx, uid, str(rr, "sessionId"))
			if e != nil {
				return e
			}
			_, e = s.appendEvent(ctx, tx, uid, nid, str(rr, "sessionId"), str(rr, "turnId"), "request.upsert", rr)
			return e
		}); e != nil {
			return e
		}
	}
	cutoff := s.now().Add(-s.cfg.ContentRetention)
	sessions, e := rowsJSON(ctx, s.db, `SELECT jsonb_build_object('id',s.id,'user',s.user_id) FROM sessions s JOIN turns current_turn ON current_turn.id=s.current_turn_id WHERE s.history_state='available' AND s.state IN ('completed','cancelled','failed','closed') AND current_turn.ended_at<$1 AND NOT EXISTS(SELECT 1 FROM operations o WHERE o.session_id=s.id AND o.state IN('accepted','delivered','reconciling')) AND NOT EXISTS(SELECT 1 FROM queue_items q WHERE q.session_id=s.id AND q.state IN('queued','starting')) LIMIT 50`, cutoff)
	if e != nil {
		return e
	}
	for _, v := range sessions {
		m := v.(M)
		uid, sid := str(m, "user"), str(m, "id")
		if e = s.transaction(ctx, uid, func(tx pgx.Tx) error {
			for _, q := range []string{`DELETE FROM checkpoint_items WHERE checkpoint_id IN(SELECT id FROM checkpoints WHERE session_id=$1)`, `DELETE FROM checkpoints WHERE session_id=$1`, `DELETE FROM timeline_items WHERE session_id=$1`, `DELETE FROM diff_files WHERE session_id=$1`, `DELETE FROM session_events WHERE session_id=$1`, `DELETE FROM interaction_requests WHERE session_id=$1`, `DELETE FROM queue_items WHERE session_id=$1`} {
				if _, e := tx.Exec(ctx, q, sid); e != nil {
					return e
				}
			}
			var rev int64
			if e := tx.QueryRow(ctx, `UPDATE sessions SET history_state='purged',earliest_sequence=last_sequence+1,title='历史正文已清理',revision=revision+1 WHERE id=$1 RETURNING revision`, sid).Scan(&rev); e != nil {
				return e
			}
			return s.change(ctx, tx, uid, "session", sid, "upsert", rev)
		}); e != nil {
			return e
		}
	}
	// Retire only fully resolved, already-purged session aggregates. Never delete uncertain command tombstones.
	retired, e := rowsJSON(ctx, s.db, `SELECT jsonb_build_object('id',s.id,'user',s.user_id,'revision',s.revision) FROM sessions s JOIN turns t ON t.id=s.current_turn_id WHERE s.history_state='purged' AND t.ended_at<$1 AND NOT EXISTS(SELECT 1 FROM sessions child WHERE child.parent_session_id=s.id) AND NOT EXISTS(SELECT 1 FROM operations o WHERE o.session_id=s.id AND o.state IN('accepted','delivered','reconciling')) LIMIT 50`, s.now().Add(-s.cfg.MetadataRetention))
	if e != nil {
		return e
	}
	for _, v := range retired {
		m := v.(M)
		sid, uid := str(m, "id"), str(m, "user")
		if e = s.transaction(ctx, uid, func(tx pgx.Tx) error {
			for _, q := range []string{`DELETE FROM notifications WHERE session_id=$1`, `DELETE FROM node_event_receipts WHERE session_id=$1`, `DELETE FROM command_outbox WHERE (user_id,operation_id) IN(SELECT user_id,id FROM operations WHERE session_id=$1)`, `DELETE FROM operations WHERE session_id=$1`, `UPDATE sessions SET current_turn_id=NULL WHERE id=$1`, `DELETE FROM turns WHERE session_id=$1`, `DELETE FROM sessions WHERE id=$1`} {
				if _, e := tx.Exec(ctx, q, sid); e != nil {
					return e
				}
			}
			return s.change(ctx, tx, uid, "session", sid, "remove", number(m, "revision")+1)
		}); e != nil {
			return e
		}
	}
	for _, q := range []string{`DELETE FROM checkpoint_items WHERE checkpoint_id IN(SELECT id FROM checkpoints WHERE expires_at<now())`, `DELETE FROM checkpoints WHERE expires_at<now()`, `DELETE FROM pairing_tickets WHERE expires_at<now()-interval '1 day'`, `DELETE FROM node_enrollments WHERE expires_at<now()-interval '1 day' AND NOT EXISTS(SELECT 1 FROM pairing_tickets t WHERE t.enrollment_id=node_enrollments.id)`, `DELETE FROM node_challenges WHERE expires_at<now()-interval '1 day'`, `DELETE FROM user_tokens WHERE expires_at<now()-interval '1 day'`, `DELETE FROM node_tokens WHERE expires_at<now()-interval '1 day'`, `UPDATE operations o SET payload=NULL WHERE o.payload IS NOT NULL AND o.state IN('confirmed','failed') AND o.updated_at<now()-interval '1 day' AND NOT EXISTS(SELECT 1 FROM queue_items q WHERE (q.user_id,q.operation_id)=(o.user_id,o.id) AND q.state IN('queued','starting')) AND NOT((o.kind='send' OR (o.kind='native' AND (o.payload->'payload'->'control'->>'action'='compact' OR (o.payload->'payload'->'control'->>'action'='workspace' AND o.payload->'payload'->'control'->'request'->>'kind'='review')))) AND o.state='confirmed' AND NOT EXISTS(SELECT 1 FROM turns t WHERE t.id=o.payload->>'nextTurnId'))`} {
		if _, e = s.db.Exec(ctx, q); e != nil {
			return e
		}
	}
	if _, e = s.db.Exec(ctx, `DELETE FROM user_changes WHERE created_at<$1`, s.now().Add(-s.cfg.ChangesRetention)); e != nil {
		return e
	}
	if _, e = s.db.Exec(ctx, `DELETE FROM audit_entries WHERE created_at<$1`, s.now().Add(-s.cfg.MetadataRetention)); e != nil {
		return e
	}
	if _, e = s.db.Exec(ctx, `DELETE FROM operations WHERE session_id IS NULL AND state IN('confirmed','failed') AND updated_at<$1`, s.now().Add(-s.cfg.MetadataRetention)); e != nil {
		return e
	}
	_, e = s.db.Exec(ctx, `DELETE FROM node_event_receipts WHERE created_at<$1`, s.now().Add(-s.cfg.MetadataRetention))
	return e
}
