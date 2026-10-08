package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/linguo2625469/workbuddy2api-panel/internal/auth"
	"github.com/linguo2625469/workbuddy2api-panel/internal/pool"
	"github.com/linguo2625469/workbuddy2api-panel/internal/session"
	"github.com/linguo2625469/workbuddy2api-panel/internal/upstream"
)

// Capture the actual HTTP payload after handler rewrites and upstream preparation.
// All upstream URLs and the HTTP client point to this local server; no real account
// or upstream is used. Keep this fixture independent of handler_test.go helpers.
func promptCacheSessionSender(t *testing.T, promptMode string) func(string, bool) string {
	t.Helper()
	type capture struct {
		key string
		err error
	}
	captured := make(chan capture, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Key string `json:"prompt_cache_key"`
		}
		err := json.NewDecoder(r.Body).Decode(&body)
		captured <- capture{body.Key, err}
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"id\":\"cache-test\",\"object\":\"chat.completion.chunk\",\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"ok\"}}]}\n\n"+
			"data: {\"id\":\"cache-test\",\"object\":\"chat.completion.chunk\",\"model\":\"glm-5.2\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n"+
			"data: [DONE]\n\n")
	}))
	t.Cleanup(srv.Close)
	p := pool.New("")
	p.Add(&auth.Auth{UID: "cache-user", AccessToken: "fake-token", ExpiresAt: 9999999999})
	p.SetCredits("cache-user", 1000, 0)
	up := &upstream.Client{HTTP: srv.Client(), ChatBaseCN: srv.URL, BillingBaseCN: srv.URL}
	h := NewHandler(Config{Pool: p, Upstream: up, PromptMode: promptMode, PromptText: "gateway system"})
	return func(body string, stream bool) string {
		t.Helper()
		var obj map[string]any
		if err := json.Unmarshal([]byte(body), &obj); err != nil {
			t.Fatal(err)
		}
		obj["stream"] = stream
		encoded, err := json.Marshal(obj)
		if err != nil {
			t.Fatal(err)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(encoded))))
		if rec.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
		}
		if stream {
			if !strings.Contains(rec.Body.String(), "data: [DONE]") {
				t.Fatalf("incomplete stream: %s", rec.Body.String())
			}
		} else {
			var response struct {
				Object string `json:"object"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || response.Object != "chat.completion" {
				t.Fatalf("invalid aggregated response: %s (err=%v)", rec.Body.String(), err)
			}
		}
		select {
		case got := <-captured:
			if got.err != nil {
				t.Fatalf("decode outbound payload: %v", got.err)
			}
			if got.key == "" {
				t.Fatal("outbound prompt_cache_key is empty")
			}
			return got.key
		default:
			t.Fatal("no local upstream request captured")
			return ""
		}
	}
}

func TestPromptCacheSessionDerived(t *testing.T) {
	for _, mode := range []string{"passthrough", "custom"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", mode, stream), func(t *testing.T) {
				send := promptCacheSessionSender(t, mode)
				first := send(`{"model":"glm-5.2","messages":[{"role":"system","content":"system A"},{"role":"user","content":"first A"}]}`, stream)
				otherUser := send(`{"model":"glm-5.2","messages":[{"role":"system","content":"system A"},{"role":"user","content":"first B"}]}`, stream)
				otherSystem := send(`{"model":"glm-5.2","messages":[{"role":"system","content":"system B"},{"role":"user","content":"first A"}]}`, stream)
				if first == otherUser || first == otherSystem || otherUser == otherSystem {
					t.Errorf("distinct derived sessions collided: first=%q otherUser=%q otherSystem=%q", first, otherUser, otherSystem)
				}
				// Cross the stream/nonstream boundary on the next turn. The last
				// user message changes, while the session's initial prefix stays.
				next := send(`{"model":"glm-5.2","messages":[{"role":"system","content":"system A"},{"role":"user","content":"first A"},{"role":"assistant","content":"ok"},{"role":"user","content":"next turn"}]}`, !stream)
				if next != first {
					t.Errorf("same session changed key across turns/modes: first=%q next=%q", first, next)
				}
			})
		}
	}
}

func TestPromptCacheSessionExplicit(t *testing.T) {
	const explicitKey = "wb2a-cache-us-0051294b03d291300a1a758835292280"
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			send := promptCacheSessionSender(t, "passthrough")
			for _, id := range []string{
				`"conversation_id":"explicit-conv"`,
				`"conversationId":"explicit-conv"`,
				`"metadata":{"conversation_id":"explicit-conv"}`,
				`"metadata":{"conversationId":"explicit-conv"}`,
			} {
				body := `{"model":"glm-5.2","messages":[{"role":"user","content":"hello"}],` + id + `}`
				if got := send(body, stream); got != explicitKey {
					t.Errorf("explicit ID behavior changed (%s): got=%q want=%q", id, got, explicitKey)
				}
			}
			for _, id := range []string{"", `,"conversation_id":"explicit-conv"`} {
				body := `{"model":"glm-5.2","messages":[{"role":"user","content":"hello"}],"prompt_cache_key":"client-cache-key"` + id + `}`
				if got := send(body, stream); got != "client-cache-key" {
					t.Errorf("client prompt_cache_key overwritten: got=%q", got)
				}
			}
		})
	}
}

func TestPromptCacheSessionEmpty(t *testing.T) {
	// No user prefix retains the old account-only fallback; user_id suppresses
	// stickiness but no longer suppresses independent cache-prefix isolation.
	const emptyKey = "wb2a-cache-us-750baa1fbc3ddfb1cb0cd014983ef281"
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", stream), func(t *testing.T) {
			send := promptCacheSessionSender(t, "passthrough")
			for _, tc := range []struct{ body, want string }{
				{`{"model":"glm-5.2","messages":[]}`, emptyKey},
				{`{"model":"glm-5.2","user_id":"user-1","messages":[]}`, emptyKey},
				{`{"model":"glm-5.2","metadata":{"user_id":"user-1"},"messages":[{"role":"system","content":"system only"}]}`, emptyKey},
				{`{"model":"glm-5.2","user_id":"user-1","messages":[{"role":"user","content":"hello"}]}`, "wb2a-cache-us-25b33b3159b3ffe8b4794242f3aeb0cd"},
				{`{"model":"glm-5.2","metadata":{"user_id":"user-1"},"messages":[{"role":"user","content":"different first turn"}]}`, "wb2a-cache-us-97ddcb2149c9c9ffd6929c397eff0898"},
			} {
				if got := send(tc.body, stream); got != tc.want {
					t.Errorf("cache fallback: got=%q want=%q body=%s", got, tc.want, tc.body)
				}
			}
		})
	}
}

func TestPromptCacheSessionUserID(t *testing.T) {
	for _, field := range []string{`"user_id":"%s"`, `"metadata":{"user_id":"%s"}`} {
		for _, mode := range []string{"passthrough", "custom"} {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stream=%t", field, mode, stream), func(t *testing.T) {
					send := promptCacheSessionSender(t, mode)
					body := func(userID, system, first, extra string) string {
						return `{"model":"glm-5.2",` + fmt.Sprintf(field, userID) + `,"messages":[{"role":"system","content":"` + system + `"},{"role":"user","content":"` + first + `"}` + extra + `]}`
					}
					firstBody := body("user-1", "system A", "first A", "")
					otherUserBody := body("user-1", "system A", "first B", "")
					otherSystemBody := body("user-1", "system B", "first A", "")
					changedIDBody := body("user-2", "system A", "first A", "")
					nextBody := body("user-1", "system A", "first A", `,{"role":"assistant","content":"ok"},{"role":"user","content":"next turn"}`)
					for _, b := range []string{firstBody, otherUserBody, otherSystemBody, changedIDBody, nextBody} {
						if got := session.ExtractKey([]byte(b)); got != "" {
							t.Fatalf("user_id request regained stickiness: key=%q body=%s", got, b)
						}
					}
					first := send(firstBody, stream)
					otherUser := send(otherUserBody, stream)
					otherSystem := send(otherSystemBody, stream)
					if first == otherUser || first == otherSystem || otherUser == otherSystem {
						t.Errorf("user_id cache prefixes collided: first=%q otherUser=%q otherSystem=%q", first, otherUser, otherSystem)
					}
					if got := send(nextBody, !stream); got != first {
						t.Errorf("appended history changed cache key: first=%q next=%q", first, got)
					}
					if got := send(changedIDBody, stream); got != first {
						t.Errorf("user_id used as cache identity: first=%q changedID=%q", first, got)
					}
					withoutID := `{"model":"glm-5.2","messages":[{"role":"system","content":"system A"},{"role":"user","content":"first A"}]}`
					if got := send(withoutID, stream); got != first {
						t.Errorf("same prefix has different key with user_id: first=%q withoutID=%q", first, got)
					}
					for _, explicit := range []string{`,"conversation_id":"explicit-conv"`, `,"prompt_cache_key":"client-cache-key"`} {
						explicitBody := strings.TrimSuffix(firstBody, "}") + explicit + "}"
						want := "wb2a-cache-us-0051294b03d291300a1a758835292280"
						if strings.Contains(explicit, "prompt_cache_key") {
							want = "client-cache-key"
						}
						if got := send(explicitBody, stream); got != want {
							t.Errorf("explicit cache identity changed with user_id: got=%q want=%q", got, want)
						}
					}
				})
			}
		}
	}
}
