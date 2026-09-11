package gateway

import (
	"context"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
)

// Organization and portable transcripts are platform operations. They never send
// prompts, delete project files, or pretend to restore a native execution state.
func (s *Server) historyCommand(ctx context.Context, r *http.Request, uid, name string, b M) (M, error) {
	op, fp := r.Header.Get("Idempotency-Key"), requestFingerprint(r, b)
	if prior, e := s.priorOp(ctx, uid, op, fp); e != nil || prior != nil {
		return prior, e
	}
	kind := map[string]string{"organizeSession": "organize", "deleteHistory": "delete_history", "importHistory": "import_history"}[name]
	sid := r.PathValue("id")
	err := s.transaction(ctx, uid, func(tx pgx.Tx) error {
		var current M
		var e error
		if name != "importHistory" {
			current, e = s.getResource(ctx, tx, uid, "sessions", sid)
			if e != nil {
				return e
			}
			if number(current, "revision") != number(b, "revision") {
				return apierr(409, "SOURCE_CONFLICT", "Session metadata changed; refresh before applying this change")
			}
		}
		nid := str(current, "nodeId")
		switch name {
		case "organizeSession":
			if str(current, "historyState") == "purged" {
				return apierr(410, "HISTORY_PURGED", "Purged history cannot be renamed")
			}
			if tags, ok := b["tags"]; ok {
				seen := map[string]bool{}
				for _, v := range tags.([]any) {
					tag := v.(string)
					if seen[tag] {
						return invalid("Duplicate tag")
					}
					seen[tag] = true
				}
			}
			if title, ok := b["title"]; ok && strings.TrimSpace(title.(string)) == "" {
				return invalid("Title cannot be blank")
			}
			var revision int64
			e = tx.QueryRow(ctx, `UPDATE sessions SET title=COALESCE($3::text,title),pinned=COALESCE($4::boolean,pinned),tags=COALESCE($5::jsonb,tags),archived=COALESCE($6::boolean,archived),revision=revision+1,updated_at=$7 WHERE id=$1 AND revision=$2 RETURNING revision`, sid, number(b, "revision"), b["title"], b["pinned"], b["tags"], b["archived"], s.now()).Scan(&revision)
			if e != nil {
				return e
			}
			if e = s.change(ctx, tx, uid, "session", sid, "upsert", revision); e != nil {
				return e
			}
		case "deleteHistory":
			targets := []any{M{"id": sid}}
			if truth(b, "includeDescendants") {
				targets, e = rowsJSON(ctx, tx, `WITH RECURSIVE tree AS (SELECT id FROM sessions WHERE id=$1 AND user_id=$2 UNION ALL SELECT c.id FROM sessions c JOIN tree p ON c.parent_session_id=p.id WHERE c.user_id=$2) SELECT jsonb_build_object('id',id) FROM tree LIMIT 101`, sid, uid)
				if e != nil {
					return e
				}
				if len(targets) > 100 {
					return invalid("Delete at most 100 related histories at a time")
				}
			}
			// Validate every target before deleting any content. Closed is required,
			// not merely an idle turn that the Node could start again.
			for _, v := range targets {
				id := str(v.(M), "id")
				target, e := s.getResource(ctx, tx, uid, "sessions", id)
				if e != nil {
					return e
				}
				if str(target, "state") != "closed" {
					return apierr(409, "READ_ONLY", "Close every selected managed process before deleting platform history")
				}
				var pending bool
				if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM operations WHERE session_id=$1 AND state IN('accepted','delivered','reconciling'))`, id).Scan(&pending); e != nil {
					return e
				}
				if pending {
					return apierr(409, "OPERATION_UNKNOWN", "Unresolved operation receipts prevent deletion")
				}
			}
			for _, v := range targets {
				id := str(v.(M), "id")
				notices, err := rowsJSON(ctx, tx, `DELETE FROM notifications WHERE session_id=$1 RETURNING jsonb_build_object('id',id,'revision',revision)`, id)
				if err != nil {
					return err
				}
				for _, notice := range notices {
					n := notice.(M)
					if err := s.change(ctx, tx, uid, "notification", str(n, "id"), "remove", number(n, "revision")+1); err != nil {
						return err
					}
				}
				for _, q := range []string{`DELETE FROM checkpoint_items WHERE checkpoint_id IN(SELECT id FROM checkpoints WHERE session_id=$1)`, `DELETE FROM checkpoints WHERE session_id=$1`, `DELETE FROM timeline_items WHERE session_id=$1`, `DELETE FROM diff_files WHERE session_id=$1`, `DELETE FROM session_events WHERE session_id=$1`, `DELETE FROM interaction_requests WHERE session_id=$1`, `DELETE FROM queue_items WHERE session_id=$1`, `UPDATE operations SET payload=NULL,result=CASE WHEN result IS NULL THEN NULL ELSE result-'native' END WHERE session_id=$1 AND state IN('confirmed','failed')`} {
					if _, e = tx.Exec(ctx, q, id); e != nil {
						return e
					}
				}
				var revision int64
				if e = tx.QueryRow(ctx, `UPDATE sessions SET history_state='purged',earliest_sequence=last_sequence+1,title='History content cleared',tags='[]',revision=revision+1,updated_at=$2 WHERE id=$1 RETURNING revision`, id, s.now()).Scan(&revision); e != nil {
					return e
				}
				if e = s.change(ctx, tx, uid, "session", id, "upsert", revision); e != nil {
					return e
				}
			}
		case "importHistory":
			nid = str(b, "nodeId")
			if e = ownNode(ctx, tx, uid, nid, false); e != nil {
				return e
			}
			for _, pair := range [][2]string{{"projects", "projectId"}, {"agents", "agentId"}} {
				target, e := s.getResource(ctx, tx, uid, pair[0], str(b, pair[1]))
				if e != nil {
					return e
				}
				if str(target, "nodeId") != nid {
					return invalid("Import project and Agent must belong to the selected Node")
				}
			}
			transcript := object(b, "transcript")
			meta := object(transcript, "session")
			sid = id("session")
			turn := id("turn")
			caps := M{"send": false, "cancel": false, "approval": false, "question": false, "diff": false, "plan": false, "usage": false, "queue": false, "steer": false, "workspace": false}
			_, e = tx.Exec(ctx, `INSERT INTO sessions(id,user_id,node_id,project_id,agent_id,title,state,mode,capabilities,capability_revision,origin_mode) VALUES($1,$2,$3,$4,$5,$6,'closed','readonly',$7,1,'import')`, sid, uid, nid, str(b, "projectId"), str(b, "agentId"), str(meta, "title"), caps)
			if e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `INSERT INTO turns(id,session_id,state,ended_at) VALUES($1,$2,'completed',$3)`, turn, sid, s.now()); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE sessions SET current_turn_id=$2 WHERE id=$1`, sid, turn); e != nil {
				return e
			}
			for _, v := range array(transcript, "items") {
				item := v.(M)
				role := str(item, "role")
				if role != "user" && role != "assistant" {
					return invalid("Portable transcript supports user and assistant messages only; system instructions are not executable imports")
				}
				if _, e = s.appendEvent(ctx, tx, uid, nid, sid, turn, "message.completed", M{"itemId": id("import"), "role": role, "text": str(item, "text"), "truncated": false}); e != nil {
					return e
				}
			}
			// Empty imports must still appear in the metadata stream.
			imported, e := s.getResource(ctx, tx, uid, "sessions", sid)
			if e != nil {
				return e
			}
			if e = s.change(ctx, tx, uid, "session", sid, "upsert", number(imported, "revision")); e != nil {
				return e
			}
		}
		result := emptyResult()
		result["sessionId"] = sid
		result["nodeId"] = nid
		if e = s.insertOp(ctx, tx, uid, op, kind, nid, sid, "", 0, fp, nil, s.now()); e != nil {
			return e
		}
		if e = s.finishOp(ctx, tx, uid, op, "confirmed", result, nil); e != nil {
			return e
		}
		return s.audit(ctx, tx, uid, op, kind, "confirmed", nid, sid)
	})
	if err != nil {
		return nil, err
	}
	return s.operation(ctx, s.db, uid, op)
}
func (s *Server) exportHistory(ctx context.Context, uid, sid string) (M, error) {
	session, e := s.getResource(ctx, s.db, uid, "sessions", sid)
	if e != nil {
		return nil, e
	}
	if str(session, "historyState") == "purged" {
		return nil, apierr(410, "HISTORY_PURGED", "History content was purged")
	}
	rows, e := rowsJSON(ctx, s.db, `SELECT body FROM timeline_items WHERE session_id=$1 AND body->>'type'='message' AND body->>'role' IN('user','assistant') ORDER BY first_sequence LIMIT 101`, sid)
	if e != nil {
		return nil, e
	}
	items := []any{}
	truncated := len(rows) > 100
	bytes := 0
	for _, v := range rows {
		m := v.(M)
		text, cut := truncate(str(m, "text"), 4000)
		item := M{"role": str(m, "role"), "text": text}
		size := len(canonical(item))
		if len(items) >= 100 || bytes+size > 56*1024 {
			truncated = true
			break
		}
		bytes += size
		items = append(items, item)
		truncated = truncated || cut || truth(m, "truncated")
	}
	agent, e := s.getResource(ctx, s.db, uid, "agents", str(session, "agentId"))
	if e != nil {
		return nil, e
	}
	return M{"format": "weagent-transcript/1", "exportedAt": timestamp(s.now()), "session": M{"title": str(session, "title"), "agent": str(agent, "name"), "parentSessionId": session["parentSessionId"]}, "items": items, "truncated": truncated}, nil
}
