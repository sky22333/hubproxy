package main

import (
	"archive/zip"
	"bufio"
	"context"
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
	"golang.org/x/sync/errgroup"
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
	lock       sync.Mutex `json:"-"` // 镜像任务自己的锁
}

// 下载任务
type DownloadTask struct {
	ID            string       `json:"id"`
	Images        []*ImageTask `json:"images"`
	CompletedCount int         `json:"completedCount"` // 已完成任务数
	TotalCount    int          `json:"totalCount"`     // 总任务数
	Status        TaskStatus   `json:"status"`
	OutputFile    string       `json:"-"` // 最终输出文件
	TempDir       string       `json:"-"` // 临时目录
	StatusLock    sync.RWMutex `json:"-"` // 状态锁，使用读写锁提高并发性
	ProgressLock  sync.RWMutex `json:"-"` // 进度锁
	ImageLock     sync.RWMutex `json:"-"` // 镜像列表锁
	updateChan    chan *ProgressUpdate `json:"-"` // 进度更新通道
	done          chan struct{} `json:"-"` // 用于安全关闭goroutine
	once          sync.Once     `json:"-"` // 确保只关闭一次
	createTime    time.Time     `json:"-"` // 创建时间，用于清理
}

// 进度更新消息
type ProgressUpdate struct {
	TaskID     string
	ImageIndex int
	Progress   float64
	Status     string
	Error      string
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

	// 创建下载任务，应用限流中间件
	ApplyRateLimit(router, "/api/download", "POST", handleDownload)

	// 获取任务状态
	router.GET("/api/task/:taskId", getTaskStatus)

	// 下载文件
	router.GET("/api/files/:filename", serveFile)
	
	// 通过任务ID下载文件
	router.GET("/api/download/:taskId/file", serveFileByTaskId)

	// 启动清理过期文件的goroutine
	go cleanupTempFiles()
	
	// 启动WebSocket连接清理goroutine
	go cleanupWebSocketConnections()
	
	// 启动过期任务清理goroutine
	go cleanupExpiredTasks()
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

	// 设置WebSocket超时
	conn.SetReadDeadline(time.Now().Add(120 * time.Second))
	conn.SetWriteDeadline(time.Now().Add(60 * time.Second))
	
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
	
	// 创建任务状态副本以避免序列化过程中的锁
	taskCopy := &DownloadTask{
		ID:            task.ID,
		CompletedCount: 0,
		TotalCount:    len(task.Images),
		Status:        TaskStatus(""),
		Images:        nil,
	}
	
	// 复制状态信息
	task.StatusLock.RLock()
	taskCopy.Status = task.Status
	task.StatusLock.RUnlock()
	
	task.ProgressLock.RLock()
	taskCopy.CompletedCount = task.CompletedCount
	task.ProgressLock.RUnlock()
	
	// 复制镜像信息
	task.ImageLock.RLock()
	taskCopy.Images = make([]*ImageTask, len(task.Images))
	for i, img := range task.Images {
		img.lock.Lock()
		taskCopy.Images[i] = &ImageTask{
			Image:    img.Image,
			Progress: img.Progress,
			Status:   img.Status,
			Error:    img.Error,
		}
		img.lock.Unlock()
	}
	task.ImageLock.RUnlock()
	
	c.JSON(http.StatusOK, taskCopy)
}

