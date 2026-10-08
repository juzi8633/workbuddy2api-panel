package main

import (
	"net/http"
	"time"
)

// newHTTPServer 构造对外 HTTP server，并承载 server.read_timeout（issue #100）。
//
// readTimeout 的两种语义：
//   - > 0：net/http 对整个请求（含 body 上传）设总时长上限。缺省 "300s"。
//   - == 0：总时长不设限。用于放行「大上下文 / 文件块经反代链慢速上传」——
//     固定 60s 曾把这类请求在第 60.0s 掐成 400 "read body: i/o timeout"
//     （2026-10-03 生产实测：3 MB @ 45 KB/s 精确 60.03s 失败，归档里 47/56
//     次失败是该形态且聚成时间簇）。此时 chat handler 走 internal/server 的
//     readBody，用**读间空闲**上限（bodyIdleTimeout）兜底停滞的连接。
//
// 抽成函数（而非 main 内联字面量）是为了让超时参数可被测试回读——这些值
// 曾因内联而长期漂移。
func newHTTPServer(addr string, handler http.Handler, readTimeout time.Duration) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		// SSE 流式响应合法时长可达数分钟，故不设全局 WriteTimeout。
		// IdleTimeout 只管 keep-alive 连接空闲（不含读取中的请求体——那一层
		// 由 ReadTimeout 或 internal/server 的 readBody 负责）。
		IdleTimeout: idleTimeout,
	}
}

const (
	// readHeaderTimeout 请求首字节（请求头）上限：慢速头攻击的唯一闸门。
	readHeaderTimeout = 30 * time.Second
	// idleTimeout keep-alive 空闲连接回收。
	idleTimeout = 120 * time.Second
)
