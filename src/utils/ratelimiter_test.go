package utils

import (
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestExtractIPFromAddress(t *testing.T) {
	if got := extractIPFromAddress("127.0.0.1:5000"); got != "127.0.0.1" {
		t.Fatalf("extract IPv4 = %q", got)
	}
	if got := extractIPFromAddress("[2001:db8::1]:5000"); got != "2001:db8::1" {
		t.Fatalf("extract IPv6 = %q", got)
	}
}

func TestNormalizeIPv6ForRateLimit(t *testing.T) {
	if got := normalizeIPForRateLimit("192.168.1.2"); got != "192.168.1.2" {
		t.Fatalf("IPv4 normalized = %q", got)
	}
	if got := normalizeIPForRateLimit("2001:db8::1"); got != "2001:db8::/64" {
		t.Fatalf("IPv6 normalized = %q", got)
	}
}

func TestClientIPIgnoresSpoofedXFFWithoutTrustedProxy(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	ConfigureTrustedProxies(router)

	var got string
	router.GET("/", func(c *gin.Context) {
		got = c.ClientIP()
	})

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "203.0.113.50:12345"
	req.Header.Set("X-Forwarded-For", "127.0.0.1")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got != "203.0.113.50" {
		t.Fatalf("ClientIP() = %q, want 203.0.113.50", got)
	}
}

func TestClientIPTrustsXFFFromTrustedProxy(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	if err := router.SetTrustedProxies([]string{"127.0.0.1"}); err != nil {
		t.Fatal(err)
	}

	var got string
	router.GET("/", func(c *gin.Context) {
		got = c.ClientIP()
	})

	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "127.0.0.1:54321"
	req.Header.Set("X-Forwarded-For", "203.0.113.50")

	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if got != "203.0.113.50" {
		t.Fatalf("ClientIP() = %q, want 203.0.113.50", got)
	}
}
