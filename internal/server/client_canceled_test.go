// client_canceled_test.go 钉住「客户端自己断开」与「池中真的没有可用账号」的
// 末态区分。
//
// 生产实证（2026-10-13/14 归档 + sub2api ops_error_logs id 3304/3312）：
// axonhub/1.0 客户端自带约 30s 超时，同一现象在两个阶段各产生一类失败——
//   - 超时落在上传请求体阶段 → body_read_error（51 条，中位 29.5s）；
//   - 超时落在等响应阶段 → 客户端断开使 r.Context() 取消 → 轮转循环
//     break → 落到末端默认分支 → **误报 no_healthy_account**（14 条，中位 29.8s）。
//
// 后者的危害不是计费或罚分（账号确实没被罚），而是**排查方向被引向账号池**：
// gateway_hint 说「池中没有可用的健康账号」，运维会去查余额/熔断/暂停，
// 而当时 8/8 账号健康、真实原因是调用方自己放弃了这个请求。
package server

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/reqlog"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// cancelingUpstream 造出「客户端在等上游响应时断开」的真实链路：
// transport 阻塞在 reqCtx.Done() 上（取消沿 r.Context() → reqCtx 正常传播），
// 然后返回 context.Canceled —— 这正是 Go 的 http.Client 在请求被 ctx 取消时
// 返回的形态（*url.Error 包裹 context.Canceled）。不是伪造错误串。
//
// entered 在 transport 被进入时关闭，让测试能确定「请求已打到上游」再取消，
// 避免取消早于选号导致走成另一条路径。
func cancelingUpstream(t *testing.T) (*upstream.Client, <-chan struct{}) {
	t.Helper()
	entered := make(chan struct{})
	return &upstream.Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			select {
			case <-entered:
			default:
				close(entered)
			}
			<-r.Context().Done()
			return nil, r.Context().Err()
		})},
		ChatBaseCN:    "https://fake.example",
		BillingBaseCN: "https://fake.example",
	}, entered
}

// gatewayErr 解析错误响应体里的 error 对象（code / gateway_hint）。
func gatewayErr(t *testing.T, body string) map[string]any {
	t.Helper()
	var parsed struct {
		Error map[string]any `json:"error"`
	}
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("响应体不是合法 JSON: %v (%s)", err, body)
	}
	if parsed.Error == nil {
		t.Fatalf("响应体缺少 error 对象: %s", body)
	}
	return parsed.Error
}

// TestClientCanceledIsNotReportedAsNoHealthyAccount 是本次修复的核心回归。
//
// 修复前：客户端断开 → 轮转循环 break → 末端默认 code=no_healthy_account、
// hint=「池中没有可用的健康账号」、outcome=http_error。账号健康却报号池故障。
//
// 修复后：识别为调用方主动断开 → code=client_canceled、outcome=interrupted、
// 状态码离开 5xx「我们的错」区间，且**不得**再出现 no_healthy_account 文案。
func TestClientCanceledIsNotReportedAsNoHealthyAccount(t *testing.T) {
	// 轮转退避置 0：本用例断言的是分类而非等待时长（见 backoff.go 的说明）。
	prevBase := rotateBackoffBase
	rotateBackoffBase = 0
	t.Cleanup(func() { rotateBackoffBase = prevBase })

	up, entered := cancelingUpstream(t)
	a := &auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}
	p := testPoolWith(a)

	logs := reqlog.New(reqlog.Config{})
	t.Cleanup(logs.Close)
	h := NewHandler(Config{Pool: p, Upstream: up, PromptMode: "custom", PromptText: "sys", RequestLog: logs})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","stream":true,"messages":[{"role":"user","content":"hi"}]}`)).
		WithContext(ctx)

	// 请求已打到上游后立即模拟客户端断开。
	go func() {
		<-entered
		cancel()
	}()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	// ① 响应体不得再把调用方自己的断开说成号池故障。
	e := gatewayErr(t, rec.Body.String())
	code, _ := e["code"].(string)
	if code == "no_healthy_account" {
		t.Fatalf("客户端断开被误报为号池故障: code=%q body=%s", code, rec.Body)
	}
	if code != "client_canceled" {
		t.Fatalf("code=%q want client_canceled（body=%s）", code, rec.Body)
	}
	// hint 也必须换掉：它是运维的排查入口，留着 noHealthyHint 等于没修。
	hint, _ := e["gateway_hint"].(string)
	if hint == "" {
		t.Fatalf("client_canceled 必须带可操作的 gateway_hint，得到空串")
	}
	if strings.Contains(hint, "池中没有可用的健康账号") {
		t.Fatalf("client_canceled 仍带号池 hint: %q", hint)
	}
	// ② 状态码离开 5xx：这不是「服务端暂时过载」，客户端按可重试处理是错的。
	if rec.Code >= 500 {
		t.Fatalf("status=%d 客户端断开不该回 5xx（会把排查引向服务端故障）", rec.Code)
	}

	// ③ 账号不得被罚（这条修复前就成立，一并钉住防回归）。
	st, ok := p.Status(a.UID)
	if !ok {
		t.Fatal("账号不在池中")
	}
	if st.Cooling || st.ConsecutiveFails != 0 || st.BreakerFails != 0 {
		t.Fatalf("客户端断开不得罚账号: cooling=%v fails=%d breaker=%d",
			st.Cooling, st.ConsecutiveFails, st.BreakerFails)
	}

	// ④ 归档里的归因必须能被人看懂是「调用方走了」而不是「我们挂了」。
	recent := logs.Snapshot().Recent
	if len(recent) != 1 {
		t.Fatalf("归档事件数 = %d want 1", len(recent))
	}
	if recent[0].ErrorCode != "client_canceled" {
		t.Errorf("归档 error_code=%q want client_canceled", recent[0].ErrorCode)
	}
	if recent[0].Outcome != reqlog.OutcomeInterrupted {
		t.Errorf("归档 outcome=%q want %q", recent[0].Outcome, reqlog.OutcomeInterrupted)
	}
	if recent[0].OK {
		t.Error("归档 ok=true：请求未完成，不得记成功")
	}
}

