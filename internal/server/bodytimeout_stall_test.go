package server

import (
	"bufio"
	"net"
	"net/http"
	"testing"
	"time"
)

// TestReadBodyAbortsStalledBody 端到端钉住「请求体读到一半停滞会被中止」。
//
// 这条防线容易在重构中被悄悄丢掉：deadline 必须在**每次 Read 之前**设置，
// 若有人把它挪到 Read 之后，或者以为「SetReadDeadline 在循环里设过就够了、
// 首轮可以先读」，那么「发完请求头就一个字节不发」的客户端就会永久占住连接
// ——去掉 server.ReadTimeout（=0，为修慢上行误杀）后，这就是唯一的请求体时限。
//
// 通过真实 listener 走一遍：ResponseController 需要底层 net.Conn 才能设 deadline，
// httptest.ResponseRecorder 那条路径会静默退化成 io.ReadAll（测不到这里）。
func TestReadBodyAbortsStalledBody(t *testing.T) {
	old := bodyIdleTimeout
	bodyIdleTimeout = 150 * time.Millisecond
	defer func() { bodyIdleTimeout = old }()

	entered := make(chan struct{})
	aborted := make(chan error, 1)

	// 与生产同口径的 server（ReadTimeout=0）：唯一的请求体时限就是 readBody。
	srv := &http.Server{
		ReadHeaderTimeout: 30 * time.Second,
		IdleTimeout:       120 * time.Second,
		ReadTimeout:       0,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			close(entered)
			_, err := readBody(w, r)
			aborted <- err
			w.WriteHeader(http.StatusOK)
		}),
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	// 发完整请求头并声明一个很大的 Content-Length，然后**一个字节 body 都不发**。
	if _, err := conn.Write([]byte(
		"POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 1048576\r\n\r\n")); err != nil {
		t.Fatalf("write head: %v", err)
	}
	<-entered

	select {
	case err := <-aborted:
		if err == nil {
			t.Fatal("停滞的请求体必须报错中止，却成功返回了")
		}
		if !isBodyIdleTimeout(err) {
			t.Fatalf("中止原因应判为空闲超时，实际: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("停滞的请求体未被中止——读 body 的第一轮没有装 deadline")
	}

	// 连接应被服务端关闭（而不是无限挂着）。
	_ = conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := http.ReadResponse(bufio.NewReader(conn), nil); err == nil {
		t.Log("服务端仍回写了响应，符合预期（中止后 handler 照常返回）")
	}
}
