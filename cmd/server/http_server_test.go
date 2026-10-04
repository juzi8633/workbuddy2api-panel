package main

import (
	"bufio"
	"bytes"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"testing"
	"time"
)

// TestHTTPServerDoesNotCapBodyReadDuration 钉住请求体读取**不受总时长限制**。
//
// 背景（2026-10-03 生产实测）：ReadTimeout=60s 与「请求体无大小上限」自相矛盾。
// 3 MB body 以 45 KB/s 上行，第 60.0s 被掐成 400「read body: i/o timeout」——
// 生产归档里 47/56 次失败是该形态，且聚成时间簇（慢上行客户端成批失败）。
//
// 断言 server 字段值与端到端慢读两条腿：字段断言抓住「有人把 ReadTimeout 加回来」；
// 端到端断言抓住「字段对了但 Server 没接线到真实 listener」。
func TestHTTPServerDoesNotCapBodyReadDuration(t *testing.T) {
	srv := newHTTPServer("127.0.0.1:0", http.NotFoundHandler())
	if srv.ReadTimeout != 0 {
		t.Fatalf("ReadTimeout=%v, want 0：请求体无大小上限时不能设总时长（慢上行会被误杀，见注释）", srv.ReadTimeout)
	}
	if srv.ReadHeaderTimeout <= 0 {
		t.Errorf("ReadHeaderTimeout=%v, want >0：慢速头攻击仍需闸门", srv.ReadHeaderTimeout)
	}
	if srv.IdleTimeout <= 0 {
		t.Errorf("IdleTimeout=%v, want >0：需要 keep-alive 回收与卡死连接兜底", srv.IdleTimeout)
	}
	if srv.WriteTimeout != 0 {
		t.Errorf("WriteTimeout=%v, want 0：全局写超时会误杀在途 SSE 长流", srv.WriteTimeout)
	}
}

// TestHTTPServerAcceptsSlowBody 端到端：一个「慢速但持续」的请求体必须被完整读入。
//
// 分 4 片、片间 10ms 停顿模拟慢上行（真实场景是 12–45 KB/s 连续慢流），断言 handler
// 收到完整 body 且返回 200——即读取路径上没有任何总时长闸门提前动手。
func TestHTTPServerAcceptsSlowBody(t *testing.T) {
	const payload = "0123456789abcdef"
	got := make(chan string, 1)

	srv := newHTTPServer("127.0.0.1:0", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		got <- string(b)
		w.WriteHeader(http.StatusOK)
	}))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	// 手工构造慢速 body：分片写入，片间停顿。http.Client 是整体 Write，测不出慢读。
	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	head := "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: " + strconv.Itoa(len(payload)) + "\r\n\r\n"
	if _, err := conn.Write([]byte(head)); err != nil {
		t.Fatalf("write head: %v", err)
	}
	for _, c := range []string{payload[:4], payload[4:8], payload[8:12], payload[12:]} {
		time.Sleep(10 * time.Millisecond)
		if _, err := conn.Write([]byte(c)); err != nil {
			t.Fatalf("write chunk: %v", err)
		}
	}

	select {
	case b := <-got:
		if b != payload {
			t.Fatalf("body=%q want %q", b, payload)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handler never saw body (server likely capped the read)")
	}

	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d want 200", resp.StatusCode)
	}
}

// TestHTTPServerConstructionStaysWired 守住「监听 server 由 newHTTPServer 构造」：
// main.go 里不允许再出现内联 &http.Server{...} 字面量，否则本文件的回归被绕开。
func TestHTTPServerConstructionStaysWired(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatalf("read main.go: %v", err)
	}
	if bytes.Contains(src, []byte("&http.Server{")) {
		t.Error("main.go 又内联构造 http.Server——超时参数会绕开 newHTTPServer 的回归保护")
	}
	if !bytes.Contains(src, []byte("newHTTPServer(cfg.Listen, h)")) {
		t.Error("main.go 未用 newHTTPServer(cfg.Listen, h) 构造监听 server")
	}
}
