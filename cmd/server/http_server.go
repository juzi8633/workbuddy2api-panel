package main

import (
	"net/http"
	"time"
)

// newHTTPServer 构造对外 HTTP server。抽成函数（而非 main 内联字面量）是为了让
// 超时参数可被测试回读——这些值曾因内联而长期漂移，见下方 readTimeout 注释。
func newHTTPServer(addr string, handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		// ReadTimeout 覆盖整个请求读取（含 body）。
		//
		// 这里**必须为 0**：请求体已无网关侧上限（max_body_mb 移除，任意大小完整
		// 读入），而固定总时长与「无上限 body」自相矛盾——按 12 KB/s 上行，1 MB
		// 就要 83s，慢速客户端（弱网 / 代理 / 多图 base64 长上下文）必然被拦。
		//
		// 实测（2026-10-03，生产 118.145.237.3）：3 MB body 以 45 KB/s 上传，第
		// 60.0s 被掐成 400「read body: i/o timeout」。请求归档里 47/56 次失败是
		// 该形态，且按时间聚成簇（22:18–22:21、22:48–23:43、00:28–00:42）——慢上行
		// 客户端成批失败的特征，与上游无关。修复后同场景 200。
		//
		// 改为只约束**首字节前**（ReadHeaderTimeout）与**连接空闲**（IdleTimeout）：
		// 客户端持续发送即不空闲，读得慢但不断即成。这与本项目对另一条链路的既有
		// 判定一致——transport.go 明确否决对 SSE 长流设总时长上限（「响应头到达后
		// 长流不受影响」），请求体侧同理。
		ReadTimeout: readTimeout,
		// IdleTimeout keep-alive 空闲连接回收：配合 chat 出站 ctx 传播防连接泄漏
		// 堆积。注意：SSE 流式响应期间连接非空闲，不受此项掐断；不设全局
		// WriteTimeout（长流式生成合法时长可达数分钟，会误杀在途 SSE）。
		IdleTimeout: idleTimeout,
	}
}

const (
	// readHeaderTimeout 请求首字节（请求头）上限：慢速头攻击的唯一闸门。
	readHeaderTimeout = 30 * time.Second
	// readTimeout 见 newHTTPServer 注释：0 = 不用总时长限制 body 读取。
	readTimeout = 0
	// idleTimeout 连接空闲上限（keep-alive 回收 + keepalive 上的慢速 body 兜底）。
	idleTimeout = 120 * time.Second
)
