package handlers

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"hubproxy/config"
	"hubproxy/utils"
)

type registryTarget struct {
	Name              string
	Upstream          string
	AuthRealm         string
	AuthService       string
	AutoLibraryPrefix bool
}

const (
	dockerHubName        = "docker.io"
	dockerHubUpstream    = "https://registry-1.docker.io"
	dockerHubAuthRealm   = "https://auth.docker.io/token"
	dockerHubAuthService = "registry.docker.io"
)

var hopByHopHeaders = map[string]struct{}{
	"connection":          {},
	"keep-alive":          {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"te":                  {},
	"trailer":             {},
	"transfer-encoding":   {},
	"upgrade":             {},
}

var forwardedRequestHeaders = []string{
	"Authorization",
	"Accept",
	"Range",
	"If-Range",
	"If-Match",
	"If-None-Match",
	"If-Modified-Since",
	"If-Unmodified-Since",
}

// 保留初始化入口，在线代理无状态。
func InitDockerProxy() {}

func defaultRegistryTarget() registryTarget {
	cfg := config.GetConfig()
	if mapping, exists := cfg.Registries[dockerHubName]; exists && mapping.Enabled {
		target := registryTargetFromMapping(dockerHubName, mapping)
		target.AuthService = dockerHubAuthService
		target.AutoLibraryPrefix = true
		return target
	}

	return registryTarget{
		Name:              dockerHubName,
		Upstream:          dockerHubUpstream,
		AuthRealm:         dockerHubAuthRealm,
		AuthService:       dockerHubAuthService,
		AutoLibraryPrefix: true,
	}
}

func registryTargetFromMapping(name string, mapping config.RegistryMapping) registryTarget {
	upstream := strings.TrimRight(strings.TrimSpace(mapping.Upstream), "/")
	if upstream == "" {
		upstream = name
	}
	if !strings.HasPrefix(upstream, "http://") && !strings.HasPrefix(upstream, "https://") {
		upstream = "https://" + upstream
	}

	authRealm := strings.TrimSpace(mapping.AuthHost)
	if authRealm == "" {
		authRealm = strings.TrimPrefix(upstream, "https://")
		authRealm = strings.TrimPrefix(authRealm, "http://")
	}
	if !strings.HasPrefix(authRealm, "http://") && !strings.HasPrefix(authRealm, "https://") {
		authRealm = "https://" + authRealm
	}

	authService := strings.TrimPrefix(strings.TrimPrefix(upstream, "https://"), "http://")

	return registryTarget{
		Name:              name,
		Upstream:          upstream,
		AuthRealm:         authRealm,
		AuthService:       authService,
		AutoLibraryPrefix: false,
	}
}

func resolveRegistryTarget(c *gin.Context, pathWithoutV2 string) (registryTarget, string) {
	cfg := config.GetConfig()

	if ns := strings.TrimSpace(c.Query("ns")); ns != "" {
		if mapping, exists := cfg.Registries[ns]; exists && mapping.Enabled {
			return registryTargetFromMapping(ns, mapping), pathWithoutV2
		}
	}

	for domain, mapping := range cfg.Registries {
		if mapping.Enabled && strings.HasPrefix(pathWithoutV2, domain+"/") {
			return registryTargetFromMapping(domain, mapping), strings.TrimPrefix(pathWithoutV2, domain+"/")
		}
	}

	return defaultRegistryTarget(), pathWithoutV2
}

func resolveTokenTarget(c *gin.Context) (registryTarget, bool) {
	name := strings.Trim(strings.TrimSpace(c.Param("path")), "/")
	if name == "" {
		return defaultRegistryTarget(), true
	}

	if name == dockerHubName || name == "dockerhub" || name == "registry-1.docker.io" {
		return defaultRegistryTarget(), true
	}

	cfg := config.GetConfig()
	if mapping, exists := cfg.Registries[name]; exists && mapping.Enabled {
		return registryTargetFromMapping(name, mapping), true
	}

	return registryTarget{}, false
}

// 透明代理 Docker Registry API v2 请求。
func ProxyDockerRegistryGin(c *gin.Context) {
	path := c.Request.URL.Path

	if path == "/v2/" {
		target, _ := resolveRegistryTarget(c, "")
		proxyRegistryHTTP(c, target, "/v2/")
		return
	}

	if !strings.HasPrefix(path, "/v2/") {
		c.String(http.StatusNotFound, "Docker Registry API v2 only")
		return
	}

	handleRegistryRequest(c, path)
}

func handleRegistryRequest(c *gin.Context, path string) {
	pathWithoutV2 := strings.TrimPrefix(path, "/v2/")
	target, targetPath := resolveRegistryTarget(c, pathWithoutV2)

	imageName, apiType, _ := parseRegistryPath(targetPath)
	if imageName == "" || apiType == "" {
		c.String(http.StatusBadRequest, "Invalid path format")
		return
	}

	if target.AutoLibraryPrefix && !strings.Contains(imageName, "/") {
		imageName = "library/" + imageName
		targetPath = strings.TrimPrefix(targetPath, strings.TrimPrefix(imageName, "library/"))
		targetPath = imageName + targetPath
	}

	accessName := imageName
	if target.Name != dockerHubName {
		accessName = target.Name + "/" + imageName
	}
	if allowed, reason := utils.GlobalAccessController.CheckDockerAccess(accessName); !allowed {
		fmt.Printf("Docker image %s access denied: %s\n", accessName, reason)
		c.String(http.StatusForbidden, reason)
		return
	}

	proxyRegistryHTTP(c, target, "/v2/"+targetPath)
}

// 解析去掉 /v2/ 前缀后的 Registry 路径。
func parseRegistryPath(path string) (imageName, apiType, reference string) {
	if idx := strings.Index(path, "/manifests/"); idx != -1 {
		return path[:idx], "manifests", path[idx+len("/manifests/"):]
	}
	if idx := strings.Index(path, "/blobs/"); idx != -1 {
		return path[:idx], "blobs", path[idx+len("/blobs/"):]
	}
	if idx := strings.Index(path, "/tags/list"); idx != -1 {
		return path[:idx], "tags", "list"
	}
	return "", "", ""
}

// 代理 Docker token 请求，并透传客户端认证头。
func ProxyDockerAuthGin(c *gin.Context) {
	target, ok := resolveTokenTarget(c)
	if !ok {
		c.String(http.StatusBadRequest, "Unknown registry target")
		return
	}

	cacheable := c.GetHeader("Authorization") == "" && utils.IsTokenCacheEnabled() && c.Request.Method == http.MethodGet
	cacheKey := utils.BuildTokenCacheKey(target.Name + ":" + c.Request.URL.RawQuery)
	if cacheable {
		if cachedToken := utils.GlobalCache.GetToken(cacheKey); cachedToken != "" {
			utils.WriteTokenResponse(c, cachedToken)
			return
		}
	}

	authURL, err := buildAuthURL(target, c.Request.URL.RawQuery)
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to build auth request")
		return
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), c.Request.Method, authURL, nil)
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to create auth request")
		return
	}
	forwardSelectedRequestHeaders(req.Header, c.Request.Header)

	resp, err := utils.GetGlobalHTTPClient().Do(req)
	if err != nil {
		c.String(http.StatusBadGateway, "Auth request failed")
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		c.String(http.StatusBadGateway, "Failed to read auth response")
		return
	}

	copyResponseHeaders(c, resp.Header, target)
	if cacheable && resp.StatusCode == http.StatusOK && len(body) > 0 {
		utils.GlobalCache.SetToken(cacheKey, string(body), utils.ExtractTTLFromResponse(body))
	}

	c.Data(resp.StatusCode, resp.Header.Get("Content-Type"), body)
}

