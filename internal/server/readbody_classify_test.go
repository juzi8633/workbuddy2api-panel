package server

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"syscall"
	"testing"
	"time"
)

type rbTimeoutErr struct{}

func (rbTimeoutErr) Error() string   { return "i/o timeout" }
func (rbTimeoutErr) Timeout() bool   { return true }
func (rbTimeoutErr) Temporary() bool { return true }

// TestClassifyReadBodyErr 钉住分类口径，特别是「我们超时」与「客户端断开」必须分开。
func TestClassifyReadBodyErr(t *testing.T) {
	wrapped := func(inner error) error { return &net.OpError{Op: "read", Net: "tcp", Err: inner} }
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"空", nil, "none"},
		{"我们的空闲守卫", &rbTimeoutErr{}, "server_timeout"},
		{"客户端取消", context.Canceled, "client_canceled"},
		{"写一半断了", io.ErrUnexpectedEOF, "peer_closed"},
		{"干净 EOF", io.EOF, "peer_closed"},
		{"连接重置", wrapped(syscall.ECONNRESET), "peer_reset"},
		{"写不动", wrapped(syscall.EPIPE), "peer_reset"},
		{"裸 ECONNRESET", syscall.ECONNRESET, "peer_reset"},
		{"其它网络错误", wrapped(errors.New("boom")), "peer_reset"},
		{"未知", errors.New("weird"), "other"},
	}
	for _, c := range cases {
		if got := classifyReadBodyErr(c.err); got != c.want {
			t.Errorf("%s: got %q want %q", c.name, got, c.want)
		}
	}
}

// TestRealClientDisconnectClassifiedAsPeer 用**真实 TCP** 复现生产最常见的形态
// （56 次里 43 次是 axonhub 在 ~30s 处失败）：声明了 Content-Length 但客户端不发
// body 就断开。断言分类结果是「对端」而不是「我们超时」——这决定了我们**不需要**
// 去调 bodyIdleTimeout。
func TestRealClientDisconnectClassifiedAsPeer(t *testing.T) {
	got := make(chan string, 1)
	srv := &http.Server{
		ReadHeaderTimeout: 30 * time.Second,
		ReadTimeout:       0, // 生产配置：read_timeout="0"
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, err := readBody(w, r)
			select {
			case got <- classifyReadBodyErr(err):
			default:
			}
		}),
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	conn, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	head := "POST /v1/chat/completions HTTP/1.1\r\nHost: x\r\n" +
		"Content-Type: application/json\r\nContent-Length: 1000000\r\n\r\n"
	if _, err := io.WriteString(conn, head); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	_ = conn.Close() // 客户端在发 body 前断开

	select {
	case c := <-got:
		t.Logf("分类结果: %s", c)
		if c == "server_timeout" {
			t.Errorf("客户端断开被误判为服务端超时——会误导我们调 bodyIdleTimeout")
		}
		if c != "peer_closed" && c != "peer_reset" {
			t.Errorf("分类=%q，期望 peer_closed 或 peer_reset", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("服务端未观察到断开（readBody 仍在等？）")
	}
}
