// realm_hint_test.go 钉住「该域一个可用账号都没有」时的可操作提示。
//
// 生产实证（2026-10-14）：`global:` 域唯一账号被移除后，每次请求都得到
//
//	{"code":"no_healthy_account",
//	 "gateway_hint":"池中没有可用的健康账号；请查看 /status 或稍后重试"}
//
// 而 `/status` 的 `realm_totals.global` 显示 `total:0` —— 提示让运维去查一个
// 会告诉他「这里本来就没有账号」的地方，没说**这个域是空的**、也没说
// 「cn 域正常、只是你请求的那个域没有号」。真实浪费：这个状态持续了数小时，
// 期间 24 条 503，每次都要人工翻 journal 才能定位。
//
// 修复：选号失败且**该域一个账号都没有**时，hint 直说这件事，并给出各域账号数
// 与恢复动作。只在「域为空」这一确定性事实上加话，不猜测成因。
package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// TestEmptyRealmHintNamesTheEmptyRealm 是本次修复的核心回归。
//
// 池中只有 cn 账号，请求 global: → 必须明确告知「global 域没有账号」，
// 而不是泛泛地说「池中没有可用的健康账号」。
func TestEmptyRealmHintNamesTheEmptyRealm(t *testing.T) {
	// 打开 global 路由开关：否则 Realm() 恒返回 cn，请求根本走不到 global 分支。
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })

	// 池里只有 cn 账号 —— global 域 total=0，这是生产实际形态。
	cn := &auth.Auth{UID: "cn1", AccessToken: "at1", ExpiresAt: 9999999999}
	p := testPoolWith(cn)
	up, _ := cancelingUpstream(t)
	h := NewHandler(Config{Pool: p, Upstream: up})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"global:deepseek-v4.1-flash","stream":false,"messages":[{"role":"user","content":"hi"}]}`)))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503", rec.Code)
	}
	e := gatewayErr(t, rec.Body.String())
	code, _ := e["code"].(string)
	if code != "no_healthy_account" {
		t.Fatalf("code=%q want no_healthy_account（这仍是本地调度失败，不改 code）", code)
	}
	hint, _ := e["gateway_hint"].(string)

	// ① 必须点名是哪个域空的。
	if !strings.Contains(hint, "global") {
		t.Errorf("hint 未点名空域: %q", hint)
	}
	// ② 必须说明该域账号数为 0（而不是「健康数」——0 个账号和 0 个健康是两回事）。
	if !strings.Contains(hint, "0") {
		t.Errorf("hint 未给出该域账号数: %q", hint)
	}
	// ③ 必须告诉运维 cn 域是好的，否则他会以为整个网关挂了。
	if !strings.Contains(hint, "cn") {
		t.Errorf("hint 未给出其他域的可用情况: %q", hint)
	}
	// ④ 必须给出恢复动作。
	if !strings.Contains(hint, "登录") && !strings.Contains(hint, "添加") {
		t.Errorf("hint 未给出恢复动作: %q", hint)
	}
}

// TestNonEmptyRealmFailureKeepsGenericHint 反面钉桩：该域**有**账号但都不可用时，
// 不得说「这个域没有账号」（那是假话），仍走通用 hint。
//
// 防的是把新文案写成「选号失败就报域为空」—— 那会让真正的账号级故障
// （全部冷却/禁用/暂停）也被误读成「没加过号」。
func TestNonEmptyRealmFailureKeepsGenericHint(t *testing.T) {
	auth.SetGlobalEnabled(true)
	t.Cleanup(func() { auth.SetGlobalEnabled(true) })

	// global 域有一个账号，但它被暂停选号 → 选号失败，域并不为空。
	g := &auth.Auth{UID: "g1", AccessToken: "at1", ExpiresAt: 9999999999}
	if _, err := auth.BackfillRealmFor(g, "global"); err != nil {
		t.Fatalf("设置 realm: %v", err)
	}
	p := testPoolWith(g)
	// 暂停选号：域内有账号但它不可选 → 选号失败，而域并不为空。
	p.Pause(g.UID)
	up, _ := cancelingUpstream(t)
	h := NewHandler(Config{Pool: p, Upstream: up})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"global:deepseek-v4.1-flash","stream":false,"messages":[{"role":"user","content":"hi"}]}`)))

	e := gatewayErr(t, rec.Body.String())
	hint, _ := e["gateway_hint"].(string)
	if strings.Contains(hint, "没有任何账号") || strings.Contains(hint, "没有账号") {
		t.Fatalf("域内有账号（只是被暂停）时不得声称该域为空: %q", hint)
	}
}
