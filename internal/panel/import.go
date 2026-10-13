package panel

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// cockpitAccount 映射 cockpit tools 导出格式的单个账号。
type cockpitAccount struct {
	ID             string `json:"id"`
	Email          string `json:"email"`
	UID            string `json:"uid"`
	Nickname       string `json:"nickname"`
	AccessToken    string `json:"access_token"`
	RefreshToken   string `json:"refresh_token"`
	TokenType      string `json:"token_type"`
	ExpiresAt      int64  `json:"expires_at"`
	Domain         string `json:"domain"`
	DosageNotify   string `json:"dosage_notify_code"`
	PaymentType    string `json:"payment_type"`
	Status         string `json:"status"`
	UsageUpdatedAt int64  `json:"usage_updated_at"`
	LastCheckin    int64  `json:"last_checkin_time"`
	CheckinStreak  int    `json:"checkin_streak"`
	CreatedAt      int64  `json:"created_at"`
	LastUsed       int64  `json:"last_used"`
}

// importCockpit 接收 cockpit tools 导出的 JSON 文件，批量导入账号到池中。
//
//	POST /panel/api/import/cockpit
//	Content-Type: multipart/form-data
//	Body: file=<json>
//
// 返回 {ok, total, imported, skipped, errors}。
func (p *Panel) importCockpit(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeErr(w, http.StatusBadRequest, "parse form: "+err.Error())
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		writeErr(w, http.StatusBadRequest, "missing file field: "+err.Error())
		return
	}
	defer file.Close()

	raw, err := io.ReadAll(file)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "read file: "+err.Error())
		return
	}

	accounts, err := parseImportAccounts(raw)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if len(accounts) == 0 {
		writeErr(w, http.StatusBadRequest, "empty accounts array")
		return
	}
	var total, imported, skipped int
	var errs []string
	var warnings []string

	for _, acc := range accounts {
		uid := strings.TrimSpace(acc.UID)
		at := strings.TrimSpace(acc.AccessToken)
		rt := strings.TrimSpace(acc.RefreshToken)
		// 只有 access_token 是硬性必填（issue #165）：refresh_token 对本项目并非必需
		// ——auth.Parse 只要求 accessToken，LoadDir 能正常加载这类凭证，且 access
		// token 自带 JWT exp，账号在到期前完全可用。旧代码把 rt 也算必填，于是
		// 「文件里全是有效 access token」被变成「一个都导不进来」。
		if at == "" {
			skipped++
			errs = append(errs, fmt.Sprintf("缺少 access_token (id=%s)", acc.ID))
			continue
		}
		// uid 回退链：uid → id → access token 的 JWT sub。
		if uid == "" {
			uid = strings.TrimSpace(acc.ID)
		}
		jwtSub, jwtExp, _ := auth.JWTPayload(at)
		if uid == "" {
			uid = jwtSub
		}
		if !validImportUID(uid) {
			skipped++
			errs = append(errs, fmt.Sprintf("uid 非法 (id=%s)", acc.ID))
			continue
		}
		// 按 domain 推断 realm：workbuddy.ai 家族 → global，否则 cn。
		realm := auth.ResolveRealm("", acc.Domain)

		// 过期时间回退链：expires_at（毫秒）→ access token 的 JWT exp → 未知(0)。
		//
		// 最后那档不再填「现在+365天」：假过期时间会**绕过** NeedsRefresh 判定，
		// 账号带一个根本不存在的有效期一路跑，直到上游 401 才发现。填 0 则
		// NeedsRefresh 恒真、语义诚实（「不知道何时过期」），但代价是每次请求都
		// 触发一次刷新尝试——因此无 refresh_token 时改为在建号处明确禁用（见下），
		// 而不是留在池里反复空转。
		// 过期时间回退链：expires_at（按量级判毫秒/秒）→ access token 的 JWT exp → 未知(0)。
		//
		// 量级判别（issue #165 附带问题 2）：旧代码无条件 /1000，若导出方给的是**秒**
		// 就得到 1970 年 —— 它非 0，绕过下面「未知」兜底，账号一进池就被 NeedsRefresh
		// 判为需刷新，在没有 refresh_token 时每次请求都在刷新失败上打转。
		//
		// 未知那档不再填「现在+365天」：假过期时间会**绕过** NeedsRefresh 判定，账号
		// 带着一个根本不存在的有效期一路跑，直到上游 401 才发现。填 0 则语义诚实
		// （「不知道何时过期」），代价是 NeedsRefresh 恒真；因此无 refresh_token 时
		// 由下方 denied 分支明确禁用该账号，而不是留在池里空转。
		expiresAt := normalizeUnixSeconds(acc.ExpiresAt)
		if expiresAt <= 0 && !jwtExp.IsZero() {
			expiresAt = jwtExp.Unix()
		}

		nickname := acc.Nickname
		if strings.TrimSpace(nickname) == "" {
			nickname = acc.Email
		}

		a := &auth.Auth{
			AccessToken:  at,
			RefreshToken: rt,
			ExpiresAt:    expiresAt,
			Domain:       acc.Domain,
			UID:          uid,
			Nickname:     nickname,
			FilePath:     filepath.Join(p.cfg.AuthDir, fmt.Sprintf("workbuddy-%s.json", uid)),
		}

		if realm == "global" {
			if _, err := auth.BackfillRealmFor(a, "global"); err != nil {
				skipped++
				errs = append(errs, fmt.Sprintf("uid=%s: set realm failed: %v", uid, err))
				continue
			}
		} else {
			_, _ = a.BackfillRealm()
		}

		if err := a.SaveAtomic(); err != nil {
			skipped++
			errs = append(errs, fmt.Sprintf("uid=%s: save auth failed: %v", uid, err))
			continue
		}

		p.cfg.Pool.Add(a)
		p.cfg.Pool.Revive(uid)

		// 无 refresh_token 的账号：access token 到期后无法自动续期（issue #165 建议 3）。
		// 这里只**警告**、不禁用——access token 在到期前完全可用，导入就禁掉等于把一个
		// 还有一个月寿命的账号直接作废。真正的禁用发生在 handler：等到确实需要刷新而
		// refresh_token 为空时再禁用（见 handler 的 refresh 分支），那时才既不能续期、
		// 也不该继续空转。
		if rt == "" {
			warnings = append(warnings, fmt.Sprintf("uid=%s：无 refresh_token——access token %s，之后无法自动续期（届时账号会被禁用，需重新登录或重新导入）",
				uid, expiryPhrase(expiresAt)))
		}
		// 顺带签到/激活（幂等；失败仅记日志，不阻断导入）。
		if realm == "global" {
			if activated, err := p.cfg.Upstream.GlobalCompleteRegistration(a); err != nil {
				log.Printf("panel: import global 注册激活 uid=%s: %v", uid, err)
			} else if activated {
				log.Printf("panel: import global 注册激活 uid=%s 完成", uid)
			}
			if claimed, err := p.cfg.Upstream.ClaimTrial(a); err != nil {
				log.Printf("panel: import global trial uid=%s: %v", uid, err)
			} else if claimed {
				log.Printf("panel: import global trial uid=%s 已领", uid)
			}
		} else {
			if err := p.cfg.Upstream.DailyCheckin(a); err != nil {
				log.Printf("panel: import checkin uid=%s: %v", uid, err)
			}
		}
		if rm, tt, err := p.cfg.Upstream.UserResource(a); err == nil {
			p.cfg.Pool.ReenableIfCredits(uid, rm, tt)
		}

		imported++
	}

	total = len(accounts)
	log.Printf("panel: cockpit import finished total=%d imported=%d skipped=%d warnings=%d", total, imported, skipped, len(warnings))
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":       true,
		"total":    total,
		"imported": imported,
		"skipped":  skipped,
		"errors":   errs,
		// warnings 与 errors 分开（issue #165 建议 2）：warnings 是「已导入但需知情」
		// （如无 refresh_token），errors 是「跳过原因」。前端两者都上屏。
		"warnings": warnings,
	})
}

