package session

import "testing"

func TestDerivePromptCacheSessionKey(t *testing.T) {
	const helloKey = "d-8a2a5c9b768827de5a9552c38a044c66"
	for _, tc := range []struct {
		name, body, want string
	}{
		{"top user_id", `{"user_id":"user-1","messages":[{"role":"user","content":"hello"}]}`, helloKey},
		{"metadata user_id", `{"metadata":{"user_id":"user-2"},"messages":[{"role":"user","content":"hello"}]}`, helloKey},
		{"appended history", `{"user_id":"user-1","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"ok"},{"role":"user","content":"next"}]}`, helloKey},
		{"empty", ``, ""},
		{"invalid", `{`, ""},
		{"null", `null`, ""},
		{"no messages", `{"user_id":"user-1"}`, ""},
		{"no user", `{"metadata":{"user_id":"user-1"},"messages":[{"role":"system","content":"system"}]}`, ""},
		{"empty user content", `{"user_id":"user-1","messages":[{"role":"user","content":""}]}`, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := DerivePromptCacheSessionKey([]byte(tc.body)); got != tc.want {
				t.Errorf("cache session key=%q want=%q", got, tc.want)
			}
			if got := ExtractKey([]byte(tc.body)); got != "" {
				t.Errorf("cache derivation must not enable sticky routing: ExtractKey=%q", got)
			}
		})
	}
}
