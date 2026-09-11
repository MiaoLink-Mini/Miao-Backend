package gateway

import (
	"context"
	"crypto/hmac"
	"github.com/jackc/pgx/v5"
	"time"
)

func (s *Server) nodeFrame(ctx context.Context, p *peer, m M) error {
	b := object(m, "data")
	switch str(m, "type") {
	case "node.heartbeat":
		if _, e := s.db.Exec(ctx, `UPDATE nodes SET last_seen_at=$2 WHERE id=$1 AND connection_epoch=$3`, p.node, s.now(), p.epoch); e != nil {
			return e
		}
		p.enqueue(frame("node.heartbeat", M{"epoch": p.epoch, "nonce": str(b, "nonce"), "serverTime": timestamp(s.now())}))
		return nil
	case "node.inventory":
		return s.inventory(ctx, p, b)
	case "command.ack", "command.status":
		return s.acknowledge(ctx, p, b, str(m, "type") == "command.status")
	case "node.events":
		for _, v := range array(b, "events") {
			if e := s.source(ctx, p, "event", v.(M)); e != nil {
				return e
			}
		}
		return nil
	case "node.session", "node.request", "node.request.cancel":
		return s.source(ctx, p, str(m, "type"), b)
	}
	return invalid("未知设备消息")
}
func (s *Server) inventory(ctx context.Context, p *peer, b M) error {
	iid := str(b, "inventoryId")
	if p.inventoryID != iid {
		p.inventoryID = iid
		p.projects = nil
		p.agents = nil
	}
	p.projects = append(p.projects, array(b, "projects")...)
	p.agents = append(p.agents, array(b, "agents")...)
	if len(p.projects) > 1000 || len(p.agents) > 1000 {
		p.projects = nil
		p.agents = nil
		return apierr(413, "PAYLOAD_TOO_LARGE", "设备目录超过配额")
	}
	if !truth(b, "complete") {
		return nil
	}
	projects, agents := p.projects, p.agents
	p.projects = nil
	p.agents = nil
	p.inventoryID = ""
	e := s.transaction(ctx, p.user, func(tx pgx.Tx) error {
		for _, group := range []struct {
			kind, table string
			items       []any
		}{{"project", "projects", projects}, {"agent", "agents", agents}} {
			ids := []string{}
			seen := map[string]bool{}
			for _, v := range group.items {
				item := v.(M)
				rid := str(item, "id")
				if seen[rid] {
					return invalid("目录 ID 重复")
				}
				seen[rid] = true
				ids = append(ids, rid)
				var owner string
				e := tx.QueryRow(ctx, "SELECT node_id FROM "+group.table+" WHERE id=$1", rid).Scan(&owner)
				if e != nil && e != pgx.ErrNoRows {
					return e
				}
				if owner != "" && owner != p.node {
					return notFound()
				}
				var rev int64
				if group.kind == "project" {
					e = tx.QueryRow(ctx, `INSERT INTO projects(id,node_id,name,description,branch,valid) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(id) DO UPDATE SET name=EXCLUDED.name,description=EXCLUDED.description,branch=EXCLUDED.branch,valid=EXCLUDED.valid,revision=projects.revision+1 RETURNING revision`, rid, p.node, str(item, "name"), str(item, "description"), item["branch"], truth(item, "valid")).Scan(&rev)
				} else {
					if owner != "" {
						var cr int64
						var caps M
						if e = tx.QueryRow(ctx, `SELECT capability_revision,capabilities FROM agents WHERE id=$1`, rid).Scan(&cr, &caps); e != nil {
							return e
						}
						if number(item, "capabilityRevision") < cr || number(item, "capabilityRevision") == cr && !equal(item["capabilities"], caps) {
							return apierr(409, "CAPABILITY_CHANGED", "能力修订不得回退或复用")
						}
					}
					e = tx.QueryRow(ctx, `INSERT INTO agents(id,node_id,name,state,version,adapter_version,capabilities,capability_revision) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(id) DO UPDATE SET name=EXCLUDED.name,state=EXCLUDED.state,version=EXCLUDED.version,adapter_version=EXCLUDED.adapter_version,capabilities=EXCLUDED.capabilities,capability_revision=EXCLUDED.capability_revision,revision=agents.revision+1 RETURNING revision`, rid, p.node, str(item, "name"), str(item, "state"), item["version"], str(item, "adapterVersion"), item["capabilities"], number(item, "capabilityRevision")).Scan(&rev)
				}
				if e != nil {
					return e
				}
				if e = s.change(ctx, tx, p.user, group.kind, rid, "upsert", rev); e != nil {
					return e
				}
			}
			set := "valid=false"
			if group.kind == "agent" {
				set = "state='unavailable'"
			}
			rows, e := rowsJSON(ctx, tx, "UPDATE "+group.table+" SET "+set+",revision=revision+1 WHERE node_id=$1 AND NOT(id=ANY($2::text[])) RETURNING jsonb_build_object('id',id,'revision',revision)", p.node, ids)
			if e != nil {
				return e
			}
			for _, v := range rows {
				x := v.(M)
				if e = s.change(ctx, tx, p.user, group.kind, str(x, "id"), "upsert", number(x, "revision")); e != nil {
					return e
				}
			}
		}
		return nil
	})
	if e != nil {
		return e
	}
	p.inventoryComplete = true
	return s.nodeReady(ctx, p)
}
func (s *Server) nodeReady(ctx context.Context, p *peer) error {
	ready := p.inventoryComplete && len(p.needsSync) == 0
	if p.ready == ready {
		return nil
	}
	p.ready = ready
	return s.transaction(ctx, p.user, func(tx pgx.Tx) error {
		var rev int64
		if e := tx.QueryRow(ctx, `UPDATE nodes SET revision=revision+1,last_seen_at=$2 WHERE id=$1 RETURNING revision`, p.node, s.now()).Scan(&rev); e != nil {
			return e
		}
		return s.change(ctx, tx, p.user, "node", p.node, "upsert", rev)
	})
}
func (s *Server) source(ctx context.Context, p *peer, kind string, b M) error {
	sid, turn := str(b, "sessionId"), str(b, "turnId")
	if kind == "node.session" {
		sid = str(object(b, "session"), "sessionId")
		turn = str(object(b, "session"), "turnId")
	}
	if kind == "node.request" {
		sid = str(object(b, "request"), "sessionId")
		turn = str(object(b, "request"), "turnId")
	}
	if nid, e := ownSession(ctx, s.db, p.user, sid); e != nil || nid != p.node {
		if e != nil {
			return e
		}
		return notFound()
	}
	hashInput := clone(b)
	delete(hashInput, "epoch")
	digest := hash(canonical(M{"kind": kind, "source": hashInput}))
	srcID, srcSeq := str(b, "sourceEventId"), number(b, "sourceSequence")
	var gatewaySeq int64
	e := s.transaction(ctx, p.user, func(tx pgx.Tx) error {
		var stored []byte
		var oldSID string
		var oldSeq int64
		e := tx.QueryRow(ctx, `SELECT source_hash,session_id,source_sequence,gateway_sequence FROM node_event_receipts WHERE node_id=$1 AND source_event_id=$2`, p.node, srcID).Scan(&stored, &oldSID, &oldSeq, &gatewaySeq)
		if e == nil {
			if !hmac.Equal(stored, digest) || oldSID != sid || oldSeq != srcSeq {
				return apierr(409, "SOURCE_CONFLICT", "source ID 被不同内容复用")
			}
			return nil
		}
		if e != pgx.ErrNoRows {
			return e
		}
		var lastSource int64
		var historyState string
		if e = tx.QueryRow(ctx, `SELECT last_source_sequence,history_state FROM sessions WHERE id=$1 FOR UPDATE`, sid).Scan(&lastSource, &historyState); e != nil {
			return e
		}
		purgedSnapshot := historyState == "purged" && kind == "node.session" && str(object(b, "session"), "state") == "closed"
		if historyState == "purged" && !purgedSnapshot {
			return apierr(410, "HISTORY_PURGED", "Purged history cannot accept new content")
		}
		if srcSeq != lastSource+1 {
			return apierr(409, "SOURCE_CONFLICT", "sourceSequence 不连续，请重发缺失段或对账")
		}
		if turn != "" {
			var exists bool
			if e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM turns WHERE id=$1 AND session_id=$2)`, turn, sid).Scan(&exists); e != nil {
				return e
			}
			if !exists && kind != "node.session" {
				return invalid("事件引用未知轮次")
			}
		}
		if purgedSnapshot {
			// A purged tombstone acknowledges closure without resurrecting a lost turn.
			if e = tx.QueryRow(ctx, `SELECT last_sequence FROM sessions WHERE id=$1 AND state='closed'`, sid).Scan(&gatewaySeq); e != nil {
				return e
			}
		} else {
			switch kind {
			case "event":
				gatewaySeq, e = s.appendEvent(ctx, tx, p.user, p.node, sid, turn, str(b, "type"), object(b, "data"))
			case "node.session":
				gatewaySeq, e = s.sessionUpdate(ctx, tx, p, sid, object(b, "session"))
			case "node.request":
				gatewaySeq, e = s.requestCreate(ctx, tx, p, object(b, "request"))
			case "node.request.cancel":
				gatewaySeq, e = s.requestCancel(ctx, tx, p, sid, turn, str(b, "requestId"))
			}
		}
		if e != nil {
			return e
		}
		if !purgedSnapshot {
			if _, e = tx.Exec(ctx, `UPDATE session_events SET source_event_id=$3,source_sequence=$4,source_hash=$5 WHERE session_id=$1 AND sequence=$2`, sid, gatewaySeq, srcID, srcSeq, digest); e != nil {
				return e
			}
		}
		if _, e = tx.Exec(ctx, `INSERT INTO node_event_receipts(node_id,source_event_id,session_id,source_sequence,source_hash,gateway_sequence) VALUES($1,$2,$3,$4,$5,$6)`, p.node, srcID, sid, srcSeq, digest, gatewaySeq); e != nil {
			return e
		}
		_, e = tx.Exec(ctx, `UPDATE sessions SET last_source_sequence=$2 WHERE id=$1`, sid, srcSeq)
		return e
	})
	if e != nil {
		return e
	}
	p.enqueue(frame("events.ack", M{"epoch": p.epoch, "sessionId": sid, "sourceSequence": srcSeq, "gatewaySequence": gatewaySeq}))
	if kind == "node.session" {
		delete(p.needsSync, sid)
		return s.nodeReady(ctx, p)
	}
	return nil
}
func validTransition(old, next string) bool {
	if old == next {
		return true
	}
	if old == "closed" {
		return false
	}
	if next == "closed" {
		return true
	}
	switch old {
	case "idle":
		return next == "running" || next == "failed"
	case "running":
		return next == "waiting_approval" || next == "waiting_input" || next == "cancelling" || next == "completed" || next == "cancelled" || next == "failed"
	case "waiting_approval", "waiting_input":
		return next == "running" || next == "cancelling" || next == "completed" || next == "cancelled" || next == "failed"
	case "cancelling":
		return next == "cancelled" || next == "completed" || next == "failed"
	}
	return false
}
func (s *Server) sessionUpdate(ctx context.Context, tx pgx.Tx, p *peer, sid string, b M) (int64, error) {
	old, e := s.getResource(ctx, tx, p.user, "sessions", sid)
	if e != nil {
		return 0, e
	}
	oldTurn, newTurn := str(old, "turnId"), str(b, "turnId")
	state := str(b, "state")
	if newTurn == "" {
		return 0, invalid("已建立会话不能移除 turnId")
	}
	if newTurn != oldTurn {
		if isActive(str(old, "state")) || str(old, "state") == "closed" || state != "running" {
			return 0, apierr(409, "STALE_TURN", "不能在当前状态开始另一轮")
		}
		var payload M
		var opID, opKind, opState string
		e = tx.QueryRow(ctx, `SELECT payload,id,kind,state FROM operations WHERE user_id=$1 AND session_id=$2 AND payload->>'nextTurnId'=$3 AND (kind IN ('send','queue') OR (kind='native' AND (payload->'payload'->'control'->>'action'='compact' OR (payload->'payload'->'control'->>'action'='workspace' AND payload->'payload'->'control'->'request'->>'kind'='review')))) AND state IN ('accepted','delivered','confirmed','reconciling')`, p.user, sid, newTurn).Scan(&payload, &opID, &opKind, &opState)
		if e != nil {
			return 0, apierr(409, "STALE_TURN", "新轮次没有已接受的预留命令")
		}
		if opKind == "queue" && str(old, "state") != "completed" {
			return 0, apierr(409, "STALE_TURN", "队列不能在取消或失败后自动执行")
		}
		if opKind == "queue" && opState != "confirmed" {
			return 0, apierr(409, "OPERATION_UNKNOWN", "队列入队尚未确认")
		}
		if opKind == "queue" {
			var head string
			if e = tx.QueryRow(ctx, `SELECT next_turn_id FROM queue_items WHERE session_id=$1 AND state IN('queued','starting') ORDER BY position LIMIT 1`, sid).Scan(&head); e != nil || head != newTurn {
				return 0, invalid("只能消费队首消息")
			}
		}
		if _, e = tx.Exec(ctx, `INSERT INTO turns(id,session_id,state,started_at) VALUES($1,$2,'running',$3)`, newTurn, sid, s.now()); e != nil {
			return 0, e
		}
		if _, e = tx.Exec(ctx, `UPDATE queue_items SET state='consumed' WHERE session_id=$1 AND next_turn_id=$2`, sid, newTurn); e != nil {
			return 0, e
		}
	} else if !validTransition(str(old, "state"), state) {
		var authorized bool
		if str(old, "state") == "closed" && state == "completed" {
			e = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM operations WHERE user_id=$1 AND session_id=$2 AND kind='native' AND state IN ('accepted','delivered','reconciling') AND payload->'payload'->>'expectedTurnId'=$3 AND payload->'payload'->'control'->>'action'='workspace' AND payload->'payload'->'control'->'request'->>'kind'='resume')`, p.user, sid, newTurn).Scan(&authorized)
			if e != nil {
				return 0, e
			}
		}
		if !authorized {
			return 0, apierr(409, "STALE_TURN", "无效的轮次状态迁移")
		}
	}
	cr := number(b, "capabilityRevision")
	if cr < number(old, "capabilityRevision") || cr == number(old, "capabilityRevision") && !equal(b["capabilities"], old["capabilities"]) {
		return 0, apierr(409, "CAPABILITY_CHANGED", "会话能力修订不匹配")
	}
	turnState := state
	if state == "idle" {
		turnState = "starting"
	}
	if state == "closed" {
		turnState = "failed"
		if str(old, "state") == "closed" {
			// Closure snapshots are replayed on reconnect. "closed" is a session
			// state, not a legal turn state; preserve the original terminal result.
			if e = tx.QueryRow(ctx, `SELECT state FROM turns WHERE id=$1 AND session_id=$2`, newTurn, sid).Scan(&turnState); e != nil {
				return 0, e
			}
		} else if !isActive(str(old, "state")) {
			turnState = str(old, "state")
		}
	}
	if _, e = tx.Exec(ctx, `UPDATE turns SET state=$2,started_at=CASE WHEN $2='running' THEN COALESCE(started_at,$3) ELSE started_at END,ended_at=CASE WHEN $2 IN ('completed','cancelled','failed') THEN COALESCE(ended_at,$3) ELSE ended_at END WHERE id=$1`, newTurn, turnState, s.now()); e != nil {
		return 0, e
	}
	if _, e = tx.Exec(ctx, `UPDATE sessions SET current_turn_id=$2,state=$3,capabilities=$4,capability_revision=$5 WHERE id=$1`, sid, newTurn, state, b["capabilities"], cr); e != nil {
		return 0, e
	}
	if e = s.queueMirror(ctx, tx, p, sid, array(b, "queue")); e != nil {
		return 0, e
	}
	seq, e := s.appendEvent(ctx, tx, p.user, p.node, sid, newTurn, "session.state", M{"state": state, "turnId": newTurn, "revision": number(old, "revision")})
	if e != nil {
		return 0, e
	}
	if !equal(old["capabilities"], b["capabilities"]) || cr != number(old, "capabilityRevision") {
		seq, e = s.appendEvent(ctx, tx, p.user, p.node, sid, newTurn, "capabilities.updated", M{"capabilities": b["capabilities"], "capabilityRevision": cr})
		if e != nil {
			return 0, e
		}
	}
	if !equal(old["queue"], b["queue"]) {
		seq, e = s.appendEvent(ctx, tx, p.user, p.node, sid, newTurn, "queue.updated", M{"items": b["queue"]})
		if e != nil {
			return 0, e
		}
	}
	if !isActive(state) || state == "cancelling" {
		rs, e := rowsJSON(ctx, tx, `SELECT jsonb_build_object('id',id) FROM interaction_requests WHERE session_id=$1 AND state='pending'`, sid)
		if e != nil {
			return 0, e
		}
		for _, v := range rs {
			seq, e = s.requestCancel(ctx, tx, p, sid, oldTurn, str(v.(M), "id"))
			if e != nil {
				return 0, e
			}
		}
	}
	if state != str(old, "state") && (state == "completed" || state == "failed") {
		if e = s.notify(ctx, tx, p.user, sid, state, "会话状态已更新"); e != nil {
			return 0, e
		}
	}
	return seq, nil
}
func (s *Server) queueMirror(ctx context.Context, tx pgx.Tx, p *peer, sid string, items []any) error {
	seen := map[string]bool{}
	ids := []string{}
	for _, v := range items {
		q := v.(M)
		qid := str(q, "id")
		op := str(q, "operationId")
		if seen[qid] {
			return invalid("队列 ID 重复")
		}
		seen[qid] = true
		ids = append(ids, qid)
		var payload M
		var state string
		if e := tx.QueryRow(ctx, `SELECT payload,state FROM operations WHERE user_id=$1 AND id=$2 AND session_id=$3 AND kind='queue'`, p.user, op, sid).Scan(&payload, &state); e != nil {
			return invalid("队列引用未知操作")
		}
		if state == "failed" || str(object(payload, "payload"), "text") != str(q, "text") {
			return invalid("队列正文与原命令不匹配")
		}
		var owner, oldOp, oldText, oldState string
		e := tx.QueryRow(ctx, `SELECT session_id,operation_id,text_content,state FROM queue_items WHERE id=$1`, qid).Scan(&owner, &oldOp, &oldText, &oldState)
		if e != nil && e != pgx.ErrNoRows {
			return e
		}
		if owner != "" && owner != sid {
			return notFound()
		}
		if owner != "" && (oldOp != op || oldText != str(q, "text") || oldState == "consumed" || oldState == "cancelled") {
			return apierr(409, "SOURCE_CONFLICT", "队列项不可替换或重复消费")
		}
		_, e = tx.Exec(ctx, `INSERT INTO queue_items(id,user_id,session_id,operation_id,next_turn_id,position,text_content,state,created_at) VALUES($1,$2,$3,$4,$5,(SELECT COALESCE(max(position),0)+1 FROM queue_items WHERE session_id=$3),$6,$7,$8) ON CONFLICT(id) DO UPDATE SET state=EXCLUDED.state`, qid, p.user, sid, op, str(payload, "nextTurnId"), str(q, "text"), str(q, "state"), parseTime(str(q, "createdAt")))
		if e != nil {
			return e
		}
	}
	var missing bool
	if e := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM queue_items WHERE session_id=$1 AND state IN ('queued','starting') AND NOT(id=ANY($2::text[])))`, sid, ids).Scan(&missing); e != nil {
		return e
	}
	if missing {
		return invalid("队列不能静默丢弃尚未消费的消息")
	}
	ordered, e := rowsJSON(ctx, tx, `SELECT jsonb_build_object('id',id) FROM queue_items WHERE session_id=$1 AND state IN('queued','starting') ORDER BY position`, sid)
	if e != nil {
		return e
	}
	for i, v := range ordered {
		if i >= len(ids) || str(v.(M), "id") != ids[i] {
			return invalid("队列顺序不可修改")
		}
	}
	return nil
}
func validateQuestions(b M) error {
	unique := func(values []any) bool {
		seen := map[string]bool{}
		for _, v := range values {
			x := str(v.(M), "id")
			if seen[x] {
				return false
			}
			seen[x] = true
		}
		return true
	}
	if str(b, "kind") == "approval" {
		if !unique(array(b, "choices")) {
			return invalid("审批选项 ID 重复")
		}
		return nil
	}
	if !unique(array(b, "questions")) {
		return invalid("问题 ID 重复")
	}
	for _, v := range array(b, "questions") {
		q := v.(M)
		if !unique(array(q, "options")) {
			return invalid("选项 ID 重复")
		}
		if str(q, "type") == "multiple" && number(q, "max") > int64(len(array(q, "options"))) {
			return invalid("多选上限超过选项数")
		}
	}
	return nil
}
func (s *Server) requestCreate(ctx context.Context, tx pgx.Tx, p *peer, b M) (int64, error) {
	sid, turn := str(b, "sessionId"), str(b, "turnId")
	session, e := s.getResource(ctx, tx, p.user, "sessions", sid)
	if e != nil {
		return 0, e
	}
	if turn != str(session, "turnId") || !isActive(str(session, "state")) || str(session, "state") == "cancelling" {
		return 0, apierr(409, "STALE_TURN", "请求不属于可交互的当前轮")
	}
	if !truth(object(session, "capabilities"), str(b, "kind")) {
		return 0, apierr(409, "CAPABILITY_UNSUPPORTED", "会话未声明此请求能力")
	}
	if e = validateQuestions(b); e != nil {
		return 0, e
	}
	created, expiry := parseTime(str(b, "createdAt")), parseTime(str(b, "expiresAt"))
	if !expiry.After(created) || !expiry.After(s.now()) || created.After(s.now().Add(time.Minute)) {
		return 0, apierr(410, "REQUEST_EXPIRED", "请求时间无效或已过期")
	}
	var existing M
	e = tx.QueryRow(ctx, `SELECT definition FROM interaction_requests WHERE id=$1`, str(b, "id")).Scan(&existing)
	if e == nil {
		if !equal(existing, b) {
			return 0, apierr(409, "SOURCE_CONFLICT", "请求定义不可变")
		}
	} else if e == pgx.ErrNoRows {
		_, e = tx.Exec(ctx, `INSERT INTO interaction_requests(id,session_id,turn_id,user_id,kind,state,definition,created_at,expires_at) VALUES($1,$2,$3,$4,$5,'pending',$6,$7,$8)`, str(b, "id"), sid, turn, p.user, str(b, "kind"), b, created, expiry)
		if e != nil {
			return 0, e
		}
		if e = s.notify(ctx, tx, p.user, sid, str(b, "kind"), "Agent 有待处理请求"); e != nil {
			return 0, e
		}
	} else {
		return 0, e
	}
	rr, e := s.getResource(ctx, tx, p.user, "requests", str(b, "id"))
	if e != nil {
		return 0, e
	}
	if e = s.change(ctx, tx, p.user, "request", str(b, "id"), "upsert", number(rr, "revision")); e != nil {
		return 0, e
	}
	return s.appendEvent(ctx, tx, p.user, p.node, sid, turn, "request.upsert", rr)
}
func (s *Server) requestCancel(ctx context.Context, tx pgx.Tx, p *peer, sid, turn, rid string) (int64, error) {
	rr, e := s.getResource(ctx, tx, p.user, "requests", rid)
	if e != nil {
		return 0, e
	}
	if str(rr, "sessionId") != sid || str(rr, "turnId") != turn {
		return 0, apierr(409, "STALE_TURN", "请求轮次不匹配")
	}
	// In-flight decisions stay deciding until their journal can prove acceptance/rejection.
	if str(rr, "state") == "pending" {
		var rev int64
		e = tx.QueryRow(ctx, `UPDATE interaction_requests SET state='cancelled',revision=revision+1 WHERE id=$1 RETURNING revision`, rid).Scan(&rev)
		if e != nil {
			return 0, e
		}
		if e = s.change(ctx, tx, p.user, "request", rid, "upsert", rev); e != nil {
			return 0, e
		}
		rr, e = s.getResource(ctx, tx, p.user, "requests", rid)
		if e != nil {
			return 0, e
		}
	}
	return s.appendEvent(ctx, tx, p.user, p.node, sid, turn, "request.upsert", rr)
}

// Only a failed create with no committed Node source can release its sync fence
// without a native snapshot. Delivered/unknown work and real failed turns cannot.
func (s *Server) releaseUnstartedSync(ctx context.Context, p *peer, sid string) error {
	if !p.needsSync[sid] {
		return nil
	}
	var neverStarted bool
	if e := s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM sessions se WHERE se.id=$1 AND se.node_id=$2 AND se.state='failed' AND se.last_source_sequence=0 AND EXISTS(SELECT 1 FROM operations op WHERE op.session_id=se.id AND op.kind='create' AND op.state='failed'))`, sid, p.node).Scan(&neverStarted); e != nil {
		return e
	}
	if neverStarted {
		delete(p.needsSync, sid)
		return s.nodeReady(ctx, p)
	}
	return nil
}