// parseImportAccounts 兼容几种实际文件形态（issue #165 建议 4）：
//
//	[ {...}, {...} ]            裸数组（cockpit tools 原始导出）
//	{ "accounts": [ {...} ] }   信封形
//	{ ...单个账号对象... }        顶层单对象
//
// 并容忍 **UTF-8 BOM**：Go 的 encoding/json 不吞 BOM，而 PowerShell 5.1 的
// `Set-Content -Encoding utf8` 会写 BOM，报错是
// `invalid character 'ï' looking for beginning of value`，用户完全无从下手。
func parseImportAccounts(raw []byte) ([]cockpitAccount, error) {
	raw = bytes.TrimPrefix(raw, []byte{0xEF, 0xBB, 0xBF}) // UTF-8 BOM
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 {
		return nil, fmt.Errorf("空文件")
	}
	// 裸数组。
	if raw[0] == '[' {
		var list []cockpitAccount
		if err := json.Unmarshal(raw, &list); err != nil {
			return nil, err
		}
		return list, nil
	}
	// 信封形 {"accounts":[...]}。
	var env struct {
		Accounts []cockpitAccount `json:"accounts"`
	}
	if err := json.Unmarshal(raw, &env); err == nil && env.Accounts != nil {
		return env.Accounts, nil
	}
	// 顶层单对象。
	var one cockpitAccount
	if err := json.Unmarshal(raw, &one); err != nil {
		return nil, err
	}
	return []cockpitAccount{one}, nil
}

// normalizeUnixSeconds 把导出方给的过期时间统一成**秒**。
//
// 量级判别而非无条件 /1000（issue #165 附带问题 2）：cockpit tools 给的是毫秒，
// 但也有导出方给秒。无条件 /1000 会把秒值变成 1970 年——非 0，于是绕过「未知」
// 兜底，账号一进池就被 NeedsRefresh 判为需刷新。阈值取 1e12：秒值到公元 33658 年
// 才达到，毫秒值在 2001 年就超过了，判别安全。
func normalizeUnixSeconds(v int64) int64 {
	if v <= 0 {
		return 0
	}
	if v > 1e12 { // 毫秒
		return v / 1000
	}
	return v
}

// expiryPhrase 把过期时间渲染成人话，供警告文案使用（0 = 未知）。
func expiryPhrase(expiresAt int64) string {
	if expiresAt <= 0 {
		return "有效期未知"
	}
	t := time.Unix(expiresAt, 0)
	d := time.Until(t)
	return fmt.Sprintf("将于 %s 到期（约 %d 天后）", t.Format("2006-01-02 15:04"), int(d.Hours()/24))
}

// validImportUID 校验导入 uid 是否可用于拼文件名（同 login.go validUID 口径）。
func validImportUID(uid string) bool {
	if uid == "" || len(uid) > 64 {
		return false
	}
	for _, c := range uid {
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
		default:
			return false
		}
	}
	return true
}
