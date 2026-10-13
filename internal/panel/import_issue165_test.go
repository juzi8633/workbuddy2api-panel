package panel

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// stubUpstream 返回一个所有出站请求都拿到空 JSON 的 Client：导入会在账号落盘后
// 顺带签到/查资源，这些调用失败只记日志、不阻断导入，用它把测试与网络隔离。
// 注意不能传 nil Client——checkinMeterPaths 会解引用 c 而 panic（本项目其它
// 面板测试之所以没暴露这点，是因为它们不走导入路径）。
func stubUpstream() *upstream.Client {
	return &upstream.Client{
		HTTP: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(strings.NewReader(`{}`)),
			}, nil
		})},
		ChatBaseCN:    "https://stub.invalid",
		BillingBaseCN: "https://stub.invalid",
		WebBaseCN:     "https://stub.invalid",
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// makeJWT 造一个只有 header.payload.signature 三段、payload 带 sub/exp 的 token。
// 本用例只验证**声明解析**，不涉及签名（网关从不校验签名，见 auth.JWTPayload 注释）。
func makeJWT(sub string, exp time.Time) string {
	head := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload := base64.RawURLEncoding.EncodeToString(
		[]byte(fmt.Sprintf(`{"sub":%q,"exp":%d}`, sub, exp.Unix())))
	return head + "." + payload + ".sig"
}

func importMultipart(t *testing.T, body string) (*httptest.ResponseRecorder, map[string]any) {
	w, out, _ := importMultipartDir(t, body)
	return w, out
}

// importMultipartDir 同 importMultipart，但一并返回凭证落盘目录，便于断言实际写入的
// ExpiresAt（过期时间回退链的端到端证据）。
func importMultipartDir(t *testing.T, body string) (*httptest.ResponseRecorder, map[string]any, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("file", "accounts.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := mw.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/panel/api/import/cockpit", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	w := httptest.NewRecorder()

	p := pool.New("")
	dir := t.TempDir()
	pn := New(Config{Pool: p, AuthDir: dir, Upstream: stubUpstream()})
	pn.importCockpit(w, req)

	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("响应不是 JSON: %v (%s)", err, w.Body.String())
	}
	return w, out, dir
}

// TestImportAcceptsAccountWithoutRefreshToken 是 issue #165 的核心回归：
// refresh_token 为空、expires_at 为 0、但 access token 是有效 JWT 的导出记录，
// 必须**导入成功**（旧行为：被当成「字段全缺」静默跳过，一条也进不来）。
func TestImportAcceptsAccountWithoutRefreshToken(t *testing.T) {
	exp := time.Now().Add(27 * 24 * time.Hour)
	uid := "b4f1c2d3-e4a5-4b6c-8d7e-9f0a1b2c3d4e"
	body := fmt.Sprintf(`[{"id":%q,"uid":%q,"email":"","nickname":"x",
		"access_token":%q,"refresh_token":"","token_type":"Bearer","expires_at":0,
		"domain":"www.workbuddy.cn","status":"active"}]`, uid, uid, makeJWT(uid, exp))

	w, out := importMultipart(t, body)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body)
	}
	if got := out["imported"]; got != float64(1) {
		t.Fatalf("imported=%v want 1（issue #165 未修复：只有 access token 应可导入）", got)
	}
	if got := out["skipped"]; got != float64(0) {
		t.Fatalf("skipped=%v want 0", got)
	}
	// 必须给出「无 refresh_token」警告，让用户知道到期后无法续期（建议 3）。
	warns, _ := out["warnings"].([]any)
	if len(warns) == 0 {
		t.Fatal("缺少无 refresh_token 的警告")
	}
	if s, _ := warns[0].(string); !strings.Contains(s, "refresh_token") {
		t.Fatalf("警告文案应提到 refresh_token: %q", s)
	}
}

// TestImportUsesJWTExpWhenExpiresAtMissing 钉住过期时间回退链：expires_at=0 时必须
// 取 JWT exp，而不是填「现在+365天」的假值（假值会绕过 NeedsRefresh 判定）。
func TestImportUsesJWTExpWhenExpiresAtMissing(t *testing.T) {
	exp := time.Now().Add(10 * 24 * time.Hour).Truncate(time.Second)
	uid := "aaaabbbbccccddddeeeeffff00001111"
	body := fmt.Sprintf(`[{"id":%q,"access_token":%q,"refresh_token":"","expires_at":0}]`,
		uid, makeJWT(uid, exp))

	_, out, dir := importMultipartDir(t, body)
	if got := out["imported"]; got != float64(1) {
		t.Fatalf("imported=%v want 1", got)
	}
	// 落盘凭据里的 ExpiresAt 必须等于 JWT exp，而不是「现在+365天」。
	raw, err := os.ReadFile(filepath.Join(dir, "workbuddy-"+uid+".json"))
	if err != nil {
		t.Fatalf("读取落盘凭据: %v", err)
	}
	a, err := auth.Parse(raw)
	if err != nil {
		t.Fatalf("解析落盘凭据: %v", err)
	}
	if a.ExpiresAt != exp.Unix() {
		t.Fatalf("ExpiresAt=%d want %d（JWT exp 未被采用；旧行为会填 now+365d）",
			a.ExpiresAt, exp.Unix())
	}
}

