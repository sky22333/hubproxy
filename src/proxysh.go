package main

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// GitHub URL正则表达式
var githubRegex = regexp.MustCompile(`https?://(?:github\.com|raw\.githubusercontent\.com|raw\.github\.com|gist\.githubusercontent\.com|gist\.github\.com|api\.github\.com)[^\s'"]+`)

// ProcessSmart Shell脚本智能处理函数
func ProcessSmart(input io.ReadCloser, isCompressed bool, host string) (io.Reader, int64, error) {
	defer input.Close()
	
	// 读取Shell脚本内容
	content, err := readShellContent(input, isCompressed)
	if err != nil {
		return nil, 0, fmt.Errorf("内容读取失败: %v", err)
	}
	
	if len(content) == 0 {
		return strings.NewReader(""), 0, nil
	}
	
	// Shell脚本大小检查 (10MB限制)
	if len(content) > 10*1024*1024 {
		return strings.NewReader(content), int64(len(content)), nil
	}
	
	// 快速检查是否包含GitHub URL
	if !strings.Contains(content, "github.com") && !strings.Contains(content, "githubusercontent.com") {
		return strings.NewReader(content), int64(len(content)), nil
	}
	
	// 执行GitHub URL替换
	processed := processGitHubURLs(content, host)
	
	return strings.NewReader(processed), int64(len(processed)), nil
}

// readShellContent 读取Shell脚本内容
func readShellContent(input io.ReadCloser, isCompressed bool) (string, error) {
	var reader io.Reader = input
	
	// 处理gzip压缩
	if isCompressed {
		// 读取前2字节检查gzip魔数
		peek := make([]byte, 2)
		n, err := input.Read(peek)
		if err != nil && err != io.EOF {
			return "", fmt.Errorf("读取数据失败: %v", err)
		}
		
		// 检查gzip魔数 (0x1f, 0x8b)
		if n >= 2 && peek[0] == 0x1f && peek[1] == 0x8b {
			// 合并peek数据和剩余流
			combinedReader := io.MultiReader(bytes.NewReader(peek[:n]), input)
			gzReader, err := gzip.NewReader(combinedReader)
			if err != nil {
				return "", fmt.Errorf("gzip解压失败: %v", err)
			}
			defer gzReader.Close()
			reader = gzReader
		} else {
			// 不是gzip格式，合并peek数据
			reader = io.MultiReader(bytes.NewReader(peek[:n]), input)
		}
	}
	
	// 读取全部内容
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", fmt.Errorf("读取内容失败: %v", err)
	}
	
	return string(data), nil
}

// processGitHubURLs 处理GitHub URL替换
func processGitHubURLs(content, host string) string {
	return githubRegex.ReplaceAllStringFunc(content, func(url string) string {
		return transformURL(url, host)
	})
}

// transformURL URL转换函数
func transformURL(url, host string) string {
	// 避免重复处理
	if strings.Contains(url, host) {
		return url
	}
	
	// 协议标准化为https
	if strings.HasPrefix(url, "http://") {
		url = "https" + url[4:]
	} else if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "//") {
		url = "https://" + url
	}
	
	// 清理host格式
	cleanHost := strings.TrimPrefix(host, "https://")
	cleanHost = strings.TrimPrefix(cleanHost, "http://")
	cleanHost = strings.TrimSuffix(cleanHost, "/")
	
	return cleanHost + "/" + url
}