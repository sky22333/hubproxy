package utils

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"regexp"
	"strings"
)

// GitHub URL正则表达式
var githubRegex = regexp.MustCompile(`(?:^|[\s'"(=,\[{;|&<>])https?://(?:github\.com|raw\.githubusercontent\.com|raw\.github\.com|gist\.githubusercontent\.com|gist\.github\.com|api\.github\.com)[^\s'")]*`)

// ProcessSmart Shell脚本智能处理函数
func ProcessSmart(input io.ReadCloser, isCompressed bool, host string) (io.Reader, int64, error) {
	defer input.Close()

	content, err := readShellContent(input, isCompressed)
	if err != nil {
		return nil, 0, fmt.Errorf("内容读取失败: %v", err)
	}

	if len(content) == 0 {
		return strings.NewReader(""), 0, nil
	}

	if len(content) > 10*1024*1024 {
		return strings.NewReader(content), int64(len(content)), nil
	}

	if !strings.Contains(content, "github.com") && !strings.Contains(content, "githubusercontent.com") {
		return strings.NewReader(content), int64(len(content)), nil
	}

	processed := processGitHubURLs(content, host)

	return strings.NewReader(processed), int64(len(processed)), nil
}

func readShellContent(input io.ReadCloser, isCompressed bool) (string, error) {
	var reader io.Reader = input

	if isCompressed {
		peek := make([]byte, 2)
		n, err := input.Read(peek)
		if err != nil && err != io.EOF {
			return "", fmt.Errorf("读取数据失败: %v", err)
		}

		if n >= 2 && peek[0] == 0x1f && peek[1] == 0x8b {
			combinedReader := io.MultiReader(bytes.NewReader(peek[:n]), input)
			gzReader, err := gzip.NewReader(combinedReader)
			if err != nil {
				return "", fmt.Errorf("gzip解压失败: %v", err)
			}
			defer gzReader.Close()
			reader = gzReader
		} else {
			reader = io.MultiReader(bytes.NewReader(peek[:n]), input)
		}
	}

	data, err := io.ReadAll(reader)
	if err != nil {
		return "", fmt.Errorf("读取内容失败: %v", err)
	}

	return string(data), nil
}

func processGitHubURLs(content, host string) string {
	return githubRegex.ReplaceAllStringFunc(content, func(match string) string {
		// 如果匹配包含前缀分隔符，保留它，防止出现重复转换
		if len(match) > 0 && match[0] != 'h' {
			prefix := match[0:1]
			url := match[1:]
			return prefix + transformURL(url, host)
		}
		return transformURL(match, host)
	})
}

// transformURL URL转换函数
func transformURL(url, host string) string {
	if strings.Contains(url, host) {
		return url
	}

	if strings.HasPrefix(url, "http://") {
		url = "https" + url[4:]
	} else if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "//") {
		url = "https://" + url
	}

	// 确保 host 有协议头
	if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
		host = "https://" + host
	}
	host = strings.TrimSuffix(host, "/")

	return host + "/" + url
}