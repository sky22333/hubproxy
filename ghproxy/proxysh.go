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

// ProcessGitHubURLs 处理数据流中的GitHub URL，将其替换为代理URL
// 参数:
//   - input: 输入数据流
//   - isCompressed: 是否为gzip压缩数据
//   - host: 代理服务器域名
//   - isShellFile: 是否为.sh文件 (如果为true，则会处理其中的GitHub URL)
//
// 返回:
//   - io.Reader: 处理后的数据流
//   - int64: 写入的字节数
//   - error: 错误信息
func ProcessGitHubURLs(input io.ReadCloser, isCompressed bool, host string, isShellFile bool) (io.Reader, int64, error) {
	debugPrintf("开始处理文件: isCompressed=%v, host=%s, isShellFile=%v\n", isCompressed, host, isShellFile)
	
	if !isShellFile {
		debugPrintf("非shell文件，跳过处理\n")
		return input, 0, nil
	}

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

		reader := input
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
		bufReader := bufio.NewReader(reader)

		var bufWriter *bufio.Writer
		if isCompressed {
			gzipWriter := gzip.NewWriter(pipeWriter)
			defer gzipWriter.Close()
			bufWriter = bufio.NewWriterSize(gzipWriter, 4096)
		} else {
			bufWriter = bufio.NewWriterSize(pipeWriter, 4096)
		}
		defer bufWriter.Flush()

		written, err = processContent(bufReader, bufWriter, host)
		debugPrintf("文件处理完成，共处理 %d 字节\n", written)
	}()

	return pipeReader, written, nil
}

// processContent 处理文件内容，返回处理的字节数
func processContent(reader *bufio.Reader, writer *bufio.Writer, host string) (int64, error) {
	var written int64
	lineNum := 0
	
	for {
		lineNum++
		line, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return written, err
		}

		if line != "" {
			// 在处理前先检查是否包含GitHub URL
			matches := urlPattern.FindAllString(line, -1)
			if len(matches) > 0 {
				debugPrintf("\n在第 %d 行发现 %d 个GitHub URL:\n", lineNum, len(matches))
				for _, match := range matches {
					debugPrintf("原始URL: %s\n", match)
				}
			}

			modifiedLine := processLine(line, host, lineNum)
			n, writeErr := writer.WriteString(modifiedLine)
			written += int64(n)
			if writeErr != nil {
				return written, writeErr
			}
		}

		if err == io.EOF {
			break
		}
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

// modifyGitHubURL 修改GitHub URL，添加代理域名前缀
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