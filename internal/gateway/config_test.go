package gateway

import (
	"bytes"
	"encoding/base64"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func TestConfigFailsClosed(t *testing.T) {
	t.Setenv("TRUSTED_PROXY_CIDRS", "")
	for _, key := range []string{"DATABASE_URL", "GATEWAY_HMAC_KEY", "AUTH_MODE", "LISTEN_ADDR", "WECHAT_APP_ID", "WECHAT_APP_SECRET", "ALLOWED_ORIGINS", "MAX_CONNECTIONS", "MAX_INFLIGHT", "HEARTBEAT_INTERVAL", "OFFLINE_AFTER", "COMMAND_TTL", "MAINTENANCE_INTERVAL", "CONTENT_RETENTION_DAYS", "METADATA_RETENTION_DAYS", "CHANGES_RETENTION_DAYS"} {
		t.Setenv(key, "")
	}
	if _, e := LoadConfig(); e == nil {
		t.Fatal("missing key accepted")
	}
	t.Setenv("GATEWAY_HMAC_KEY", base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{3}, 32)))
	t.Setenv("DATABASE_URL", "postgres://fixture.invalid/db")
	t.Setenv("AUTH_MODE", "development")
	t.Setenv("LISTEN_ADDR", "0.0.0.0:8080")
	if _, e := LoadConfig(); e == nil {
		t.Fatal("development auth exposed publicly")
	}
	t.Setenv("LISTEN_ADDR", "127.0.0.1:18080")
	if _, e := LoadConfig(); e != nil {
		t.Fatal(e)
	}
	t.Setenv("ALLOWED_ORIGINS", "https://*.example.invalid")
	if _, e := LoadConfig(); e == nil {
		t.Fatal("wildcard origin accepted")
	}
	t.Setenv("ALLOWED_ORIGINS", "")
	t.Setenv("METADATA_RETENTION_DAYS", "1")
	if _, e := LoadConfig(); e == nil {
		t.Fatal("unsafe journal retention accepted")
	}
	t.Setenv("METADATA_RETENTION_DAYS", "90")
	t.Setenv("AUTH_MODE", "wechat")
	if _, e := LoadConfig(); e == nil {
		t.Fatal("production silently fell back without AppID/secret")
	}
}

func TestProxyClientIdentityCannotBeSpoofedByUntrustedPeers(t *testing.T) {
	s := &Server{cfg: Config{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("172.30.90.2/32")}}}
	for _, tc := range []struct{ peer, header, expected string }{
		{"203.0.113.1:123", "198.51.100.9", "203.0.113.1"},
		{"172.30.90.2:123", "198.51.100.9", "198.51.100.9"},
		{"172.30.90.3:123", "198.51.100.9", "172.30.90.3"},
		{"172.30.90.2:123", "198.51.100.9, 192.0.2.1", "172.30.90.2"},
		{"172.30.90.2:123", "invalid", "172.30.90.2"},
		{"172.30.90.2:123", "fe80::1%eth0", "172.30.90.2"},
	} {
		r := httptest.NewRequest("GET", "/", nil)
		r.RemoteAddr = tc.peer
		r.Header.Set("X-Real-IP", tc.header)
		r.Header.Set("X-Forwarded-For", "192.0.2.99")
		if got := s.clientIP(r); got != tc.expected {
			t.Fatalf("peer %s: got %s want %s", tc.peer, got, tc.expected)
		}
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "172.30.90.2:123"
	r.Header.Add("X-Real-IP", "192.0.2.1")
	r.Header.Add("X-Real-IP", "192.0.2.2")
	if s.clientIP(r) != "172.30.90.2" {
		t.Fatal("multiple headers accepted")
	}
	s.cfg.TrustedProxies = nil
	r.Header.Set("X-Real-IP", "192.0.2.1")
	if s.clientIP(r) != "172.30.90.2" {
		t.Fatal("proxy headers trusted by default")
	}
}
