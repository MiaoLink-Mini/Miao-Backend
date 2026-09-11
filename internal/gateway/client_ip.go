package gateway

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Only an explicitly trusted proxy may supply a single X-Real-IP value.
// The ingress must overwrite this header, never append untrusted client input.
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return "invalid-peer"
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return "invalid-peer"
	}
	peer = peer.Unmap()
	for _, prefix := range s.cfg.TrustedProxies {
		if prefix.Contains(peer) {
			values := r.Header.Values("X-Real-IP")
			if len(values) == 1 {
				client, err := netip.ParseAddr(strings.TrimSpace(values[0]))
				if err == nil && client.Zone() == "" {
					return client.Unmap().String()
				}
			}
			break
		}
	}
	return peer.String()
}
