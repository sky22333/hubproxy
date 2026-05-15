package handlers

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"hubproxy/config"
	"hubproxy/utils"
)

type zeroReader struct{}

func (zeroReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0
	}
	return len(p), nil
}

type discardResponseWriter struct {
	header http.Header
	status int
	bytes  int64
}

func newDiscardResponseWriter() *discardResponseWriter {
	return &discardResponseWriter{header: make(http.Header)}
}

func (w *discardResponseWriter) Header() http.Header {
	return w.header
}

func (w *discardResponseWriter) WriteHeader(status int) {
	w.status = status
}

func (w *discardResponseWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	w.bytes += int64(len(p))
	return len(p), nil
}

func TestParseRegistryPath(t *testing.T) {
	tests := []struct {
		path      string
		image     string
		apiType   string
		reference string
	}{
		{"library/nginx/manifests/latest", "library/nginx", "manifests", "latest"},
		{"library/nginx/blobs/sha256:abc", "library/nginx", "blobs", "sha256:abc"},
		{"library/nginx/tags/list", "library/nginx", "tags", "list"},
	}

	for _, tt := range tests {
		image, apiType, reference := parseRegistryPath(tt.path)
		if image != tt.image || apiType != tt.apiType || reference != tt.reference {
			t.Fatalf("parseRegistryPath(%q) = %q %q %q", tt.path, image, apiType, reference)
		}
	}
}

func TestParseRegistryPathInvalid(t *testing.T) {
	image, apiType, reference := parseRegistryPath("library/nginx/unknown/latest")
	if image != "" || apiType != "" || reference != "" {
		t.Fatalf("invalid path parsed as %q %q %q", image, apiType, reference)
	}
}

type testEnv interface {
	Helper()
	TempDir() string
	Setenv(string, string)
	Fatal(...interface{})
}

func initDockerProxyTest(t testEnv, configBody string) {
	t.Helper()

	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(configBody), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CONFIG_PATH", path)
	if err := config.LoadConfig(); err != nil {
		t.Fatal(err)
	}
	utils.InitHTTPClients()
}

func TestRewriteAuthChallengePreservesScopeAndUsesProxyRealm(t *testing.T) {
	target := registryTarget{
		Name:        "ghcr.io",
		AuthService: "ghcr.io",
	}

	got := rewriteAuthChallenge(
		`Bearer realm="https://ghcr.io/token",service="ghcr.io",scope="repository:owner/image:pull"`,
		target,
		"https://proxy.example.com",
	)

	want := `Bearer realm="https://proxy.example.com/token/ghcr.io",service="ghcr.io",scope="repository:owner/image:pull"`
	if got != want {
		t.Fatalf("challenge = %q, want %q", got, want)
	}
}

func TestBuildAuthURLForDockerHubAddsLibraryScopeAndService(t *testing.T) {
	got, err := buildAuthURL(
		defaultRegistryTarget(),
		"service=ignored&scope=repository%3Aalpine%3Apull&client_id=docker",
	)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.HasPrefix(got, dockerHubAuthRealm+"?") {
		t.Fatalf("auth URL = %q", got)
	}
	if !strings.Contains(got, "service=registry.docker.io") {
		t.Fatalf("auth URL missing service: %q", got)
	}
	if !strings.Contains(got, "scope=repository%3Alibrary%2Falpine%3Apull") {
		t.Fatalf("auth URL missing normalized scope: %q", got)
	}
}

func TestDockerIODefaultTargetUsesBuiltInWhenUnconfigured(t *testing.T) {
	initDockerProxyTest(t, "")

	target := defaultRegistryTarget()
	if target.Upstream != dockerHubUpstream {
		t.Fatalf("Upstream = %q, want %q", target.Upstream, dockerHubUpstream)
	}
	if target.AuthRealm != dockerHubAuthRealm {
		t.Fatalf("AuthRealm = %q, want %q", target.AuthRealm, dockerHubAuthRealm)
	}
	if !target.AutoLibraryPrefix {
		t.Fatal("AutoLibraryPrefix = false, want true")
	}
}

