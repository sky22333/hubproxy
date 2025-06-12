package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/spf13/viper"
	"github.com/fsnotify/fsnotify"
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

	// Token缓存配置
	TokenCache struct {
		Enabled    bool   `toml:"enabled"`    // 是否启用token缓存
		DefaultTTL string `toml:"defaultTTL"` // 默认缓存时间
	} `toml:"tokenCache"`
}

var (
	appConfig     *AppConfig
	appConfigLock sync.RWMutex
	isViperEnabled bool
	viperInstance  *viper.Viper
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
			"registry.k8s.io": {
				Upstream: "registry.k8s.io",
				AuthHost: "registry.k8s.io",
				AuthType: "anonymous",
				Enabled:  true,
			},
		},
		TokenCache: struct {
			Enabled    bool   `toml:"enabled"`
			DefaultTTL string `toml:"defaultTTL"`
		}{
			Enabled:    true, // docker认证的匿名Token缓存配置，用于提升性能
			DefaultTTL: "20m",
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
	
	// 🔥 首次加载后启用Viper热重载
	if !isViperEnabled {
		go enableViperHotReload()
	}
	
	fmt.Printf("配置加载成功: 监听 %s:%d, 文件大小限制 %d MB, 限流 %d请求/%g小时, 离线镜像并发数 %d\n",
		cfg.Server.Host, cfg.Server.Port, cfg.Server.FileSize/(1024*1024), 
		cfg.RateLimit.RequestLimit, cfg.RateLimit.PeriodHours, cfg.Download.MaxImages)
	
	return nil
}

// enableViperHotReload enables hot reloading of the configuration file using Viper.
// If hot reload is already enabled, the function returns immediately.
// On detecting changes to the configuration file, it triggers a reload of the application configuration.
func enableViperHotReload() {
	if isViperEnabled {
		return
	}
	
	// 创建Viper实例
	viperInstance = viper.New()
	
	// 配置Viper
	viperInstance.SetConfigName("config")
	viperInstance.SetConfigType("toml")
	viperInstance.AddConfigPath(".")
	
	// 读取配置文件
	if err := viperInstance.ReadInConfig(); err != nil {
		fmt.Printf("读取配置失败，继续使用当前配置: %v\n", err)
		return
	}
	
	isViperEnabled = true
	fmt.Println("热重载已启用")
	
	// 🚀 启用文件监听
	viperInstance.WatchConfig()
	viperInstance.OnConfigChange(func(e fsnotify.Event) {
		fmt.Printf("检测到配置文件变化: %s\n", e.Name)
		hotReloadWithViper()
	})
}

// 🔥 使用Viper进行热重载
func hotReloadWithViper() {
	start := time.Now()
	fmt.Println("🔄 自动热重载...")
	
	// 创建新配置
	cfg := DefaultConfig()
	
	// 使用Viper解析配置到结构体
	if err := viperInstance.Unmarshal(cfg); err != nil {
		fmt.Printf("❌ 配置解析失败: %v\n", err)
		return
	}
	
	// 从环境变量覆盖（保持原有功能）
	overrideFromEnv(cfg)
	
	// 原子性更新配置
	setConfig(cfg)
	
	// 异步更新受影响的组件
	go func() {
		updateAffectedComponents()
		fmt.Printf("✅ Viper配置热重载完成，耗时: %v\n", time.Since(start))
	}()
}

// 🔧 更新受配置影响的组件
func updateAffectedComponents() {
	// 重新初始化限流器
	if globalLimiter != nil {
		fmt.Println("📡 重新初始化限流器...")
		initLimiter()
	}
	
	// 重新加载访问控制
	fmt.Println("🔒 重新加载访问控制规则...")
	if GlobalAccessController != nil {
		GlobalAccessController.Reload()
	}
	
	// 🔥 刷新Registry配置映射
	fmt.Println("🌐 更新Registry配置映射...")
	reloadRegistryConfig()
	
	// 其他需要重新初始化的组件可以在这里添加
	fmt.Println("🔧 组件更新完成")
}

// 🔥 重新加载Registry配置
func reloadRegistryConfig() {
	cfg := GetConfig()
	enabledCount := 0
	
	// 统计启用的Registry数量
	for _, mapping := range cfg.Registries {
		if mapping.Enabled {
			enabledCount++
		}
	}
	
	fmt.Printf("🌐 Registry配置已更新: %d个启用\n", enabledCount)
	
	// Registry配置是动态读取的，每次请求都会调用GetConfig()
	// 所以这里只需要简单通知，实际生效是自动的
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