// TestEmptyPoolStillReportsNoHealthyAccount 对照面：池中确实没有可用账号时，
// 必须仍然报 no_healthy_account。防止「客户端断开」的修复把真正的调度故障
// 一起改掉口径（那会让运维再也看不到号池问题）。
func TestEmptyPoolStillReportsNoHealthyAccount(t *testing.T) {
	prevBase := rotateBackoffBase
	rotateBackoffBase = 0
	t.Cleanup(func() { rotateBackoffBase = prevBase })

	up, _ := cancelingUpstream(t)
	p := testPoolWith() // 空池：没有任何账号
	h := NewHandler(Config{Pool: p, Upstream: up})

	rec := httptest.NewRecorder()
	// 注意：本用例的客户端**没有**断开（ctx 未被取消）。
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","stream":true,"messages":[{"role":"user","content":"hi"}]}`)))

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want 503（空池是服务端调度故障）", rec.Code)
	}
	e := gatewayErr(t, rec.Body.String())
	if code, _ := e["code"].(string); code != "no_healthy_account" {
		t.Fatalf("code=%q want no_healthy_account（空池口径不得被本次修复改掉）", code)
	}
}

// TestClientCanceledBeforeSelectionIsAlsoClassified 边界：客户端在**选号阶段**
// 之前就断开（轮转一次都没进上游）时，同样不得报号池故障。
// 覆盖的是循环末尾 break 之外的出口——循环条件本身因 ctx 取消而退出。
func TestClientCanceledBeforeSelectionIsAlsoClassified(t *testing.T) {
	prevBase := rotateBackoffBase
	rotateBackoffBase = 0
	t.Cleanup(func() { rotateBackoffBase = prevBase })

	a := &auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}
	p := testPoolWith(a)
	up, _ := cancelingUpstream(t)
	h := NewHandler(Config{Pool: p, Upstream: up})

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 进入 handler 前就已取消
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","stream":true,"messages":[{"role":"user","content":"hi"}]}`)).
		WithContext(ctx)

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	e := gatewayErr(t, rec.Body.String())
	if code, _ := e["code"].(string); code == "no_healthy_account" {
		t.Fatalf("已取消的请求仍被误报为号池故障: %s", rec.Body)
	}
}

// TestNonStreamClientCanceledIsNotUpstreamParse 是**真实上游烟测发现**的第二条
// 同类缺陷（单元测试最初漏了它，隔离实例上 max_tokens=4000 的非流式请求被
// curl -m 6 掐断后归档写出 upstream_parse/502）。
//
// 成因：客户端断开使 r.Context() 取消 → 我们读上游 body 被打断 → upstream.Aggregate
// 报读失败 → 直接归类 upstream_parse 502。但上游完全正常，502 是替调用方认错，
// 会把排查引向「上游协议兼容性」。
//
// 修复后：同一条 ctx 判据 → client_canceled / 499 / interrupted。
func TestNonStreamClientCanceledIsNotUpstreamParse(t *testing.T) {
	prevBase := rotateBackoffBase
	rotateBackoffBase = 0
	t.Cleanup(func() { rotateBackoffBase = prevBase })

	// transport 返回 200 + 一个「读到一半就取消」的 body：Aggregate 必然报错。
	up := &upstream.Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			pr, pw := io.Pipe()
			go func() {
				<-r.Context().Done()
				_ = pw.CloseWithError(r.Context().Err())
			}()
			return &http.Response{
				StatusCode: 200,
				Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
				Body:       pr,
			}, nil
		})},
		ChatBaseCN:    "https://fake.example",
		BillingBaseCN: "https://fake.example",
	}
	a := &auth.Auth{UID: "u1", AccessToken: "at1", ExpiresAt: 9999999999}
	p := testPoolWith(a)
	logs := reqlog.New(reqlog.Config{})
	t.Cleanup(logs.Close)
	h := NewHandler(Config{Pool: p, Upstream: up, PromptMode: "custom", PromptText: "sys", RequestLog: logs})

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("POST", "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","stream":false,"max_tokens":4000,"messages":[{"role":"user","content":"hi"}]}`)).
		WithContext(ctx)
	go func() {
		time.Sleep(50 * time.Millisecond) // 让请求先打到上游
		cancel()
	}()

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	e := gatewayErr(t, rec.Body.String())
	code, _ := e["code"].(string)
	if code == "upstream_parse" {
		t.Fatalf("客户端断开被误报为上游解析失败: %s", rec.Body)
	}
	if code != "client_canceled" {
		t.Fatalf("code=%q want client_canceled（body=%s）", code, rec.Body)
	}
	if rec.Code != statusClientClosedRequest {
		t.Fatalf("status=%d want %d", rec.Code, statusClientClosedRequest)
	}
	recent := logs.Snapshot().Recent
	if len(recent) != 1 || recent[0].Outcome != reqlog.OutcomeInterrupted {
		t.Fatalf("归档 outcome 应为 interrupted: %+v", recent)
	}
}
