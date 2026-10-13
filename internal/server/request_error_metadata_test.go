package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/reqlog"
)

type failedUpload struct{ err error }

func (r failedUpload) Read([]byte) (int, error) { return 0, r.err }
func (r failedUpload) Close() error             { return nil }

type uploadTimeout struct{}

func (uploadTimeout) Error() string   { return "sensitive network details" }
func (uploadTimeout) Timeout() bool   { return true }
func (uploadTimeout) Temporary() bool { return true }

func TestFailedUploadRecordsReasonWithoutPayload(t *testing.T) {
	for _, tc := range []struct {
		name, code string
		err        error
	}{
		{"timeout", "body_read_timeout", uploadTimeout{}},
		{"truncated", "body_read_error", io.ErrUnexpectedEOF},
		{"read_failure", "body_read_error", errors.New("private credential and prompt")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var journal bytes.Buffer
			previous := log.Writer()
			log.SetOutput(&journal)
			t.Cleanup(func() { log.SetOutput(previous) })
			logs := reqlog.New(reqlog.Config{})
			defer logs.Close()
			h := NewHandler(Config{RequestLog: logs}) // no upstream: failure must stop before selection
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			r.Body = failedUpload{err: tc.err}
			w := httptest.NewRecorder()
			h.ServeHTTP(w, r)
			if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), `"code":"invalid_request"`) {
				t.Fatalf("response compatibility changed: %d %s", w.Code, w.Body)
			}
			recent := logs.Snapshot().Recent
			if len(recent) != 1 {
				t.Fatalf("events = %d want 1", len(recent))
			}
			data, err := json.Marshal(recent[0])
			if err != nil {
				t.Fatal(err)
			}
			var event map[string]any
			if err := json.Unmarshal(data, &event); err != nil {
				t.Fatal(err)
			}
			if event["error_code"] != tc.code || event["error_stage"] != "read_body" {
				t.Fatalf("missing upload error classification: %s", data)
			}
			if recent[0].RequestID != w.Header().Get("X-Request-Id") || recent[0].OK || recent[0].Attempts != 0 {
				t.Fatalf("wrong request attribution: %+v", recent[0])
			}
			if !strings.Contains(journal.String(), "request="+recent[0].RequestID) || !strings.Contains(journal.String(), "code="+tc.code) {
				t.Fatalf("journal cannot be correlated to archive: %s", journal.String())
			}
			if strings.Contains(journal.String(), tc.err.Error()) {
				t.Fatalf("raw read error leaked into journal: %s", journal.String())
			}
			if bytes.Contains(data, []byte(tc.err.Error())) {
				t.Fatalf("raw read error leaked into metadata: %s", data)
			}
		})
	}
}

func TestGatewayErrorsRecordCodeWithoutMessage(t *testing.T) {
	logs := reqlog.New(reqlog.Config{})
	defer logs.Close()
	h := NewHandler(Config{RequestLog: logs, APIKey: "secret"})
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("private prompt")))
	data, err := json.Marshal(logs.Snapshot().Recent[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"error_code":"invalid_api_key"`)) || bytes.Contains(data, []byte("private prompt")) {
		t.Fatalf("error metadata: %s", data)
	}
}

func TestSuccessfulRequestCorrelatesTableAndArchive(t *testing.T) {
	withChatLog(t)
	logs := reqlog.New(reqlog.Config{})
	defer logs.Close()
	up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, sseOK, true })
	h := NewHandler(Config{RequestLog: logs, Upstream: up, Pool: testPoolWith(&auth.Auth{UID: "u1", AccessToken: "fake", ExpiresAt: 9999999999})})
	w := httptest.NewRecorder()
	output := captureStdout(t, func() {
		h.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","stream":true,"messages":[]}`)))
	})
	e := logs.Snapshot().Recent[0]
	if !e.OK || e.RequestID != w.Header().Get("X-Request-Id") || !strings.Contains(output, "rid="+e.RequestID) {
		t.Fatalf("correlation failed: status=%d event=%+v log=%s", w.Code, e, output)
	}
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`"error_code"`)) || bytes.Contains(data, []byte(`"error_stage"`)) {
		t.Fatalf("success must omit error metadata: %s", data)
	}
}

// TestReadBodyFailureIncludesClassifiedDetail 是上面用例的补充：error_detail 必须是
// **固定分类标签**，让运维能区分「客户端断的」与「我们掐的」，同时仍然不泄漏原文。
//
// 为什么需要（2026-10-13 生产核对）：56 次 read_body 失败此前只有 body_read_error，
// 300s 空闲守卫一次都没触发，但日志无从看出失败是客户端自己断开（43/56 是 axonhub
// 在 ~30s 处断开）还是我们的 deadline。没有分类就只能靠时长分布猜。
func TestReadBodyFailureIncludesClassifiedDetail(t *testing.T) {
	for _, tc := range []struct {
		name, wantDetail string
		err              error
	}{
		{"我们的空闲守卫到期", "server_timeout", uploadTimeout{}},
		{"客户端提前断开", "peer_closed", io.ErrUnexpectedEOF},
		{"无法归类", "other", errors.New("private credential and prompt")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := reqlog.New(reqlog.Config{})
			defer logs.Close()
			h := NewHandler(Config{RequestLog: logs})
			r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			r.Body = failedUpload{err: tc.err}
			h.ServeHTTP(httptest.NewRecorder(), r)

			recent := logs.Snapshot().Recent
			if len(recent) != 1 {
				t.Fatalf("events = %d want 1", len(recent))
			}
			if got := recent[0].ErrorDetail; got != tc.wantDetail {
				t.Fatalf("error_detail=%q want %q", got, tc.wantDetail)
			}
			data, err := json.Marshal(recent[0])
			if err != nil {
				t.Fatal(err)
			}
			// 分类标签之外不得夹带原始错误文本。
			if bytes.Contains(data, []byte(tc.err.Error())) && tc.err.Error() != tc.wantDetail {
				t.Fatalf("raw read error leaked into metadata: %s", data)
			}
		})
	}
}

// TestSuccessfulRequestOmitsErrorDetail 确保新字段不污染成功事件（零回归）。
func TestSuccessfulRequestOmitsErrorDetail(t *testing.T) {
	withChatLog(t)
	logs := reqlog.New(reqlog.Config{})
	defer logs.Close()
	up := newFakeUpstream(t, func(string) (int, string, bool) { return 200, sseOK, true })
	h := NewHandler(Config{RequestLog: logs, Upstream: up, Pool: testPoolWith(&auth.Auth{UID: "u1", AccessToken: "fake", ExpiresAt: 9999999999})})
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"glm-5.2","stream":true,"messages":[]}`)))
	e := logs.Snapshot().Recent[0]
	if e.ErrorDetail != "" {
		t.Fatalf("success must not carry error_detail: %q", e.ErrorDetail)
	}
	data, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`"error_detail"`)) {
		t.Fatalf("success must omit error_detail: %s", data)
	}
}
