package handlers

import (
	"log"
	"net/http"

	"github.com/gin-gonic/gin"
	"hubproxy/config"
	"hubproxy/utils"
)

// LoginPageHandler 登录页面处理器
func LoginPageHandler(c *gin.Context) {
	cfg := config.GetConfig()

	// 如果认证未启用，重定向到首页
	if !cfg.Auth.Enabled {
		c.Redirect(http.StatusFound, "/")
		return
	}

	// 如果已经登录，重定向到首页
	token, err := c.Cookie("token")
	if err == nil && token != "" {
		jm := utils.GetJWTManager()
		if jm != nil {
			if _, verifyErr := jm.VerifyToken(token); verifyErr == nil {
				c.Redirect(http.StatusFound, "/")
				return
			}
		}
	}

	// 返回登录页面HTML
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.String(http.StatusOK, loginPageHTML)
}

// LoginRequest 登录请求
type LoginRequest struct {
	Username string `json:"username" binding:"required"`
	Password string `json:"password" binding:"required"`
}

// LoginAPIHandler 登录API处理器
func LoginAPIHandler(c *gin.Context) {
	cfg := config.GetConfig()

	// 如果认证未启用
	if !cfg.Auth.Enabled {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "认证功能未启用",
		})
		return
	}

	var req LoginRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "请求参数错误",
		})
		return
	}

	// 验证用户名和密码
	if req.Username != cfg.Auth.Username || req.Password != cfg.Auth.Password {
		log.Printf("登录失败: 用户 %s 认证失败", req.Username)
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "用户名或密码错误",
		})
		return
	}

	// 生成JWT token
	jm := utils.GetJWTManager()
	token, err := jm.SignToken(req.Username)
	if err != nil {
		log.Printf("Token生成失败: %v", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Token生成失败",
		})
		return
	}

	// 检测是否通过HTTPS访问（Nginx反代）
	secure := c.GetHeader("X-Forwarded-Proto") == "https"

	maxAge := cfg.Auth.TokenExpireHours * 3600

	// 设置HttpOnly cookie
	c.SetCookie("token", token, maxAge, "/", "", secure, true)

	// 追加SameSite=Strict（Gin的SetCookie不支持，手动追加）
	existingCookie := c.Writer.Header().Get("Set-Cookie")
	if existingCookie != "" {
		c.Writer.Header().Set("Set-Cookie", existingCookie+"; SameSite=Strict")
	}

	log.Printf("用户 %s 登录成功", req.Username)
	c.JSON(http.StatusOK, gin.H{
		"message": "登录成功",
	})
}

// LogoutAPIHandler 登出API处理器
func LogoutAPIHandler(c *gin.Context) {
	// 清除cookie（MaxAge=-1 立即过期）
	c.SetCookie("token", "", -1, "/", "", false, true)

	c.JSON(http.StatusOK, gin.H{
		"message": "登出成功",
	})
}

// loginPageHTML 登录页面HTML（内嵌，无需额外文件）
const loginPageHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
    <meta charset="UTF-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>HubProxy - 登录</title>
    <style>
        * { margin: 0; padding: 0; box-sizing: border-box; }
        body {
            font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, "Helvetica Neue", Arial, sans-serif;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            min-height: 100vh;
            display: flex;
            align-items: center;
            justify-content: center;
        }
        .login-container {
            background: white;
            padding: 40px;
            border-radius: 10px;
            box-shadow: 0 10px 40px rgba(0,0,0,0.2);
            width: 100%;
            max-width: 400px;
        }
        .logo { text-align: center; margin-bottom: 30px; }
        .logo h1 { color: #667eea; font-size: 32px; margin-bottom: 5px; }
        .logo p { color: #666; font-size: 14px; }
        .form-group { margin-bottom: 20px; }
        label { display: block; margin-bottom: 8px; color: #333; font-weight: 500; }
        input[type="text"], input[type="password"] {
            width: 100%;
            padding: 12px;
            border: 2px solid #e1e8ed;
            border-radius: 6px;
            font-size: 14px;
            transition: border-color 0.3s;
        }
        input[type="text"]:focus, input[type="password"]:focus {
            outline: none;
            border-color: #667eea;
        }
        .btn-login {
            width: 100%;
            padding: 12px;
            background: linear-gradient(135deg, #667eea 0%, #764ba2 100%);
            color: white;
            border: none;
            border-radius: 6px;
            font-size: 16px;
            font-weight: 600;
            cursor: pointer;
            transition: opacity 0.2s;
        }
        .btn-login:hover { opacity: 0.9; }
        .btn-login:disabled { opacity: 0.6; cursor: not-allowed; }
        .error-msg {
            display: none;
            background: #fee;
            color: #c33;
            padding: 12px;
            border-radius: 6px;
            margin-bottom: 20px;
            font-size: 14px;
            border-left: 4px solid #c33;
        }
        .error-msg.show { display: block; }
    </style>
</head>
<body>
    <div class="login-container">
        <div class="logo">
            <h1>🐳 HubProxy</h1>
            <p>Docker &amp; GitHub 加速代理</p>
        </div>
        <div id="error" class="error-msg"></div>
        <form id="loginForm">
            <div class="form-group">
                <label for="username">用户名</label>
                <input type="text" id="username" name="username" required autocomplete="username" placeholder="请输入用户名">
            </div>
            <div class="form-group">
                <label for="password">密码</label>
                <input type="password" id="password" name="password" required autocomplete="current-password" placeholder="请输入密码">
            </div>
            <button type="submit" class="btn-login" id="submitBtn">登录</button>
        </form>
    </div>
    <script>
        document.getElementById('loginForm').addEventListener('submit', async (e) => {
            e.preventDefault();
            const username = document.getElementById('username').value.trim();
            const password = document.getElementById('password').value;
            const errorDiv = document.getElementById('error');
            const submitBtn = document.getElementById('submitBtn');

            errorDiv.classList.remove('show');
            submitBtn.disabled = true;
            submitBtn.textContent = '登录中...';

            try {
                const response = await fetch('/api/login', {
                    method: 'POST',
                    headers: { 'Content-Type': 'application/json' },
                    body: JSON.stringify({ username, password }),
                });
                const data = await response.json();
                if (response.ok) {
                    // 登录成功，跳转首页
                    const redirect = new URLSearchParams(window.location.search).get('redirect') || '/';
                    window.location.href = redirect;
                } else {
                    errorDiv.textContent = data.error || '登录失败，请重试';
                    errorDiv.classList.add('show');
                }
            } catch (err) {
                errorDiv.textContent = '网络错误，请重试';
                errorDiv.classList.add('show');
            } finally {
                submitBtn.disabled = false;
                submitBtn.textContent = '登录';
            }
        });
    </script>
</body>
</html>`
