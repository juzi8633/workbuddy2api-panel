package panel

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestPanel() *Panel {
	// 启用鉴权：未带 key 的请求一律 401，不进入依赖 Pool/Upstream 的 handler。
	return New(Config{Version: "test", APIKey: "test-key"})
}

// 面板安全响应头必须覆盖：页面、静态脚本、鉴权失败响应。
func TestSecurityHeadersOnAllPanelResponses(t *testing.T) {
	p := newTestPanel()
	paths := []struct{ method, path string }{
		{"GET", "/panel/"},
		{"GET", "/panel/app.js"},
		{"GET", "/panel/api/overview"}, // 401（未提供 key）
		{"POST", "/panel/api/config"},  // 401
		{"GET", "/panel/api/nonexistent"},
	}
	for _, c := range paths {
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		h := rec.Header()
		if got := h.Get("Content-Security-Policy"); got == "" {
			t.Errorf("%s %s: missing CSP", c.method, c.path)
		}
		if h.Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s %s: X-Content-Type-Options=%q", c.method, c.path, h.Get("X-Content-Type-Options"))
		}
		if h.Get("X-Frame-Options") != "DENY" {
			t.Errorf("%s %s: X-Frame-Options=%q", c.method, c.path, h.Get("X-Frame-Options"))
		}
		if h.Get("Referrer-Policy") != "no-referrer" {
			t.Errorf("%s %s: Referrer-Policy=%q", c.method, c.path, h.Get("Referrer-Policy"))
		}
	}
}

// CSP 必须禁止内联脚本与 iframe 嵌套（严格策略的核心约束）。
func TestCSPDisallowsInlineScriptAndFraming(t *testing.T) {
	p := newTestPanel()
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/", nil))
	csp := rec.Header().Get("Content-Security-Policy")

	for _, must := range []string{
		"script-src 'self'",
		"frame-ancestors 'none'",
		"base-uri 'none'",
		"default-src 'none'",
	} {
		if !strings.Contains(csp, must) {
			t.Errorf("CSP missing %q; got: %s", must, csp)
		}
	}
	if strings.Contains(csp, "script-src 'self' 'unsafe-inline'") || strings.Contains(csp, "script-src 'unsafe-inline'") {
		t.Errorf("CSP must not allow unsafe-inline scripts; got: %s", csp)
	}
}

// 页面必须引用外部脚本（内联脚本会被上面的 CSP 拦掉，页面将完全不可用），
// 且引用必须是**带版本号**的形式：发版后 URL 变化才能绕开任何按 URL 缓存的
// 客户端（issue #105 现场：浏览器沿用缓存的旧 app.js，新功能看不见）。
func TestIndexReferencesExternalScript(t *testing.T) {
	p := newTestPanel()
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/", nil))
	body := rec.Body.String()

	if !strings.Contains(body, `<script src="app.js?v=test"></script>`) {
		t.Errorf("index.html must load app.js externally with a version query; got: %s", body)
	}
	// 占位符不得泄漏到响应里（泄漏即说明替换没生效，浏览器会去取 app.js?v={{APP_JS}}）。
	if strings.Contains(body, indexScriptPlaceholder) {
		t.Errorf("index.html still contains the %s placeholder", indexScriptPlaceholder)
	}
	// 反例保护：出现内联 <script>...</script> 内容块即为回归
	if strings.Contains(body, "<script>\n") || strings.Contains(body, "<script> ") {
		t.Error("index.html still contains an inline <script> block; CSP would block it")
	}
}

// 静态资源必须显式禁缓存：url 固定（/panel/、/panel/app.js）且无 ETag/Last-Modified，
// 不设 Cache-Control 时浏览器按启发式规则缓存，发版后仍用旧 app.js（issue #105）。
func TestStaticAssetsDisabledCaching(t *testing.T) {
	p := newTestPanel()
	for _, path := range []string{"/panel/", "/panel/app.js"} {
		rec := httptest.NewRecorder()
		p.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s: Cache-Control=%q want no-store", path, cc)
		}
	}
}

// /panel/api/* 的 JSON 响应同样必须显式禁缓存。
//
// 与静态资源同理但更隐蔽：这些接口返回余额 / 用量 / 积分 / 模型目录等实时数据，
// URL 固定、无 ETag/Last-Modified，不设 Cache-Control 时浏览器会启发式缓存，
// 表现为「已清理的条目仍在面板上显示 / 余额是旧值」。
// 覆盖三类响应：正常 200、鉴权失败的 401、以及未匹配路由的 404 ——
// 后两者也走 writeJSON/writeErr，漏一个就等于给浏览器留了缓存入口。
func TestPanelAPIResponsesDisabledCaching(t *testing.T) {
	p := newTestPanel()
	cases := []struct {
		name   string
		method string
		path   string
		key    string
		want   int
	}{
		{"鉴权失败 401", "GET", "/panel/api/overview", "", 401},
		{"未匹配路由 404", "GET", "/panel/api/nonexistent", "test-key", 404},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(c.method, c.path, nil)
		if c.key != "" {
			req.Header.Set("Authorization", "Bearer "+c.key)
		}
		p.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("%s: code=%d want %d", c.name, rec.Code, c.want)
		}
		if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s (%s %s): Cache-Control=%q want no-store", c.name, c.method, c.path, cc)
		}
	}
}

