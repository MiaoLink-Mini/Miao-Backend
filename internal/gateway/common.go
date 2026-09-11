package gateway

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"strconv"
	"time"
)

type M = map[string]any

func str(m M, k string) string { s, _ := m[k].(string); return s }
func number(m M, k string) int64 {
	switch n := m[k].(type) {
	case float64:
		return int64(n)
	case int64:
		return n
	case int:
		return int64(n)
	case json.Number:
		v, e := n.Int64()
		if e == nil {
			return v
		}
		f, _ := n.Float64()
		return int64(f)
	}
	return 0
}
func object(m M, k string) M { x, _ := m[k].(map[string]any); return x }
func array(m M, k string) []any {
	a, _ := m[k].([]any)
	if a == nil {
		return []any{}
	}
	return a
}
func truth(m M, k string) bool     { b, _ := m[k].(bool); return b }
func timestamp(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }
func parseTime(s string) time.Time { t, _ := time.Parse(time.RFC3339Nano, s); return t }
func id(prefix string) string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(b[:])
}
func secret() string {
	var b [32]byte
	if _, e := rand.Read(b[:]); e != nil {
		panic(e)
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}
func hash(b []byte) []byte { h := sha256.Sum256(b); return h[:] }
func canonical(v any) []byte {
	b, e := json.Marshal(v)
	if e != nil {
		panic(e)
	}
	// Inputs have already passed the safe-number schema. Normalize equivalent JSON numeric spellings.
	var normalized any
	if e = json.Unmarshal(b, &normalized); e != nil {
		panic(e)
	}
	b, e = json.Marshal(normalized)
	if e != nil {
		panic(e)
	}
	return b
}
func clone(v M) M { var m M; _ = json.Unmarshal(canonical(v), &m); return m }
func (s *Server) digest(purpose, value string) []byte {
	h := hmac.New(sha256.New, s.cfg.Key)
	h.Write([]byte(purpose + "\x00" + value))
	return h.Sum(nil)
}
func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}
func emptyResult() M {
	return M{"sessionId": nil, "turnId": nil, "requestId": nil, "nodeId": nil, "queueItemId": nil}
}
func frame(kind string, data any) M { return M{"version": "weagent/1", "type": kind, "data": data} }

type APIError struct {
	Status        int
	Code, Message string
	Retry         bool
}

func (e *APIError) Error() string { return e.Code }
func apierr(status int, code, msg string) *APIError {
	return &APIError{Status: status, Code: code, Message: msg, Retry: status == 429 || status == 503}
}
func notFound() *APIError      { return apierr(404, "NOT_FOUND", "资源不存在或无权访问") }
func invalid(msg string) error { return apierr(400, "VALIDATION_FAILED", msg) }
func normalizeError(err error) *APIError {
	var e *APIError
	if errors.As(err, &e) {
		return e
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return notFound()
	}
	return apierr(500, "INTERNAL", "服务内部错误")
}
func errorBody(e *APIError, trace string) M {
	return M{"code": e.Code, "message": e.Message, "retryable": e.Retry, "requestId": trace}
}
func equal(a, b any) bool { return string(canonical(a)) == string(canonical(b)) }
func (s *Server) cursor(value M) string {
	b := base64.RawURLEncoding.EncodeToString(canonical(value))
	return b + "." + base64.RawURLEncoding.EncodeToString(s.digest("cursor", b))
}
func (s *Server) decodeCursor(token string) (M, error) {
	var body, sig string
	for i, c := range token {
		if c == '.' {
			body, sig = token[:i], token[i+1:]
			break
		}
	}
	raw, e := base64.RawURLEncoding.DecodeString(sig)
	if e != nil || !hmac.Equal(raw, s.digest("cursor", body)) {
		return nil, invalid("分页游标无效")
	}
	b, e := base64.RawURLEncoding.DecodeString(body)
	if e != nil {
		return nil, invalid("分页游标无效")
	}
	var m M
	if json.Unmarshal(b, &m) != nil {
		return nil, invalid("分页游标无效")
	}
	if number(m, "expires") < s.now().Unix() {
		return nil, apierr(410, "CURSOR_EXPIRED", "分页游标已过期")
	}
	return m, nil
}
func parseLimit(value string, def, max int) (int, error) {
	if value == "" {
		return def, nil
	}
	n, e := strconv.Atoi(value)
	if e != nil || n < 1 || n > max {
		return 0, invalid(fmt.Sprintf("limit 必须在 1..%d", max))
	}
	return n, nil
}
func parseSequence(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	n, e := strconv.ParseInt(value, 10, 64)
	if e != nil || n < 0 || n > 9007199254740991 {
		return 0, invalid("游标格式无效")
	}
	return n, nil
}
