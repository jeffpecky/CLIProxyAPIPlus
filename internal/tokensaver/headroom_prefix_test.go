package tokensaver

import (
	"reflect"
	"testing"
	"time"
)

func TestHeadroomPrefixCacheReusesOnlyMatchingSessionPrefix(t *testing.T) {
	now := time.Unix(100, 0)
	cache := newHeadroomPrefixCache(2, time.Minute, func() time.Time { return now })
	source := []any{message("user", "long"), message("assistant", "answer")}
	forwarded := []any{message("user", "short"), message("assistant", "answer")}
	scopeA := headroomPrefixScope{Session: "session-a"}
	cache.store(scopeA, source, forwarded)

	next := append(append([]any{}, source...), message("user", "next"))
	got, frozen := cache.reuse(scopeA, next)
	if frozen != 2 || !reflect.DeepEqual(got[:2], forwarded) {
		t.Fatalf("reuse = %#v frozen=%d", got, frozen)
	}
	other, frozen := cache.reuse(headroomPrefixScope{Session: "session-b"}, next)
	if frozen != 0 || !reflect.DeepEqual(other, next) {
		t.Fatalf("cross-session reuse = %#v frozen=%d", other, frozen)
	}
	changed := []any{message("user", "changed"), message("assistant", "answer"), message("user", "next")}
	got, frozen = cache.reuse(scopeA, changed)
	if frozen != 0 || !reflect.DeepEqual(got, changed) {
		t.Fatalf("changed prefix reused = %#v frozen=%d", got, frozen)
	}
}

func TestHeadroomPrefixCacheExpiresAndEvictsLRU(t *testing.T) {
	now := time.Unix(100, 0)
	cache := newHeadroomPrefixCache(2, time.Minute, func() time.Time { return now })
	cache.store(headroomPrefixScope{Session: "a"}, []any{message("user", "a")}, []any{message("user", "A")})
	cache.store(headroomPrefixScope{Session: "b"}, []any{message("user", "b")}, []any{message("user", "B")})
	cache.reuse(headroomPrefixScope{Session: "a"}, []any{message("user", "a")})
	cache.store(headroomPrefixScope{Session: "c"}, []any{message("user", "c")}, []any{message("user", "C")})
	if _, frozen := cache.reuse(headroomPrefixScope{Session: "b"}, []any{message("user", "b")}); frozen != 0 {
		t.Fatal("least-recently-used entry retained")
	}
	now = now.Add(2 * time.Minute)
	if _, frozen := cache.reuse(headroomPrefixScope{Session: "a"}, []any{message("user", "a")}); frozen != 0 {
		t.Fatal("expired entry reused")
	}
}

func TestHeadroomPrefixCacheIsolatesFullCompressionScope(t *testing.T) {
	cache := newHeadroomPrefixCache(8, time.Minute, time.Now)
	source := []any{message("user", "long")}
	forwarded := []any{message("user", "short")}
	scope := headroomPrefixScope{Session: "s", Format: "claude", Model: "m", Endpoint: "http://headroom/v1/compress", Config: "lossy_inline|users=false"}
	cache.store(scope, source, forwarded)
	for name, changed := range map[string]headroomPrefixScope{
		"format":   {Session: "s", Format: "openai", Model: "m", Endpoint: scope.Endpoint, Config: scope.Config},
		"model":    {Session: "s", Format: "claude", Model: "other", Endpoint: scope.Endpoint, Config: scope.Config},
		"endpoint": {Session: "s", Format: "claude", Model: "m", Endpoint: "http://other/v1/compress", Config: scope.Config},
		"config":   {Session: "s", Format: "claude", Model: "m", Endpoint: scope.Endpoint, Config: "lossy_inline|users=true"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, frozen := cache.reuse(changed, source); frozen != 0 {
				t.Fatalf("scope %s reused", name)
			}
		})
	}
}

func message(role, content string) any {
	return map[string]any{"role": role, "content": content}
}
