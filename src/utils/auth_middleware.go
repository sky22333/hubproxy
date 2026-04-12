package utils

import (
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"hubproxy/config"
)

var globalJWTManager *JWTManager

// InitJWTManager 初始化全局JWT管理器
func InitJWTManager() {
	cfg := config.GetConfig()
	globalJWTManager = NewJWTManager(cfg.Auth.JWTSecret, cfg.Auth.TokenExpireHours)

	// 警告: 使用默认JWT密钥
	if cfg.Auth.Enabled && cfg.Auth.JWTSecret == "CHANGE-THIS-TO-A-RANDOM-SECRET-KEY" {
		log.Println("⚠️  警告: 正在使用默认的JWT密钥，请在生产环境中更改！")
	}
}

// GetJWTManager 获取全局JWT管理器
func GetJWTManager() *JWTManager {
	return globalJWTManager
}

// AuthMiddleware 认证中间件
func AuthMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg := config.GetConfig()

		// 如果认证未启用，直接放行
		if !cfg.Auth.Enabled {
			c.Next()
			return
		}

		path := c.Request.URL.Path

		// 白名单路径，无需认证
		whitelistPaths := []string{"/login", "/api/login", "/api/logout", "/ready"}
		for _, wp := range whitelistPaths {
			if path == wp {
				c.Next()
				return
			}
		}

		// /token 端点由 Docker token handler 自行处理认证（Basic Auth）
		if path == "/token" || strings.HasPrefix(path, "/token/") {
			c.Next()
			return
		}

		// 尝试从Cookie获取token
		token, err := c.Cookie("token")
		if err != nil || token == "" {
			// 尝试从Authorization header获取token
			authHeader := c.GetHeader("Authorization")
			if authHeader != "" && strings.HasPrefix(authHeader, "Bearer ") {
				token = strings.TrimPrefix(authHeader, "Bearer ")
			}
		}

		// 如果没有token
		if token == "" {
			// Web请求重定向到登录页
			accept := c.GetHeader("Accept")
			if accept != "" && strings.Contains(accept, "text/html") {
				c.Redirect(http.StatusFound, "/login")
				c.Abort()
				return
			}
			// API/Docker请求返回401
			c.Header("WWW-Authenticate", `Bearer realm="`+c.Request.Host+`/token",service="registry"`)
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "未授权: 需要登录",
			})
			c.Abort()
			return
		}

		// 验证token
		username, err := globalJWTManager.VerifyToken(token)
		if err != nil {
			errorMsg := "无效的认证凭证"
			if strings.Contains(err.Error(), "expired") {
				errorMsg = "Token已过期，请重新登录"
			}

			// Web请求重定向到登录页
			accept := c.GetHeader("Accept")
			if accept != "" && strings.Contains(accept, "text/html") {
				c.Redirect(http.StatusFound, "/login")
				c.Abort()
				return
			}

			// API/Docker请求返回401
			c.Header("WWW-Authenticate", `Bearer realm="`+c.Request.Host+`/token",service="registry"`)
			c.JSON(http.StatusUnauthorized, gin.H{
				"error": errorMsg,
			})
			c.Abort()
			return
		}

		// 将用户名存入上下文
		c.Set("username", username)
		c.Next()
	}
}
