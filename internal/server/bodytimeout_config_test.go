package server

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Exercise the real ResponseController path: honoring an explicit total timeout
// must not be bypassed by the fork's idle-deadline renewal.
func TestReadBodyHonorsConfiguredTotalTimeout(t *testing.T) {
	const timeout = 50 * time.Millisecond
	readErr := make(chan error, 1)
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, err := readBodyWithTimeout(w, r, timeout)
		readErr <- err
		if err != nil {
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	srv.Config.ReadTimeout = timeout
	srv.Start()
	defer srv.Close()

	conn, err := net.Dial("tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	// Deliberately leave the second byte pending until the configured deadline.
	if _, err := io.WriteString(conn, "POST / HTTP/1.1\r\nHost: x\r\nContent-Length: 2\r\n\r\nx"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status=%d, want 400 for incomplete upload", resp.StatusCode)
	}
	if err := <-readErr; !isBodyIdleTimeout(err) {
		t.Fatalf("read error=%v, want timeout", err)
	}
}
