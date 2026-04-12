# HubProxy Copilot Instructions

HubProxy is a lightweight, high-performance Go-based proxy service for Docker images, GitHub files, and AI models. It provides acceleration, rate limiting, and access control features.

## Build & Test Commands

### Build
```bash
cd src && go build -o hubproxy .
```

### Build with UPX compression
```bash
cd src && go build -ldflags="-s -w" -o hubproxy . && upx -9 hubproxy
```

### Run locally
```bash
cd src && go run .
```

### Build Docker image
```bash
docker build -t hubproxy:latest .
```

### Run with Docker
```bash
docker run -d --name hubproxy -p 5000:5000 hubproxy:latest
```

### Test the service
```bash
curl http://localhost:5000/ready
```

## Architecture Overview

### Project Structure
- **`src/main.go`** - Entry point; initializes router, middleware (rate limiting, recovery), and HTTP/2 support
- **`src/handlers/`** - Request handlers for different proxy services:
  - `docker.go` - Docker Registry v2 API proxy (token auth, layer streaming)
  - `github.go` - GitHub file proxy (releases, raw files, API)
  - `imagetar.go` - Offline image tar download with streaming
  - `search.go` - Docker Hub image search
- **`src/utils/`** - Shared utilities:
  - `ratelimiter.go` - IP-based rate limiting with configurable periods
  - `access_control.go` - Whitelist/blacklist support for Docker images and GitHub repos (pattern matching)
  - `cache.go` - Token and manifest caching with TTL
  - `http_client.go` - Shared HTTP client initialization
  - `proxy_shell.go` - Shell script proxy helpers
- **`src/config/`** - Configuration loading from `config.toml` and environment variables
- **`src/public/`** - Static frontend (embedded in binary with `//go:embed`)

### Request Flow
1. **Router** (Gin framework) - Routes requests based on path
2. **Rate Limit Middleware** - Checks IP against rate limiter (configured per period)
3. **Request Handler** - Routes to appropriate handler:
   - `/v2/*` → Docker Registry proxy
   - `/token*` → Docker auth proxy
   - Static routes → Frontend (if enabled)
   - Everything else → GitHub proxy (NoRoute)
4. **Streaming Response** - Large files streamed directly; small files buffered

### Key Design Patterns

**Configuration Management**
- Config loaded at startup from `config.toml` and environment variables
- Env vars override TOML values
- Access via `config.GetConfig()`

**Rate Limiting**
- IP-based with configurable request limit and period (hours)
- Whitelist support (IPs not rate-limited)
- Stored in memory; reset on period expiry

**Caching**
- Token cache: Docker auth tokens valid for ~20 mins (configurable TTL)
- Manifest cache: Image manifests cached to reduce registry calls
- Redis-ready (optional enhancement)

**Streaming Architecture**
- Docker layer pulls streamed directly to client (no buffering)
- Offline image tar downloads support range requests
- Debouncer prevents duplicate concurrent downloads of same image

**Access Control**
- Supports whitelist (allow-only) and blacklist (deny) modes
- Pattern matching: `user/*`, `*/repo`, exact matches
- Applies to both Docker images and GitHub repos

## Key Conventions

### Naming
- Handler functions: `Proxy<Service>Handler` or `Proxy<Service>Gin`
- Middleware: `<Feature>Middleware`
- Initialization: `Init<Component>()`

### Error Handling
- Log errors with context using `log.Printf`
- Return JSON error responses: `{"error": "message", "code": "ERROR_CODE"}`
- Panic recovery middleware at router level catches unhandled panics

### Embedded Assets
- Static files in `src/public/` embedded at compile time using `//go:embed`
- Served as JSON responses with appropriate content types

### HTTP Timeouts
- Read: 60 seconds
- Write: 30 minutes (large downloads)
- Idle: 120 seconds

### Docker Support
- HTTP/2 with h2c can be enabled via `ENABLE_H2C=true`
- Supports parallel streams: max 250 concurrent
- Binary uses UPX compression for smaller image size

**Authentication System**
- JWT-based (HS256) for both web (HttpOnly cookie) and Docker CLI (Bearer token)
- Auth middleware at `utils.AuthMiddleware()`, runs after rate limiter
- Whitelist: `/login`, `/api/login`, `/api/logout`, `/token`, `/token/*` bypass middleware
- `/token` handles its own Basic Auth verification in `handlers.ProxyDockerAuthGin`
- Configuration: `config.Auth` struct, override with `ENABLE_AUTH`, `AUTH_USERNAME`, `AUTH_PASSWORD`, `AUTH_JWT_SECRET`, `AUTH_TOKEN_EXPIRE_HOURS`
- `utils.InitJWTManager()` must be called at startup; `utils.GetJWTManager()` returns the global instance
- Login handler at `handlers.LoginPageHandler` (GET /login), `handlers.LoginAPIHandler` (POST /api/login), `handlers.LogoutAPIHandler` (POST /api/logout)
- Web flow: unauthenticated HTML request → 302 redirect to /login; unauthenticated API/Docker request → 401 JSON
- Docker flow: GET /v2/ → 401 + WWW-Authenticate → GET /token with Basic Auth → JWT → Bearer token in subsequent requests

## Testing the Proxy

### Docker acceleration (auth disabled)
```bash
docker pull yourdomain.com:5000/nginx
```

### Docker acceleration (auth enabled)
```bash
docker login yourdomain.com:5000
docker pull yourdomain.com:5000/nginx
```

### GitHub file acceleration
```bash
curl "http://localhost:5000/https://github.com/user/repo/releases/download/v1.0.0/file.tar.gz"
```

### Health check
```bash
curl http://localhost:5000/ready
```

### Image search
```bash
curl "http://localhost:5000/search?name=nginx&limit=10"
```