func (s *Server) acknowledge(ctx context.Context, p *peer, b M, isQuery bool) error {
	op, state := str(b, "operationId"), str(b, "state")
	operation, e := s.operation(ctx, s.db, p.user, op)
	if e != nil {
		return e
	}
	var nid, sid string
	var cmd M
	e = s.db.QueryRow(ctx, `SELECT node_id,COALESCE(session_id,''),payload FROM operations WHERE user_id=$1 AND id=$2`, p.user, op).Scan(&nid, &sid, &cmd)
	if e != nil {
		return e
	}
	if nid != p.node {
		return notFound()
	}
	if str(operation, "state") == "confirmed" || str(operation, "state") == "failed" {
		if state == "confirmed" && !equal(operation["result"], b["result"]) {
			return apierr(409, "SOURCE_CONFLICT", "最终回执不可变")
		}
		return s.releaseUnstartedSync(ctx, p, sid)
	}
	if !isQuery && number(cmd, "nodeEpoch") != p.epoch {
		return apierr(409, "PROTOCOL_UNSUPPORTED", "旧连接命令只能查询对账")
	}
	if state == "not_seen" {
		if !isQuery {
			return invalid("not_seen 仅用于 journal 查询")
		}
		var delivery string
		if e := s.db.QueryRow(ctx, `SELECT state FROM command_outbox WHERE user_id=$1 AND operation_id=$2`, p.user, op).Scan(&delivery); e != nil {
			return e
		}
		if delivery == "delivered" {
			if e := s.transaction(ctx, p.user, func(tx pgx.Tx) error { return s.finishOp(ctx, tx, p.user, op, "reconciling", nil, nil) }); e != nil {
				return e
			}
			return apierr(409, "SOURCE_CONFLICT", "journal 丢失了此前已确认送达的操作，禁止重发")
		}
		if e := s.failUnsent(ctx, p.user, op, apierr(409, "OPERATION_UNKNOWN", "Node journal 确认未接收此命令")); e != nil {
			return e
		}
		return s.releaseUnstartedSync(ctx, p, sid)
	}
	if state == "unknown" {
		return s.transaction(ctx, p.user, func(tx pgx.Tx) error { return s.finishOp(ctx, tx, p.user, op, "reconciling", nil, nil) })
	}
	if state == "delivered" {
		return s.transaction(ctx, p.user, func(tx pgx.Tx) error {
			if _, e := tx.Exec(ctx, `UPDATE command_outbox SET state='delivered' WHERE user_id=$1 AND operation_id=$2`, p.user, op); e != nil {
				return e
			}
			return s.finishOp(ctx, tx, p.user, op, "delivered", nil, nil)
		})
	}
	if state == "rejected" {
		eb := object(b, "error")
		if e := s.failUnsent(ctx, p.user, op, &APIError{Status: 409, Code: str(eb, "code"), Message: "设备拒绝此操作", Retry: truth(eb, "retryable")}); e != nil {
			return e
		}
		return s.releaseUnstartedSync(ctx, p, sid)
	}
	result := object(b, "result")
	if str(result, "sessionId") != sid || str(result, "nodeId") != p.node {
		return invalid("回执资源与命令不匹配")
	}
	kind := str(operation, "kind")
	expectedTurn := str(object(cmd, "payload"), "expectedTurnId")
	if kind == "create" {
		expectedTurn = str(cmd, "turnId")
	}
	if kind == "send" || kind == "native" && str(cmd, "nextTurnId") != "" {
		expectedTurn = str(cmd, "nextTurnId")
	}
	if str(result, "turnId") != expectedTurn {
		return invalid("回执轮次与命令不匹配")
	}
	if kind == "respond" && str(result, "requestId") != str(cmd, "requestId") {
		return invalid("回执请求不匹配")
	}
	if kind != "respond" && result["requestId"] != nil {
		return invalid("回执含额外请求")
	}
	if kind != "queue" && result["queueItemId"] != nil {
		return invalid("回执含额外队列项")
	}
	if str(cmd, "kind") == "history" {
		native := object(result, "native")
		if str(native, "action") != "workspace" || str(native, "requestKind") != str(object(object(cmd, "payload"), "request"), "kind") {
			return invalid("Project history receipt mismatch")
		}
	} else if kind == "native" {
		control := object(object(cmd, "payload"), "control")
		native := object(result, "native")
		if str(control, "action") == "workspace" && str(native, "requestKind") != str(object(control, "request"), "kind") {
			return invalid("Workspace receipt does not match the requested operation")
		}
		if str(native, "action") != str(control, "action") || str(control, "action") == "set_model" && str(native, "modelId") != str(control, "modelId") {
			return invalid("原生操作回执与命令不匹配")
		}
	} else if result["native"] != nil {
		return invalid("回执含额外原生操作结果")
	}
	return s.transaction(ctx, p.user, func(tx pgx.Tx) error {
		if kind == "queue" {
			qid := str(result, "queueItemId")
			if qid == "" {
				return invalid("排队回执缺少队列 ID")
			}
			items := []any{M{"id": qid, "operationId": op, "text": str(object(cmd, "payload"), "text"), "state": "queued", "createdAt": timestamp(s.now())}}
			current, e := s.getResource(ctx, tx, p.user, "sessions", sid)
			if e != nil {
				return e
			}
			found := false
			for _, v := range array(current, "queue") {
				if str(v.(M), "id") == qid {
					found = true
				}
			}
			if !found {
				items = append(array(current, "queue"), items...)
				if len(items) > 20 {
					return invalid("队列超过配额")
				}
				if e = s.queueMirror(ctx, tx, p, sid, items); e != nil {
					return e
				}
				if _, e = s.appendEvent(ctx, tx, p.user, p.node, sid, str(current, "turnId"), "queue.updated", M{"items": items}); e != nil {
					return e
				}
			}
		}
		if kind == "respond" {
			rid := str(cmd, "requestId")
			var rev int64
			e := tx.QueryRow(ctx, `UPDATE interaction_requests SET state='resolved',resolved_at=$2,revision=revision+1 WHERE id=$1 AND decision_operation_id=$3 RETURNING revision`, rid, s.now(), op).Scan(&rev)
			if e != nil {
				return e
			}
			if e = s.change(ctx, tx, p.user, "request", rid, "upsert", rev); e != nil {
				return e
			}
			rr, e := s.getResource(ctx, tx, p.user, "requests", rid)
			if e != nil {
				return e
			}
			if _, e = s.appendEvent(ctx, tx, p.user, p.node, sid, str(rr, "turnId"), "request.upsert", rr); e != nil {
				return e
			}
		}
		if kind == "cancel" {
			current, e := s.getResource(ctx, tx, p.user, "sessions", sid)
			if e != nil {
				return e
			}
			if str(current, "turnId") == expectedTurn && isActive(str(current, "state")) && str(current, "state") != "cancelling" {
				if _, e = tx.Exec(ctx, `UPDATE sessions SET state='cancelling' WHERE id=$1`, sid); e != nil {
					return e
				}
				if _, e = tx.Exec(ctx, `UPDATE turns SET state='cancelling' WHERE id=$1`, expectedTurn); e != nil {
					return e
				}
				if _, e = s.appendEvent(ctx, tx, p.user, p.node, sid, expectedTurn, "session.state", M{"state": "cancelling", "turnId": expectedTurn, "revision": number(current, "revision")}); e != nil {
					return e
				}
			}
		}
		if _, e := tx.Exec(ctx, `UPDATE command_outbox SET state='delivered' WHERE user_id=$1 AND operation_id=$2`, p.user, op); e != nil {
			return e
		}
		if e := s.finishOp(ctx, tx, p.user, op, "confirmed", result, nil); e != nil {
			return e
		}
		return s.audit(ctx, tx, p.user, op, kind, "confirmed", p.node, sid)
	})
}