// TestImportNormalizesSecondEpoch 钉住量级判别（#165 附带问题 2）：expires_at 给的
// 是**秒**时不得再 /1000（旧代码无条件 /1000 → 1970 年 → 账号一进池就需刷新）。
func TestImportNormalizesSecondEpoch(t *testing.T) {
	sec := time.Now().Add(30 * 24 * time.Hour).Unix()
	if got := normalizeUnixSeconds(sec); got != sec {
		t.Errorf("秒值被改动: %d → %d", sec, got)
	}
	ms := sec * 1000
	if got := normalizeUnixSeconds(ms); got != sec {
		t.Errorf("毫秒值应折成秒: %d → %d (want %d)", ms, got, sec)
	}
	if got := normalizeUnixSeconds(0); got != 0 {
		t.Errorf("0 应保持 0（未知）: %d", got)
	}
}

// TestImportToleratesFileShapes 顶对象 / accounts 信封 / UTF-8 BOM 三种形态。
func TestImportToleratesFileShapes(t *testing.T) {
	exp := time.Now().Add(20 * 24 * time.Hour)
	uid := "0123456789abcdef0123456789abcdef"
	jwt := makeJWT(uid, exp)
	one := fmt.Sprintf(`{"id":%q,"access_token":%q,"refresh_token":"rt","expires_at":%d}`,
		uid, jwt, exp.UnixMilli())
	cases := []struct {
		name, body string
	}{
		{"裸数组", "[" + one + "]"},
		{"accounts 信封", `{"accounts":[` + one + `]}`},
		{"顶层单对象", one},
		{"带 UTF-8 BOM", "\uFEFF" + "[" + one + "]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			w, out := importMultipart(t, c.body)
			if w.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", w.Code, w.Body)
			}
			if got := out["imported"]; got != float64(1) {
				t.Fatalf("imported=%v want 1（形态未兼容）", got)
			}
		})
	}
}

// TestImportRejectsMissingAccessToken 仍必须拒绝真正的字段缺失——放宽 refresh_token
// 不等于放宽 access_token（access token 才是唯一不可省的凭证）。
func TestImportRejectsMissingAccessToken(t *testing.T) {
	uid := "deadbeefdeadbeefdeadbeefdeadbeef"
	body := fmt.Sprintf(`[{"id":%q,"uid":%q,"access_token":"","refresh_token":"rt"}]`, uid, uid)
	_, out := importMultipart(t, body)
	if got := out["imported"]; got != float64(0) {
		t.Fatalf("imported=%v want 0", got)
	}
	errs, _ := out["errors"].([]any)
	if len(errs) == 0 {
		t.Fatal("必须有跳过原因上屏（#165 附带问题 1）")
	}
	if s, _ := errs[0].(string); !strings.Contains(s, "access_token") {
		t.Fatalf("原因应指明缺 access_token: %q", s)
	}
}

// TestImportResponseSeparatesWarningsFromErrors 钉住响应契约：warnings 与 errors
// 是两个字段，前者是「已导入但需知情」，后者是「跳过原因」。
func TestImportResponseSeparatesWarningsFromErrors(t *testing.T) {
	exp := time.Now().Add(15 * 24 * time.Hour)
	uid := "11112222333344445555666677778888"
	body := fmt.Sprintf(`[{"id":%q,"access_token":%q,"refresh_token":"","expires_at":0}]`,
		uid, makeJWT(uid, exp))
	_, out := importMultipart(t, body)
	if _, ok := out["warnings"]; !ok {
		t.Fatal("响应缺少 warnings 字段")
	}
	if _, ok := out["errors"]; !ok {
		t.Fatal("响应缺少 errors 字段")
	}
}

// TestJWTPayloadParsing 直接钉住声明解析（含损坏输入不得 panic）。
func TestJWTPayloadParsing(t *testing.T) {
	exp := time.Now().Add(time.Hour).Truncate(time.Second)
	tok := makeJWT("user-1", exp)
	sub, gotExp, ok := auth.JWTPayload(tok)
	if !ok || sub != "user-1" || !gotExp.Equal(exp) {
		t.Fatalf("JWTPayload=%q/%v/%v want user-1/%v/true", sub, gotExp, ok, exp)
	}
	for _, bad := range []string{"", "x", "a.b", "a.b.c", "....", "eyJhbGciOiJIUzI1NiJ9.!!!.sig"} {
		if _, _, ok := auth.JWTPayload(bad); ok && bad == "a.b" {
			t.Errorf("损坏 token 不应解析成功: %q", bad)
		}
	}
}
