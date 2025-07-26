package utils

import (
	"net"
	"net/http"
	"os"
	"time"

	"hubproxy/config"
)

var (
	globalHTTPClient *http.Client
	searchHTTPClient *http.Client
)

// InitHTTPClients 初始化HTTP客户端
func InitHTTPClients() {
	cfg := config.GetConfig()

	if p := cfg.Access.Proxy; p != "" {
		os.Setenv("HTTP_PROXY", p)
		os.Setenv("HTTPS_PROXY", p)
	}

	globalHTTPClient = &http.Client{
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   30 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:          1000,
			MaxIdleConnsPerHost:   1000,
			IdleConnTimeout:       90 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			ResponseHeaderTimeout: 300 * time.Second,
		},
	}

	searchHTTPClient = &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   5 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
			TLSHandshakeTimeout: 5 * time.Second,
			DisableCompression:  false,
		},
	}
}

// GetGlobalHTTPClient 获取全局HTTP客户端
func GetGlobalHTTPClient() *http.Client {
	return globalHTTPClient
}

// GetSearchHTTPClient 获取搜索HTTP客户端
func GetSearchHTTPClient() *http.Client {
	return searchHTTPClient
}