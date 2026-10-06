package upstream

import (
	"strings"
	"testing"
)

// TestGatewayHintChineseByDefault gateway_hint 必须用中文（面板/客户端主要中文
// 用户；上游原始 body 里 code/msg/requestId 已是中英混排，hint 再写英文会让同一
// 响应出现两种语言）。本测试守住语言契约：已覆盖的 Kind 不得回落成英文。
func TestGatewayHintChineseByDefault(t *testing.T) {
	long := "request context exceeds the model's limit"
	kinds := []struct {
		kind ErrKind
		msg  string
	}{
		{ErrPromptTooLong, long},
		{ErrImageInvalid, "invalid image"},
		{ErrWafBlock, "waf"},
		{ErrSoftRate, "rate limited"},
		{ErrAccountFault, "account fault"},
		{ErrSessionDead, "session dead"},
		{ErrHardCredit, "credits exhausted"},
		{ErrModelBlocked, "no such model"},
		{ErrBadParams, "bad params"},
		{ErrContentBlocked, "content policy"},
		{ErrClient, `{"code":11133,"msg":"the request parameters were rejected by the model provider"}`},
	}
	for _, c := range kinds {
		got := GatewayHint(c.kind, c.msg, HintContext{})
		if got == "" {
			continue // 无 hint 的形态（本测试不强制每种都有）
		}
		if !strings.ContainsAny(got, "，。；：、请该") && !containsHan(got) {
			t.Errorf("kind=%v hint is not Chinese: %q", c.kind, got)
		}
	}
	// 图片家族（11133 + 带图 + 目录判定不支持）也要中文。
	img := GatewayHint(ErrBadParams, `{"code":11133,"msg":"model_param_invalid"}`,
		HintContext{Model: "glm-5.2", HasImage: true, ModelInCatalog: true, ModelSupportsImages: false})
	if !containsHan(img) {
		t.Errorf("image-family hint is not Chinese: %q", img)
	}
	// 11135 家族同理。
	if h := GatewayHint(ErrClient, `{"code":11135,"msg":"invalid_image_data"}`, HintContext{}); !containsHan(h) {
		t.Errorf("11135 hint is not Chinese: %q", h)
	}
	// 本地调度 hint 也中文化。
	if !containsHan(NoHealthyAccountHint()) {
		t.Errorf("NoHealthyAccountHint is not Chinese: %q", NoHealthyAccountHint())
	}
	// 上游超时 hint 同理，且**必须**与"池中无健康号"区分开——两者排查方向不同。
	if !containsHan(UpstreamTimeoutHint()) {
		t.Errorf("UpstreamTimeoutHint is not Chinese: %q", UpstreamTimeoutHint())
	}
	if UpstreamTimeoutHint() == NoHealthyAccountHint() {
		t.Error("UpstreamTimeoutHint 与 NoHealthyAccountHint 不能相同：超时是上游侧问题，不是账号池问题")
	}
}

// containsHan 报告 s 是否含至少一个 CJK 汉字。
func containsHan(s string) bool {
	for _, r := range s {
		if r >= 0x4E00 && r <= 0x9FFF {
			return true
		}
	}
	return false
}
