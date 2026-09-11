package gateway

import (
	"context"
	"github.com/jackc/pgx/v5"
	"net/http"
	"strings"
	"unicode/utf8"
)

func isActive(state string) bool {
	return state == "running" || state == "waiting_approval" || state == "waiting_input" || state == "cancelling" || state == "idle"
}
func capability(session M, key string, revision int64) error {
	if str(session, "historyState") == "purged" {
		return apierr(410, "HISTORY_PURGED", "历史会话已清理，请创建新会话")
	}
	if str(session, "mode") == "readonly" {
		return apierr(409, "READ_ONLY", "当前会话只读")
	}
	if str(session, "state") == "closed" {
		return apierr(409, "STALE_TURN", "原生进程已关闭")
	}
	if number(session, "capabilityRevision") != revision {
		return apierr(409, "CAPABILITY_CHANGED", "能力已更新，请刷新")
	}
	if !truth(object(session, "capabilities"), key) {
		return apierr(409, "CAPABILITY_UNSUPPORTED", "当前会话不支持此操作")
	}
	return nil
}
func (s *Server) command(ctx context.Context, r *http.Request, uid, name string, b M) (M, error) {
	op := r.Header.Get("Idempotency-Key")
	fp := requestFingerprint(r, b)
	prior, e := s.priorOp(ctx, uid, op, fp)
	if e != nil || prior != nil {
		return prior, e
	}
	nid, sid, turn := "", r.PathValue("id"), ""
	kind := ""
	var session, request, agent M
	if name == "createSession" || name == "projectHistory" {
		nid = str(b, "nodeId")
		if e = ownNode(ctx, s.db, uid, nid, false); e != nil {
			return nil, e
		}
		project, e := s.getResource(ctx, s.db, uid, "projects", str(b, "projectId"))
		if e != nil {
			return nil, e
		}
		if str(project, "nodeId") != nid || !truth(project, "valid") {
			return nil, invalid("项目不可用或不属于当前设备")
		}
		agent, e = s.getResource(ctx, s.db, uid, "agents", str(b, "agentId"))
		if e != nil {
			return nil, e
		}
		if str(agent, "nodeId") != nid || str(agent, "state") != "ready" {
			return nil, invalid("Agent 不可用或不属于当前设备")
		}
		if number(agent, "capabilityRevision") != number(b, "capabilityRevision") {
			return nil, apierr(409, "CAPABILITY_CHANGED", "Agent 能力已更新")
		}
		if name == "projectHistory" && !truth(object(agent, "capabilities"), "historyImport") {
			return nil, apierr(409, "CAPABILITY_UNSUPPORTED", "Agent does not expose project history")
		}
		if !truth(object(agent, "capabilities"), "send") {
			return nil, apierr(409, "CAPABILITY_UNSUPPORTED", "Agent 不支持托管输入")
		}
		if (truth(b, "historyOnly") && len(object(b, "origin")) > 0 || str(object(b, "origin"), "mode") == "import") && !truth(object(agent, "capabilities"), "historyImport") {
			return nil, apierr(409, "CAPABILITY_UNSUPPORTED", "Agent does not support native history import")
		}
		for _, source := range []string{"origin", "preset"} {
			origin := object(b, source)
			if len(origin) == 0 {
				continue
			}
			if source == "origin" && truth(origin, "projectHistory") {
				continue
			}
			parent, err := s.getResource(ctx, s.db, uid, "sessions", str(origin, "sessionId"))
			if err != nil {
				return nil, err
			}
			if str(parent, "nodeId") != nid || str(parent, "projectId") != str(b, "projectId") || str(parent, "agentId") != str(b, "agentId") || str(parent, "historyState") == "purged" {
				return nil, apierr(403, "FORBIDDEN", "History origin must belong to the same project and Agent")
			}
		}
		if origin := object(b, "origin"); len(origin) != 0 && str(origin, "mode") != "import" {
			actualSource, err := s.getResource(ctx, s.db, uid, "sessions", str(origin, "sourceSessionId"))
			if err != nil {
				return nil, err
			}
			if str(actualSource, "nodeId") != nid || str(actualSource, "projectId") != str(b, "projectId") || str(actualSource, "agentId") != str(b, "agentId") || str(actualSource, "historyState") == "purged" {
				return nil, apierr(403, "FORBIDDEN", "Native history source must have the same authorized project and Agent")
			}
		}
		kind = "create"
		sid = id("session")
		turn = id("turn")
		if name == "projectHistory" {
			kind = "native"
			sid = ""
			turn = ""
		}
	} else {
		if name == "respondRequest" {
			request, e = s.getResource(ctx, s.db, uid, "requests", sid)
			if e != nil {
				return nil, e
			}
			sid = str(request, "sessionId")
		}
		session, e = s.getResource(ctx, s.db, uid, "sessions", sid)
		if e != nil {
			return nil, e
		}
		nid = str(session, "nodeId")
		turn = str(session, "turnId")
		if name == "respondRequest" && request["decision"] != nil {
			if equal(request["decision"], b["decision"]) {
				return s.operation(ctx, s.db, uid, str(request, "decisionOperationId"))
			}
			return nil, apierr(409, "REQUEST_ALREADY_DECIDED", "该请求已有其他决定")
		}
		if turn != str(b, "expectedTurnId") || turn == "" {
			return nil, apierr(409, "STALE_TURN", "原轮次已结束，请刷新")
		}
		switch name {
		case "nativeControl":
			kind = "native"
			if str(object(b, "control"), "action") == "workspace" {
				if e = s.workspaceGate(session, b); e != nil {
					return nil, e
				}
				break
			}
			if isActive(str(session, "state")) {
				return nil, apierr(409, "STALE_TURN", "请等待本轮结束后操作原生配置")
			}
			key := "models"
			if str(object(b, "control"), "action") == "compact" {
				key = "compact"
			}
			if e = capability(session, key, number(b, "capabilityRevision")); e != nil {
				return nil, e
			}
		case "sendMessage":
			kind = str(b, "mode")
			if kind == "queue" {
				if !isActive(str(session, "state")) || str(session, "state") == "cancelling" || str(session, "state") == "idle" {
					return nil, apierr(409, "STALE_TURN", "当前状态不能排队")
				}
				if len(array(session, "queue")) >= 20 {
					return nil, apierr(409, "CAPABILITY_UNSUPPORTED", "队列已满")
				}
			} else if isActive(str(session, "state")) {
				return nil, apierr(409, "STALE_TURN", "当前轮仍在执行，请明确选择排队")
			}
			if e = capability(session, kind, number(b, "capabilityRevision")); e != nil {
				return nil, e
			}
		case "cancelTurn":
			kind = "cancel"
			if !isActive(str(session, "state")) || str(session, "state") == "idle" {
				return nil, apierr(409, "STALE_TURN", "当前轮不可取消")
			}
			if e = capability(session, "cancel", number(b, "capabilityRevision")); e != nil {
				return nil, e
			}
		case "respondRequest":
			kind = "respond"
			if str(request, "turnId") != turn {
				return nil, apierr(409, "STALE_TURN", "请求不属于当前轮")
			}
			if str(request, "state") == "expired" || !parseTime(str(request, "expiresAt")).After(s.now()) {
				return nil, apierr(410, "REQUEST_EXPIRED", "请求已过期")
			}
			if str(request, "state") != "pending" || str(session, "state") == "cancelling" {
				return nil, apierr(409, "REQUEST_ALREADY_DECIDED", "请求正在处理或已经结束")
			}
			if number(request, "revision") != number(b, "requestRevision") {
				return nil, apierr(409, "REQUEST_ALREADY_DECIDED", "请求已变更，请刷新")
			}
			if e = capability(session, str(request, "kind"), number(b, "capabilityRevision")); e != nil {
				return nil, e
			}
			if e = validateDecision(request, object(b, "decision")); e != nil {
				return nil, e
			}
		}
	}
	if !s.online(nid) {
		return nil, apierr(503, "NODE_OFFLINE", "设备离线或尚未完成状态同步")
	}
	p := s.nodes[nid]
	deadline := s.now().Add(s.cfg.CommandTTL)
	// Serialize incompatible controls while their acceptance is still unknown.
	if name != "createSession" && name != "projectHistory" {
		var busy bool
		e = s.db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM operations WHERE session_id=$1 AND state IN ('accepted','delivered','reconciling') AND kind IN ('create','send','cancel','respond','native'))`, sid).Scan(&busy)
		if e != nil {
			return nil, e
		}
		if busy {
			return nil, apierr(409, "OPERATION_UNKNOWN", "当前会话有待确认操作，请先查询回执")
		}
	}
	cmd := M{"operationId": op, "nodeEpoch": p.epoch, "deadlineAt": timestamp(deadline), "kind": kind, "sessionId": sid, "payload": b}
	if kind == "create" {
		cmd["turnId"] = turn
	}
	if kind == "send" || kind == "queue" {
		cmd["kind"] = "send"
		cmd["nextTurnId"] = id("turn")
	}
	if kind == "respond" {
		cmd["requestId"] = str(request, "id")
	}
	if kind == "native" {
		cmd["nextTurnId"] = nil
		if str(object(b, "control"), "action") == "compact" || workspaceTurn(object(b, "control")) {
			cmd["nextTurnId"] = id("turn")
		}
	}
	if name == "projectHistory" {
		cmd["kind"] = "history"
		delete(cmd, "sessionId")
		delete(cmd, "nextTurnId")
	}
	if e = s.validator.Validate("NodeCommand", cmd); e != nil {
		return nil, e
	}
	e = s.transaction(ctx, uid, func(tx pgx.Tx) error {
		if kind == "create" {
			title, _ := truncate(strings.TrimSpace(str(b, "prompt")), 200)
			if truth(b, "historyOnly") {
				title = "新会话"
				if origin := object(b, "origin"); str(origin, "mode") == "resume" {
					if err := tx.QueryRow(ctx, `SELECT title FROM sessions WHERE id=$1 AND user_id=$2`, str(origin, "sourceSessionId"), uid).Scan(&title); err != nil {
						return err
					}
				}
			}
			_, e := tx.Exec(ctx, `INSERT INTO sessions(id,user_id,node_id,project_id,agent_id,title,state,mode,capabilities,capability_revision) VALUES($1,$2,$3,$4,$5,$6,'idle','managed',$7,$8)`, sid, uid, nid, str(b, "projectId"), str(b, "agentId"), title, agent["capabilities"], number(agent, "capabilityRevision"))
			if e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `INSERT INTO turns(id,session_id,state) VALUES($1,$2,'starting')`, turn, sid); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE sessions SET current_turn_id=$2 WHERE id=$1`, sid, turn); e != nil {
				return e
			}
			if origin := object(b, "origin"); len(origin) != 0 {
				var parent any = str(origin, "sourceSessionId")
				if str(origin, "mode") == "import" {
					parent = nil
				}
				if _, e = tx.Exec(ctx, `UPDATE sessions SET parent_session_id=$2,origin_mode=$3,origin_point=$4 WHERE id=$1`, sid, parent, str(origin, "mode"), origin["pointLabel"]); e != nil {
					return e
				}
			}
			if e = s.change(ctx, tx, uid, "session", sid, "upsert", 1); e != nil {
				return e
			}
		}
		if e := s.insertOp(ctx, tx, uid, op, kind, nid, sid, turn, p.epoch, fp, cmd, deadline); e != nil {
			return e
		}
		if e := s.recordShareAdmission(ctx, tx, uid, op); e != nil {
			return e
		}
		if _, e := tx.Exec(ctx, `INSERT INTO command_outbox(user_id,operation_id,node_id,node_epoch,state,deadline_at) VALUES($1,$2,$3,$4,'pending',$5)`, uid, op, nid, p.epoch, deadline); e != nil {
			return e
		}
		if kind == "respond" {
			var rev int64
			e := tx.QueryRow(ctx, `UPDATE interaction_requests SET state='deciding',decision=$2,decision_operation_id=$3,revision=revision+1 WHERE id=$1 RETURNING revision`, str(request, "id"), b["decision"], op).Scan(&rev)
			if e != nil {
				return e
			}
			if e = s.change(ctx, tx, uid, "request", str(request, "id"), "upsert", rev); e != nil {
				return e
			}
			rr, e := s.getResource(ctx, tx, uid, "requests", str(request, "id"))
			if e != nil {
				return e
			}
			if _, e = s.appendEvent(ctx, tx, uid, nid, sid, turn, "request.upsert", rr); e != nil {
				return e
			}
		}
		return s.audit(ctx, tx, uid, op, kind, "accepted", nid, sid)
	})
	if e != nil {
		return nil, e
	}
	if e = s.dispatch(ctx, p, uid, op, cmd); e != nil {
		return nil, e
	}
	return s.operation(ctx, s.db, uid, op)
}

