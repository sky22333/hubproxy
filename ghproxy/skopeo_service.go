package main

import (
	"archive/zip"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// 任务状态
type TaskStatus string

const (
	StatusPending   TaskStatus = "pending"
	StatusRunning   TaskStatus = "running"
	StatusCompleted TaskStatus = "completed"
	StatusFailed    TaskStatus = "failed"
)

// 镜像下载任务
type ImageTask struct {
	Image      string  `json:"image"`
	Progress   float64 `json:"progress"`
	Status     string  `json:"status"`
	Error      string  `json:"error,omitempty"`
	OutputPath string  `json:"-"` // 输出文件路径，不发送给客户端
}

// 下载任务
type DownloadTask struct {
	ID            string       `json:"id"`
	Images        []*ImageTask `json:"images"`
	TotalProgress float64      `json:"totalProgress"`
	Status        TaskStatus   `json:"status"`
	OutputFile    string       `json:"-"` // 最终输出文件
	TempDir       string       `json:"-"` // 临时目录
	Lock          sync.Mutex   `json:"-"` // 锁，防止并发冲突
}

// WebSocket客户端
type Client struct {
	Conn      *websocket.Conn
	TaskID    string
	Send      chan []byte
	CloseOnce sync.Once
}

var (
	// WebSocket升级器
	upgrader = websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 1024,
		CheckOrigin: func(r *http.Request) bool {
			return true // 允许所有源
		},
	}

	// 活跃任务映射
	tasks      = make(map[string]*DownloadTask)
	tasksLock  sync.Mutex
	clients    = make(map[string]*Client)
	clientLock sync.Mutex
)

// 初始化Skopeo相关路由
func initSkopeoRoutes(router *gin.Engine) {
	// 创建临时目录
	os.MkdirAll("./temp", 0755)

	// WebSocket路由 - 用于实时获取进度
	router.GET("/ws/:taskId", handleWebSocket)

	// 创建下载任务
	router.POST("/api/download", handleDownload)

	// 获取任务状态
	router.GET("/api/task/:taskId", getTaskStatus)

	// 下载文件
	router.GET("/api/files/:filename", serveFile)

	// 启动清理过期文件的goroutine
	go cleanupTempFiles()
}

// 处理WebSocket连接
func handleWebSocket(c *gin.Context) {
	taskID := c.Param("taskId")
	
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		fmt.Printf("WebSocket升级失败: %v\n", err)
		return
	}

	client := &Client{
		Conn:   conn,
		TaskID: taskID,
		Send:   make(chan []byte, 256),
	}

	// 注册客户端
	clientLock.Lock()
	clients[taskID] = client
	clientLock.Unlock()

	// 启动goroutine处理消息发送
	go client.writePump()

	// 如果任务已存在，立即发送当前状态
	tasksLock.Lock()
	if task, exists := tasks[taskID]; exists {
		tasksLock.Unlock()
		taskJSON, _ := json.Marshal(task)
		client.Send <- taskJSON
	} else {
		tasksLock.Unlock()
	}

	// 处理WebSocket关闭
	conn.SetCloseHandler(func(code int, text string) error {
		client.CloseOnce.Do(func() {
			close(client.Send)
			clientLock.Lock()
			delete(clients, taskID)
			clientLock.Unlock()
		})
		return nil
	})
}

// 客户端消息发送loop
func (c *Client) writePump() {
	defer func() {
		c.Conn.Close()
	}()

	for message := range c.Send {
		err := c.Conn.WriteMessage(websocket.TextMessage, message)
		if err != nil {
			fmt.Printf("发送WS消息失败: %v\n", err)
			break
		}
	}
}

// 获取任务状态
func getTaskStatus(c *gin.Context) {
	taskID := c.Param("taskId")
	
	tasksLock.Lock()
	task, exists := tasks[taskID]
	tasksLock.Unlock()
	
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "任务不存在"})
		return
	}
	
	c.JSON(http.StatusOK, task)
}

// 生成随机任务ID
func generateTaskID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// 处理下载请求
func handleDownload(c *gin.Context) {
	type DownloadRequest struct {
		Images   []string `json:"images"`
		Platform string   `json:"platform"` // 平台: amd64, arm64等
	}

	var req DownloadRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的请求参数"})
		return
	}

	if len(req.Images) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "请提供至少一个镜像"})
		return
	}

	// 创建新任务
	taskID := generateTaskID()
	tempDir := filepath.Join("./temp", taskID)
	os.MkdirAll(tempDir, 0755)

	// 初始化任务
	imageTasks := make([]*ImageTask, len(req.Images))
	for i, image := range req.Images {
		imageTasks[i] = &ImageTask{
			Image:    image,
			Progress: 0,
			Status:   string(StatusPending),
		}
	}

	task := &DownloadTask{
		ID:            taskID,
		Images:        imageTasks,
		TotalProgress: 0,
		Status:        StatusPending,
		TempDir:       tempDir,
	}

	// 保存任务
	tasksLock.Lock()
	tasks[taskID] = task
	tasksLock.Unlock()

	// 异步处理下载
	go func() {
		processDownloadTask(task, req.Platform)
	}()

	c.JSON(http.StatusOK, gin.H{
		"taskId": taskID,
		"status": "started",
	})
}

