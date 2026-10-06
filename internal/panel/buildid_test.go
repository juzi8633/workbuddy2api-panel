package panel

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http/httptest"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
)

// TestSelfBuildIDMatchesOwnBinary build 里的哈希必须等于**本测试二进制**的
// sha256 前 8 位 —— 这正是"自证"的意义：面板报的指纹与 sha256sum 逐位可比。
//
// 注意断言对象是测试二进制（go test 产物），不是 wb2api；只要自哈希读的是
// /proc/self/exe，两者都成立。
func TestSelfBuildIDMatchesOwnBinary(t *testing.T) {
	id := SelfBuildID()
	if !strings.HasPrefix(id, "wb") {
		t.Fatalf("build id 应以 wb 开头: %q", id)
	}
	parts := strings.SplitN(id, "+", 2)
	if len(parts) != 2 || len(parts[1]) != 8 {
		t.Fatalf("build id 应为 wbYYYY.MM.DD+<8 位哈希>: %q", id)
	}
	// 日期段的形状
	if !regexp.MustCompile(`^wb\d{4}\.\d{2}\.\d{2}$`).MatchString(parts[0]) {
		t.Fatalf("日期段格式不符: %q", parts[0])
	}
	exe, err := os.Executable()
	if err != nil {
		t.Skip("无法取自身路径")
	}
	if resolved, err := os.Readlink("/proc/self/exe"); err == nil {
		exe = resolved
	}
	raw, err := os.ReadFile(exe)
	if err != nil {
		t.Skipf("无法读自身: %v", err)
	}
	sum := sha256.Sum256(raw)
	want := hex.EncodeToString(sum[:])[:8]
	if parts[1] != want {
		t.Fatalf("build 哈希 %s != 自身 sha256 前 8 位 %s", parts[1], want)
	}
}

// TestSelfBuildIDNeverEmpty 读自身失败时也必须返回带日期的串，不能返回空。
// 空值会让 overview 的 build 字段消失，运维就无法核对部署了。
func TestSelfBuildIDNeverEmpty(t *testing.T) {
	if id := SelfBuildID(); id == "" || !strings.Contains(id, "wb") {
		t.Fatalf("build id 不得为空: %q", id)
	}
}

// TestOverviewExposesBuild /panel/api/overview 必须同时给出 version 与 build。
// version = 批次身份（cache-busting 键），build = 产物指纹（可 sha256sum 核对）。
func TestOverviewExposesBuild(t *testing.T) {
	// overview 会读 Pool 统计 → 必须给一个真实（空）Pool，否则 nil 解引用。
	p := New(Config{Version: "1.11.11-wb8", Build: "wb2026.10.04+deadbeef",
		Pool: pool.New("")})
	rec := httptest.NewRecorder()
	p.ServeHTTP(rec, httptest.NewRequest("GET", "/panel/api/overview", nil))
	body := rec.Body.String()
	if !strings.Contains(body, `"version":"1.11.11-wb8"`) {
		t.Errorf("overview 缺 version: %s", body)
	}
	if !strings.Contains(body, `"build":"wb2026.10.04+deadbeef"`) {
		t.Errorf("overview 缺 build: %s", body)
	}
}

// TestAssetVersionUsesVersionNotBuild cache-busting 键必须用 **version**，不用 build。
//
// 为什么：build 含自哈希，而自哈希在**同一次构建**里稳定、跨构建也会变（这正是它的
// 用途）。但若把 build 用作 cache-busting 键，则"只重新构建、没改任何代码"也会
// 让 URL 变化，等于每次发布都强制全体客户端重取静态资源。version 表达"这一批改了
// 什么"，才是该变化的粒度。
//
// 反过来说：version 必须每批变化（硬要求），所以本测试同时钉住"version 是键"。
func TestAssetVersionUsesVersionNotBuild(t *testing.T) {
	p := New(Config{Version: "1.11.11-wb8", Build: "wb2026.10.04+deadbeef"})
	if got := p.assetVersion(); got != "1.11.11-wb8" {
		t.Fatalf("assetVersion=%q want 1.11.11-wb8（应取 Version 而非 Build）", got)
	}
	// 版本串里不该再出现特性名堆叠（回归保护：别再把 cache/expiry/... 拼回来）
	if strings.Contains(p.assetVersion(), "+") {
		t.Errorf("assetVersion 含 '+'（特性名堆叠回归?）: %q", p.assetVersion())
	}
}
