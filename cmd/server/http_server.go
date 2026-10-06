package main

import (
	"net/http"
	"time"
)

// newHTTPServer keeps server construction testable while honoring the upstream
// server.read_timeout setting. Zero disables the total body-read limit; the chat
// handler then applies an idle deadline so ongoing slow uploads can complete.
func newHTTPServer(addr string, handler http.Handler, readTimeout time.Duration) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		// SSE responses may legitimately last minutes, so there is no global
		// WriteTimeout. IdleTimeout only governs idle keep-alive connections.
		IdleTimeout: idleTimeout,
	}
}

const (
	readHeaderTimeout = 30 * time.Second
	idleTimeout       = 120 * time.Second
)
