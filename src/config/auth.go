package config

// AuthConfig 认证配置
type AuthConfig struct {
	Enabled          bool   `toml:"enabled"`
	Username         string `toml:"username"`
	Password         string `toml:"password"`
	JWTSecret        string `toml:"jwtSecret"`
	TokenExpireHours int    `toml:"tokenExpireHours"`
}

// DefaultAuthConfig 返回默认认证配置
func DefaultAuthConfig() AuthConfig {
	return AuthConfig{
		Enabled:          false,
		Username:         "admin",
		Password:         "change-me-please",
		JWTSecret:        "CHANGE-THIS-TO-A-RANDOM-SECRET-KEY",
		TokenExpireHours: 24,
	}
}
