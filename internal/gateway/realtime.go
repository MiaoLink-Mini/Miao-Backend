package gateway

import (
	"context"
	"github.com/coder/websocket"
	"github.com/jackc/pgx/v5"
	"net/http"
	"sync"
	"time"
	"weagent/backend/internal/protocol"
)

type outbound struct {
	body    []byte
	command M
}
type peer struct {
	s                 *Server
	conn              *websocket.Conn
	ctx               context.Context
	cancel            context.CancelFunc
	user, node, token string
	epoch             int64
	expires, lastSeen time.Time
	queue             chan outbound
	qmu               sync.Mutex
	bytes             int
	closed            bool
	revision          int64
	watching          bool
	watched           map[string]int64
	inventoryID       string
	projects, agents  []any
	inventoryComplete bool
	ready             bool
	needsSync         map[string]bool
}

func (p *peer) stop() {
	p.qmu.Lock()
	if p.closed {
		p.qmu.Unlock()
		return
	}
	p.closed = true
	p.cancel()
	p.qmu.Unlock()
	_ = p.conn.CloseNow()
}
func (p *peer) enqueue(value M) bool {
	model := "ServerFrame"
	if p.node != "" {
		model = "GatewayToNodeFrame"
	}
	if p.s.validator.Validate(model, value) != nil {
		p.s.log.Error("outgoing websocket schema mismatch", "type", str(value, "type"))
		p.stop()
		return false
	}
	b := canonical(value)
	p.qmu.Lock()
	if p.closed || len(b) > 256<<10 || p.bytes+len(b) > p.s.cfg.QueueBytes {
		p.qmu.Unlock()
		p.stop()
		return false
	}
	x := outbound{body: b}
	if str(value, "type") == "command" {
		x.command = object(value, "data")
	}
	select {
	case p.queue <- x:
		p.bytes += len(b)
		p.qmu.Unlock()
		return true
	default:
		p.qmu.Unlock()
		p.stop()
		return false
	}
}
func (p *peer) writer() {
	defer p.s.wg.Done()
	defer p.stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case x := <-p.queue:
			p.qmu.Lock()
			p.bytes -= len(x.body)
			p.qmu.Unlock()
			// Lock through each bounded write: revoke and new-epoch registration cannot race an old queued write.
			p.s.mu.Lock()
			if p.s.draining || p.ctx.Err() != nil || (p.node != "" && p.s.nodes[p.node] != p) {
				p.s.mu.Unlock()
				return
			}
			if x.command != nil {
				if !parseTime(str(x.command, "deadlineAt")).After(p.s.now()) || !p.s.online(p.node) {
					ctx, c := context.WithTimeout(p.s.ctx, 5*time.Second)
					_ = p.s.failUnsent(ctx, p.user, str(x.command, "operationId"), apierr(503, "NODE_OFFLINE", "命令发送前已到期或设备失联"))
					c()
					p.s.mu.Unlock()
					continue
				}
			}
			ctx, cancel := context.WithTimeout(p.ctx, 5*time.Second)
			err := p.conn.Write(ctx, websocket.MessageText, x.body)
			cancel()
			p.s.mu.Unlock()
			if err != nil {
				return
			}
		}
	}
}
func (s *Server) online(nid string) bool {
	p := s.nodes[nid]
	return p != nil && p.ctx.Err() == nil && p.ready && p.s.now().Sub(p.lastSeen) < s.cfg.OfflineAfter
}
func (s *Server) socket(w http.ResponseWriter, r *http.Request, isNode bool) {
	trace := id("trace")
	select {
	case s.inflight <- struct{}{}:
		defer func() { <-s.inflight }()
	default:
		s.writeError(w, apierr(503, "SERVICE_UNAVAILABLE", "握手过多"), trace)
		return
	}
	origin := r.Header.Get("Origin")
	if origin != "" {
		ok := false
		for _, x := range s.cfg.Origins {
			if origin == x {
				ok = true
			}
		}
		if !ok {
			s.writeError(w, apierr(403, "FORBIDDEN", "Origin 不在允许列表"), trace)
			return
		}
	}
	s.mu.Lock()
	ip := s.clientIP(r)
	if !s.rate("socket:"+ip, 120) {
		s.mu.Unlock()
		s.writeError(w, apierr(429, "RATE_LIMITED", "连接尝试过于频繁"), trace)
		return
	}
	if s.draining || len(s.nodes)+len(s.clients) >= s.cfg.MaxConnections {
		s.mu.Unlock()
		s.writeError(w, apierr(503, "SERVICE_UNAVAILABLE", "连接数达到上限"), trace)
		return
	}
	uid, nid := "", ""
	var expiry time.Time
	var epoch int64
	var err error
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	if isNode {
		err = s.db.QueryRow(ctx, `SELECT n.id,a.user_id FROM node_tokens t JOIN nodes n ON n.id=t.node_id JOIN node_access a ON a.node_id=n.id WHERE t.token_hash=$1 AND t.expires_at>$2 AND t.revoked_at IS NULL AND t.credential_version=n.credential_version AND n.revoked_at IS NULL AND a.revoked_at IS NULL`, hash([]byte(bearer(r))), s.now()).Scan(&nid, &uid)
		expiry = s.now().Add(time.Hour)
	} else {
		uid, expiry, err = s.authenticate(ctx, r)
		count := 0
		for p := range s.clients {
			if p.user == uid {
				count++
			}
		}
		if count >= 3 {
			s.mu.Unlock()
			s.writeError(w, apierr(429, "RATE_LIMITED", "当前用户连接数达到上限"), trace)
			return
		}
	}
	if err != nil {
		s.mu.Unlock()
		s.writeError(w, apierr(401, "UNAUTHENTICATED", "连接身份已失效"), trace)
		return
	}
	// Exact Origin was validated above; native Mini Program/Node have no Origin. No auth bypass.
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		s.mu.Unlock()
		return
	}
	conn.SetReadLimit(256 << 10)
	pctx, pcancel := context.WithCancel(s.ctx)
	p := &peer{s: s, conn: conn, ctx: pctx, cancel: pcancel, user: uid, node: nid, token: bearer(r), expires: expiry, lastSeen: s.now(), queue: make(chan outbound, s.cfg.QueueFrames), watched: map[string]int64{}, needsSync: map[string]bool{}}
	if isNode {
		err = s.transaction(ctx, uid, func(tx pgx.Tx) error {
			return tx.QueryRow(ctx, `UPDATE nodes SET connection_epoch=connection_epoch+1,revision=revision+1,last_seen_at=$2 WHERE id=$1 RETURNING connection_epoch`, nid, s.now()).Scan(&epoch)
		})
		if err != nil {
			s.mu.Unlock()
			p.stop()
			return
		}
		p.epoch = epoch
		old := s.nodes[nid]
		s.nodes[nid] = p
		if old != nil {
			old.stop()
		}
		// A failed create with no Node source was never a native session. Requiring
		// its snapshot would permanently fence a healthy Node after not_seen.
		rows, e := s.db.Query(ctx, `SELECT se.id FROM sessions se WHERE se.node_id=$1 AND se.state<>'closed' AND se.history_state='available' AND NOT(se.last_source_sequence=0 AND se.state='failed' AND EXISTS(SELECT 1 FROM operations op WHERE op.session_id=se.id AND op.kind='create' AND op.state='failed'))`, nid)
		if e != nil {
			delete(s.nodes, nid)
			s.mu.Unlock()
			p.stop()
			return
		}
		for rows.Next() {
			var sid string
			if rows.Scan(&sid) == nil {
				p.needsSync[sid] = true
			}
		}
		rows.Close()
		p.enqueue(frame("node.ready", M{"nodeId": nid, "epoch": epoch, "heartbeatSeconds": int64(s.cfg.Heartbeat / time.Second), "serverTime": timestamp(s.now())}))
		ops, _ := rowsJSON(ctx, s.db, `SELECT jsonb_build_object('id',id) FROM operations WHERE node_id=$1 AND state IN ('accepted','delivered','reconciling') ORDER BY created_at LIMIT 200`, nid)
		for _, v := range ops {
			p.enqueue(frame("command.query", M{"epoch": epoch, "operationId": str(v.(M), "id")}))
		}
	} else {
		s.clients[p] = true
	}
	s.wg.Add(2)
	go p.writer()
	go p.reader()
	s.mu.Unlock()
}
func (p *peer) reader() {
	defer p.s.wg.Done()
	defer func() {
		p.stop()
		p.s.mu.Lock()
		defer p.s.mu.Unlock()
		if p.node == "" {
			delete(p.s.clients, p)
		} else if p.s.nodes[p.node] == p {
			delete(p.s.nodes, p.node)
			ctx, c := context.WithTimeout(context.WithoutCancel(p.s.ctx), 5*time.Second)
			defer c()
			_ = p.s.nodeDisconnected(ctx, p)
			p.s.pump(ctx)
		}
	}()
	for {
		ctx, c := context.WithTimeout(p.ctx, p.s.cfg.OfflineAfter)
		typ, b, e := p.conn.Read(ctx)
		c()
		if e != nil {
			return
		}
		if typ != websocket.MessageText {
			return
		}
		v, e := protocol.Decode(b)
		schema := "ClientFrame"
		if p.node != "" {
			schema = "NodeToGatewayFrame"
		}
		if e != nil || p.s.validator.Validate(schema, v) != nil {
			p.enqueue(frame("error", errorBody(apierr(400, "PROTOCOL_UNSUPPORTED", "不支持的消息或协议字段"), id("trace"))))
			return
		}
		m := v.(M)
		p.s.mu.Lock()
		if p.ctx.Err() != nil || (p.node != "" && p.s.nodes[p.node] != p) {
			p.s.mu.Unlock()
			return
		}
		ctx, c = context.WithTimeout(p.ctx, 10*time.Second)
		if p.node != "" {
			if number(object(m, "data"), "epoch") != p.epoch {
				e = apierr(409, "PROTOCOL_UNSUPPORTED", "连接代次已失效")
			} else {
				e = p.s.nodeFrame(ctx, p, m)
			}
		} else {
			e = p.s.clientFrame(ctx, p, m)
		}
		if e == nil {
			p.lastSeen = p.s.now()
			p.s.pump(ctx)
		} else {
			p.enqueue(frame("error", errorBody(normalizeError(e), id("trace"))))
		}
		c()
		p.s.mu.Unlock()
		if e != nil && normalizeError(e).Code == "PROTOCOL_UNSUPPORTED" {
			return
		}
	}
}
func (s *Server) clientFrame(ctx context.Context, p *peer, m M) error {
	b := object(m, "data")
	switch str(m, "type") {
	case "heartbeat":
		p.enqueue(frame("heartbeat", M{"nonce": str(b, "nonce")}))
		return nil
	case "unwatch":
		for _, sid := range array(b, "sessionIds") {
			delete(p.watched, sid.(string))
		}
		return nil
	case "watch":
		var rev int64
		if e := s.db.QueryRow(ctx, `SELECT last_revision FROM users WHERE id=$1`, p.user).Scan(&rev); e != nil {
			return e
		}
		if number(b, "revision") > rev {
			return apierr(409, "CURSOR_AHEAD", "变化游标超过服务器")
		}
		if _, e := s.readChanges(ctx, p.user, number(b, "revision"), rev, 1); e != nil {
			return e
		}
		watched := map[string]int64{}
		sessions := []any{}
		for _, v := range array(b, "sessions") {
			q := v.(M)
			sid := str(q, "sessionId")
			if _, duplicate := watched[sid]; duplicate {
				return invalid("重复会话订阅")
			}
			if _, e := ownSession(ctx, s.db, p.user, sid); e != nil {
				return e
			}
			var last, first int64
			var history string
			if e := s.db.QueryRow(ctx, `SELECT last_sequence,earliest_sequence,history_state FROM sessions WHERE id=$1`, sid).Scan(&last, &first, &history); e != nil {
				return e
			}
			if number(q, "after") > last {
				return apierr(409, "CURSOR_AHEAD", "会话游标超过服务器")
			}
			if history == "purged" {
				return apierr(410, "HISTORY_PURGED", "正文已清理")
			}
			if number(q, "after") < first-1 {
				return apierr(410, "CURSOR_EXPIRED", "请加载历史基线")
			}
			watched[sid] = last
			sessions = append(sessions, M{"sessionId": sid, "highWater": last})
		}
		p.watched = watched
		p.revision = rev
		p.watching = true
		p.enqueue(frame("ready", M{"revision": rev, "sessions": sessions, "heartbeatSeconds": int64(s.cfg.Heartbeat / time.Second)}))
		return nil
	}
	return invalid("未知客户端消息")
}
func (s *Server) pump(ctx context.Context) {
	for p := range s.clients {
		if !p.watching || p.ctx.Err() != nil {
			continue
		}
		var rev int64
		if e := s.db.QueryRow(ctx, `SELECT last_revision FROM users WHERE id=$1`, p.user).Scan(&rev); e != nil {
			p.stop()
			continue
		}
		changes, e := s.readChanges(ctx, p.user, p.revision, rev, 200)
		if e != nil {
			p.enqueue(frame("error", errorBody(normalizeError(e), id("trace"))))
			p.watching = false
			continue
		}
		for _, x := range changes {
			change := x.(M)
			if !p.enqueue(frame("change", change)) {
				break
			}
			p.revision = number(change, "revision")
			if str(change, "resourceType") == "operation" {
				op, e := s.operation(ctx, s.db, p.user, str(change, "resourceId"))
				if e == nil {
					p.enqueue(frame("operation", op))
				}
			}
		}
		for sid, after := range p.watched {
			if _, e = ownSession(ctx, s.db, p.user, sid); e != nil {
				p.stop()
				break
			}
			var last int64
			if e = s.db.QueryRow(ctx, `SELECT last_sequence FROM sessions WHERE id=$1`, sid).Scan(&last); e != nil {
				p.stop()
				break
			}
			events, e := s.readEvents(ctx, s.db, sid, after, last, 200)
			if e != nil {
				p.enqueue(frame("error", errorBody(normalizeError(e), id("trace"))))
				delete(p.watched, sid)
				continue
			}
			for _, x := range events {
				event := x.(M)
				if !p.enqueue(frame("event", event)) {
					break
				}
				p.watched[sid] = number(event, "sequence")
			}
		}
	}
}
func (s *Server) nodeDisconnected(ctx context.Context, p *peer) error {
	return s.transaction(ctx, p.user, func(tx pgx.Tx) error {
		var rev int64
		e := tx.QueryRow(ctx, `UPDATE nodes SET revision=revision+1 WHERE id=$1 AND connection_epoch=$2 RETURNING revision`, p.node, p.epoch).Scan(&rev)
		if e == pgx.ErrNoRows {
			return nil
		}
		if e != nil {
			return e
		}
		return s.change(ctx, tx, p.user, "node", p.node, "upsert", rev)
	})
}
