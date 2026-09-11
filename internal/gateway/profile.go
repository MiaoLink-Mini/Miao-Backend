package gateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"image"
	"image/jpeg"
	_ "image/png"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxAvatarBytes = 36 << 10

// Decode only bounded raster input and re-encode it, discarding EXIF and trailing data.
// No remote URLs or user-supplied filesystem paths are accepted.
func canonicalAvatar(encoded string) ([]byte, error) {
	if len(encoded) > 49152 {
		return nil, invalid("头像过大，请选择较小的图片")
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil || len(raw) > maxAvatarBytes {
		return nil, invalid("头像编码无效或图片过大")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(raw))
	if err != nil || (format != "jpeg" && format != "png") || cfg.Width < 1 || cfg.Height < 1 || cfg.Width > 512 || cfg.Height > 512 {
		return nil, invalid("头像须为不超过 512 × 512 的 PNG 或 JPEG 图片")
	}
	im, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return nil, invalid("头像图片损坏")
	}
	var out bytes.Buffer
	if err = jpeg.Encode(&out, im, &jpeg.Options{Quality: 75}); err != nil || out.Len() > maxAvatarBytes {
		return nil, invalid("头像过大，请选择较小的图片")
	}
	return out.Bytes(), nil
}

func (s *Server) userProfile(ctx context.Context, uid string) (M, error) {
	var name string
	var avatar []byte
	var revision int64
	if err := s.db.QueryRow(ctx, `SELECT display_name,avatar_jpeg,profile_revision FROM users WHERE id=$1`, uid).Scan(&name, &avatar, &revision); err != nil {
		return nil, err
	}
	var encoded any
	if len(avatar) > 0 {
		encoded = base64.StdEncoding.EncodeToString(avatar)
	}
	return M{"displayName": name, "avatarBase64": encoded, "revision": revision}, nil
}

func (s *Server) updateUserProfile(ctx context.Context, uid string, body M) (M, error) {
	if !s.rate("profile:"+uid, 10) {
		return nil, apierr(429, "RATE_LIMITED", "资料修改过于频繁，请稍后重试")
	}
	name := strings.TrimSpace(str(body, "displayName"))
	if name == "" || utf8.RuneCountInString(name) > 32 || strings.ContainsFunc(name, func(r rune) bool { return unicode.IsControl(r) || unicode.In(r, unicode.Cf) }) {
		return nil, invalid("昵称须为 1–32 个字符，不能包含控制字符")
	}
	value, replaceAvatar := body["avatarBase64"]
	var avatar []byte
	if replaceAvatar && value != nil {
		var err error
		avatar, err = canonicalAvatar(str(body, "avatarBase64"))
		if err != nil {
			return nil, err
		}
	}
	result, err := s.db.Exec(ctx, `UPDATE users SET display_name=$2,avatar_jpeg=CASE WHEN $3 THEN $4::bytea ELSE avatar_jpeg END,profile_revision=profile_revision+1 WHERE id=$1 AND profile_revision=$5 AND profile_revision<9007199254740991`, uid, name, replaceAvatar, avatar, number(body, "revision"))
	if err != nil {
		return nil, err
	}
	if result.RowsAffected() != 1 {
		return nil, apierr(409, "SOURCE_CONFLICT", "资料已在其他页面更新，请重新加载后再保存")
	}
	return s.userProfile(ctx, uid)
}
