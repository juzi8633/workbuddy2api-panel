package server

import (
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestHeaderTimeoutDoesNotLeakIntoBodyRead 判定：server.read_timeout=0（生产配置）
// 时，net/http 的**请求头**截止时间不会泄漏到请求体读取。
//
// 为什么值得钉住（2026-10-13 生产实测）：3 天内 56 次 body_read_error，其中 43 次
// 来自 axonhub/1.0，时长精确聚在 29.4–30.0s —— 与 cmd/server/http_server.go 的
// readHeaderTimeout = 30s 同值。当时有两种假设：
//
//	① 我们的头超时把 body 读也卡在 30s（配置 bug，需要改代码）
//	② 客户端自己 30s 超时后断开（与我们的配置无关）
//
// 本用例用缩短的头超时把两种可能分开：头超时 1s，客户端在 2s 后才发 body。若头
// 截止泄漏，请求必然失败；实际返回 200 完整读到 body → 假设 ② 成立。
//
// 依据是 Go 的 conn.readRequest：先按 ReadHeaderTimeout 设 deadline 读头，读完头
// 后再 SetReadDeadline(wholeReqDeadline)；ReadTimeout=0 时后者是零值（无截止），
// 于是头截止被清除。
func TestHeaderTimeoutDoesNotLeakIntoBodyRead(t *testing.T) {
	srv := &http.Server{
		ReadHeaderTimeout: 1 * time.Second,
		ReadTimeout:       0, // 生产配置：read_timeout="0"
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := readBody(w, r)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = fmt.Fprintf(w, "ERR: %v", err)
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = fmt.Fprintf(w, "OK:%d", len(body))
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
	defer conn.Close()

	payload := `{"model":"m","messages":[]}`
	// Connection: close 让服务端读完就关，客户端才能用 ReadAll 拿到完整响应。
	head := "POST /v1/chat/completions HTTP/1.1\r\nHost: x\r\nConnection: close\r\n" +
		fmt.Sprintf("Content-Length: %d\r\n", len(payload)) +
		"Content-Type: application/json\r\n\r\n"
	if _, err := io.WriteString(conn, head); err != nil {
		t.Fatal(err)
	}
	// 故意等 2s（> 1s 头超时）再发 body。
	time.Sleep(2 * time.Second)
	if _, err := io.WriteString(conn, payload); err != nil {
		t.Fatalf("write body: %v", err)
	}

	_ = conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	raw, err := io.ReadAll(conn)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	resp := string(raw)
	t.Logf("响应首行: %s", strings.SplitN(resp, "\r\n", 2)[0])

	if strings.Contains(resp, "ERR:") {
		t.Errorf("头超时泄漏进了 body 读取——生产 30s 簇就会是我们的配置问题: %s", resp)
		return
	}
	if !strings.Contains(resp, fmt.Sprintf("OK:%d", len(payload))) {
		t.Errorf("期望完整读到 body，实际: %s", resp)
	}
}