// 生成随机任务ID
func generateTaskID() string {
	b := make([]byte, 16)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// 初始化任务并启动进度处理器
func initTask(task *DownloadTask) {
	// 创建进度更新通道和控制通道
	task.updateChan = make(chan *ProgressUpdate, 100)
	task.done = make(chan struct{})
	task.createTime = time.Now()
	
	// 启动进度处理goroutine
	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Printf("任务 %s 进度处理goroutine异常: %v\n", task.ID, r)
			}
		}()
		
		// 处理消息的函数
		processUpdate := func(update *ProgressUpdate) {
			if update == nil {
				return
			}
			
			// 获取更新的镜像
			task.ImageLock.RLock()
			if update.ImageIndex < 0 || update.ImageIndex >= len(task.Images) {
				task.ImageLock.RUnlock()
				return
			}
			imgTask := task.Images[update.ImageIndex]
			task.ImageLock.RUnlock()
			
			statusChanged := false
			prevStatus := ""
			
			// 更新镜像进度和状态
			imgTask.lock.Lock()
			if update.Progress > 0 {
				imgTask.Progress = update.Progress
			}
			if update.Status != "" && update.Status != imgTask.Status {
				prevStatus = imgTask.Status
				imgTask.Status = update.Status
				statusChanged = true
			}
			if update.Error != "" {
				imgTask.Error = update.Error
			}
			imgTask.lock.Unlock()
			
			// 检查状态变化并更新完成计数
			if statusChanged {
				task.ProgressLock.Lock()
				// 如果之前不是Completed，现在是Completed，增加计数
				if prevStatus != string(StatusCompleted) && update.Status == string(StatusCompleted) {
					task.CompletedCount++
					fmt.Printf("任务 %s: 镜像 %d 完成，当前完成数: %d/%d\n", 
						task.ID, update.ImageIndex, task.CompletedCount, task.TotalCount)
				}
				// 如果之前是Completed，现在不是，减少计数
				if prevStatus == string(StatusCompleted) && update.Status != string(StatusCompleted) {
					task.CompletedCount--
					if task.CompletedCount < 0 {
						task.CompletedCount = 0
					}
				}
				task.ProgressLock.Unlock()
			}
			
			// 发送更新到客户端
			sendTaskUpdate(task)
		}
		
		// 主处理循环
		for {
			select {
			case update := <-task.updateChan:
				if update == nil {
					// 通道关闭信号，直接退出
					return
				}
				processUpdate(update)
				
			case <-task.done:
				// 收到关闭信号，进入drain模式处理剩余消息
				goto drainMode
			}
		}
		
	drainMode:
		// 处理通道中剩余的所有消息，确保不丢失任何更新
		for {
			select {
			case update := <-task.updateChan:
				if update == nil {
					// 通道关闭，安全退出
					return
				}
				processUpdate(update)
			default:
				// 没有更多待处理的消息，安全退出
				return
			}
		}
	}()
}

// 安全关闭任务的goroutine和通道
func (task *DownloadTask) Close() {
	task.once.Do(func() {
		close(task.done)
		// 给一点时间让goroutine退出，然后安全关闭updateChan
		time.AfterFunc(100*time.Millisecond, func() {
			task.safeCloseUpdateChan()
		})
	})
}

// 安全关闭updateChan，防止重复关闭
func (task *DownloadTask) safeCloseUpdateChan() {
	defer func() {
		if r := recover(); r != nil {
			// 捕获关闭已关闭channel的panic，忽略它
			fmt.Printf("任务 %s: updateChan已经关闭\n", task.ID)
		}
	}()
	close(task.updateChan)
}

// 发送进度更新
func sendProgressUpdate(task *DownloadTask, index int, progress float64, status string, errorMsg string) {
	// 检查任务是否已经关闭
	select {
	case <-task.done:
		// 任务已关闭，不发送更新
		return
	default:
	}

	// 安全发送进度更新
	select {
	case task.updateChan <- &ProgressUpdate{
		TaskID:     task.ID,
		ImageIndex: index,
		Progress:   progress,
		Status:     status,
		Error:      errorMsg,
	}:
		// 成功发送
	case <-task.done:
		// 在发送过程中任务被关闭
		return
	default:
		// 通道已满，丢弃更新
		fmt.Printf("Warning: Update channel for task %s is full\n", task.ID)
	}
}