func TestDockerIODefaultTargetCanBeOverriddenByConfig(t *testing.T) {
	initDockerProxyTest(t, `
[registries."docker.io"]
upstream = "mirror.local"
authHost = "auth.mirror.local/token"
authType = "docker"
enabled = true
`)

	target := defaultRegistryTarget()
	if target.Upstream != "https://mirror.local" {
		t.Fatalf("Upstream = %q, want custom mirror", target.Upstream)
	}
	if target.AuthRealm != "https://auth.mirror.local/token" {
		t.Fatalf("AuthRealm = %q, want custom auth realm", target.AuthRealm)
	}
	if target.AuthService != dockerHubAuthService {
		t.Fatalf("AuthService = %q, want %q", target.AuthService, dockerHubAuthService)
	}
	if !target.AutoLibraryPrefix {
		t.Fatal("AutoLibraryPrefix = false, want true")
	}
}

func TestDockerIODefaultTargetIgnoresDisabledOverride(t *testing.T) {
	initDockerProxyTest(t, `
[registries."docker.io"]
upstream = "mirror.local"
authHost = "auth.mirror.local/token"
authType = "docker"
enabled = false
`)

	target := defaultRegistryTarget()
	if target.Upstream != dockerHubUpstream {
		t.Fatalf("Upstream = %q, want built-in %q", target.Upstream, dockerHubUpstream)
	}
}

func TestProxyDockerRegistryTransparentlyForwardsAuthAndRewritesChallenge(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/team/app/manifests/latest" {
			t.Fatalf("upstream path = %q", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer client-token" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.Header.Get("Accept"); got != "application/vnd.docker.distribution.manifest.v2+json" {
			t.Fatalf("Accept = %q", got)
		}
		if got := r.Header.Get("Range"); got != "bytes=0-99" {
			t.Fatalf("Range = %q", got)
		}

		w.Header().Set("WWW-Authenticate", `Bearer realm="https://upstream.example/token",service="upstream.example",scope="repository:team/app:pull"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer upstream.Close()

	initDockerProxyTest(t, `
[registries."test.local"]
upstream = "`+upstream.URL+`"
authHost = "https://auth.test.local/token"
authType = "anonymous"
enabled = true
`)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Any("/v2/*path", ProxyDockerRegistryGin)

	req := httptest.NewRequest(http.MethodGet, "/v2/test.local/team/app/manifests/latest", nil)
	req.Host = "proxy.example.com"
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Authorization", "Bearer client-token")
	req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json")
	req.Header.Set("Range", "bytes=0-99")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", w.Code, w.Body.String())
	}

	wantChallenge := `Bearer realm="https://proxy.example.com/token/test.local",service="` + strings.TrimPrefix(upstream.URL, "http://") + `",scope="repository:team/app:pull"`
	if got := w.Header().Get("WWW-Authenticate"); got != wantChallenge {
		t.Fatalf("WWW-Authenticate = %q, want %q", got, wantChallenge)
	}
}

func TestDockerV2BaseProxiesUpstreamChallenge(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/" {
			t.Fatalf("upstream path = %q", r.URL.Path)
		}
		w.Header().Set("WWW-Authenticate", `Bearer realm="https://registry.example/token",service="registry.example"`)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer upstream.Close()

	initDockerProxyTest(t, `
[registries."docker.io"]
upstream = "`+upstream.URL+`"
authHost = "https://auth.example/token"
authType = "docker"
enabled = true
`)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Any("/v2/", ProxyDockerRegistryGin)

	req := httptest.NewRequest(http.MethodGet, "/v2/", nil)
	req.Host = "hub.example.com"
	req.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401; body=%s", w.Code, w.Body.String())
	}

	wantChallenge := `Bearer realm="https://hub.example.com/token/docker.io",service="registry.docker.io"`
	if got := w.Header().Get("WWW-Authenticate"); got != wantChallenge {
		t.Fatalf("WWW-Authenticate = %q, want %q", got, wantChallenge)
	}
}

func TestProxyDockerAuthForwardsBasicCredentials(t *testing.T) {
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Basic dXNlcjpwYXNz" {
			t.Fatalf("Authorization = %q", got)
		}
		if got := r.URL.Query().Get("service"); got != "127.0.0.1" {
			t.Fatalf("service = %q", got)
		}
		if got := r.URL.Query().Get("scope"); got != "repository:team/app:pull" {
			t.Fatalf("scope = %q", got)
		}

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"token":"secret","expires_in":3600}`))
	}))
	defer authServer.Close()

	initDockerProxyTest(t, `
