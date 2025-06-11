package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/pelletier/go-toml/v2"
)

// RegistryMapping Registry映射配置
type RegistryMapping struct {
	Upstream string `toml:"upstream"` // 上游Registry地址
	AuthHost string `toml:"authHost"` // 认证服务器地址
	AuthType string `toml:"authType"` // 认证类型: docker/github/google/basic
	Enabled  bool   `toml:"enabled"`  // 是否启用
}

// AppConfig 应用配置结构体
type AppConfig struct {
	Server struct {
		Host     string `toml:"host"`     // 监听地址
		Port     int    `toml:"port"`     // 监听端口
		FileSize int64  `toml:"fileSize"` // 文件大小限制（字节）
	} `toml:"server"`

	RateLimit struct {
		RequestLimit int     `toml:"requestLimit"` // 每小时请求限制
		PeriodHours  float64 `toml:"periodHours"`  // 限制周期（小时）
	} `toml:"rateLimit"`

	Security struct {
		WhiteList []string `toml:"whiteList"` // 白名单IP/CIDR列表
		BlackList []string `toml:"blackList"` // 黑名单IP/CIDR列表
	} `toml:"security"`

	Proxy struct {
		WhiteList []string `toml:"whiteList"` // 代理白名单（仓库级别）
		BlackList []string `toml:"blackList"` // 代理黑名单（仓库级别）
	} `toml:"proxy"`

	Download struct {
		MaxImages int `toml:"maxImages"` // 单次下载最大镜像数量限制
	} `toml:"download"`

	// 新增：Registry映射配置
	Registries map[string]RegistryMapping `toml:"registries"`
}

var (
	appConfig     *AppConfig
	appConfigLock sync.RWMutex
)

// DefaultConfig 返回默认配置
func DefaultConfig() *AppConfig {
	return &AppConfig{
		Server: struct {
			Host     string `toml:"host"`
			Port     int    `toml:"port"`
			FileSize int64  `toml:"fileSize"`
		}{
			Host:     "0.0.0.0",
			Port:     5000,
			FileSize: 2 * 1024 * 1024 * 1024, // 2GB
		},
		RateLimit: struct {
			RequestLimit int     `toml:"requestLimit"`
			PeriodHours  float64 `toml:"periodHours"`
		}{
			RequestLimit: 20,
			PeriodHours:  1.0,
		},
		Security: struct {
			WhiteList []string `toml:"whiteList"`
			BlackList []string `toml:"blackList"`
		}{
			WhiteList: []string{},
			BlackList: []string{},
		},
		Proxy: struct {
			WhiteList []string `toml:"whiteList"`
			BlackList []string `toml:"blackList"`
		}{
			WhiteList: []string{},
			BlackList: []string{},
		},
		Download: struct {
			MaxImages int `toml:"maxImages"`
		}{
			MaxImages: 10, // 默认值：最多同时下载10个镜像
		},
		Registries: map[string]RegistryMapping{
			"ghcr.io": {
				Upstream: "ghcr.io",
				AuthHost: "ghcr.io/token",
				AuthType: "github",
				Enabled:  true,
			},
			"gcr.io": {
				Upstream: "gcr.io",
				AuthHost: "gcr.io/v2/token",
				AuthType: "google",
				Enabled:  true,
			},
			"quay.io": {
				Upstream: "quay.io",
				AuthHost: "quay.io/v2/auth",
				AuthType: "quay",
				Enabled:  true,
			},
		},
	}
}

// GetConfig 安全地获取配置副本
func GetConfig() *AppConfig {
	appConfigLock.RLock()
	defer appConfigLock.RUnlock()
	
	if appConfig == nil {
		return DefaultConfig()
	}
	
	// 返回配置的深拷贝
	configCopy := *appConfig
	configCopy.Security.WhiteList = append([]string(nil), appConfig.Security.WhiteList...)
	configCopy.Security.BlackList = append([]string(nil), appConfig.Security.BlackList...)
	configCopy.Proxy.WhiteList = append([]string(nil), appConfig.Proxy.WhiteList...)
	configCopy.Proxy.BlackList = append([]string(nil), appConfig.Proxy.BlackList...)
	
	return &configCopy
}

// setConfig 安全地设置配置
func setConfig(cfg *AppConfig) {
	appConfigLock.Lock()
	defer appConfigLock.Unlock()
	appConfig = cfg
}

// LoadConfig 加载配置文件
func LoadConfig() error {
	// 首先使用默认配置
	cfg := DefaultConfig()
	
	// 尝试加载TOML配置文件
	if data, err := os.ReadFile("config.toml"); err == nil {
		if err := toml.Unmarshal(data, cfg); err != nil {
			return fmt.Errorf("解析配置文件失败: %v", err)
		}
	} else {
		fmt.Println("未找到config.toml，使用默认配置")
	}
	
	// 从环境变量覆盖配置
	overrideFromEnv(cfg)
	
	// 设置配置
	setConfig(cfg)
	
	fmt.Printf("配置加载成功: 监听 %s:%d, 文件大小限制 %d MB, 限流 %d请求/%g小时, 离线镜像并发数 %d\n",
		cfg.Server.Host, cfg.Server.Port, cfg.Server.FileSize/(1024*1024), 
		cfg.RateLimit.RequestLimit, cfg.RateLimit.PeriodHours, cfg.Download.MaxImages)
	
	return nil
}

// overrideFromEnv 从环境变量覆盖配置
func overrideFromEnv(cfg *AppConfig) {
	// 服务器配置
	if val := os.Getenv("SERVER_HOST"); val != "" {
		cfg.Server.Host = val
	}
	if val := os.Getenv("SERVER_PORT"); val != "" {
		if port, err := strconv.Atoi(val); err == nil && port > 0 {
			cfg.Server.Port = port
		}
	}
	if val := os.Getenv("MAX_FILE_SIZE"); val != "" {
		if size, err := strconv.ParseInt(val, 10, 64); err == nil && size > 0 {
			cfg.Server.FileSize = size
		}
	}
	
	// 限流配置
	if val := os.Getenv("RATE_LIMIT"); val != "" {
		if limit, err := strconv.Atoi(val); err == nil && limit > 0 {
			cfg.RateLimit.RequestLimit = limit
		}
	}
	if val := os.Getenv("RATE_PERIOD_HOURS"); val != "" {
		if period, err := strconv.ParseFloat(val, 64); err == nil && period > 0 {
			cfg.RateLimit.PeriodHours = period
		}
	}
	
	// IP限制配置
	if val := os.Getenv("IP_WHITELIST"); val != "" {
		cfg.Security.WhiteList = append(cfg.Security.WhiteList, strings.Split(val, ",")...)
	}
	if val := os.Getenv("IP_BLACKLIST"); val != "" {
		cfg.Security.BlackList = append(cfg.Security.BlackList, strings.Split(val, ",")...)
	}
	
	// 下载限制配置
	if val := os.Getenv("MAX_IMAGES"); val != "" {
		if maxImages, err := strconv.Atoi(val); err == nil && maxImages > 0 {
			cfg.Download.MaxImages = maxImages
		}
	}
}

// CreateDefaultConfigFile 创建默认配置文件
func CreateDefaultConfigFile() error {
	cfg := DefaultConfig()
	
	data, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("序列化默认配置失败: %v", err)
	}
	
	return os.WriteFile("config.toml", data, 0644)
} 