// 更新总进度 - 重新计算已完成任务数
func updateTaskTotalProgress(task *DownloadTask) {
	task.ProgressLock.Lock()
	defer task.ProgressLock.Unlock()
	
	completedCount := 0
	
	task.ImageLock.RLock()
	totalCount := len(task.Images)
	task.ImageLock.RUnlock()
	
	if totalCount == 0 {
		return
	}
	
	task.ImageLock.RLock()
	for _, img := range task.Images {
		img.lock.Lock()
		if img.Status == string(StatusCompleted) {
			completedCount++
		}
		img.lock.Unlock()
	}
	task.ImageLock.RUnlock()
	
	task.CompletedCount = completedCount
	task.TotalCount = totalCount
	
	fmt.Printf("任务 %s: 进度更新 %d/%d 已完成\n", task.ID, completedCount, totalCount)
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

	// Docker镜像访问控制检查
	for _, image := range req.Images {
		if allowed, reason := GlobalAccessController.CheckDockerAccess(image); !allowed {
			fmt.Printf("Docker镜像 %s 下载被拒绝: %s\n", image, reason)
			c.JSON(http.StatusForbidden, gin.H{
				"error": fmt.Sprintf("镜像 %s 访问被限制: %s", image, reason),
			})
			return
		}
	}

	// 获取配置中的镜像数量限制
	cfg := GetConfig()
	maxImages := cfg.Download.MaxImages
	if maxImages <= 0 {
		maxImages = 10 // 安全默认值，防止配置错误
	}

	// 检查镜像数量限制，防止恶意刷流量
	if len(req.Images) > maxImages {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("单次下载镜像数量超过限制，最多允许 %d 个镜像", maxImages),
		})
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
		CompletedCount: 0,
		TotalCount:    len(imageTasks),
		Status:        StatusPending,
		TempDir:       tempDir,
	}
	
	// 初始化任务通道和处理器
	initTask(task)

	// 保存任务
	tasksLock.Lock()
	tasks[taskID] = task
	tasksLock.Unlock()

	// 异步处理下载
	go func() {
		defer func() {
			// 任务完成后安全关闭更新通道
			task.safeCloseUpdateChan()
		}()
		processDownloadTask(task, req.Platform)
	}()

	c.JSON(http.StatusOK, gin.H{
		"taskId": taskID,
		"status": "started",
		"totalCount": len(imageTasks),
	})
}

// 处理下载任务
func processDownloadTask(task *DownloadTask, platform string) {
	// 设置任务状态为运行中
	task.StatusLock.Lock()
	task.Status = StatusRunning
	task.StatusLock.Unlock()
	
	// 初始化总任务数
	task.ImageLock.RLock()
	imageCount := len(task.Images)
	task.ImageLock.RUnlock()
	
	task.ProgressLock.Lock()
	task.TotalCount = imageCount
	task.CompletedCount = 0
	task.ProgressLock.Unlock()
	
	// 通知客户端任务已开始
	sendTaskUpdate(task)

	// 创建错误组，用于管理所有下载goroutine
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // 确保资源被释放
	
	g, ctx := errgroup.WithContext(ctx)
	
	// 启动并发下载
	task.ImageLock.RLock()
	imageCount = len(task.Images)
	task.ImageLock.RUnlock()
	
	// 创建工作池限制并发数
	const maxConcurrent = 5
	semaphore := make(chan struct{}, maxConcurrent)
	
	// 添加下载任务
	for i := 0; i < imageCount; i++ {
		index := i // 捕获循环变量
		
		g.Go(func() error {
			// 获取信号量，限制并发
			semaphore <- struct{}{}
			defer func() { <-semaphore }()
			
			task.ImageLock.RLock()
			imgTask := task.Images[index]
			task.ImageLock.RUnlock()
			
			// 下载镜像
			err := downloadImageWithContext(ctx, task, index, imgTask, platform)
			if err != nil {
				fmt.Printf("镜像 %s 下载失败: %v\n", imgTask.Image, err)
				return err
			}
			return nil
		})
	}
	
	// 等待所有下载完成
	err := g.Wait()
	
	// 再次计算已完成任务数，确保正确
	updateTaskTotalProgress(task)
	
	// 检查是否有错误发生
	if err != nil {
		task.StatusLock.Lock()
		task.Status = StatusFailed
		task.StatusLock.Unlock()
		sendTaskUpdate(task)
		// 任务失败时关闭goroutine
		task.Close()
		return
	}
	
	// 判断是单个tar还是需要打包
	var finalFilePath string
	
	task.StatusLock.Lock()
	
	// 检查是否所有镜像都下载成功
	allSuccess := true
	task.ImageLock.RLock()
	for _, img := range task.Images {
		img.lock.Lock()
		if img.Status != string(StatusCompleted) {
			allSuccess = false
		}
		img.lock.Unlock()
	}
	task.ImageLock.RUnlock()
	
	if !allSuccess {
		task.Status = StatusFailed
		task.StatusLock.Unlock()
		sendTaskUpdate(task)
		return
	}

	// 如果只有一个文件，直接使用它
	task.ImageLock.RLock()
	if imageCount == 1 {
		imgTask := task.Images[0]
		imgTask.lock.Lock()
		if imgTask.Status == string(StatusCompleted) {
			finalFilePath = imgTask.OutputPath
			// 重命名为更友好的名称
			imageName := strings.ReplaceAll(imgTask.Image, "/", "_")
			imageName = strings.ReplaceAll(imageName, ":", "_")
			newPath := filepath.Join(task.TempDir, imageName+".tar")
			os.Rename(finalFilePath, newPath)
			finalFilePath = newPath
		}
		imgTask.lock.Unlock()
	} else {
		// 多个文件打包成zip
		task.ImageLock.RUnlock()
		var zipErr error
		finalFilePath, zipErr = createZipArchive(task)
		if zipErr != nil {
			task.Status = StatusFailed
			task.StatusLock.Unlock()
			sendTaskUpdate(task)
			return
		}
	}
	
	if imageCount == 1 {
		task.ImageLock.RUnlock()
	}

	task.OutputFile = finalFilePath
	task.Status = StatusCompleted
	
	// 设置完成计数为总任务数
	task.ProgressLock.Lock()
	task.CompletedCount = task.TotalCount
	task.ProgressLock.Unlock()
	
	task.StatusLock.Unlock()

	// 发送最终状态更新
	sendTaskUpdate(task)
	
	// 确保所有进度都达到100%
	ensureTaskCompletion(task)
	
	// 任务完成时关闭goroutine
	task.Close()
	
	fmt.Printf("任务 %s 全部完成: %d/%d\n", task.ID, task.CompletedCount, task.TotalCount)
}

