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
// 为什么需要它：Go 的 Server.IdleTimeout 只管**两个请求之间**的连接空闲
// （net/http/server.go 的 conn.serve 循环，只在写完响应回到 StateIdle 时才装
// deadline），**不覆盖请求体读到一半的停滞**。所以当 server.read_timeout 配成
// "0"（= 不设总时长，用于放行慢上行）时，这一层是「发完头就挂着不发 body」的
// 唯一时限——没有它，半死连接可以永久占用。
//
// 与 server.read_timeout 的分工：
//   - read_timeout > 0（缺省 300s）：走 net/http 的总时长上限，本文件的
//     readBodyWithTimeout 直接 io.ReadAll，不叠加 idle 层（总时长已经更严）。
//   - read_timeout = 0：总时长不设限，由本文件按下述 idle 规则兜底。
//
// 取值与 upstream.idle_timeout_seconds（默认 300s）同一口径：那个值是 SSE 流中
// 空闲上限——「沉寂即视为死连接」的判定标准。请求体侧沿用同一标准，客户端持续
// 发送即续命，只惩罚真停滞。
//
// deadline 装在**每次 Read 之前**（循环顶部），所以首读同样有界——这正是
// TestReadBodyAbortsStalledBody 钉住的不变式。
// 用 var 而非 const：它是**可被测试缩短**的唯一旋钮。默认值即生产值；测试把
// 它调到毫秒级才能在秒级内复现「停滞即中止」。（改动它的测试不得并行。）
var bodyIdleTimeout = 300 * time.Second

// readBodyWithTimeout 按 server.read_timeout 选择读 body 的时限策略：
// 配了总时长就交给 net/http 的 ReadTimeout（io.ReadAll 即可，别叠加更松的
// idle 层）；配成 0 则用 readBody 的逐次续期 idle 兜底。
func readBodyWithTimeout(w http.ResponseWriter, r *http.Request, totalTimeout time.Duration) ([]byte, error) {
	if totalTimeout > 0 {
		// net/http 已按 ReadTimeout 给整个请求（含 body）装了截止时间。
		return io.ReadAll(r.Body)
	}
	return readBody(w, r)
}

// readBody 完整读入请求体，并对读间空闲施加 bodyIdleTimeout。
//
// 与 io.ReadAll(r.Body) 的差别是逐次重置读 deadline：只要客户端还在发数据，
// 无论总时长多久都读得完（放行慢上行）；一旦静默超过 bodyIdleTimeout 就中止，
// 不让半死连接无限占用。
//
// ResponseController 不可用时（如 httptest.ResponseRecorder，不实现底层
// net.Conn 的 deadline 接口）静默退化为普通读取——那是测试路径，不该为了
// 这层防护而强制改用真实 listener。

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
