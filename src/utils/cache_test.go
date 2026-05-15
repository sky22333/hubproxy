package utils

import (
	"testing"
	"time"
)

func TestUniversalCacheSetGetAndExpire(t *testing.T) {
	cache := &UniversalCache{}

	cache.Set("k", []byte("v"), "text/plain", map[string]string{"X-Test": "1"}, time.Minute)
	if got := cache.Get("k"); got == nil || string(got.Data) != "v" || got.Headers["X-Test"] != "1" {
		t.Fatalf("cache hit mismatch: %#v", got)
	}

	cache.Set("expired", []byte("v"), "", nil, -time.Second)
	if got := cache.Get("expired"); got != nil {
		t.Fatalf("expired item returned: %#v", got)
	}
}

func TestTokenCacheHelpers(t *testing.T) {
	cache := &UniversalCache{}
	cache.SetToken("token", `{"token":"abc"}`, time.Minute)

	if got := cache.GetToken("token"); got != `{"token":"abc"}` {
		t.Fatalf("GetToken = %q", got)
	}
}

func TestExtractTTLFromResponse(t *testing.T) {
	ttl := ExtractTTLFromResponse([]byte(`{"expires_in":3600}`))
	if ttl != 55*time.Minute {
		t.Fatalf("TTL = %s, want 55m", ttl)
	}

	if ttl := ExtractTTLFromResponse([]byte(`{"expires_in":300}`)); ttl != 270*time.Second {
		t.Fatalf("short TTL = %s, want 270s", ttl)
	}

	if ttl := ExtractTTLFromResponse([]byte(`{}`)); ttl != 30*time.Minute {
		t.Fatalf("default TTL = %s", ttl)
	}
}

func TestBuildCacheKeyStable(t *testing.T) {
	a := BuildCacheKey("p", "query")
	b := BuildCacheKey("p", "query")
	c := BuildCacheKey("p", "other")
	if a != b || a == c {
		t.Fatalf("unexpected keys: %q %q %q", a, b, c)
	}
}
