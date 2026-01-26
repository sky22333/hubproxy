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

// MaxShellSize 限制最大处理大小为 10MB
const MaxShellSize = 10 * 1024 * 1024

// ProcessSmart Shell脚本智能处理函数
func ProcessSmart(input io.Reader, isCompressed bool, host string) (io.Reader, int64, error) {
	content, err := readShellContent(input, isCompressed)
	if err != nil {
		return nil, 0, err
	}

	if len(content) == 0 {
		return strings.NewReader(""), 0, nil
	}

	if !bytes.Contains(content, []byte("github.com")) && !bytes.Contains(content, []byte("githubusercontent.com")) {
		return bytes.NewReader(content), int64(len(content)), nil
	}

	processed := processGitHubURLs(string(content), host)

	return strings.NewReader(processed), int64(len(processed)), nil
}

func readShellContent(input io.Reader, isCompressed bool) ([]byte, error) {
	var reader io.Reader = input

	if isCompressed {
		peek := make([]byte, 2)
		n, err := input.Read(peek)
		if err != nil && err != io.EOF {
			return nil, fmt.Errorf("读取数据失败: %v", err)
		}

		if n >= 2 && peek[0] == 0x1f && peek[1] == 0x8b {
			combinedReader := io.MultiReader(bytes.NewReader(peek[:n]), input)
			gzReader, err := gzip.NewReader(combinedReader)
			if err != nil {
				return nil, fmt.Errorf("gzip解压失败: %v", err)
			}
			defer gzReader.Close()
			reader = gzReader
		} else {
			reader = io.MultiReader(bytes.NewReader(peek[:n]), input)
		}
	}

	limit := int64(MaxShellSize + 1)
	limitedReader := io.LimitReader(reader, limit)

	data, err := io.ReadAll(limitedReader)
	if err != nil {
		return nil, fmt.Errorf("读取内容失败: %v", err)
	}

	if int64(len(data)) > MaxShellSize {
		return nil, fmt.Errorf("脚本文件过大，超过 %d MB 限制", MaxShellSize/1024/1024)
	}

	return data, nil
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
