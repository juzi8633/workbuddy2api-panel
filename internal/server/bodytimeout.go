package server

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"
)

// bodyIdleTimeout 请求体读取的**空闲**上限：两次读之间静默超过该值即放弃。
//
// 为什么需要它：newHTTPServer 把 ReadTimeout 设为 0（固定总时长会误杀慢上行，
// 见 cmd/server/http_server.go 注释），而 Go 的 Server.IdleTimeout 只管**两个请求
// 之间**的连接空闲（net/http/server.go 的 conn.serve 循环，只在写完响应回到
// StateIdle 时才装 deadline），**不覆盖请求体读到一半的停滞**。若不加这层，
// 去掉 ReadTimeout 就等于向「发完头就挂着不发 body」的慢速攻击完全敞开。
//
// 取值与 upstream.idle_timeout_seconds（默认 300s）同一口径：那个值是 SSE 流中
// 空闲上限——「沉寂即视为死连接」的判定标准。请求体侧沿用同一标准，客户端持续
// 发送即续命，只惩罚真停滞。
const bodyIdleTimeout = 300 * time.Second

// readBody 完整读入请求体，并对读间空闲施加 bodyIdleTimeout。
//
// 与 io.ReadAll(r.Body) 的唯一差别是逐次重置读 deadline：只要客户端还在发数据，
// 无论总时长多久都读得完（修复慢上行被 60s 误杀）；一旦静默超过 bodyIdleTimeout
// 就中止，不让半死连接无限占用。
//
// customWriter 不可用时（如测试里的 httptest.ResponseRecorder，不实现
// ResponseController 所需的接口）静默退化为普通读取——测试不该为了这层防护
// 而改用真实 listener。
func readBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	rc := http.NewResponseController(w)
	// 清掉可能存在的 server 级 deadline：我们接管读 deadline 的管理。
	// ReadTimeout=0 时本就无 deadline，这里是防未来误配的显式声明。
	if err := rc.SetReadDeadline(time.Time{}); err != nil {
		// 该 writer 不支持 deadline（ResponseRecorder 等）→ 退回普通读取。
		return io.ReadAll(r.Body)
	}

	var buf bytes.Buffer
	tmp := make([]byte, 32*1024)
	for {
		// 每次读之前续期：上一次读成功即证明客户端仍在发送，空闲窗口重新计时。
		if err := rc.SetReadDeadline(time.Now().Add(bodyIdleTimeout)); err != nil {
			return io.ReadAll(r.Body) // 中途不支持了：退回普通读取
		}
		n, err := r.Body.Read(tmp)
		if n > 0 {
			buf.Write(tmp[:n])
		}
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			return buf.Bytes(), nil
		}
		if isBodyIdleTimeout(err) {
			return nil, fmt.Errorf("body idle for %s (no data received): %w", bodyIdleTimeout, err)
		}
		return nil, err
	}
}

// isBodyIdleTimeout 判定读 body 超时是「空闲停滞」而非其他 I/O 故障。
func isBodyIdleTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}
