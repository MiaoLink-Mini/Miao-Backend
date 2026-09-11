package gateway

import (
	"context"
	"github.com/jackc/pgx/v5"
	"net/http"
	"time"
)

func (s *Server) eventsPage(ctx context.Context, r *http.Request, uid string) (M, error) {
	sid := r.PathValue("id")
	if _, e := ownSession(ctx, s.db, uid, sid); e != nil {
		return nil, e
	}
	after, e := parseSequence(r.URL.Query().Get("after"))
	if e != nil {
		return nil, e
	}
	limit, e := parseLimit(r.URL.Query().Get("limit"), 100, 200)
	if e != nil {
		return nil, e
	}
	var last, first int64
	var history string
	e = s.db.QueryRow(ctx, `SELECT last_sequence,earliest_sequence,history_state FROM sessions WHERE id=$1`, sid).Scan(&last, &first, &history)
	if e != nil {
		return nil, e
	}
	if history == "purged" {
		return nil, apierr(410, "HISTORY_PURGED", "会话正文已超过保留期")
	}
	until := last
	if value := r.URL.Query().Get("until"); value != "" {
		until, e = parseSequence(value)
		if e != nil {
			return nil, e
		}
	}
	if after > until || until > last {
		return nil, apierr(409, "CURSOR_AHEAD", "游标超过已提交历史")
	}
	if after < first-1 {
		return nil, apierr(410, "CURSOR_EXPIRED", "请加载历史 checkpoint")
	}
	events, e := s.readEvents(ctx, s.db, sid, after, until, limit)
	if e != nil {
		return nil, e
	}
	next := after
	for _, v := range events {
		next = number(v.(M), "sequence")
	}
	return M{"sessionId": sid, "after": after, "highWater": until, "events": events, "nextAfter": next, "hasMore": next < until, "earliestSequence": first}, nil
}
func (s *Server) readEvents(ctx context.Context, db DB, sid string, after, until int64, limit int) ([]any, error) {
	rows, e := rowsJSON(ctx, db, `SELECT jsonb_build_object('version','weagent/1','id',id,'sessionId',session_id,'turnId',turn_id,'sequence',sequence,'createdAt',created_at,'type',type,'data',data) FROM session_events WHERE session_id=$1 AND sequence>$2 AND sequence<=$3 ORDER BY sequence LIMIT $4`, sid, after, until, limit)
	if e != nil {
		return nil, e
	}
	out := []any{}
	bytes := 0
	expected := after + 1
	for _, v := range rows {
		m := v.(M)
		if number(m, "sequence") != expected {
			return nil, apierr(410, "CURSOR_EXPIRED", "历史前缀已不可用")
		}
		size := len(canonical(m))
		if len(out) > 0 && bytes+size > 1<<20 {
			break
		}
		bytes += size
		out = append(out, m)
		expected++
	}
	if len(out) == 0 && after < until {
		return nil, apierr(410, "CURSOR_EXPIRED", "历史前缀已不可用")
	}
	return out, nil
}
func (s *Server) changesPage(ctx context.Context, r *http.Request, uid string) (M, error) {
	after, e := parseSequence(r.URL.Query().Get("after"))
	if e != nil {
		return nil, e
	}
	var last int64
	e = s.db.QueryRow(ctx, `SELECT last_revision FROM users WHERE id=$1`, uid).Scan(&last)
	if e != nil {
		return nil, e
	}
	until := last
	if value := r.URL.Query().Get("until"); value != "" {
		until, e = parseSequence(value)
		if e != nil {
			return nil, e
		}
	}
	if after > until || until > last {
		return nil, apierr(409, "CURSOR_AHEAD", "游标超过已提交变化")
	}
	limit, e := parseLimit(r.URL.Query().Get("limit"), 100, 200)
	if e != nil {
		return nil, e
	}
	changes, e := s.readChanges(ctx, uid, after, until, limit)
	if e != nil {
		return nil, e
	}
	next := after
	for _, v := range changes {
		next = number(v.(M), "revision")
	}
	return M{"after": after, "highWater": until, "changes": changes, "nextAfter": next, "hasMore": next < until}, nil
}
func (s *Server) readChanges(ctx context.Context, uid string, after, until int64, limit int) ([]any, error) {
	changes, e := rowsJSON(ctx, s.db, `SELECT jsonb_build_object('revision',revision,'resourceType',resource_type,'resourceId',resource_id,'action',action,'resourceRevision',resource_revision) FROM user_changes WHERE user_id=$1 AND revision>$2 AND revision<=$3 ORDER BY revision LIMIT $4`, uid, after, until, limit)
	if e != nil {
		return nil, e
	}
	expected := after + 1
	for _, v := range changes {
		if number(v.(M), "revision") != expected {
			return nil, apierr(410, "CURSOR_EXPIRED", "请重新加载 bootstrap")
		}
		expected++
	}
	if len(changes) == 0 && after < until {
		return nil, apierr(410, "CURSOR_EXPIRED", "请重新加载 bootstrap")
	}
	return changes, nil
}
func (s *Server) checkpoint(ctx context.Context, uid, sid string) (M, error) {
	if _, e := ownSession(ctx, s.db, uid, sid); e != nil {
		return nil, e
	}
	cid := id("checkpoint")
	expiry := s.now().Add(10 * time.Minute)
	var sequence, count, size int64
	var history string
	e := s.transaction(ctx, uid, func(tx pgx.Tx) error {
		if e := tx.QueryRow(ctx, `SELECT last_sequence,history_state FROM sessions WHERE id=$1 FOR UPDATE`, sid).Scan(&sequence, &history); e != nil {
			return e
		}
		if history == "purged" {
			return apierr(410, "HISTORY_PURGED", "正文已超过保留期")
		}
		if e := tx.QueryRow(ctx, `SELECT count(*),COALESCE(sum(pg_column_size(body)),0) FROM timeline_items WHERE session_id=$1`, sid).Scan(&count, &size); e != nil {
			return e
		}
		if count > 10000 || size > 32<<20 {
			return apierr(413, "PAYLOAD_TOO_LARGE", "历史基线超过当前服务配额")
		}
		if _, e := tx.Exec(ctx, `INSERT INTO checkpoints(id,session_id,sequence,item_count,expires_at) VALUES($1,$2,$3,$4,$5)`, cid, sid, sequence, count, expiry); e != nil {
			return e
		}
		_, e := tx.Exec(ctx, `INSERT INTO checkpoint_items(checkpoint_id,ordinal,body) SELECT $1,row_number() OVER(ORDER BY first_sequence,turn_id,item_id),body FROM timeline_items WHERE session_id=$2`, cid, sid)
		return e
	})
	if e != nil {
		return nil, e
	}
	return rowJSON(ctx, s.db, `SELECT jsonb_build_object('id',id,'sessionId',session_id,'sequence',sequence,'createdAt',created_at,'itemCount',item_count,'expiresAt',expires_at) FROM checkpoints WHERE id=$1`, cid)
}
func (s *Server) checkpointItems(ctx context.Context, r *http.Request, uid string) (M, error) {
	cid := r.PathValue("id")
	var sid string
	var seq, count int64
	var expiry time.Time
	e := s.db.QueryRow(ctx, `SELECT session_id,sequence,item_count,expires_at FROM checkpoints WHERE id=$1`, cid).Scan(&sid, &seq, &count, &expiry)
	if e != nil {
		return nil, e
	}
	if _, e = ownSession(ctx, s.db, uid, sid); e != nil {
		return nil, e
	}
	if !expiry.After(s.now()) {
		return nil, apierr(410, "CURSOR_EXPIRED", "checkpoint 已过期")
	}
	limit, e := parseLimit(r.URL.Query().Get("limit"), 50, 100)
	if e != nil {
		return nil, e
	}
	after := int64(0)
	if token := r.URL.Query().Get("pageToken"); token != "" {
		c, e := s.decodeCursor(token)
		if e != nil {
			return nil, e
		}
		if str(c, "user") != uid || str(c, "kind") != "checkpoint" || str(c, "id") != cid {
			return nil, invalid("checkpoint 游标不匹配")
		}
		after = number(c, "after")
	}
	rows, e := rowsJSON(ctx, s.db, `SELECT jsonb_build_object('ordinal',ordinal,'body',body) FROM checkpoint_items WHERE checkpoint_id=$1 AND ordinal>$2 ORDER BY ordinal LIMIT $3`, cid, after, limit)
	if e != nil {
		return nil, e
	}
	items := []any{}
	used := 0
	next := after
	for _, v := range rows {
		m := v.(M)
		body := object(m, "body")
		sz := len(canonical(body))
		if len(items) > 0 && used+sz > 1<<20 {
			break
		}
		used += sz
		items = append(items, body)
		next = number(m, "ordinal")
	}
	var token any
	if next < count {
		token = s.cursor(M{"user": uid, "kind": "checkpoint", "id": cid, "after": next, "expires": expiry.Unix()})
	}
	return M{"checkpointId": cid, "sequence": seq, "items": items, "nextPageToken": token}, nil
}

