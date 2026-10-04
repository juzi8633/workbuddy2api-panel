package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
)

// errorFrameSSE 上游「200 已开流 + 一帧 error」的真实形态：HTTP 层成功，
// 错误在 SSE 帧里（6004 模型级限流是这种形态的典型）。
const errorFrameSSE = "data: {\"id\":\"c1\",\"object\":\"chat.completion.chunk\",\"created\":1," +
	"\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hi\"}}]}\n\n" +
	"data: {\"error\":{\"code\":6004,\"message\":\"使用量已超出频率限制，将在 2026-10-03 00:46:20 UTC+8 重置\"}}\n\n" +
	"data: [DONE]\n\n"

// TestStreamErrorFrameDoesNotMarkAccountHealthy 钉住「限流号不得被记成健康号」。
//
// 修复前：NoteSuccess / 清 11102 负缓存 / 绑粘性 都在**读第一帧之前**执行。上游
// 「200 已开流 + 一帧 error」（6004 限流）时，被限流的账号被记成成功，粘性还会把
// 会话钉死在它身上，后续每一轮都打同一个限流号。
//
// 修复后：成功判定延后到流读完且无 error 帧；error 帧按 FrameKind 分类处置账号。
func TestStreamErrorFrameDoesNotMarkAccountHealthy(t *testing.T) {
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 200, errorFrameSSE, true
	})
	a := &auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}
	p := testPoolWith(a)
	h := NewHandler(Config{Pool: p, Upstream: up, PromptMode: "custom", PromptText: "sys"})

	body := `{"model":"glm-5.2","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))

	st, ok := p.Status(a.UID)
	if !ok {
		t.Fatal("账号不在池中")
	}
	// 账号不得被记为成功。
	if st.SuccessCount != 0 {
		t.Errorf("success_count=%d want 0（error 帧不得记成功）", st.SuccessCount)
	}
	// 6004 是模型级限流：应产生该模型的限流台账/冷却，而不是把号留在池里当健康号。
	limited := len(st.RateLimitedModels) > 0 || st.Cooling
	if !limited {
		t.Errorf("6004 error 帧未对该账号产生任何限流处置: %+v", st)
	}
}

// TestStreamRealSuccessStillMarksAccountHealthy 对照：正常流（无 error 帧）必须
// 照常记成功——延后判定不能把正常路径一起关掉。
func TestStreamRealSuccessStillMarksAccountHealthy(t *testing.T) {
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 200, sseOK, true
	})
	a := &auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}
	p := testPoolWith(a)
	h := NewHandler(Config{Pool: p, Upstream: up, PromptMode: "custom", PromptText: "sys"})

	body := `{"model":"glm-5.2","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))

	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d body=%s", rec.Code, rec.Body)
	}
	st, _ := p.Status(a.UID)
	if st.SuccessCount != 1 {
		t.Errorf("success_count=%d want 1（正常流必须记成功）", st.SuccessCount)
	}
}

// TestUpstreamTimeoutDoesNotRotateOrPenalize 钉住超时止损：
//
// 修复前：超时与"网络抖动"共用同一条换号路径——注定超时的请求会轮转 MaxRotate 次
// （每次再等一个 header_timeout），期间给一串健康号喂连败计数。生产归档里 503 的
// duration 精确为 125s（= 2×60s + 退避）即该缺陷的实证。
//
// 修复后：超时**不换号、不罚号**，只打一次上游，且末端返回可区分的 upstream_timeout。
func TestUpstreamTimeoutDoesNotRotateOrPenalize(t *testing.T) {
	// 用两个账号：若发生无谓轮转，上游会被调用 2 次。
	up := newFakeUpstream(t, func(authz string) (int, string, bool) {
		return 200, sseOK, true
	})
	// 让 ChatHTTP 走一个"响应头超时"的传输：直接构造 net.Error 超时。
	up.HTTP = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return nil, timeoutNetErr{}
	})}

	a1 := &auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}
	a2 := &auth.Auth{UID: "u2", AccessToken: "at2", ExpiresAt: 9999999999}
	p := testPoolWith(a1, a2)
	h := NewHandler(Config{Pool: p, Upstream: up, PromptMode: "custom", PromptText: "sys"})

	body := `{"model":"glm-5.2","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions", strings.NewReader(body)))

	if !strings.Contains(rec.Body.String(), "upstream_timeout") {
		t.Errorf("末端 code 应为 upstream_timeout，body=%s", rec.Body.String())
	}
	// 两个账号都不得被喂连败/熔断计数。
	for _, a := range []*auth.Auth{a1, a2} {
		st, ok := p.Status(a.UID)
		if !ok {
			continue
		}
		if st.BreakerFails != 0 || st.ConsecutiveFails != 0 || st.Cooling {
			t.Errorf("账号 %s 被罚：breaker=%d consecutive=%d cooling=%v（超时不罚号）",
				a.UID, st.BreakerFails, st.ConsecutiveFails, st.Cooling)
		}
	}
}

// timeoutNetErr 实现 net.Error 且自报超时（模拟 ResponseHeaderTimeout）。
type timeoutNetErr struct{}

func (timeoutNetErr) Error() string   { return "net/http: timeout awaiting response headers" }
func (timeoutNetErr) Timeout() bool   { return true }
func (timeoutNetErr) Temporary() bool { return true }
