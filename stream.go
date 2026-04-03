package main

import (
	"context"
	"fmt"
	"log"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/amarnathcjd/gogram/telegram"
)

// Task 代表一个下载分片任务
type Task struct {
	Offset       int64      // 任务在分片内的偏移量
	ContentStart int64      // 任务请求的数据起点（绝对位置）
	ContentEnd   int64      // 任务请求的数据终点（绝对位置）
	Version      int64      // 任务对应的文件版本号，用于处理引用过期
	Error        error      // 下载过程中产生的错误
	Done         *bool      // 标记任务是否完成
	Cond         *sync.Cond // 用于通知等待该任务完成的协程
	Content      *[]byte    // 下载到的二进制内容
}

// Stream 结构体用于管理大文件的并发下载和流式传输
type Stream struct {
	Ctx          context.Context        // 上下文，用于取消下载
	Client       *telegram.Client       // Gogram 客户端实例
	Src          *telegram.MessageMedia // Telegram 消息媒体源
	Workers      int                    // 下载并发协程数
	MID          int32                  // Telegram 消息 ID
	CID          int64                  // Telegram 频道/会话 ID
	ChunkSize    int64                  // 每个下载分片的大小（通常 512KB 或 1MB）
	ContentSize  int64                  // 文件的总大小
	MaxCacheSize int64                  // 最大缓存大小
	TaskStart    *int64                 // 当前已分配任务的下载起点
	TaskEnd      *int64                 // 当前已分配任务的下载终点
	FileName     string                 // 文件名
	Error        error                  // 整个流运行过程中的错误
	Count        atomic.Int64           // 当前正在运行的协程数量
	Version      atomic.Int64           // 文件版本号，因引用过期刷新后递增
	Mutex        *sync.Mutex            // 用于保护并发安全
	Tasks        chan *Task             // 任务管道，用于向工作协程分发下载任务
}

// newTask 初始化并返回一个 Task 对象
func newTask() *Task {
	return &Task{
		Error:   nil,
		Done:    new(bool),
		Content: new([]byte),
		Cond:    sync.NewCond(new(sync.Mutex)),
	}
}

// newStream 初始化并返回一个 Stream 对象，负责管理特定文件的流式下载
func newStream(ctx context.Context, client *telegram.Client, media telegram.MessageMedia, workers int, mid int32, cid int64, name string) *Stream {
	// 根据并发数动态调整分片大小
	chunkSize := int64(512 * 1024)
	if workers == 1 {
		chunkSize = 1024 * 1024
	}
	// 默认 32MB 缓存
	maxCacheSize := infos.Conf.MaxSize
	if maxCacheSize == 0 {
		maxCacheSize = 32 * 1024 * 1024
	}
	// 计算任务管道的容量
	maxChans := int(maxCacheSize / chunkSize)
	if maxChans == 0 {
		maxChans = 1
	}
	return &Stream{
		Ctx:          ctx,
		Client:       client,
		Src:          &media,
		Workers:      workers,
		FileName:     name,
		MID:          mid,
		CID:          cid,
		ChunkSize:    512 * 1024, // 这里设置了固定值，可以根据需要调整
		MaxCacheSize: maxCacheSize,
		Tasks:        make(chan *Task, maxChans),
		Mutex:        new(sync.Mutex),
		TaskStart:    new(int64),
		TaskEnd:      new(int64),
		Count:        atomic.Int64{},
		Version:      atomic.Int64{},
	}
}

// start 启动工作协程开始下载任务
func (stream *Stream) start(contentStart, contentEnd int64) {
	// 计算任务总数
	maxTasks := int(math.Ceil(float64(stream.ContentSize) / float64(stream.ChunkSize)))
	// 限制并发协程数不超过配置值
	if maxTasks > stream.Workers {
		maxTasks = stream.Workers
	}

	for numTask := 1; numTask <= maxTasks; numTask++ {
		stream.Count.Add(1)
		go func(numTask int) {
			defer stream.Count.Add(-1)
			stream.download(numTask, contentStart, contentEnd)
		}(numTask)
	}
}

