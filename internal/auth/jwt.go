package auth

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"time"
)

// JWTPayload 从 access token 中读取 sub / exp 声明，用于**元数据兜底**。
//
// 用途：cockpit tools 导出的账号可能带 refresh_token 为空、expires_at 为 0，
// 而 access token 本身是完全有效的 JWT（含 exp）。导入时若无此兜底，这类账号
// 要么被拒之门外（issue #165：字段校验把「只有 access token」与「字段全缺」
// 合并成同一个跳过分支），要么拿到一个假过期时间（现在+365天）绕过 NeedsRefresh
// 判定，一进池就被判「需刷新」而在没有 refresh_token 的刷新上反复打转。
//
// 安全性：**不做签名校验**。这里只解析自身持有的 token 的公开声明，用于填
// uid / 过期时间这类元数据；token 是否被上游接受由上游判定，网关不据此授权任何
// 东西。因此不需要（也不应该）在此引入密钥。声明缺失或格式异常时返回零值 +
// ok=false，调用方自行回退。
func JWTPayload(token string) (sub string, exp time.Time, ok bool) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return "", time.Time{}, false
	}
	// JWT 用 base64url 无填充；部分实现会补 '='，RawURLEncoding 不接受，故先剔除。
	payload := strings.TrimRight(parts[1], "=")
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		// 容忍标准 base64 变体（历史上见过带 +/ 的脏 token）。
		raw, err = base64.RawStdEncoding.DecodeString(payload)
		if err != nil {
			return "", time.Time{}, false
		}
	}
	var claims struct {
		Sub string          `json:"sub"`
		Exp json.RawMessage `json:"exp"`
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		return "", time.Time{}, false
	}
	sub = strings.TrimSpace(claims.Sub)
	if len(claims.Exp) > 0 {
		// exp 规范上是 NumericDate（秒），容忍被写成字符串或毫秒的形式。
		var secs int64
		if err := json.Unmarshal(claims.Exp, &secs); err != nil {
			var s string
			if err := json.Unmarshal(claims.Exp, &s); err == nil {
				secs = parseNumericDate(s)
			}
		}
		if secs > 0 {
			if secs > 1e12 { // 毫秒
				secs /= 1000
			}
			exp = time.Unix(secs, 0)
		}
	}
	ok = sub != "" || !exp.IsZero()
	return sub, exp, ok
}

// parseNumericDate 解析字符串形式的 NumericDate（秒或毫秒）。
func parseNumericDate(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int64(c-'0')
	}
	return n
}