// 处理下载任务
func processDownloadTask(task *DownloadTask, platform string) {
	task.Lock.Lock()
	task.Status = StatusRunning
	task.Lock.Unlock()
	
	// 通知客户端任务已开始
	sendTaskUpdate(task)

	// 使用WaitGroup等待所有镜像下载完成
	var wg sync.WaitGroup
	wg.Add(len(task.Images))

	// 使用并发下载镜像
	for i, imgTask := range task.Images {
		go func(idx int, imgTask *ImageTask) {
			defer wg.Done()
			downloadImage(task, idx, imgTask, platform)
		}(i, imgTask)
	}

	// 等待所有下载完成
	wg.Wait()

	// 判断是单个tar还是需要打包
	var finalFilePath string
	var err error

	task.Lock.Lock()
	allSuccess := true
	for _, img := range task.Images {
		if img.Status == string(StatusFailed) {
			allSuccess = false
			break
		}
	}

	if !allSuccess {
		task.Status = StatusFailed
		task.Lock.Unlock()
		sendTaskUpdate(task)
		return
	}

	// 如果只有一个文件，直接使用它
	if len(task.Images) == 1 && task.Images[0].Status == string(StatusCompleted) {
		finalFilePath = task.Images[0].OutputPath
		// 重命名为更友好的名称
		imageName := strings.ReplaceAll(task.Images[0].Image, "/", "_")
		imageName = strings.ReplaceAll(imageName, ":", "_")
		newPath := filepath.Join(task.TempDir, imageName+".tar")
		os.Rename(finalFilePath, newPath)
		finalFilePath = newPath
	} else {
		// 多个文件打包成zip
		finalFilePath, err = createZipArchive(task)
		if err != nil {
			task.Status = StatusFailed
			task.Lock.Unlock()
			sendTaskUpdate(task)
			return
		}
	}

	task.OutputFile = finalFilePath
	task.Status = StatusCompleted
	task.TotalProgress = 100
	task.Lock.Unlock()

	// 发送最终状态更新
	sendTaskUpdate(task)
}

// 下载单个镜像
func downloadImage(task *DownloadTask, index int, imgTask *ImageTask, platform string) {
	imgTask.Status = string(StatusRunning)
	sendImageUpdate(task, index)

	// 创建输出文件名
	outputFileName := fmt.Sprintf("image_%d.tar", index)
	outputPath := filepath.Join(task.TempDir, outputFileName)
	imgTask.OutputPath = outputPath

	// 创建skopeo命令
	platformArg := ""
	if platform != "" {
		// 支持手动输入完整的平台参数
		if strings.Contains(platform, "--") {
			platformArg = platform
		} else {
			// 仅指定架构名称的情况
			platformArg = fmt.Sprintf("--override-os linux --override-arch %s", platform)
		}
	}

	// 构建命令
	cmd := fmt.Sprintf("skopeo copy %s docker://%s docker-archive:%s", 
		platformArg, imgTask.Image, outputPath)
	
	// 执行命令
	command := exec.Command("sh", "-c", cmd)
	
	// 获取命令输出
	stderr, err := command.StderrPipe()
	if err != nil {
		imgTask.Status = string(StatusFailed)
		imgTask.Error = fmt.Sprintf("无法创建输出管道: %v", err)
		sendImageUpdate(task, index)
		return
	}

	if err := command.Start(); err != nil {
		imgTask.Status = string(StatusFailed)
		imgTask.Error = fmt.Sprintf("启动命令失败: %v", err)
		sendImageUpdate(task, index)
		return
	}

	// 读取stderr以获取进度信息
	go func() {
		buf := make([]byte, 1024)
		for {
			n, err := stderr.Read(buf)
			if n > 0 {
				output := string(buf[:n])
				// 解析进度信息 (这里简化处理，假设skopeo输出进度信息)
				// 实际需要根据skopeo的真实输出格式进行解析
				if strings.Contains(output, "%") {
					// 简单解析，实际使用时可能需要更复杂的解析逻辑
					parts := strings.Split(output, "%")
					if len(parts) > 0 {
						numStr := strings.TrimSpace(parts[0])
						numStr = strings.TrimLeft(numStr, "Copying blob ")
						numStr = strings.TrimLeft(numStr, "Copying config ")
						numStr = strings.TrimRight(numStr, " / ")
						numStr = strings.TrimSpace(numStr)
						// 尝试提取最后一个数字作为进度
						fields := strings.Fields(numStr)
						if len(fields) > 0 {
							lastField := fields[len(fields)-1]
							progress := 0.0
							fmt.Sscanf(lastField, "%f", &progress)
							if progress > 0 && progress <= 100 {
								imgTask.Progress = progress
								updateTaskProgress(task)
								sendImageUpdate(task, index)
							}
						}
					}
				}
			}
			if err != nil {
				break
			}
		}
	}()

	if err := command.Wait(); err != nil {
		imgTask.Status = string(StatusFailed)
		imgTask.Error = fmt.Sprintf("命令执行失败: %v", err)
		sendImageUpdate(task, index)
		return
	}

	// 检查文件是否成功创建
	if _, err := os.Stat(outputPath); os.IsNotExist(err) {
		imgTask.Status = string(StatusFailed)
		imgTask.Error = "文件未成功创建"
		sendImageUpdate(task, index)
		return
	}

	// 更新状态为已完成
	imgTask.Status = string(StatusCompleted)
	imgTask.Progress = 100
	updateTaskProgress(task)
	sendImageUpdate(task, index)
}

