package main

import (
	"net"
	"net/http"
	"time"
)

var (
	// 全局HTTP客户端 - 用于代理请求（长超时）
	globalHTTPClient *http.Client
	// 搜索HTTP客户端 - 用于API请求（短超时） 
	searchHTTPClient *http.Client
)

// initHTTPClients 初始化HTTP客户端
func initHTTPClients() {
	// 代理客户端配置 - 适用于大文件传输
	globalHTTPClient = &http.Client{
		Transport: &http.Transport{
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
	
	// 搜索客户端配置 - 适用于API调用
	searchHTTPClient = &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
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

// GetGlobalHTTPClient 获取全局HTTP客户端（用于代理）
func GetGlobalHTTPClient() *http.Client {
	return globalHTTPClient
}

// GetSearchHTTPClient 获取搜索HTTP客户端（用于API调用）
func GetSearchHTTPClient() *http.Client {
	return searchHTTPClient
} 