[registries."test.local"]
upstream = "https://127.0.0.1"
authHost = "`+authServer.URL+`"
authType = "anonymous"
enabled = true
`)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Any("/token/*path", ProxyDockerAuthGin)

	req := httptest.NewRequest(http.MethodGet, "/token/test.local?scope=repository:team/app:pull", nil)
	req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	w := httptest.NewRecorder()

	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); !strings.Contains(got, `"token":"secret"`) {
		t.Fatalf("body = %q", got)
	}
}

func TestDockerHubShortNameIsProxiedWithLibraryPrefix(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/library/nginx/manifests/latest" {
			t.Fatalf("upstream path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/vnd.docker.distribution.manifest.v2+json")
		w.Header().Set("Docker-Content-Digest", "sha256:abc")
		_, _ = w.Write([]byte(`{"schemaVersion":2}`))
	}))
	defer upstream.Close()

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Any("/v2/*path", ProxyDockerRegistryGin)

	target := defaultRegistryTarget()
	target.Upstream = upstream.URL

	req := httptest.NewRequest(http.MethodGet, "/v2/nginx/manifests/latest", nil)
	w := httptest.NewRecorder()

	c, _ := gin.CreateTestContext(w)
	c.Request = req
	proxyRegistryHTTP(c, target, "/v2/library/nginx/manifests/latest")

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if got := w.Header().Get("Docker-Content-Digest"); got != "sha256:abc" {
		t.Fatalf("Docker-Content-Digest = %q", got)
	}
}

func TestProxyDockerRegistryHeadReturnsHeadersWithoutBody(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Fatalf("method = %s, want HEAD", r.Method)
		}
		w.Header().Set("Content-Length", "123")
		w.Header().Set("Docker-Content-Digest", "sha256:head")
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	initDockerProxyTest(t, `
[registries."test.local"]
upstream = "`+upstream.URL+`"
authHost = "https://auth.test.local/token"
authType = "anonymous"
enabled = true
`)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Any("/v2/*path", ProxyDockerRegistryGin)

	req := httptest.NewRequest(http.MethodHead, "/v2/test.local/team/app/blobs/sha256:abc", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	if got := w.Header().Get("Docker-Content-Digest"); got != "sha256:head" {
		t.Fatalf("Docker-Content-Digest = %q", got)
	}
	if body := w.Body.String(); body != "" {
		t.Fatalf("HEAD body = %q, want empty", body)
	}
}

func TestProxyDockerRegistryStreamsBlobAndSkipsHopByHopHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Connection", "close")
		w.Header().Set("Transfer-Encoding", "chunked")
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.WriteString(w, "layer-data")
	}))
	defer upstream.Close()

	initDockerProxyTest(t, `
[registries."test.local"]
upstream = "`+upstream.URL+`"
authHost = "https://auth.test.local/token"
authType = "anonymous"
enabled = true
`)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Any("/v2/*path", ProxyDockerRegistryGin)

	req := httptest.NewRequest(http.MethodGet, "/v2/test.local/team/app/blobs/sha256:abc", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if got := w.Body.String(); got != "layer-data" {
		t.Fatalf("body = %q", got)
	}
	if got := w.Header().Get("Connection"); got != "" {
		t.Fatalf("Connection header leaked: %q", got)
	}
}

func TestProxyDockerRegistryUsesNsQueryForContainerd(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/team/app/manifests/latest" {
			t.Fatalf("upstream path = %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("ns"); got != "test.local" {
			t.Fatalf("ns query = %q", got)
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()

	initDockerProxyTest(t, `
[registries."test.local"]
upstream = "`+upstream.URL+`"
authHost = "https://auth.test.local/token"
authType = "anonymous"
enabled = true
`)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Any("/v2/*path", ProxyDockerRegistryGin)

	req := httptest.NewRequest(http.MethodGet, "/v2/team/app/manifests/latest?ns=test.local", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
}

func TestProxyDockerAuthCachesOnlyAnonymousTokenRequests(t *testing.T) {
	var hits int32
	authServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := atomic.AddInt32(&hits, 1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"token":"token-%d","expires_in":3600}`, count)
	}))
	defer authServer.Close()

	initDockerProxyTest(t, `
[registries."test.local"]
upstream = "https://test.local"
authHost = "`+authServer.URL+`"
authType = "anonymous"
enabled = true
`)
	utils.GlobalCache = &utils.UniversalCache{}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Any("/token/*path", ProxyDockerAuthGin)

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/token/test.local?scope=repository:team/app:pull", nil)
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("anonymous request %d status = %d; body=%s", i, w.Code, w.Body.String())
		}
		if got := w.Body.String(); !strings.Contains(got, `"token":"token-1"`) {
			t.Fatalf("anonymous request %d body = %q", i, got)
		}
	}

	if got := atomic.LoadInt32(&hits); got != 1 {
		t.Fatalf("anonymous token hits = %d, want 1", got)
	}

	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodGet, "/token/test.local?scope=repository:team/app:pull", nil)
		req.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
		w := httptest.NewRecorder()
		router.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("authenticated request %d status = %d; body=%s", i, w.Code, w.Body.String())
		}
	}

	if got := atomic.LoadInt32(&hits); got != 3 {
		t.Fatalf("authenticated token hits total = %d, want 3", got)
	}
}

