// index.go 面板静态资源与安全响应头。
//
// 资源经 go:embed 打进二进制（随服务部署，无外部构建步骤）：
//   - index.html  页面骨架
//   - app.js      全部前端逻辑（独立文件而非内联，为了启用无需 unsafe-inline 的严格 CSP）
//
// 安全头对"面板页面与全部 /panel/api/* 响应"统一生效：CSP 限制脚本只能来自本服务，
// 禁止被 iframe 嵌套（防点击劫持），禁 MIME 嗅探，并声明不泄露 Referer 出去。
package panel

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
)

//go:embed index.html
var indexHTML []byte

//go:embed app.js
var appJS []byte

// csp 内容安全策略（严格版，无需 unsafe-inline）：
//   - default-src 'none'        默认全禁，逐个开口
//   - script-src 'self'         只跑同源脚本（app.js）；页面无内联事件处理器/内联脚本
//   - style-src 'self' 'unsafe-inline'
//     style 的内联是设计取舍：页面有少量 style="..." 属性（进度条宽度、表格列宽），
//     允许内联样式不会导致脚本执行；仍禁止外部样式域与 @import 外链。
//   - connect-src 'self'        前端 fetch 只能打本服务
//   - img-src 'self' data:      图标/内联图
//   - form-action 'none'        页面无表单提交目标（配置页是 JS 提交）
//   - frame-ancestors 'none'    禁止被任何站点 iframe 嵌套（点击劫持）
//   - base-uri 'none'          禁止注入 <base> 改写相对路径
const csp = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"connect-src 'self'; img-src 'self' data:; form-action 'none'; " +
	"frame-ancestors 'none'; base-uri 'none'"

// setSecurityHeaders 写入面板统一安全响应头（页面与 API 都要，API 也含 JSON 数据）。
func setSecurityHeaders(w http.ResponseWriter) {
	w.Header().Set("Content-Security-Policy", csp)
	w.Header().Set("X-Content-Type-Options", "nosniff") // 禁 MIME 嗅探
	w.Header().Set("X-Frame-Options", "DENY")           // 老浏览器兜底（CSP frame-ancestors 的等价项）
	w.Header().Set("Referrer-Policy", "no-referrer")    // 不外泄面板地址给外部站点
	w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
	w.Header().Set("Cross-Origin-Resource-Policy", "same-origin")
}

// 静态资源缓存控制。
//
// 为什么必须显式禁缓存：index.html 与 app.js 都是 go:embed 进二进制的，
// 每次发版 URL 不变（/panel/、/panel/app.js），也没有 ETag/Last-Modified。
// 浏览器于是按自身启发式规则缓存 —— 发版后用户仍拿旧 app.js 渲染新后端，
// 表现为「新功能看不见 / 报已修掉的旧错误」（上游 issue #105 的现场）。
//
// 对策两条，互为补充：
//   - no-store：告诉浏览器与中间层都不要存（Cloudflare 侧本就是 DYNAMIC 不缓存，
//     这条针对浏览器）。
//   - scriptURL()：给 <script src> 挂 ?v=<版本>，版本一变 URL 就变，任何仍按
//     URL 缓存的客户端（或用户在途的旧页面）也会重新取。
//
// 为什么不加 ETag/Last-Modified 走协商缓存：index.html 只有几 KB，app.js 每次
// 发版必变，省下的那点带宽远不如「一定拿到当次版本」重要。静态资源无秘密，
// 也没有隐私/一致性之外的考量。

// assetVersion 返回用于静态资源 URL 版本化的短标识：优先面板版本号（发版即
// 变化），为空时回落到内容哈希（Version 未注入的自建场景仍能版本化）。
func (p *Panel) assetVersion() string {
	if v := strings.TrimSpace(p.cfg.Version); v != "" {
		return url.QueryEscape(v)
	}
	sum := sha256.Sum256(appJS)
	return hex.EncodeToString(sum[:4])
}

// scriptURL 输出带版本查询串的 app.js 地址（供 index() 注入 index.html）。
func (p *Panel) scriptURL() string { return "app.js?v=" + p.assetVersion() }

// index 输出面板页面（静态无秘密；数据接口 /panel/api/* 才走鉴权）。
//
// 页面里的 {{APP_JS}} 占位符替换为带版本号的脚本地址 —— 版本化在服务端做，
// 让 index.html 自身保持纯静态（不需要构建步骤，也不引入模板引擎依赖）。
// 替换用 ReplaceAll 保证幂等：无占位符时输出与替换前逐字节一致。
func (p *Panel) index(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(bytes.ReplaceAll(indexHTML, []byte(indexScriptPlaceholder), []byte(p.scriptURL())))
}

// indexScriptPlaceholder index.html 中 app.js 地址的占位符；由 index() 在响应时
// 替换为 scriptURL()。放包级常量是为了让测试与 index.html 共用同一字面量。
const indexScriptPlaceholder = "{{APP_JS}}"

// appScript 输出前端逻辑（同源脚本，供 CSP script-src 'self' 加载）。
func (p *Panel) appScript(w http.ResponseWriter, r *http.Request) {
	setSecurityHeaders(w)
	w.Header().Set("Content-Type", "text/javascript; charset=utf-8")
	// 见上方静态资源缓存控制注释：no-store 是防止发版后浏览器沿用旧 app.js 的关键。
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(appJS)
}
