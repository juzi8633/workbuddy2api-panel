package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/reqlog"
)

// TestMissingRefreshTokenDisablesAccountInsteadOfRotating 钉住 issue #165 建议 3：
// 「需要刷新但 refresh_token 为空」是**永久性**状态，必须禁用该账号并给出可读原因，
// 而不是走通用失败分支——否则每次请求都白跑一轮刷新、顺带 NoteError 堆熔断计数，
// 把整个池拖慢（其它账号要陪着退避换号）。
func TestMissingRefreshTokenDisablesAccountInsteadOfRotating(t *testing.T) {
	withChatLog(t)
	logs := reqlog.New(reqlog.Config{})
	defer logs.Close()

	// token 已过期（NeedsRefresh 恒真）+ 无 refresh_token → 必然进入该分支。
	a := &auth.Auth{UID: "noref", AccessToken: "expired-at", ExpiresAt: time.Now().Add(-time.Hour).Unix()}
	p := testPoolWith(a)
	up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, sseOK, true })
	h := NewHandler(Config{RequestLog: logs, Upstream: up, Pool: p, MaxRotate: 3})

	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions",
		strings.NewReader(`{"model":"glm-5.2","stream":true,"messages":[]}`)))

	// 账号必须被禁用，且原因是缺 refresh_token。
	s, ok := p.Status("noref")
	if !ok {
		t.Fatal("状态里找不到 noref")
	}
	if !s.Disabled {
		t.Fatalf("账号未被禁用：%+v（应在「需要刷新但无 refresh_token」时禁用）", s)
	}
	if !strings.Contains(s.Reason, "refresh_token") {
		t.Fatalf("禁用原因应指明缺 refresh_token，实际 %q", s.Reason)
	}
	// 不得把它算作「健康问题」计入失败计数（那不是网络或上游的错）。
	if s.ErrTotal > 0 {
		t.Errorf("不应累计 err_total（永久性配置问题，非上游故障）：%d", s.ErrTotal)
	}

	// 请求本身应当失败（400/502 均可），但不应泄漏 token 内容。
	if w.Code == http.StatusOK {
		t.Errorf("无 refresh_token 且 token 过期时不应返回 200: %s", w.Body.String())
	}
	data, err := json.Marshal(w.Body.String())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), "expired-at") {
		t.Errorf("响应泄漏了 access token: %s", data)
	}
}