func buildAuthURL(target registryTarget, rawQuery string) (string, error) {
	authURL, err := url.Parse(target.AuthRealm)
	if err != nil {
		return "", err
	}

	query := authURL.Query()
	query.Set("service", target.AuthService)

	if rawQuery != "" {
		incoming, err := url.ParseQuery(rawQuery)
		if err != nil {
			return "", err
		}
		for key, values := range incoming {
			if strings.EqualFold(key, "service") {
				continue
			}
			for _, value := range values {
				if strings.EqualFold(key, "scope") && target.AutoLibraryPrefix {
					value = addLibraryPrefixToScope(value)
				}
				query.Add(key, value)
			}
		}
	}

	authURL.RawQuery = query.Encode()
	return authURL.String(), nil
}

func addLibraryPrefixToScope(scope string) string {
	parts := strings.Split(scope, ":")
	if len(parts) != 3 || parts[0] != "repository" || strings.Contains(parts[1], "/") {
		return scope
	}
	return "repository:library/" + parts[1] + ":" + parts[2]
}

func proxyRegistryHTTP(c *gin.Context, target registryTarget, upstreamPath string) {
	targetURL := target.Upstream + upstreamPath
	if c.Request.URL.RawQuery != "" {
		targetURL += "?" + c.Request.URL.RawQuery
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), c.Request.Method, targetURL, nil)
	if err != nil {
		c.String(http.StatusInternalServerError, "Failed to create registry request")
		return
	}
	forwardSelectedRequestHeaders(req.Header, c.Request.Header)

	resp, err := utils.GetGlobalHTTPClient().Do(req)
	if err != nil {
		c.String(http.StatusBadGateway, "Registry request failed")
		return
	}
	defer resp.Body.Close()

	copyResponseHeaders(c, resp.Header, target)
	c.Status(resp.StatusCode)

	if c.Request.Method == http.MethodHead {
		return
	}
	if _, err := io.Copy(c.Writer, resp.Body); err != nil {
		fmt.Printf("Failed to stream registry response: %v\n", err)
	}
}

