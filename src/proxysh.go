package main

import (
	"bufio"
	"compress/gzip"
	"fmt"
	"io"
	"regexp"
	"strings"
)

var (
	// gitHubDomains 定义所有支持的GitHub相关域名
	gitHubDomains = []string{
		"github.com",
		"raw.githubusercontent.com",
		"raw.github.com",
		"gist.githubusercontent.com",
		"gist.github.com",
		"api.github.com",
	}
	
	// urlPattern 使用gitHubDomains构建正则表达式
	urlPattern = regexp.MustCompile(`https?://(?:` + strings.Join(gitHubDomains, "|") + `)[^\s'"]+`)

	// 是否启用脚本嵌套代理的调试日志
	DebugLog = true
)

// 打印调试日志的辅助函数
func debugPrintf(format string, args ...interface{}) {
	if DebugLog {
		fmt.Printf(format, args...)
	}
}

// ProcessGitHubURLs 处理数据流中的GitHub URL，将其替换为代理URL。
// 此处思路借鉴了 https://github.com/WJQSERVER-STUDIO/ghproxy/blob/main/proxy/nest.go

func ProcessGitHubURLs(input io.ReadCloser, isCompressed bool, host string, isShellFile bool) (io.Reader, int64, error) {
	debugPrintf("开始处理文件: isCompressed=%v, host=%s, isShellFile=%v\n", isCompressed, host, isShellFile)
	
	if !isShellFile {
		debugPrintf("非shell文件，跳过处理\n")
		return input, 0, nil
	}

	// 使用更大的缓冲区以提高性能
	pipeReader, pipeWriter := io.Pipe()
	var written int64

	go func() {
		var err error
		defer func() {
			if err != nil {
				debugPrintf("处理过程中发生错误: %v\n", err)
				_ = pipeWriter.CloseWithError(err)
			} else {
				_ = pipeWriter.Close()
			}
		}()

		defer input.Close()

		var reader io.Reader = input
		if isCompressed {
			debugPrintf("检测到压缩文件，进行解压处理\n")
			gzipReader, gzipErr := gzip.NewReader(input)
			if gzipErr != nil {
				err = gzipErr
				return
			}
			defer gzipReader.Close()
			reader = gzipReader
		}

		// 使用更大的缓冲区
		bufReader := bufio.NewReaderSize(reader, 32*1024) // 32KB buffer
		var writer io.Writer = pipeWriter

		if isCompressed {
			gzipWriter := gzip.NewWriter(writer)
			defer gzipWriter.Close()
			writer = gzipWriter
		}

		bufWriter := bufio.NewWriterSize(writer, 32*1024) // 32KB buffer
		defer bufWriter.Flush()

		written, err = processContent(bufReader, bufWriter, host)
		if err != nil {
			debugPrintf("处理内容时发生错误: %v\n", err)
			return
		}
		
		debugPrintf("文件处理完成，共处理 %d 字节\n", written)
	}()

	return pipeReader, written, nil
}

// processContent 优化处理文件内容的函数
func processContent(reader *bufio.Reader, writer *bufio.Writer, host string) (int64, error) {
	var written int64
	lineNum := 0
	
	for {
		lineNum++
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return written, fmt.Errorf("读取行时发生错误: %w", err)
		}

		if line != "" {
			// 在处理前先检查是否包含GitHub URL
			if strings.Contains(line, "github.com") || 
			   strings.Contains(line, "raw.githubusercontent.com") {
				matches := urlPattern.FindAllString(line, -1)
				if len(matches) > 0 {
					debugPrintf("\n在第 %d 行发现 %d 个GitHub URL:\n", lineNum, len(matches))
					for _, match := range matches {
						debugPrintf("原始URL: %s\n", match)
					}
				}
				
				modifiedLine := processLine(line, host, lineNum)
				n, writeErr := writer.WriteString(modifiedLine)
				if writeErr != nil {
					return written, fmt.Errorf("写入修改后的行时发生错误: %w", writeErr)
				}
				written += int64(n)
			} else {
				// 如果行中没有GitHub URL，直接写入
				n, writeErr := writer.WriteString(line)
				if writeErr != nil {
					return written, fmt.Errorf("写入原始行时发生错误: %w", writeErr)
				}
				written += int64(n)
			}
		}

		if err == io.EOF {
			break
		}
	}

	// 确保所有数据都被写入
	if err := writer.Flush(); err != nil {
		return written, fmt.Errorf("刷新缓冲区时发生错误: %w", err)
	}
	
	return written, nil
}

// processLine 处理单行文本，替换所有匹配的GitHub URL
func processLine(line string, host string, lineNum int) string {
	return urlPattern.ReplaceAllStringFunc(line, func(url string) string {
		newURL := modifyGitHubURL(url, host)
		if newURL != url {
			debugPrintf("第 %d 行URL替换:\n  原始: %s\n  替换后: %s\n", lineNum, url, newURL)
		}
		return newURL
	})
}

// 判断代理域名前缀
func modifyGitHubURL(url string, host string) string {
	for _, domain := range gitHubDomains {
		hasHttps := strings.HasPrefix(url, "https://"+domain)
		hasHttp := strings.HasPrefix(url, "http://"+domain)
		
		if hasHttps || hasHttp || strings.HasPrefix(url, domain) {
			if !hasHttps && !hasHttp {
				url = "https://" + url
			}
			if hasHttp {
				url = "https://" + strings.TrimPrefix(url, "http://")
			}
			// 移除host开头的协议头（如果有）
			host = strings.TrimPrefix(host, "https://")
			host = strings.TrimPrefix(host, "http://")
			// 返回组合后的URL
			return host + "/" + url
		}
	}
	return url
}

// IsShellFile 检查文件是否为shell文件（基于文件名）
func IsShellFile(filename string) bool {
	return strings.HasSuffix(filename, ".sh")
}