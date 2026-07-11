package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"hubproxy/config"
	"hubproxy/handlers"
	"hubproxy/utils"
)

func newTestRouter(t *testing.T, configBody string) *gin.Engine {
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
	globalLimiter = utils.InitGlobalLimiter()
	handlers.InitDockerProxy()
	handlers.InitImageStreamer()
	handlers.InitDebouncer()

	return buildRouter(config.GetConfig())
}

func performRequest(router http.Handler, method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("User-Agent", "hubproxy-test")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestReadyRoute(t *testing.T) {
	router := newTestRouter(t, "")

	w := performRequest(router, http.MethodGet, "/ready", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["ready"] != true || got["service"] != "hubproxy" {
		t.Fatalf("unexpected ready response: %#v", got)
	}
}

func TestFrontendDisabledRoutesReturnNotFound(t *testing.T) {
	router := newTestRouter(t, `
[server]
enableFrontend = false
`)

	for _, path := range []string{"/", "/images", "/search", "/favicon.svg"} {
		w := performRequest(router, http.MethodGet, path, "")
		if w.Code != http.StatusNotFound {
			t.Fatalf("%s status = %d, want 404", path, w.Code)
		}
	}
}

func TestSingleImageDownloadPrepareReturnsURL(t *testing.T) {
	router := newTestRouter(t, "")

	w := performRequest(router, http.MethodGet, "/api/image/download?image=nginx&mode=prepare", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	var got struct {
		DownloadURL string `json:"download_url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got.DownloadURL, "image=nginx") || !strings.Contains(got.DownloadURL, "token=") {
		t.Fatalf("download_url = %q", got.DownloadURL)
	}
	if !strings.HasPrefix(got.DownloadURL, "/api/image/download?") {
		t.Fatalf("download_url = %q", got.DownloadURL)
	}
}

func TestBatchImageDownloadPrepareReturnsURL(t *testing.T) {
	router := newTestRouter(t, "")

	body := `{"images":["nginx"],"useCompressedLayers":true}`
	w := performRequest(router, http.MethodPost, "/api/image/batch?mode=prepare", body)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	var got struct {
		DownloadURL string `json:"download_url"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got.DownloadURL, "/api/image/batch?token=") {
		t.Fatalf("download_url = %q", got.DownloadURL)
	}
}

func TestBatchImageDownloadRejectsTooManyImages(t *testing.T) {
	router := newTestRouter(t, `
[download]
maxImages = 1
`)

	body := `{"images":["nginx","redis"],"useCompressedLayers":true}`
	w := performRequest(router, http.MethodPost, "/api/image/batch?mode=prepare", body)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestGitHubNoRouteRejectsUnsupportedHost(t *testing.T) {
	router := newTestRouter(t, "")

	w := performRequest(router, http.MethodGet, "/https://example.com/file.zip", "")
	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403; body=%s", w.Code, w.Body.String())
	}
}

func TestDockerV2PingAndInvalidPath(t *testing.T) {
	router := newTestRouter(t, "")

	w := performRequest(router, http.MethodGet, "/v2/", "")
	if w.Code != http.StatusOK {
		t.Fatalf("/v2/ status = %d, want 200; body=%s", w.Code, w.Body.String())
	}

	w = performRequest(router, http.MethodGet, "/v2/library/nginx/unknown/latest", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid v2 status = %d, want 400; body=%s", w.Code, w.Body.String())
	}
}

func TestSearchAPIRejectsMissingQuery(t *testing.T) {
	router := newTestRouter(t, `
[server]
enableFrontend = false
`)

	w := performRequest(router, http.MethodGet, "/api/search", "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", w.Code, w.Body.String())
	}

	var got map[string]string
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["error"] == "" {
		t.Fatalf("missing error response: %#v", got)
	}
}

func TestSearchServesSPAWhenFrontendEnabled(t *testing.T) {
	router := newTestRouter(t, `
[server]
enableFrontend = true
`)

	w := performRequest(router, http.MethodGet, "/search?q=nginx", "")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Header().Get("Content-Type"), "text/html") {
		t.Fatalf("content-type = %q, want text/html", w.Header().Get("Content-Type"))
	}
	if !strings.Contains(w.Body.String(), `<div id="app">`) {
		t.Fatalf("SPA shell missing: %s", w.Body.String())
	}
}