// 下载单个镜像（带上下文控制）
func downloadImageWithContext(ctx context.Context, task *DownloadTask, index int, imgTask *ImageTask, platform string) error {
	// 更新状态为运行中
	sendProgressUpdate(task, index, 0, string(StatusRunning), "")

	// 创建输出文件名
	outputFileName := fmt.Sprintf("image_%d.tar", index)
	outputPath := filepath.Join(task.TempDir, outputFileName)
	
	imgTask.lock.Lock()
	imgTask.OutputPath = outputPath
	imgTask.lock.Unlock()

	// 创建skopeo命令
	platformArg := ""
	if platform != "" {
		// 支持手动输入完整的平台参数
		if strings.Contains(platform, "--") {
			platformArg = platform
		} else {
			// 处理特殊架构格式，如 arm/v7
			if strings.Contains(platform, "/") {
				parts := strings.Split(platform, "/")
				if len(parts) == 2 {
					// 适用于arm/v7这样的格式
					platformArg = fmt.Sprintf("--override-os linux --override-arch %s --override-variant %s", parts[0], parts[1])
				} else {
					// 对于其他带/的格式，直接按原格式处理
					platformArg = fmt.Sprintf("--override-os linux --override-arch %s", platform)
				}
			} else {
				// 仅指定架构名称的情况
				platformArg = fmt.Sprintf("--override-os linux --override-arch %s", platform)
			}
		}
	}

	// 构建命令
	cmdStr := fmt.Sprintf("skopeo copy %s docker://%s docker-archive:%s", 
		platformArg, imgTask.Image, outputPath)
	
	fmt.Printf("执行命令: %s\n", cmdStr)
	
	// 创建可取消的命令
	cmd := exec.CommandContext(ctx, "sh", "-c", cmdStr)
	
	// 获取命令输出
	stderr, err := cmd.StderrPipe()
	if err != nil {
		errMsg := fmt.Sprintf("无法创建输出管道: %v", err)
		sendProgressUpdate(task, index, 0, string(StatusFailed), errMsg)
		return fmt.Errorf(errMsg)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		errMsg := fmt.Sprintf("无法创建标准输出管道: %v", err)
		sendProgressUpdate(task, index, 0, string(StatusFailed), errMsg)
		return fmt.Errorf(errMsg)
	}

	if err := cmd.Start(); err != nil {
		errMsg := fmt.Sprintf("启动命令失败: %v", err)
		sendProgressUpdate(task, index, 0, string(StatusFailed), errMsg)
		return fmt.Errorf(errMsg)
	}

	// 使用进度通道传递进度信息
	outputChan := make(chan string, 20)
	done := make(chan struct{})
	
	// 初始进度
	sendProgressUpdate(task, index, 5, "", "")
	
	// 进度聚合器
	go func() {
		// 镜像获取阶段的进度标记
		downloadStages := map[string]float64{
			"Getting image source signatures": 10,
			"Copying blob": 30,
			"Copying config": 70,
			"Writing manifest": 90,
		}
		
		// 进度增长的定时器
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		
		lastProgress := 5.0
		stagnantTime := 0
		
		for {
			select {
			case <-ctx.Done():
				// 上下文取消
				return
				
			case <-done:
				// 命令完成，强制更新到100%
				if lastProgress < 100 {
					fmt.Printf("镜像 %s 下载完成，强制更新进度到100%%\n", imgTask.Image)
					sendProgressUpdate(task, index, 100, string(StatusCompleted), "")
				}
				return
				
			case output := <-outputChan:
				// 解析输出更新进度
				for marker, progress := range downloadStages {
					if strings.Contains(output, marker) && progress > lastProgress {
						lastProgress = progress
						sendProgressUpdate(task, index, progress, "", "")
						stagnantTime = 0
						break
					}
				}
				
				// 解析百分比
				if strings.Contains(output, "%") {
					parts := strings.Split(output, "%")
					if len(parts) > 0 {
						numStr := strings.TrimSpace(parts[0])
						fields := strings.Fields(numStr)
						if len(fields) > 0 {
							lastField := fields[len(fields)-1]
							parsedProgress := 0.0
							_, err := fmt.Sscanf(lastField, "%f", &parsedProgress)
							if err == nil && parsedProgress > 0 && parsedProgress <= 100 {
								// 根据当前阶段调整进度值
								var adjustedProgress float64
								if lastProgress < 30 {
									// Copying blob阶段，进度在10-30%之间
									adjustedProgress = 10 + (parsedProgress / 100) * 20
								} else if lastProgress < 70 {
									// Copying config阶段，进度在30-70%之间
									adjustedProgress = 30 + (parsedProgress / 100) * 40
								} else if lastProgress < 90 {
									// Writing manifest阶段，进度在70-90%之间
									adjustedProgress = 70 + (parsedProgress / 100) * 20
								}
								
								if adjustedProgress > lastProgress {
									lastProgress = adjustedProgress
									sendProgressUpdate(task, index, adjustedProgress, "", "")
									stagnantTime = 0
								}
							}
						}
					}
				}
				
				// 如果发现完成标记，立即更新到100%
				if checkForCompletionMarkers(output) {
					fmt.Printf("镜像 %s 检测到完成标记\n", imgTask.Image)
					lastProgress = 100
					sendProgressUpdate(task, index, 100, string(StatusCompleted), "")
					stagnantTime = 0
				}
				
			case <-ticker.C:
				// 如果进度长时间无变化，缓慢增加
				stagnantTime += 100 // 100ms
				if stagnantTime >= 10000 && lastProgress < 95 { // 10秒无变化
					// 每10秒增加5%进度，确保不超过95%
					newProgress := lastProgress + 5
					if newProgress > 95 {
						newProgress = 95
					}
					lastProgress = newProgress
					sendProgressUpdate(task, index, newProgress, "", "")
					stagnantTime = 0
				}
			}
		}
	}()
	
	// 读取标准输出
	go func() {
		defer func() {
			// 确保pipe在goroutine退出时关闭
			stdout.Close()
		}()
		scanner := bufio.NewScanner(stdout)
		for {
			// 检查context是否已取消
			select {
			case <-ctx.Done():
				return
			default:
			}
			
			if !scanner.Scan() {
				break // EOF或错误，正常退出
			}
			
			output := scanner.Text()
			fmt.Printf("镜像 %s 标准输出: %s\n", imgTask.Image, output)
			select {
			case outputChan <- output:
			case <-ctx.Done():
				return
			default:
				// 通道已满，丢弃
			}
		}
	}()
	
	// 读取错误输出
	go func() {
		defer func() {
			// 确保pipe在goroutine退出时关闭
			stderr.Close()
		}()
		scanner := bufio.NewScanner(stderr)
		for {
			// 检查context是否已取消
			select {
			case <-ctx.Done():
				return
			default:
			}
			
			if !scanner.Scan() {
				break // EOF或错误，正常退出
			}
			
			output := scanner.Text()
			fmt.Printf("镜像 %s 错误输出: %s\n", imgTask.Image, output)
			select {
			case outputChan <- output:
			case <-ctx.Done():
				return
			default:
				// 通道已满，丢弃
			}
		}
	}()
	
	// 等待命令完成
	cmdErr := cmd.Wait()
	close(done) // 通知进度聚合器退出
	
	if cmdErr != nil {
		errMsg := fmt.Sprintf("命令执行失败: %v", cmdErr)
		sendProgressUpdate(task, index, 0, string(StatusFailed), errMsg)
		return fmt.Errorf(errMsg)
	}
	
	// 检查文件是否成功创建
	if _, err := os.Stat(outputPath); os.IsNotExist(err) {
		errMsg := "文件未成功创建"
		sendProgressUpdate(task, index, 0, string(StatusFailed), errMsg)
		return fmt.Errorf(errMsg)
	}
	
	// 确保更新状态为已完成，进度为100%
	sendProgressUpdate(task, index, 100, string(StatusCompleted), "")
	return nil
}

