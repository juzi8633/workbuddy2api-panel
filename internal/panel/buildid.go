package panel

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"time"
)

// SelfBuildID 由二进制自身算出构建标识，形如 "wb2026.10.04+7d7b4c9c"。
//
// 为什么自哈希而不是用 -ldflags 注入：
//   - 注入的哈希要求**人记得传参**，而且必须传"预期的最终哈希"——那是鸡生蛋问题
//     （哈希在链接后才存在）。常见的绕法是注入 commit 号，但本 fork 的构建环境
//     不保证有 .git，且 -buildvcs=false 已经明确不依赖 VCS。
//   - 自哈希是**自证**的：读 /proc/self/exe（Linux）/ os.Executable（回退），
//     算 sha256 前 8 位。它与部署时 `sha256sum wb2api` 的结果逐位可对，
//     因此"面板上显示的"与"实际在跑的"永不漂移。
//
// 代价是启动时多读一遍自己的可执行文件：实测 8.8MB 二进制 31ms（Python 口径）；
// Go 侧同为单遍流式 sha256，属启动期一次性开销，可忽略。
//
// 失败时返回只带日期的串（如 "wb2026.10.04"）——绝不返回空：空值会让
// /panel/api/overview 的 build 字段消失，运维就无法核对部署了。
func SelfBuildID() string {
	day := "wb" + time.Now().Format("2006.01.02")
	path, err := os.Executable()
	if err != nil {
		return day
	}
	// 解析软链：os.Executable 在部分平台返回 /proc/self/exe 的链接路径，
	// 直接 Open 也能读到内容，但 EvalSymlinks 让错误信息更可读。
	if resolved, err := os.Readlink("/proc/self/exe"); err == nil {
		path = resolved
	}
	f, err := os.Open(path)
	if err != nil {
		return day
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return day
	}
	return day + "+" + hex.EncodeToString(h.Sum(nil))[:8]
}
