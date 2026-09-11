package gateway

import (
	"context"
	"encoding/hex"
	"fmt"
	"github.com/jackc/pgx/v5"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const activeAccess = `a.user_id=$1 AND a.revoked_at IS NULL AND n.revoked_at IS NULL`
const catalogJoin = ` JOIN node_access a ON a.node_id=t.node_id JOIN nodes n ON n.id=t.node_id `
const sessionExpr = `jsonb_build_object('id',t.id,'nodeId',t.node_id,'projectId',t.project_id,'agentId',t.agent_id,'title',t.title,'state',t.state,'turnId',t.current_turn_id,'mode',t.mode,'capabilities',t.capabilities,'capabilityRevision',t.capability_revision,'createdAt',t.created_at,'updatedAt',t.updated_at,'revision',t.revision,'lastSequence',t.last_sequence,'historyState',t.history_state,'pinned',t.pinned,'tags',t.tags,'archived',t.archived,'parentSessionId',t.parent_session_id,'originMode',t.origin_mode,'originPoint',t.origin_point,'queue',COALESCE((SELECT jsonb_agg(jsonb_build_object('id',q.id,'operationId',q.operation_id,'text',q.text_content,'state',q.state,'createdAt',q.created_at) ORDER BY q.position) FROM queue_items q WHERE q.session_id=t.id AND q.state IN ('queued','starting')),'[]'::jsonb))`
const requestExpr = `t.definition || jsonb_build_object('id',t.id,'sessionId',t.session_id,'turnId',t.turn_id,'kind',t.kind,'state',CASE WHEN t.state='pending' AND t.expires_at<=now() THEN 'expired' ELSE t.state END,'createdAt',t.created_at,'expiresAt',t.expires_at,'revision',t.revision,'decision',t.decision,'decisionOperationId',t.decision_operation_id)`

type resourceSpec struct{ From, Expr, Where, Sort string }

var resourceSpecs = map[string]resourceSpec{
	"nodes":         {`nodes t JOIN node_access a ON a.node_id=t.id`, `jsonb_build_object('id',t.id,'name',t.name,'platform',t.platform,'version',t.version,'online',false,'lastSeen',t.last_seen_at,'revoked',t.revoked_at IS NOT NULL OR a.revoked_at IS NOT NULL,'revision',t.revision)`, `a.user_id=$1`, `t.created_at`},
	"projects":      {`projects t` + catalogJoin, `jsonb_build_object('id',t.id,'nodeId',t.node_id,'name',t.name,'description',t.description,'branch',t.branch,'valid',t.valid,'revision',t.revision)`, activeAccess, `t.id`},
	"agents":        {`agents t` + catalogJoin, `jsonb_build_object('id',t.id,'nodeId',t.node_id,'name',t.name,'state',t.state,'version',t.version,'adapterVersion',t.adapter_version,'capabilities',t.capabilities,'capabilityRevision',t.capability_revision,'revision',t.revision)`, activeAccess, `t.id`},
	"sessions":      {`sessions t` + catalogJoin, sessionExpr, activeAccess + ` AND t.user_id=$1`, `t.updated_at`},
	"requests":      {`interaction_requests t JOIN sessions ss ON ss.id=t.session_id JOIN node_access a ON (a.user_id,a.node_id)=(ss.user_id,ss.node_id) JOIN nodes n ON n.id=ss.node_id`, requestExpr, activeAccess + ` AND t.user_id=$1`, `t.created_at`},
	"notifications": {`notifications t`, `jsonb_build_object('id',t.id,'sessionId',t.session_id,'kind',t.kind,'title',t.title,'summary',t.summary,'read',t.read_at IS NOT NULL,'createdAt',t.created_at,'revision',t.revision)`, `t.user_id=$1 AND (t.session_id IS NULL OR EXISTS (SELECT 1 FROM sessions ss JOIN node_access a ON (a.user_id,a.node_id)=(ss.user_id,ss.node_id) JOIN nodes n ON n.id=ss.node_id WHERE ss.id=t.session_id AND ` + activeAccess + `))`, `t.created_at`},
	"audit":         {`audit_entries t`, `jsonb_build_object('id',t.id,'title',t.title,'action',t.action,'state',t.state,'operationId',t.operation_id,'nodeId',t.node_id,'sessionId',t.session_id,'createdAt',t.created_at)`, `t.user_id=$1`, `t.created_at`},
}

func (s *Server) resource(ctx context.Context, r *http.Request, uid, name string) (any, error) {
	kinds := map[string]string{"Nodes": "nodes", "Node": "nodes", "Projects": "projects", "Project": "projects", "Agents": "agents", "Agent": "agents", "Sessions": "sessions", "Session": "sessions", "Requests": "requests", "InteractionRequest": "requests", "Notifications": "notifications", "Audits": "audit"}
	kind := kinds[strings.TrimPrefix(strings.TrimPrefix(name, "list"), "get")]
	if kind == "" {
		return nil, notFound()
	}
	if strings.HasPrefix(name, "get") {
		return s.getResource(ctx, s.db, uid, kind, r.PathValue("id"))
	}
	return s.listResource(ctx, s.db, uid, kind, r.URL.Query())
}
func (s *Server) getResource(ctx context.Context, db DB, uid, kind, rid string) (M, error) {
	sp := resourceSpecs[kind]
	m, e := rowJSON(ctx, db, "SELECT "+sp.Expr+" FROM "+sp.From+" WHERE "+sp.Where+" AND t.id=$2", uid, rid)
	if e == nil && kind == "nodes" {
		m["online"] = s.online(rid)
	}
	return m, e
}
func (s *Server) listResource(ctx context.Context, db DB, uid, kind string, q url.Values) (M, error) {
	sp := resourceSpecs[kind]
	limit, e := parseLimit(q.Get("limit"), 50, 100)
	if e != nil {
		return nil, e
	}
	where := sp.Where
	args := []any{uid}
	add := func(expr string, value any) {
		args = append(args, value)
		where += " AND " + strings.ReplaceAll(expr, "?", fmt.Sprintf("$%d", len(args)))
	}
	for _, key := range []string{"nodeId", "projectId", "agentId", "sessionId"} {
		if value := q.Get(key); value != "" {
			col := ""
			switch key {
			case "nodeId":
				if kind == "projects" || kind == "agents" || kind == "sessions" {
					col = "node_id"
				}
			case "projectId", "agentId":
				if kind == "sessions" {
					col = map[string]string{"projectId": "project_id", "agentId": "agent_id"}[key]
				}
			case "sessionId":
				if kind == "requests" {
					col = "session_id"
				}
			}
			if col == "" {
				return nil, invalid("不支持的筛选条件")
			}
			add("t."+col+"=?", value)
		}
	}
	if kind == "sessions" {
		for _, key := range []string{"archived", "pinned"} {
			if value := q.Get(key); value != "" {
				if value != "true" && value != "false" {
					return nil, invalid("Invalid organization filter")
				}
				add("t."+key+"=?", value == "true")
			}
		}
		if state := q.Get("state"); state != "" {
			if s.validator.Validate("SessionState", state) != nil {
				return nil, invalid("状态无效")
			}
			add("t.state=?", state)
		}
		if text := q.Get("q"); text != "" {
			if len([]rune(text)) > 100 {
				return nil, invalid("搜索文字过长")
			}
			escaped := strings.NewReplacer(`\`, `\\`, "%", `\%`, "_", `\_`).Replace(text)
			add("t.title ILIKE ?", "%"+escaped+"%")
		}
	}
	if kind == "requests" {
		switch q.Get("bucket") {
		case "", "pending":
			where += ` AND (t.state='deciding' OR (t.state='pending' AND t.expires_at>now()))`
		case "history":
			where += ` AND (t.state NOT IN ('pending','deciding') OR (t.state='pending' AND t.expires_at<=now()))`
		default:
			return nil, invalid("bucket 无效")
		}
	}
	if kind == "notifications" && q.Get("unread") != "" {
		switch q.Get("unread") {
		case "true":
			where += " AND t.read_at IS NULL"
		case "false":
			where += " AND t.read_at IS NOT NULL"
		default:
			return nil, invalid("unread 无效")
		}
	}
	filter := url.Values{}
	for k, v := range q {
		if k != "pageToken" && k != "limit" {
			filter[k] = v
		}
	}
	fh := hex.EncodeToString(hash([]byte(filter.Encode())))
	if token := q.Get("pageToken"); token != "" {
		c, e := s.decodeCursor(token)
		if e != nil {
			return nil, e
		}
		if str(c, "user") != uid || str(c, "kind") != kind || str(c, "filter") != fh {
			return nil, invalid("分页游标不属于当前查询")
		}
		args = append(args, str(c, "sort"), str(c, "id"))
		cast := "::timestamptz"
		if sp.Sort == "t.id" {
			cast = "::text"
		}
		where += fmt.Sprintf(" AND (%s,t.id)<($%d%s,$%d)", sp.Sort, len(args)-1, cast, len(args))
	}
	args = append(args, limit+1)
	query := fmt.Sprintf("SELECT %s || jsonb_build_object('__sort',%s) FROM %s WHERE %s ORDER BY %s DESC,t.id DESC LIMIT $%d", sp.Expr, sp.Sort, sp.From, where, sp.Sort, len(args))
	rows, e := rowsJSON(ctx, db, query, args...)
	if e != nil {
		return nil, e
	}
	items := []any{}
	used := 0
	lastSort, lastID := "", ""
	for _, x := range rows {
		m := x.(M)
		if len(items) >= limit || (len(items) > 0 && used+len(canonical(m)) > 256<<10) {
			break
		}
		lastSort, lastID = str(m, "__sort"), str(m, "id")
		delete(m, "__sort")
		if kind == "nodes" {
			m["online"] = s.online(str(m, "id"))
		}
		used += len(canonical(m))
		items = append(items, m)
	}
	var next any
	if len(items) < len(rows) {
		next = s.cursor(M{"user": uid, "kind": kind, "filter": fh, "sort": lastSort, "id": lastID, "expires": s.now().Add(10 * time.Minute).Unix()})
	}
	return M{"items": items, "nextPageToken": next}, nil
}
func (s *Server) bootstrap(ctx context.Context, uid string) (M, error) {
	tx, e := s.db.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if e != nil {
		return nil, e
	}
	defer tx.Rollback(context.WithoutCancel(ctx))
	user, e := userJSON(ctx, tx, uid)
	if e != nil {
		return nil, e
	}
	var rev int64
	if e = tx.QueryRow(ctx, `SELECT last_revision FROM users WHERE id=$1`, uid).Scan(&rev); e != nil {
		return nil, e
	}
	m := M{"user": user, "revision": rev, "serverTime": timestamp(s.now())}
	for _, kind := range []string{"nodes", "projects", "agents", "sessions", "requests", "notifications", "audit"} {
		page, e := s.listResource(ctx, tx, uid, kind, url.Values{})
		if e != nil {
			return nil, e
		}
		m[kind] = page
	}
	var sessions, requests, unread int64
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM sessions t`+catalogJoin+` WHERE `+activeAccess+` AND t.user_id=$1`, uid).Scan(&sessions); e != nil {
		return nil, e
	}
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM `+resourceSpecs["requests"].From+` WHERE `+resourceSpecs["requests"].Where+` AND (t.state='deciding' OR (t.state='pending' AND t.expires_at>now()))`, uid).Scan(&requests); e != nil {
		return nil, e
	}
	if e = tx.QueryRow(ctx, `SELECT count(*) FROM `+resourceSpecs["notifications"].From+` WHERE `+resourceSpecs["notifications"].Where+` AND t.read_at IS NULL`, uid).Scan(&unread); e != nil {
		return nil, e
	}
	online := 0
	for _, p := range s.nodes {
		if p.user == uid && s.online(p.node) {
			online++
		}
	}
	m["counts"] = M{"onlineNodes": online, "sessions": sessions, "pendingRequests": requests, "unreadNotifications": unread}
	return m, tx.Commit(ctx)
}
func (s *Server) diffFile(ctx context.Context, uid, rid string) (M, error) {
	return rowJSON(ctx, s.db, `SELECT jsonb_build_object('id',f.id,'sessionId',f.session_id,'itemId',f.item_id,'path',f.display_path,'language',f.language,'patch',f.patch,'truncated',f.truncated,'createdAt',f.created_at) FROM diff_files f JOIN sessions ss ON ss.id=f.session_id JOIN node_access a ON (a.user_id,a.node_id)=(ss.user_id,ss.node_id) JOIN nodes n ON n.id=ss.node_id WHERE `+activeAccess+` AND f.id=$2`, uid, rid)
}