func (s *Server) appendEvent(ctx context.Context, tx pgx.Tx, uid, nid, sid, turn, kind string, data M) (int64, error) {
	var sequence, rev int64
	e := tx.QueryRow(ctx, `UPDATE sessions SET last_sequence=last_sequence+1,revision=revision+1,updated_at=$2 WHERE id=$1 RETURNING last_sequence,revision`, sid, s.now()).Scan(&sequence, &rev)
	if e != nil {
		return 0, e
	}
	if kind == "session.state" {
		data = clone(data)
		data["revision"] = rev
	}
	event := M{"id": id("event"), "version": "weagent/1", "sessionId": sid, "turnId": nullString(turn), "sequence": sequence, "createdAt": timestamp(s.now()), "type": kind, "data": data}
	if e = s.validator.Validate("SessionEvent", event); e != nil {
		return 0, invalid("事件内容无效")
	}
	if _, e = tx.Exec(ctx, `INSERT INTO session_events(session_id,sequence,id,node_id,turn_id,type,data,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, sid, sequence, str(event, "id"), nid, nullString(turn), kind, data, s.now()); e != nil {
		return 0, e
	}
	if e = s.projectItem(ctx, tx, sid, turn, kind, data, sequence); e != nil {
		return 0, e
	}
	return sequence, s.change(ctx, tx, uid, "session", sid, "upsert", rev)
}
func truncate(text string, max int) (string, bool) {
	r := []rune(text)
	if len(r) > max {
		return string(r[:max]), true
	}
	return text, false
}
func (s *Server) projectItem(ctx context.Context, tx pgx.Tx, sid, turn, kind string, data M, sequence int64) error {
	var item M
	switch kind {
	case "message.delta", "message.completed":
		var prev M
		old, e := rowJSON(ctx, tx, `SELECT body FROM timeline_items WHERE session_id=$1 AND turn_id=$2 AND item_id=$3`, sid, turn, str(data, "itemId"))
		if e != nil && e != pgx.ErrNoRows {
			return e
		}
		prev = old
		if kind == "message.delta" && truth(prev, "complete") {
			return nil
		}
		text := str(data, "text")
		if kind == "message.delta" {
			text = str(prev, "text") + text
		}
		text, cut := truncate(text, 48000)
		item = M{"type": "message", "itemId": str(data, "itemId"), "turnId": turn, "role": str(data, "role"), "text": text, "complete": kind == "message.completed", "truncated": cut || truth(data, "truncated") || (kind == "message.delta" && truth(prev, "truncated"))}
	case "item.upsert":
		item = data
		if str(item, "turnId") != turn {
			return invalid("item turnId 不匹配")
		}
		if str(item, "type") == "request" {
			var exists bool
			if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM interaction_requests WHERE id=$1 AND session_id=$2 AND turn_id=$3)`, str(item, "requestId"), sid, turn).Scan(&exists); e != nil {
				return e
			}
			if !exists {
				return invalid("未知请求引用")
			}
		}
		if str(item, "type") == "diff" {
			for _, v := range array(item, "files") {
				f := v.(M)
				var exists bool
				if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM diff_files WHERE id=$1 AND session_id=$2 AND turn_id=$3)`, str(f, "fileId"), sid, turn).Scan(&exists); e != nil {
					return e
				}
				if !exists {
					return invalid("未知 Diff 快照引用")
				}
			}
		}
	case "request.upsert":
		item = M{"type": "request", "itemId": str(data, "id"), "turnId": turn, "requestId": str(data, "id")}
	case "diff.file":
		if str(data, "sessionId") != sid {
			return invalid("Diff sessionId 不匹配")
		}
		var existing string
		e := tx.QueryRow(ctx, `SELECT session_id FROM diff_files WHERE id=$1`, str(data, "id")).Scan(&existing)
		if e != pgx.ErrNoRows {
			if e != nil {
				return e
			}
			return apierr(409, "SOURCE_CONFLICT", "Diff 快照 ID 不可变")
		}
		_, e = tx.Exec(ctx, `INSERT INTO diff_files(id,session_id,turn_id,item_id,display_path,language,patch,truncated,created_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, str(data, "id"), sid, turn, str(data, "itemId"), str(data, "path"), data["language"], str(data, "patch"), truth(data, "truncated"), parseTime(str(data, "createdAt")))
		return e
	default:
		return nil
	}
	if s.validator.Validate("TimelineItem", item) != nil {
		return invalid("时间线投影无效")
	}
	_, e := tx.Exec(ctx, `INSERT INTO timeline_items(session_id,turn_id,item_id,first_sequence,last_sequence,body) VALUES($1,$2,$3,$4,$4,$5) ON CONFLICT(session_id,turn_id,item_id) DO UPDATE SET last_sequence=EXCLUDED.last_sequence,body=EXCLUDED.body`, sid, turn, str(item, "itemId"), sequence, item)
	return e
}
