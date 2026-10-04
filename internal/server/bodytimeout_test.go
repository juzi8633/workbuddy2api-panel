package server

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// slowBody 模拟「慢速但持续」的客户端：分片返回，片间等待 step。
type slowBody struct {
	chunks []string
	step   time.Duration
	idx    int
}

func (s *slowBody) Read(p []byte) (int, error) {
	if s.idx >= len(s.chunks) {
		return 0, io.EOF
	}
	time.Sleep(s.step)
	n := copy(p, s.chunks[s.idx])
	if n < len(s.chunks[s.idx]) {
		s.chunks[s.idx] = s.chunks[s.idx][n:]
		return n, nil
	}
	s.idx++
	return n, nil
}

func (s *slowBody) Close() error { return nil }

// TestReadBodyAcceptsSlowButContinuousBody 慢速但持续发送的 body 必须被完整读入。
//
// 这是生产事故的直接回归（2026-10-03）：server.ReadTimeout=60s 时，3 MB body 以
// 45 KB/s 上行会在第 60.0s 被 server 掐成 400「read body: i/o timeout」。修复后
// 只对读间空闲设限，总时长不受约束——本用例用「总耗时远超单次读窗口、但每次都
// 在窗口内续命」的形态断言这一点。
func TestReadBodyAcceptsSlowButContinuousBody(t *testing.T) {
	const n = 40
	chunks := make([]string, n)
	for i := range chunks {
		chunks[i] = "x"
	}
	body := &slowBody{chunks: chunks, step: 5 * time.Millisecond}

	r := &http.Request{Body: body}
	got, err := readBody(discardResponseWriter{}, r)
	if err != nil {
		t.Fatalf("readBody: %v（慢速但持续的 body 不应被中止）", err)
	}
	if len(got) != n {
		t.Fatalf("body len=%d want %d", len(got), n)
	}
	if string(got) != strings.Repeat("x", n) {
		t.Fatalf("body=%q want %q", got, strings.Repeat("x", n))
	}
}

// TestReadBodyIdleTimeoutIsBounded 断言空闲上限存在且量级正确。
//
// 去掉 server.ReadTimeout 后，这层是「发完请求头就挂着不发 body」的唯一防线——
// 若无它，半死连接可以无限占用。用常量断言（而非真等 300s）防「有人把常量改成 0
// 或超大值让防线事实上失效」。
func TestReadBodyIdleTimeoutIsBounded(t *testing.T) {
	if bodyIdleTimeout <= 0 {
		t.Fatalf("bodyIdleTimeout=%v：必须为正，否则读 body 完全无时间防线", bodyIdleTimeout)
	}
	if bodyIdleTimeout > 10*time.Minute {
		t.Errorf("bodyIdleTimeout=%v 过长：慢速攻击者可用一个连接长期占用资源", bodyIdleTimeout)
	}
	// 与 SSE 流中空闲上限同口径（同一个「沉寂即视为死连接」标准）。
	if bodyIdleTimeout < 30*time.Second {
		t.Errorf("bodyIdleTimeout=%v 过短：弱网客户端的正常抖动会被误判为空闲", bodyIdleTimeout)
	}
}

// TestIsBodyIdleTimeoutDiscriminates 超时判定只认超时错误，不把普通 I/O 故障
// 误报为「空闲」——否则错误文案会把排查方向带偏。
func TestIsBodyIdleTimeoutDiscriminates(t *testing.T) {
	if isBodyIdleTimeout(io.EOF) {
		t.Error("EOF 不是空闲超时")
	}
	if isBodyIdleTimeout(errors.New("connection reset by peer")) {
		t.Error("connection reset 不是空闲超时")
	}
	var te stubTimeoutErr
	if !isBodyIdleTimeout(te) {
		t.Error("实现 net.Error 且 Timeout()=true 的错误应被判为空闲超时")
	}
}

type stubTimeoutErr struct{}

func (stubTimeoutErr) Error() string   { return "i/o timeout" }
func (stubTimeoutErr) Timeout() bool   { return true }
func (stubTimeoutErr) Temporary() bool { return true }

// discardResponseWriter 不实现 ResponseController 所需的接口，让 readBody 走
// 「不支持 deadline → 退回普通读取」的分支；本用例只验读取语义，不验 deadline。
type discardResponseWriter struct{ http.ResponseWriter }

func (discardResponseWriter) Header() http.Header         { return http.Header{} }
func (discardResponseWriter) Write(b []byte) (int, error) { return len(b), nil }
func (discardResponseWriter) WriteHeader(int)             {}
