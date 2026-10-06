// model_blocked_test.go 钉住「模型级阻塞」与「池子真没号」在客户端可见层面的分离。
//
// 背景：选号返回 nil 有两种成因，处置相反——
//
//	(a) 池子真没可用号 → 该等（no_healthy_account / 503）；
//	(b) 号都在，但每个号都对该模型处于模型级冷却（11102/6004）→ 该换模型（400）。
//
// 丢信息的时序：首次请求还能看到上游原文（lastErr 非空），一旦负缓存写入，
// 后续请求 lastErr 为空，就只剩「池子没号」，上游原文与解封时间全丢。
//
// 另有一处必须钉住的细节：本包的 hint 纪律是**中文**（见 internal/upstream/hint.go
// 文件头），所以模型级阻塞的 hint 不能沿用 NoHealthyAccountHint 的措辞。
package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// allModelsCooled 让池中**所有**账号都对指定模型处于模型级冷却，模拟
// 「负缓存已写好、后续请求 lastErr 为空」的那个时点。
func allModelsCooled(t *testing.T, p *pool.Pool, model string, until time.Time) {
	t.Helper()
	for _, s := range p.List() {
		// reason 用真实的 11102 原文形态，与生产负缓存一致。
		p.BlockModelBackoff(s.UID, model, "11102 model ["+model+"] service info not found")
		_ = until // BlockModelBackoff 自算 Until；此处只保证条目存在
	}
}

// TestModelBlockedNotReportedAsAccountExhaustion 是 issue #102a 的核心断言：
// 全池都对该模型冷却时，客户端必须看到 model_unavailable（400，换模型），
// 而不是 no_healthy_account（503，服务端过载 → 客户端会无脑重试，#81）。
func TestModelBlockedNotReportedAsAccountExhaustion(t *testing.T) {
	p := testPoolWith(
		&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999},
		&auth.Auth{UID: "u2", AccessToken: "at2", ExpiresAt: 9999999999},
	)
	allModelsCooled(t, p, "glm-5.3", time.Now().Add(6*time.Hour))

	// 上游不应被触达：选号阶段就应因模型级冷却拿不到号。
	var upstreamHits int
	up := newFakeUpstream(t, func(string) (int, string, bool) {
		upstreamHits++
		return 500, `{"code":1,"msg":"should not be reached"}`, false
	})
	h := NewHandler(Config{Pool: p, Upstream: up})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}`)))

	if rec.Code != http.StatusBadRequest {
		t.Errorf("code=%d want 400（模型级阻塞不可重试），body=%s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "model_unavailable") {
		t.Errorf("响应应含 model_unavailable，得到 %s", body)
	}
	if strings.Contains(body, "no_healthy_account") {
		t.Errorf("模型级阻塞不得报成账号耗尽：%s", body)
	}
	// hint 必须指明「换模型」，而不是把排查引向账号池。
	if strings.Contains(body, "池中没有可用的健康账号") {
		t.Errorf("hint 仍指向账号池（方向错误）：%s", body)
	}
	if upstreamHits != 0 {
		t.Errorf("模型级冷却应在选号阶段拦下，不应触达上游（实际 %d 次）", upstreamHits)
	}
}

// TestModelBlockedHintCarriesCountAndUnblock 钉住 hint 里必须带出「几个账号被挡」
// 与「最早解封」，这两件事上游不会告诉客户端。
func TestModelBlockedHintCarriesCountAndUnblock(t *testing.T) {
	p := testPoolWith(
		&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999},
		&auth.Auth{UID: "u2", AccessToken: "at2", ExpiresAt: 9999999999},
		&auth.Auth{UID: "u3", AccessToken: "at3", ExpiresAt: 9999999999},
	)
	allModelsCooled(t, p, "glm-5.3", time.Now())
	up := newFakeUpstream(t, func(string) (int, string, bool) { return 500, "{}", false })
	h := NewHandler(Config{Pool: p, Upstream: up})

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}`)))

	body := rec.Body.String()
	if !strings.Contains(body, "3 个账号被挡") {
		t.Errorf("hint 应带出被挡账号数（3），得到 %s", body)
	}
	if !strings.Contains(body, "最早解封") {
		t.Errorf("hint 应带出最早解封时间，得到 %s", body)
	}
}

// TestModelNotBlockedWhenSomeAccountServes 反向用例：只要还有账号能服务该模型，
// 就**不能**报模型级阻塞——此时选号失败另有原因（账号级冷却/在途占满/积分保底），
// 应继续走原有的 no_healthy_account 口径。这是防「误报成模型不可用、把用户赶去换模型」。
func TestModelNotBlockedWhenSomeAccountServes(t *testing.T) {
	p := testPoolWith(
		&auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999},
		&auth.Auth{UID: "u2", AccessToken: "at2", ExpiresAt: 9999999999},
	)
	// 只有 u1 对该模型冷却，u2 可以服务 → 不是全池阻塞。
	allModelsCooled(t, p, "glm-5.3", time.Now())
	p.BlockModelClear("u2", "glm-5.3")
	// 但把 u2 整体打成不可用，让选号仍然失败（模拟账号级冷却）。
	p.Cooldown("u2", pool.CoolHard, time.Hour, "test")

	up := newFakeUpstream(t, func(string) (int, string, bool) { return 500, "{}", false })
	h := NewHandler(Config{Pool: p, Upstream: up})
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.3","messages":[{"role":"user","content":"hi"}]}`)))

	body := rec.Body.String()
	if strings.Contains(body, "model_unavailable") {
		t.Errorf("还有账号能服务该模型时不得报 model_unavailable：%s", body)
	}
}

// TestModelBlockedHintDiffersFromNoHealthyHint 语言与语义纪律：
// 模型级阻塞的 hint 必须与「池中没有可用的健康账号」不同，否则排查方向相反。
func TestModelBlockedHintDiffersFromNoHealthyHint(t *testing.T) {
	hint := upstream.ModelBlockedHint()
	if hint == "" {
		t.Fatal("ModelBlockedHint 不应为空（空串会让响应丢字段）")
	}
	if hint == upstream.NoHealthyAccountHint() {
		t.Fatal("模型级阻塞 hint 与 noHealthyHint 相同——两者排查方向相反，必须区分")
	}
	if !strings.Contains(hint, "模型") {
		t.Errorf("hint 应按本包纪律使用中文，得到 %q", hint)
	}
	if !strings.Contains(hint, "换") {
		t.Errorf("hint 应传达「换账号无用」，得到 %q", hint)
	}
}
