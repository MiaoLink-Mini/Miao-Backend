package gateway

import (
	"encoding/base64"
	"errors"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	TrustedProxies                                                                       []netip.Prefix
	Subscriptions                                                                        []SubscriptionTemplateConfig
	Listen, DatabaseURL, AuthMode, WechatAppID, WechatSecret                             string
	Key                                                                                  []byte
	Origins                                                                              []string
	Heartbeat, OfflineAfter, CommandTTL, UserTokenTTL, NodeTokenTTL, MaintenanceInterval time.Duration
	MaxConnections, MaxInflight, QueueFrames, QueueBytes                                 int
	ContentRetention, MetadataRetention, ChangesRetention                                time.Duration
}

func Defaults() Config {
	return Config{Listen: "127.0.0.1:8080", AuthMode: "wechat", Heartbeat: 15 * time.Second, OfflineAfter: 45 * time.Second, CommandTTL: 15 * time.Second, UserTokenTTL: 2 * time.Hour, NodeTokenTTL: 10 * time.Minute, MaintenanceInterval: time.Minute, MaxConnections: 256, MaxInflight: 128, QueueFrames: 256, QueueBytes: 2 << 20, ContentRetention: 30 * 24 * time.Hour, MetadataRetention: 90 * 24 * time.Hour, ChangesRetention: 7 * 24 * time.Hour}
}
func LoadConfig() (Config, error) {
	c := Defaults()
	c.DatabaseURL = os.Getenv("DATABASE_URL")
	c.WechatAppID = os.Getenv("WECHAT_APP_ID")
	c.WechatSecret = os.Getenv("WECHAT_APP_SECRET")
	for _, value := range strings.Split(os.Getenv("TRUSTED_PROXY_CIDRS"), ",") {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		prefix, err := netip.ParsePrefix(value)
		if err != nil || prefix.Bits() == 0 {
			return c, errors.New("TRUSTED_PROXY_CIDRS requires explicit non-global CIDRs")
		}
		c.TrustedProxies = append(c.TrustedProxies, prefix)
	}
	if s := os.Getenv("LISTEN_ADDR"); s != "" {
		c.Listen = s
	}
	if s := os.Getenv("AUTH_MODE"); s != "" {
		c.AuthMode = s
	}
	var err error
	c.Key, err = base64.StdEncoding.DecodeString(os.Getenv("GATEWAY_HMAC_KEY"))
	if err != nil || len(c.Key) < 32 {
		return c, errors.New("GATEWAY_HMAC_KEY must be base64 with at least 32 decoded bytes")
	}
	if c.DatabaseURL == "" {
		return c, errors.New("DATABASE_URL is required")
	}
	if c.AuthMode != "wechat" && c.AuthMode != "development" {
		return c, errors.New("AUTH_MODE must be wechat or development")
	}
	if c.AuthMode == "development" {
		host, _, e := net.SplitHostPort(c.Listen)
		if e != nil || net.ParseIP(host) == nil || !net.ParseIP(host).IsLoopback() {
			return c, errors.New("development auth requires a literal loopback LISTEN_ADDR")
		}
	}
	if c.AuthMode == "wechat" && (c.WechatAppID == "" || c.WechatSecret == "") {
		return c, errors.New("WECHAT_APP_ID and WECHAT_APP_SECRET required for wechat auth")
	}
	for _, s := range strings.Split(os.Getenv("ALLOWED_ORIGINS"), ",") {
		if s = strings.TrimSpace(s); s != "" {
			if strings.ContainsAny(s, "*?") {
				return c, errors.New("wildcard origins forbidden")
			}
			c.Origins = append(c.Origins, s)
		}
	}
	for key, target := range map[string]*int{"MAX_CONNECTIONS": &c.MaxConnections, "MAX_INFLIGHT": &c.MaxInflight} {
		if value := os.Getenv(key); value != "" {
			n, e := strconv.Atoi(value)
			if e != nil || n < 1 || n > 10000 {
				return c, errors.New("invalid limit " + key)
			}
			*target = n
		}
	}
	for key, target := range map[string]*time.Duration{"HEARTBEAT_INTERVAL": &c.Heartbeat, "OFFLINE_AFTER": &c.OfflineAfter, "COMMAND_TTL": &c.CommandTTL, "MAINTENANCE_INTERVAL": &c.MaintenanceInterval} {
		if value := os.Getenv(key); value != "" {
			d, e := time.ParseDuration(value)
			if e != nil || d < time.Second || d > time.Hour {
				return c, errors.New("invalid duration " + key)
			}
			*target = d
		}
	}
	if c.Heartbeat < 5*time.Second || c.Heartbeat > 60*time.Second || c.OfflineAfter < 2*c.Heartbeat {
		return c, errors.New("heartbeat must be 5s..60s and offline timeout at least twice heartbeat")
	}
	for key, target := range map[string]*time.Duration{"CONTENT_RETENTION_DAYS": &c.ContentRetention, "METADATA_RETENTION_DAYS": &c.MetadataRetention, "CHANGES_RETENTION_DAYS": &c.ChangesRetention} {
		if value := os.Getenv(key); value != "" {
			days, e := strconv.Atoi(value)
			if e != nil || days < 1 || days > 3650 {
				return c, errors.New("invalid retention " + key)
			}
			*target = time.Duration(days) * 24 * time.Hour
		}
	}
	if c.MetadataRetention < c.ContentRetention || c.MetadataRetention < 90*24*time.Hour {
		return c, errors.New("metadata/journal retention must be at least 90 days and no shorter than content retention")
	}
	if c.Subscriptions, err = loadSubscriptions(os.Getenv("WECHAT_SUBSCRIPTIONS_FILE")); err != nil {
		return c, err
	}
	return c, nil
}