func forwardSelectedRequestHeaders(dst http.Header, src http.Header) {
	for _, name := range forwardedRequestHeaders {
		for _, value := range src.Values(name) {
			dst.Add(name, value)
		}
	}
}

func copyResponseHeaders(c *gin.Context, headers http.Header, target registryTarget) {
	for name, values := range headers {
		if shouldSkipResponseHeader(name) {
			continue
		}

		for _, value := range values {
			if strings.EqualFold(name, "WWW-Authenticate") {
				value = rewriteAuthChallenge(value, target, publicBaseURL(c))
				c.Header("WWW-Authenticate", value)
			} else {
				c.Header(name, value)
			}
		}
	}
}

func shouldSkipResponseHeader(name string) bool {
	_, hopByHop := hopByHopHeaders[strings.ToLower(name)]
	return hopByHop
}

func publicBaseURL(c *gin.Context) string {
	proto := "http"
	if c.Request.TLS != nil {
		proto = "https"
	}
	if forwardedProto := strings.TrimSpace(c.GetHeader("X-Forwarded-Proto")); forwardedProto != "" {
		proto = strings.Split(forwardedProto, ",")[0]
	}

	host := c.Request.Host
	if host == "" {
		cfg := config.GetConfig()
		host = fmt.Sprintf("%s:%d", cfg.Server.Host, cfg.Server.Port)
		if cfg.Server.Host == "0.0.0.0" {
			host = fmt.Sprintf("localhost:%d", cfg.Server.Port)
		}
	}

	return strings.TrimRight(proto+"://"+host, "/")
}

func rewriteAuthChallenge(authHeader string, target registryTarget, baseURL string) string {
	if !strings.HasPrefix(strings.TrimSpace(strings.ToLower(authHeader)), "bearer ") {
		return authHeader
	}

	scope := bearerParam(authHeader, "scope")
	challenge := fmt.Sprintf(
		`Bearer realm="%s/token/%s",service="%s"`,
		baseURL,
		escapeAuthParam(target.Name),
		escapeAuthParam(target.AuthService),
	)
	if scope != "" {
		challenge += fmt.Sprintf(`,scope="%s"`, escapeAuthParam(scope))
	}
	return challenge
}

func bearerParam(authHeader, paramName string) string {
	input := strings.TrimSpace(authHeader)
	if len(input) < len("Bearer ") || !strings.EqualFold(input[:len("Bearer ")], "Bearer ") {
		return ""
	}
	input = strings.TrimSpace(input[len("Bearer "):])

	for input != "" {
		input = strings.TrimLeft(input, ", \t")
		key, rest, found := strings.Cut(input, "=")
		if !found {
			return ""
		}
		key = strings.TrimSpace(key)
		rest = strings.TrimSpace(rest)

		var value string
		if strings.HasPrefix(rest, `"`) {
			rest = rest[1:]
			var b strings.Builder
			escaped := false
			end := -1
			for i, r := range rest {
				if escaped {
					b.WriteRune(r)
					escaped = false
					continue
				}
				if r == '\\' {
					escaped = true
					continue
				}
				if r == '"' {
					end = i + 1
					break
				}
				b.WriteRune(r)
			}
			if end == -1 {
				return ""
			}
			value = b.String()
			input = rest[end:]
		} else {
			value, input, _ = strings.Cut(rest, ",")
			value = strings.TrimSpace(value)
		}

		if strings.EqualFold(key, paramName) {
			return value
		}
	}

	return ""
}

func escapeAuthParam(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	return strings.ReplaceAll(value, `"`, `\"`)
}
