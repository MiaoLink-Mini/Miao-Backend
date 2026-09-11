package gateway

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"strings"
	"testing"
)

func avatarFixture(t *testing.T, size int) string {
	t.Helper()
	var b bytes.Buffer
	if e := png.Encode(&b, image.NewRGBA(image.Rect(0, 0, size, size))); e != nil {
		t.Fatal(e)
	}
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

func TestCanonicalAvatar(t *testing.T) {
	good := avatarFixture(t, 64)
	jpeg, err := canonicalAvatar(good)
	if err != nil || !bytes.HasPrefix(jpeg, []byte{0xff, 0xd8}) {
		t.Fatal("not canonical JPEG", err)
	}
	for _, bad := range []string{"", "https://example.org/avatar.png", "%%%%", base64.StdEncoding.EncodeToString([]byte("<svg onload='alert(1)'/>")), strings.Repeat("a", 49156), avatarFixture(t, 513)} {
		if _, err := canonicalAvatar(bad); err == nil {
			t.Fatal("accepted invalid avatar")
		}
	}
}

func TestOwnProfileIsolationAndPersistence(t *testing.T) {
	f := newFixture(t)
	a, b := f.login("alice"), f.login("bob")
	f.call("GET", "/v1/me/profile", "", "", nil, 401)
	f.call("PATCH", "/v1/me/profile", "", "", M{"displayName": "bad", "revision": 0}, 401)
	initial := f.call("GET", "/v1/me/profile", a, "", nil, 200)
	if initial["avatarBase64"] != nil || number(initial, "revision") != 0 {
		t.Fatal(initial)
	}
	updated := f.call("PATCH", "/v1/me/profile", a, "", M{"displayName": "  阿林  ", "revision": 0, "avatarBase64": avatarFixture(t, 64)}, 200)
	if str(updated, "displayName") != "阿林" || number(updated, "revision") != 1 || str(updated, "avatarBase64") == "" {
		t.Fatal("profile not saved")
	}
	other := f.call("GET", "/v1/me/profile", b, "", nil, 200)
	if str(other, "displayName") == "阿林" || other["avatarBase64"] != nil {
		t.Fatal("cross-user profile leak")
	}
	f.call("PATCH", "/v1/me/profile", a, "", M{"displayName": "overwrite", "revision": 0}, 409)
	f.call("PATCH", "/v1/me/profile", a, "", M{"displayName": "bad", "revision": 1, "userId": "bob"}, 400)
	renamed := f.call("PATCH", "/v1/me/profile", a, "", M{"displayName": "新昵称", "revision": 1}, 200)
	if str(renamed, "avatarBase64") != str(updated, "avatarBase64") {
		t.Fatal("omitted avatar must preserve")
	}
	for _, name := range []string{" ", "bad\nname", "bad\u202ename", strings.Repeat("字", 33)} {
		f.call("PATCH", "/v1/me/profile", a, "", M{"displayName": name, "revision": 2}, 400)
	}
	f.call("PATCH", "/v1/me/profile", a, "", M{"displayName": "invalid avatar", "revision": 2, "avatarBase64": "YWJj"}, 400)
	// Re-login does not replace a custom profile with the provider default.
	f.call("POST", "/v1/auth/logout", a, "", M{}, 200)
	a = f.login("alice")
	persisted := f.call("GET", "/v1/me/profile", a, "", nil, 200)
	if str(persisted, "displayName") != "新昵称" || number(persisted, "revision") != 2 {
		t.Fatal("profile did not persist")
	}
	cleared := f.call("PATCH", "/v1/me/profile", a, "", M{"displayName": "新昵称", "revision": 2, "avatarBase64": nil}, 200)
	if cleared["avatarBase64"] != nil {
		t.Fatal("avatar removal failed")
	}
}
