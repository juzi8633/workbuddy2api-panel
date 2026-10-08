package session

import "encoding/json"

// DerivePromptCacheSessionKey derives a cache identity from the original request's
// system and first user content. Call before rewriting prompts, and only when
// ExtractKey is empty. Unlike ExtractKey, user_id does not suppress this fallback:
// cache prefix isolation must not opt a user_id client into sticky routing.
// Explicit conversation IDs and prompt_cache_key are handled by the caller.
// Invalid bodies or missing user content retain the empty cache fallback.
func DerivePromptCacheSessionKey(body []byte) string {
	var obj map[string]any
	if err := json.Unmarshal(body, &obj); err != nil {
		return ""
	}
	return deriveKey(obj)
}