// 更新任务总进度
func updateTaskProgress(task *DownloadTask) {
	task.Lock.Lock()
	defer task.Lock.Unlock()

	totalProgress := 0.0
	for _, img := range task.Images {
		totalProgress += img.Progress
	}
	task.TotalProgress = totalProgress / float64(len(task.Images))
}

// 创建ZIP归档
func createZipArchive(task *DownloadTask) (string, error) {
	zipFilePath := filepath.Join(task.TempDir, "images.zip")
	zipFile, err := os.Create(zipFilePath)
	if err != nil {
		return "", err
	}
	defer zipFile.Close()

	zipWriter := zip.NewWriter(zipFile)
	defer zipWriter.Close()

	for _, img := range task.Images {
		if img.Status != string(StatusCompleted) || img.OutputPath == "" {
			continue
		}

		// 创建ZIP条目
		imgFile, err := os.Open(img.OutputPath)
		if err != nil {
			return "", err
		}

		// 使用镜像名作为文件名
		imageName := strings.ReplaceAll(img.Image, "/", "_")
		imageName = strings.ReplaceAll(imageName, ":", "_")
		fileName := imageName + ".tar"

		fileInfo, err := imgFile.Stat()
		if err != nil {
			imgFile.Close()
			return "", err
		}

		header, err := zip.FileInfoHeader(fileInfo)
		if err != nil {
			imgFile.Close()
			return "", err
		}

		header.Name = fileName
		header.Method = zip.Deflate

		writer, err := zipWriter.CreateHeader(header)
		if err != nil {
			imgFile.Close()
			return "", err
		}

		_, err = io.Copy(writer, imgFile)
		imgFile.Close()
		if err != nil {
			return "", err
		}
	}

	return zipFilePath, nil
}

// 发送任务更新到WebSocket
func sendTaskUpdate(task *DownloadTask) {
	taskJSON, err := json.Marshal(task)
	if err != nil {
		fmt.Printf("序列化任务失败: %v\n", err)
		return
	}

	clientLock.Lock()
	client, exists := clients[task.ID]
	clientLock.Unlock()

	if exists {
		select {
		case client.Send <- taskJSON:
		default:
			// 通道已满或关闭，忽略
		}
	}
}

// 发送单个镜像更新
func sendImageUpdate(task *DownloadTask, imageIndex int) {
	sendTaskUpdate(task)
}

// 提供文件下载
func serveFile(c *gin.Context) {
	filename := c.Param("filename")
	
	// 安全检查，防止任意文件访问
	if strings.Contains(filename, "..") {
		c.JSON(http.StatusForbidden, gin.H{"error": "无效的文件名"})
		return
	}
	
	// 根据任务ID和文件名查找文件
	parts := strings.Split(filename, "_")
	if len(parts) < 2 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "无效的文件名格式"})
		return
	}
	
	taskID := parts[0]
	
	tasksLock.Lock()
	task, exists := tasks[taskID]
	tasksLock.Unlock()
	
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "任务不存在"})
		return
	}
	
	// 检查文件是否存在
	filePath := task.OutputFile
	if filePath == "" || !fileExists(filePath) {
		c.JSON(http.StatusNotFound, gin.H{"error": "文件不存在"})
		return
	}
	
	// 获取文件信息
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "无法获取文件信息"})
		return
	}
	
	// 设置文件名 - 提取有意义的文件名
	downloadName := filepath.Base(filePath)
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", downloadName))
	c.Header("Content-Length", fmt.Sprintf("%d", fileInfo.Size()))
	
	// 返回文件
	c.File(filePath)
}

// 检查文件是否存在
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return !os.IsNotExist(err)
}

// 清理过期临时文件
func cleanupTempFiles() {
	for {
		time.Sleep(1 * time.Hour)
		
		// 遍历temp目录
		err := filepath.Walk("./temp", func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			
			// 跳过根目录
			if path == "./temp" {
				return nil
			}
			
			// 如果文件或目录超过24小时未修改，则删除
			if time.Since(info.ModTime()) > 24*time.Hour {
				if info.IsDir() {
					os.RemoveAll(path)
					return filepath.SkipDir
				}
				os.Remove(path)
			}
			
			return nil
		})
		
		if err != nil {
			fmt.Printf("清理临时文件失败: %v\n", err)
		}
	}
} 