func TestProxyDockerAuthRejectsUnknownRegistry(t *testing.T) {
	initDockerProxyTest(t, "")

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Any("/token/*path", ProxyDockerAuthGin)

	req := httptest.NewRequest(http.MethodGet, "/token/missing.local?scope=repository:team/app:pull", nil)
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestProxyDockerRegistryConcurrentRequests(t *testing.T) {
	const requests = 64
	var hits int32

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&hits, 1)
		if got := r.Header.Get("Authorization"); got == "" {
			t.Fatal("missing Authorization")
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write([]byte("ok"))
	}))
	defer upstream.Close()

	initDockerProxyTest(t, `
[registries."test.local"]
upstream = "`+upstream.URL+`"
authHost = "https://auth.test.local/token"
authType = "anonymous"
enabled = true
`)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Any("/v2/*path", ProxyDockerRegistryGin)

	var wg sync.WaitGroup
	errs := make(chan string, requests)
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/v2/test.local/team/app/blobs/sha256:abc", nil)
			req.Header.Set("Authorization", fmt.Sprintf("Bearer token-%d", i))
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)
			if w.Code != http.StatusOK || w.Body.String() != "ok" {
				errs <- fmt.Sprintf("request %d status=%d body=%q", i, w.Code, w.Body.String())
			}
		}(i)
	}
	wg.Wait()
	close(errs)

	for err := range errs {
		t.Fatal(err)
	}
	if got := atomic.LoadInt32(&hits); got != requests {
		t.Fatalf("hits = %d, want %d", got, requests)
	}
}

func TestProxyDockerRegistryLargeBlobStreamsWithoutRecorderBuffer(t *testing.T) {
	const blobSize = 8 << 20

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", blobSize))
		_, _ = io.CopyN(w, zeroReader{}, blobSize)
	}))
	defer upstream.Close()

	initDockerProxyTest(t, `
[registries."test.local"]
upstream = "`+upstream.URL+`"
authHost = "https://auth.test.local/token"
authType = "anonymous"
enabled = true
`)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Any("/v2/*path", ProxyDockerRegistryGin)

	req := httptest.NewRequest(http.MethodGet, "/v2/test.local/team/app/blobs/sha256:large", nil)
	w := newDiscardResponseWriter()
	router.ServeHTTP(w, req)

	if w.status != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.status)
	}
	if w.bytes != blobSize {
		t.Fatalf("streamed bytes = %d, want %d", w.bytes, blobSize)
	}
	if got := w.Header().Get("Content-Length"); got != fmt.Sprintf("%d", blobSize) {
		t.Fatalf("Content-Length = %q", got)
	}
}

func BenchmarkProxyDockerRegistryBlobStreaming(b *testing.B) {
	const blobSize = 1 << 20

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Length", fmt.Sprintf("%d", blobSize))
		_, _ = io.CopyN(w, zeroReader{}, blobSize)
	}))
	defer upstream.Close()

	initDockerProxyTest(b, `
[registries."bench.local"]
upstream = "`+upstream.URL+`"
authHost = "https://auth.bench.local/token"
authType = "anonymous"
enabled = true
`)

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Any("/v2/*path", ProxyDockerRegistryGin)

	b.ReportAllocs()
	b.SetBytes(blobSize)
	b.ResetTimer()

	for i := 0; i < b.N; i++ {
		req := httptest.NewRequest(http.MethodGet, "/v2/bench.local/team/app/blobs/sha256:bench", nil)
		w := newDiscardResponseWriter()
		router.ServeHTTP(w, req)
		if w.status != http.StatusOK || w.bytes != blobSize {
			b.Fatalf("status=%d bytes=%d", w.status, w.bytes)
		}
	}
}