// 创建ZIP归档
func createZipArchive(task *DownloadTask) (string, error) {
	zipFilePath := filepath.Join(task.TempDir, "images.zip")
	zipFile, err := os.Create(zipFilePath)
	if err != nil {
		return "", fmt.Errorf("创建ZIP文件失败: %w", err)
	}
	defer zipFile.Close()

	zipWriter := zip.NewWriter(zipFile)
	defer zipWriter.Close()

	task.ImageLock.RLock()
	images := make([]*ImageTask, len(task.Images))
	copy(images, task.Images) // 创建副本避免长时间持有锁
	task.ImageLock.RUnlock()

	for _, img := range images {
		img.lock.Lock()
		status := img.Status
		outputPath := img.OutputPath
		image := img.Image
		img.lock.Unlock()
		
		if status != string(StatusCompleted) || outputPath == "" {
			continue
		}

		// 创建ZIP条目
		imgFile, err := os.Open(outputPath)
		if err != nil {
			return "", fmt.Errorf("无法打开镜像文件 %s: %w", outputPath, err)
		}

		// 使用镜像名作为文件名
		imageName := strings.ReplaceAll(image, "/", "_")
		imageName = strings.ReplaceAll(imageName, ":", "_")
		fileName := imageName + ".tar"

		fileInfo, err := imgFile.Stat()
		if err != nil {
			imgFile.Close()
			return "", fmt.Errorf("无法获取文件信息: %w", err)
		}

		header, err := zip.FileInfoHeader(fileInfo)
		if err != nil {
			imgFile.Close()
			return "", fmt.Errorf("创建ZIP头信息失败: %w", err)
		}

		header.Name = fileName
		header.Method = zip.Deflate

		writer, err := zipWriter.CreateHeader(header)
		if err != nil {
			imgFile.Close()
			return "", fmt.Errorf("添加文件到ZIP失败: %w", err)
		}

		_, err = io.Copy(writer, imgFile)
		imgFile.Close()
		if err != nil {
			return "", fmt.Errorf("写入ZIP文件失败: %w", err)
		}
	}

	return zipFilePath, nil
}