// download 是工作协程的核心逻辑，负责循环领取并下载文件分片
func (stream *Stream) download(numTask int, contentStart, contentEnd int64) {
	log.Printf("协程%d开始下载: cid=%d, mid=%d, fileName=%s", numTask, stream.CID, stream.MID, stream.FileName)
	defer log.Printf("协程%d结束下载: cid=%d, mid=%d, fileName=%s", numTask, stream.CID, stream.MID, stream.FileName)
	for {
		stream.Mutex.Lock()
		task := newTask()
		// 计算当前任务的下载范围
		if *stream.TaskStart == 0 {
			task.ContentStart = contentStart
		} else {
			task.ContentStart = *stream.TaskStart
		}
		// 处理偏移量，确保分片按照 ChunkSize 对齐，提高 Telegram 服务器读取效率
		task.Offset = task.ContentStart - (task.ContentStart/stream.ChunkSize)*stream.ChunkSize
		task.ContentStart = task.ContentStart - task.Offset
		task.ContentEnd = task.ContentStart + stream.ChunkSize - 1

		// 如果下载起点超过了请求范围，则结束下载
		if task.ContentStart > contentEnd {
			stream.Mutex.Unlock()
			return
		}

		// 将任务推入管道供下游消费（HTTP 响应层）
		select {
		case <-stream.Ctx.Done():
			stream.Mutex.Unlock()
			return
		default:
			select {
			case <-stream.Ctx.Done():
				stream.Mutex.Unlock()
				return
			case stream.Tasks <- task:
				// 成功发送任务
			default:
				// 任务队列已满，这里保持阻塞直到能存入或取消
				log.Printf("任务队列已满: cid=%d, mid=%d, fileName=%s", stream.CID, stream.MID, stream.FileName)
				stream.Tasks <- task
			}
		}
		// 更新流的状态，为下一个任务做准备
		*stream.TaskStart = task.ContentEnd + 1
		*stream.TaskEnd = *stream.TaskStart + stream.ChunkSize - 1
		stream.Mutex.Unlock()

		// 尝试下载该分片，最多重试 3 次
		for num := 1; num <= 3; num++ {
			version := stream.Version.Load()
			// 调用 Gogram 接口从 Telegram 下载特定范围的文件块
			content, fileName, err := stream.Client.DownloadChunk(*stream.Src, int(task.ContentStart), int(task.ContentEnd), int(stream.ChunkSize), false, stream.Ctx, 90*time.Second)
			if err != nil {
				switch {
				case telegram.MatchError(err, "FILE_REFERENCE_EXPIRED"):
					// 如果报错文件引用过期，则调用 refresh 重新获取消息并更新引用
					log.Printf("文件引用已过期: cid=%d, mid=%d, version=%d, fileName=%s, numTask=%d", stream.CID, stream.MID, version, fileName, numTask)
					if err := stream.refresh(numTask, version); err != nil {
						task.Error = err
						task.Cond.L.Lock()
						*task.Done = true
						task.Cond.Signal()
						task.Cond.L.Unlock()
						return
					}
					// 刷新成功后继续重试当前分片
					continue
				}
				// 遇到其他不可恢复错误，终止下载
				task.Error = err
				task.Cond.L.Lock()
				*task.Done = true
				task.Cond.Signal()
				task.Cond.L.Unlock()
				return
			} else {
				task.Cond.L.Lock()
				// 根据初始偏移量截取内容
				content = content[task.Offset:]
				// 裁剪末尾：最后一个分片可能超出实际请求范围（contentEnd），
				// 防止写入 HTTP 响应时超过声明的 Content-Length
				if task.ContentEnd > contentEnd {
					overshoot := task.ContentEnd - contentEnd
					if int64(len(content)) > overshoot {
						content = content[:int64(len(content))-overshoot]
					}
					task.ContentEnd = contentEnd
				}
				if task.Content == nil {
					task.Content = &content
				} else {
					*task.Content = content
				}
				*task.Done = true
				task.Cond.Signal() // 唤醒等待此分片的协程
				task.Cond.L.Unlock()
				break
			}
		}
	}
}

// clean 清理未完成或已读取的任务管道，防止内存泄漏
func (stream *Stream) clean() {
	// 创建计时器，避免死循环
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()

	// 循环排出管道内容
	for {
		select {
		case task := <-stream.Tasks:
			if task != nil {
				task.Content = nil
				task = nil
			}
			// 重置计时器
			if !timeout.Stop() {
				<-timeout.C
			}
			timeout.Reset(5 * time.Second)
		case <-timeout.C:
			// 超时退出
			return
		default:
			// 任务队列已清空
			return
		}
	}
}

// refresh 重新从 Telegram 获取消息以更新文件引用 (file_reference)
// 分布式锁/互斥锁确保并发情况下只刷新一次
func (stream *Stream) refresh(numTask int, version int64) (err error) {
	stream.Mutex.Lock()
	defer stream.Mutex.Unlock()

	// 如果版本号已经变了，说明其他协程已经完成了刷新
	if version != stream.Version.Load() {
		log.Printf("文件引用已刷新, 直接使用新版本: cid=%d, mid=%d, numTask=%d, version=%d, newVersion=%d", stream.CID, stream.MID, numTask, version, stream.Version.Load())
		return
	}

	// 重新获取消息
	ms, err := stream.Client.GetMessages(stream.CID, &telegram.SearchOption{IDs: []int32{stream.MID}})
	if err != nil {
		stream.Error = err
		return err
	}
	if len(ms) == 0 {
		err = fmt.Errorf("获取消息失败: cid=%d, mid=%d, err=未获取到消息", stream.CID, stream.MID)
		stream.Error = err
		return err
	}
	src := ms[0]

	// 确保消息依然包含媒体内容
	if !src.IsMedia() {
		err = fmt.Errorf("消息不包含媒体: cid=%d, mid=%d", stream.CID, stream.MID)
		stream.Error = err
		return err
	}
	// 更新流中的媒体引用
	*stream.Src = src.Media()
	stream.Version.Add(1) // 递增版本号
	log.Printf("文件引用已刷新: cid=%d, mid=%d, numTask=%d, version=%d, newVersion=%d", stream.CID, stream.MID, numTask, version, stream.Version.Load())
	return nil
}