func validateDecision(request, decision M) error {
	if str(request, "kind") != str(decision, "kind") {
		return invalid("回答类型与请求不匹配")
	}
	if str(request, "kind") == "approval" {
		for _, v := range array(request, "choices") {
			if str(v.(M), "id") == str(decision, "choiceId") {
				return nil
			}
		}
		return invalid("无效的审批选项")
	}
	answers := object(decision, "answers")
	known := map[string]bool{}
	for _, v := range array(request, "questions") {
		q := v.(M)
		qid := str(q, "id")
		known[qid] = true
		answer, exists := answers[qid]
		required := truth(q, "required")
		if !exists {
			if required {
				return invalid("必填问题未回答")
			}
			continue
		}
		switch str(q, "type") {
		case "text":
			text, ok := answer.(string)
			if !ok || utf8.RuneCountInString(text) > int(number(q, "maxLength")) {
				return invalid("文本回答格式或长度无效")
			}
			if required && strings.TrimSpace(text) == "" {
				return invalid("必填文本不能为空")
			}
		case "single", "multiple":
			options := map[string]bool{}
			for _, x := range array(q, "options") {
				options[str(x.(M), "id")] = true
			}
			if str(q, "type") == "single" {
				value, ok := answer.(string)
				if !ok || (!options[value] && (required || value != "")) {
					return invalid("无效单选答案")
				}
			} else {
				values, ok := answer.([]any)
				if !ok || len(values) > int(number(q, "max")) || (required && len(values) == 0) {
					return invalid("多选答案数量无效")
				}
				seen := map[string]bool{}
				for _, x := range values {
					value, ok := x.(string)
					if !ok || !options[value] || seen[value] {
						return invalid("多选答案包含未知或重复选项")
					}
					seen[value] = true
				}
			}
		}
	}
	for k := range answers {
		if !known[k] {
			return invalid("答案包含未知 questionId")
		}
	}
	return nil
}
func (s *Server) dispatch(ctx context.Context, p *peer, uid, op string, cmd M) error {
	if !s.online(p.node) || s.nodes[p.node] != p || !parseTime(str(cmd, "deadlineAt")).After(s.now()) {
		return s.failUnsent(ctx, uid, op, apierr(503, "NODE_OFFLINE", "命令尚未发送，设备已离线"))
	}
	// Persist write intent before handing bytes to the sole socket writer. A crash here is uncertain, never replayed.
	if _, e := s.db.Exec(ctx, `UPDATE command_outbox SET state='dispatching' WHERE user_id=$1 AND operation_id=$2 AND state='pending'`, uid, op); e != nil {
		return e
	}
	if !p.enqueue(frame("command", cmd)) {
		return s.failUnsent(ctx, uid, op, apierr(503, "SERVICE_UNAVAILABLE", "设备写队列已满，命令未发送"))
	}
	return nil
}
func (s *Server) failUnsent(ctx context.Context, uid, op string, reason *APIError) error {
	return s.transaction(ctx, uid, func(tx pgx.Tx) error {
		if _, e := tx.Exec(ctx, `UPDATE command_outbox SET state='not_sent' WHERE user_id=$1 AND operation_id=$2`, uid, op); e != nil {
			return e
		}
		if e := s.finishOp(ctx, tx, uid, op, "failed", nil, reason); e != nil {
			return e
		}
		return s.rejectedEffects(ctx, tx, uid, op)
	})
}
func (s *Server) rejectedEffects(ctx context.Context, tx pgx.Tx, uid, op string) error {
	var kind string
	var sid *string
	e := tx.QueryRow(ctx, `SELECT kind,session_id FROM operations WHERE user_id=$1 AND id=$2`, uid, op).Scan(&kind, &sid)
	if e != nil {
		return e
	}
	if sid == nil {
		return nil
	}
	if kind == "create" {
		session, e := s.getResource(ctx, tx, uid, "sessions", *sid)
		if e == pgx.ErrNoRows {
			return nil
		}
		if e != nil {
			return e
		}
		if str(session, "state") == "idle" {
			if _, e = tx.Exec(ctx, `UPDATE turns SET state='failed',ended_at=$2 WHERE id=$1`, str(session, "turnId"), s.now()); e != nil {
				return e
			}
			if _, e = tx.Exec(ctx, `UPDATE sessions SET state='failed' WHERE id=$1`, *sid); e != nil {
				return e
			}
			_, e = s.appendEvent(ctx, tx, uid, str(session, "nodeId"), *sid, str(session, "turnId"), "session.state", M{"state": "failed", "turnId": session["turnId"], "revision": number(session, "revision")})
			return e
		}
	}
	if kind == "respond" {
		rows, e := rowsJSON(ctx, tx, `UPDATE interaction_requests SET state=CASE WHEN expires_at>now() THEN 'pending' ELSE 'expired' END,decision=NULL,decision_operation_id=NULL,revision=revision+1 WHERE user_id=$1 AND decision_operation_id=$2 AND state='deciding' RETURNING jsonb_build_object('id',id,'revision',revision)`, uid, op)
		if e != nil {
			return e
		}
		for _, v := range rows {
			m := v.(M)
			if e = s.change(ctx, tx, uid, "request", str(m, "id"), "upsert", number(m, "revision")); e != nil {
				return e
			}
			rr, e := s.getResource(ctx, tx, uid, "requests", str(m, "id"))
			if e == pgx.ErrNoRows {
				continue
			}
			if e != nil {
				return e
			}
			nid, e := ownSession(ctx, tx, uid, str(rr, "sessionId"))
			if e != nil {
				return e
			}
			if _, e = s.appendEvent(ctx, tx, uid, nid, str(rr, "sessionId"), str(rr, "turnId"), "request.upsert", rr); e != nil {
				return e
			}
		}
	}
	return nil
}