// 发送任务更新到WebSocket
func sendTaskUpdate(task *DownloadTask) {
	// 复制任务状态避免序列化时锁定
	taskCopy := &DownloadTask{
		ID:            task.ID,
		CompletedCount: 0,
		TotalCount:    len(task.Images),
		Status:        TaskStatus(""),
		Images:        nil,
	}
	
	// 复制状态信息
	task.StatusLock.RLock()
	taskCopy.Status = task.Status
	task.StatusLock.RUnlock()
	
	task.ProgressLock.RLock()
	taskCopy.CompletedCount = task.CompletedCount
	task.ProgressLock.RUnlock()
	
	// 复制镜像信息
	task.ImageLock.RLock()
	taskCopy.Images = make([]*ImageTask, len(task.Images))
	for i, img := range task.Images {
		img.lock.Lock()
		taskCopy.Images[i] = &ImageTask{
			Image:    img.Image,
			Progress: img.Progress,
			Status:   img.Status,
			Error:    img.Error,
		}
		img.lock.Unlock()
	}
	task.ImageLock.RUnlock()
	
	// 序列化并发送
	taskJSON, err := json.Marshal(taskCopy)
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
			// 成功发送
		default:
			// 通道已满或关闭，忽略
		}
	}
}

// 通过任务ID提供文件下载
func serveFileByTaskId(c *gin.Context) {
	taskID := c.Param("taskId")
	
	tasksLock.Lock()
	task, exists := tasks[taskID]
	tasksLock.Unlock()
	
	if !exists {
		c.JSON(http.StatusNotFound, gin.H{"error": "任务不存在"})
		return
	}
	
	// 确保任务状态为已完成
	task.StatusLock.RLock()
	isCompleted := task.Status == StatusCompleted
	task.StatusLock.RUnlock()
	
	if !isCompleted {
		c.JSON(http.StatusBadRequest, gin.H{"error": "任务尚未完成"})
		return
	}
	
	// 确保所有进度都是100%
	ensureTaskCompletion(task)
	
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
	
	// 设置文件名
	downloadName := filepath.Base(filePath)
	c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", downloadName))
	c.Header("Content-Length", fmt.Sprintf("%d", fileInfo.Size()))
	
	// 返回文件
	c.File(filePath)
}

