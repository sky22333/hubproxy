package main

import (
	"context"
	"log"
	"net"
	"net/http"
	"net/url"
	"time"

	"golang.org/x/net/proxy"
)

var (
	// 全局HTTP客户端 - 用于代理请求（长超时）
	globalHTTPClient *http.Client
	// 搜索HTTP客户端 - 用于API请求（短超时） 
	searchHTTPClient *http.Client
)

// initHTTPClients 初始化HTTP客户端
func initHTTPClients() {
	cfg := GetConfig()
	
	// 创建DialContext函数，支持SOCKS5代理
	createDialContext := func(timeout time.Duration) func(ctx context.Context, network, addr string) (net.Conn, error) {
		if cfg.Proxy.Socks5 == "" {
			// 没有配置代理，使用直连
			dialer := &net.Dialer{
				Timeout:   timeout,
				KeepAlive: 30 * time.Second,
			}
			return dialer.DialContext
		}
		
		// 解析SOCKS5代理URL
		proxyURL, err := url.Parse(cfg.Proxy.Socks5)
		if err != nil {
			log.Printf("SOCKS5代理配置错误，使用直连: %v", err)
			dialer := &net.Dialer{
				Timeout:   timeout,
				KeepAlive: 30 * time.Second,
			}
			return dialer.DialContext
		}
		
		// 创建基础dialer
		baseDialer := &net.Dialer{
			Timeout:   timeout,
			KeepAlive: 30 * time.Second,
		}
		
		// 创建SOCKS5代理dialer
		var auth *proxy.Auth
		if proxyURL.User != nil {
			if password, ok := proxyURL.User.Password(); ok {
				auth = &proxy.Auth{
					User:     proxyURL.User.Username(),
					Password: password,
				}
			}
		}
		
		socks5Dialer, err := proxy.SOCKS5("tcp", proxyURL.Host, auth, baseDialer)
		if err != nil {
			log.Printf("创建SOCKS5代理失败，使用直连: %v", err)
			return baseDialer.DialContext
		}
		
		log.Printf("使用SOCKS5代理: %s", proxyURL.Host)
		
		// 返回带上下文的dial函数
		return func(ctx context.Context, network, addr string) (net.Conn, error) {
			return socks5Dialer.Dial(network, addr)
		}
	}
	
	// 代理客户端配置 - 适用于大文件传输
	globalHTTPClient = &http.Client{
		Transport: &http.Transport{
			DialContext:           createDialContext(30 * time.Second),
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
			DialContext:         createDialContext(5 * time.Second),
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