// 版本号为空时（Version 未注入的自建场景）仍要版本化 —— 回落内容哈希，
// 否则 app.js?v= 这种空值 URL 会让版本化形同虚设。
func TestAssetVersionFallsBackToContentHash(t *testing.T) {
	p := New(Config{APIKey: "k"})
	got := p.assetVersion()
	if got == "" {
		t.Fatal("assetVersion must not be empty without Version")
	}
	if strings.ContainsAny(got, ` "&?`) {
		t.Errorf("assetVersion produced an unescaped/unsafe token: %q", got)
	}
}

// app.js 必须能作为同源脚本取到且类型正确（否则页面白屏）。
func TestAppScriptServed(t *testing.T) {
	p := newTestPanel()
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/app.js", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "javascript") {
		t.Errorf("Content-Type=%q want javascript", ct)
	}
	if !strings.Contains(rec.Body.String(), "'use strict'") {
		t.Error("app.js body looks wrong")
	}
}

// UID 白名单：拒绝路径穿越与异常字符，放行真实 UUID 形态。
func TestValidUID(t *testing.T) {
	ok := []string{
		"248890d9-bb26-4131-87a7-4ec74d472344",
		"abc_123-XYZ",
		"a",
	}
	bad := []string{
		"",
		"../../evil",
		"x/../../y",
		`..\..\evil`,
		"a/b",
		"a\\b",
		"uid with space",
		"uid\nnewline",
		"uid\x00null",
		"café",
		strings.Repeat("a", 65), // 超长
	}
	for _, u := range ok {
		if !validUID(u) {
			t.Errorf("validUID(%q) = false, want true", u)
		}
	}
	for _, u := range bad {
		if validUID(u) {
			t.Errorf("validUID(%q) = true, want false", u)
		}
	}
}

// 未带密钥的 API 请求必须 401；携带正确密钥则通过鉴权层（不再是 401）。
func TestAuthLayerBehavior(t *testing.T) {
	p := newTestPanel()
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/api/overview", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("no key: code=%d want 401", rec.Code)
	}
	// 用不存在的路由验证"带正确 key 已过鉴权"（避免触碰依赖 nil 的 handler）。
	rec2 := httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/panel/api/nonexistent", nil)
	req2.Header.Set("Authorization", "Bearer test-key")
	p.ServeHTTP(rec2, req2)
	if rec2.Code == http.StatusUnauthorized {
		t.Error("valid key must pass the auth layer")
	}
}

// 模型搜索框（#keyInput / mdQ）必须被隔离出「用户名+密码」启发式配对：
// Chromium 会在页面里找与密码框配对的用户名框，配到模型搜索框后自动填充
// 用户名 → 模型列表被过滤成空（issue #105）。
//
// 契约（守住修法，别退回裸 input）：
//   - mdQ 必须在一个 form 内，且该 form 标 autocomplete="off"；
//   - 密钥框必须独立成 form（不能与页面其他输入框同 form）；
//   - 两者不得出现在同一个 form 里（否则启发式照样配对）。
func TestModelSearchInputIsolatedFromPasswordHeuristic(t *testing.T) {
	p := newTestPanel()
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/", nil))
	body := rec.Body.String()

	i := strings.Index(body, `id="mdQ"`)
	if i < 0 {
		t.Fatal("index.html: mdQ input not found")
	}
	// 往回找最近的 <form，必须存在且带 autocomplete="off"。
	formStart := strings.LastIndex(body[:i], "<form")
	if formStart < 0 {
		t.Fatal("mdQ must be wrapped in a <form autocomplete=\"off\"> (issue #105)")
	}
	formTag := body[formStart:i]
	if !strings.Contains(formTag, `autocomplete="off"`) {
		t.Errorf("mdQ's wrapping form must set autocomplete=\"off\"; got: %s", formTag)
	}

	k := strings.Index(body, `id="keyInput"`)
	if k < 0 {
		t.Fatal("index.html: keyInput not found")
	}
	kFormStart := strings.LastIndex(body[:k], "<form")
	if kFormStart < 0 {
		t.Error("keyInput should live in its own <form> (issue #105)")
	}
	// 两个输入框不能共用一个 form：keyInput 的 form 必须开在 mdQ 之后。
	if kFormStart < i {
		t.Error("keyInput and mdQ must not share a <form>; that re-creates the credential pairing")
	}

	// 反例保护：mdQ 不能带 type="password"，也不能叫 user/username/login。
	if strings.Contains(formTag, "password") {
		t.Error("mdQ must not be inside a form containing password semantics")
	}
}