// 提供文件下载
func serveFile(c *gin.Context) {
	filename := c.Param("filename")
	
	// 增强安全检查，防止路径遍历攻击
	if strings.Contains(filename, "..") || 
	   strings.Contains(filename, "/") || 
	   strings.Contains(filename, "\\") ||
	   strings.Contains(filename, "\x00") {
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
	
	// 确保任务状态为已完成，并且所有进度都是100%
	task.StatusLock.RLock()
	isCompleted := task.Status == StatusCompleted
	task.StatusLock.RUnlock()
	
	if isCompleted {
		// 确保所有进度达到100%
		ensureTaskCompletion(task)
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
	// 创建两个定时器
	hourlyTicker := time.NewTicker(1 * time.Hour)
	fiveMinTicker := time.NewTicker(5 * time.Minute)
	
	// 清理所有文件的函数
	cleanAll := func() {
		fmt.Printf("执行清理所有临时文件\n")
		entries, err := os.ReadDir("./temp")
		if err == nil {
			for _, entry := range entries {
				entryPath := filepath.Join("./temp", entry.Name())
				info, err := entry.Info()
				if err == nil {
					if info.IsDir() {
						os.RemoveAll(entryPath)
					} else {
						os.Remove(entryPath)
					}
				}
			}
		} else {
			fmt.Printf("清理临时文件失败: %v\n", err)
		}
	}
	
	// 检查文件大小并在需要时清理
	checkSizeAndClean := func() {
		var totalSize int64 = 0
		err := filepath.Walk("./temp", func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			
			// 跳过根目录
			if path == "./temp" {
				return nil
			}
			
			if !info.IsDir() {
				totalSize += info.Size()
			}
			
			return nil
		})
		
		if err != nil {
			fmt.Printf("计算临时文件总大小失败: %v\n", err)
			return
		}
		
		// 如果总大小超过10GB，清理所有文件，防止恶意下载导致磁盘爆满
		if totalSize > 10*1024*1024*1024 {
			fmt.Printf("临时文件总大小超过10GB (当前: %.2f GB)，清理所有文件\n", float64(totalSize)/(1024*1024*1024))
			cleanAll()
		} else {
			fmt.Printf("临时文件总大小: %.2f GB\n", float64(totalSize)/(1024*1024*1024))
		}
	}
	
	// 主循环
	for {
		select {
		case <-hourlyTicker.C:
			// 每小时清理所有文件
			cleanAll()
		case <-fiveMinTicker.C:
			// 每5分钟检查一次总文件大小
			checkSizeAndClean()
		}
	}
}

// 完成任务处理函数，确保进度是100%
func ensureTaskCompletion(task *DownloadTask) {
	// 重新检查一遍所有镜像的进度
	task.ImageLock.RLock()
	completedCount := 0
	totalCount := len(task.Images)
	
	for i, img := range task.Images {
		img.lock.Lock()
		if img.Status == string(StatusCompleted) {
			// 确保进度为100%
			if img.Progress < 100 {
				img.Progress = 100
				fmt.Printf("确保镜像 %d 进度为100%%\n", i)
			}
			completedCount++
		}
		img.lock.Unlock()
	}
	task.ImageLock.RUnlock()
	
	// 更新完成计数
	task.ProgressLock.Lock()
	task.CompletedCount = completedCount
	task.TotalCount = totalCount
	task.ProgressLock.Unlock()
	
	// 如果任务状态为已完成，但计数不匹配，修正计数
	task.StatusLock.RLock()
	isCompleted := task.Status == StatusCompleted
	task.StatusLock.RUnlock()
	
	if isCompleted && completedCount != totalCount {
		task.ProgressLock.Lock()
		task.CompletedCount = totalCount
		task.ProgressLock.Unlock()
		fmt.Printf("任务 %s 状态已完成，强制设置计数为 %d/%d\n", task.ID, totalCount, totalCount)
	}
	
	// 发送最终更新
	sendTaskUpdate(task)
}

// 处理下载单个镜像的输出中的完成标记
func checkForCompletionMarkers(output string) bool {
	// 已知的完成标记
	completionMarkers := []string{
		"Writing manifest to image destination",
		"Copying config complete",
		"Storing signatures",
		"Writing manifest complete",
	}
	
	for _, marker := range completionMarkers {
		if strings.Contains(output, marker) {
			return true
		}
	}
	
	return false
}

// cleanupWebSocketConnections 定期清理无效的WebSocket连接
func cleanupWebSocketConnections() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()
	
	for range ticker.C {
		clientLock.Lock()
		disconnectedClients := make([]string, 0)
		
		for taskID, client := range clients {
			// 检查连接是否仍然活跃
			if err := client.Conn.WriteMessage(websocket.PingMessage, []byte{}); err != nil {
				// 连接已断开，标记待清理
				disconnectedClients = append(disconnectedClients, taskID)
			}
		}
		
		// 清理断开的连接
		for _, taskID := range disconnectedClients {
			if client, exists := clients[taskID]; exists {
				client.CloseOnce.Do(func() {
					close(client.Send)
					client.Conn.Close()
				})
				delete(clients, taskID)
			}
		}
		
		clientLock.Unlock()
		
		if len(disconnectedClients) > 0 {
			fmt.Printf("清理了 %d 个断开的WebSocket连接\n", len(disconnectedClients))
		}
	}
}

