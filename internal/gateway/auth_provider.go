package gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Identity struct{ Subject, Name string }
type IdentityProvider interface {
	Exchange(context.Context, string) (Identity, error)
}
type WechatProvider struct {
	AppID, Secret string
	Client        *http.Client
	Endpoint      string
}

func (p WechatProvider) Exchange(ctx context.Context, code string) (Identity, error) {
	if strings.TrimSpace(code) == "" || strings.HasPrefix(code, "dev:") {
		return Identity{}, errors.New("WeChat login code required")
	}
	endpoint := p.Endpoint
	if endpoint == "" {
		endpoint = "https://api.weixin.qq.com/sns/jscode2session"
	}
	u, e := url.Parse(endpoint)
	if e != nil {
		return Identity{}, errors.New("invalid identity endpoint")
	}
	q := u.Query()
	q.Set("appid", p.AppID)
	q.Set("secret", p.Secret)
	q.Set("js_code", code)
	q.Set("grant_type", "authorization_code")
	u.RawQuery = q.Encode()
	r, e := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if e != nil {
		return Identity{}, errors.New("identity request failed")
	}
	client := p.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	resp, e := client.Do(r)
	if e != nil {
		return Identity{}, errors.New("identity provider unavailable")
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(io.LimitReader(resp.Body, 65537))
	if e != nil || len(b) > 65536 || resp.StatusCode != 200 {
		return Identity{}, errors.New("identity provider failed")
	}
	var result struct {
		OpenID    string `json:"openid"`
		ErrorCode int    `json:"errcode"`
	}
	if json.Unmarshal(b, &result) != nil || result.ErrorCode != 0 || result.OpenID == "" {
		return Identity{}, errors.New("identity exchange rejected")
	}
	return Identity{Subject: p.AppID + ":" + result.OpenID, Name: "微信用户"}, nil
}

// DevelopmentProvider is explicit loopback-only integration support, never a production fallback.
type DevelopmentProvider struct{}

func (DevelopmentProvider) Exchange(_ context.Context, code string) (Identity, error) {
	if !strings.HasPrefix(code, "dev:") || len(code) < 5 || len(code) > 100 {
		return Identity{}, errors.New("development code must be dev:<identity>")
	}
	h := sha256.Sum256([]byte(code))
	return Identity{Subject: "development:" + hex.EncodeToString(h[:]), Name: "本地测试用户"}, nil
}