// cleanupExpiredTasks 清理过期任务
func cleanupExpiredTasks() {
	ticker := time.NewTicker(30 * time.Minute) // 每30分钟清理一次
	defer ticker.Stop()
	
	for range ticker.C {
		now := time.Now()
		expiredTasks := make([]string, 0)
		
		tasksLock.Lock()
		for taskID, task := range tasks {
			// 清理超过2小时的已完成任务，或超过6小时的任何任务
			isExpired := false
			
			task.StatusLock.RLock()
			taskStatus := task.Status
			task.StatusLock.RUnlock()
			
			// 已完成或失败的任务：2小时后清理
			if (taskStatus == StatusCompleted || taskStatus == StatusFailed) && 
			   now.Sub(task.createTime) > 2*time.Hour {
				isExpired = true
			}
			// 任何任务：6小时后强制清理
			if now.Sub(task.createTime) > 6*time.Hour {
				isExpired = true
			}
			
			if isExpired {
				expiredTasks = append(expiredTasks, taskID)
			}
		}
		
		// 清理过期任务
		for _, taskID := range expiredTasks {
			if task, exists := tasks[taskID]; exists {
				// 安全关闭任务的goroutine
				task.Close()
				
				// 清理临时文件
				if task.TempDir != "" {
					os.RemoveAll(task.TempDir)
				}
				if task.OutputFile != "" && fileExists(task.OutputFile) {
					os.Remove(task.OutputFile)
				}
				
				delete(tasks, taskID)
			}
		}
		tasksLock.Unlock()
		
		if len(expiredTasks) > 0 {
			fmt.Printf("清理了 %d 个过期任务\n", len(expiredTasks))
		}
		
		// 输出统计信息
		tasksLock.Lock()
		activeTaskCount := len(tasks)
		tasksLock.Unlock()
		
		clientLock.Lock()
		activeClientCount := len(clients)
		clientLock.Unlock()
		
		fmt.Printf("当前活跃任务: %d, 活跃WebSocket连接: %d\n", activeTaskCount, activeClientCount)
	}
} 