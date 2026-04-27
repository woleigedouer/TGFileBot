package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/amarnathcjd/gogram/telegram"
)

// Task 代表一个下载分片任务
type Task struct {
	Offset       int64         // 任务在分片内的偏移量
	ContentStart int64         // 任务请求的数据起点（绝对位置）
	ContentEnd   int64         // 任务请求的数据终点（绝对位置）
	Version      int64         // 任务对应的文件版本号, 用于处理引用过期
	Error        error         // 下载过程中产生的错误
	Done         chan struct{} // 用于通知该任务完成
	Content      []byte        // 下载到的二进制内容
}

// Stream 结构体用于管理大文件的并发下载和流式传输
type Stream struct {
	Ctx          context.Context        // 上下文, 用于取消下载
	Client       *telegram.Client       // Gogram 客户端实例
	Src          *telegram.MessageMedia // Telegram 消息媒体源
	Workers      int                    // 下载并发协程数
	MID          int32                  // Telegram 消息 ID
	CID          int64                  // Telegram 频道/会话 ID
	ChunkSize    int64                  // 每个下载分片的大小（通常 512KB 或 1MB）
	ContentSize  int64                  // 文件的总大小
	MaxCacheSize int64                  // 最大缓存大小
	HeadSize     int64                  // 头部缓存大小
	TailSize     int64                  // 尾部缓存大小
	TaskStart    *int64                 // 当前已分配任务的下载起点
	TaskEnd      *int64                 // 当前已分配任务的下载终点
	FileName     string                 // 文件名
	Error        error                  // 整个流运行过程中的错误
	Count        atomic.Int64           // 当前正在运行的协程数量
	Version      atomic.Int64           // 文件版本号, 因引用过期刷新后递增
	Mutex        *sync.Mutex            // 用于保护并发安全
	Tasks        chan *Task             // 任务管道, 用于向工作协程分发下载任务
}

// newTask 初始化并返回一个 Task 对象
func newTask() *Task {
	return &Task{
		Error: nil,
		Done:  make(chan struct{}),
	}
}

// newStream 初始化并返回一个 Stream 对象, 负责管理特定文件的流式下载
func newStream(ctx context.Context, client *telegram.Client, media telegram.MessageMedia, workers int, mid int32, cid, contentSize int64, name string) *Stream {
	// 根据并发数动态调整分片大小
	chunkSize := int64(1 * 1024 * 1024)
	// 默认 32MB 缓存
	maxCacheSize := infos.Conf.MaxSize
	if maxCacheSize == 0 {
		maxCacheSize = 32 * 1024 * 1024
	}
	headSize := maxCacheSize / 2
	if headSize > 8*1024*1024 {
		headSize = 8 * 1024 * 1024
	}
	tailSize := maxCacheSize / 2
	if tailSize > 8*1024*1024 {
		tailSize = 8 * 1024 * 1024
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
		ContentSize:  contentSize,
		ChunkSize:    chunkSize, // 这里设置了固定值, 可以根据需要调整
		MaxCacheSize: maxCacheSize,
		HeadSize:     headSize,
		TailSize:     tailSize,
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
	maxTasks := int((contentEnd - contentStart + 1 + stream.ChunkSize - 1) / stream.ChunkSize)
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

// download 是工作协程的核心逻辑, 负责循环领取并下载文件分片
func (stream *Stream) download(numTask int, contentStart, contentEnd int64) {
	cacheKey := mediaCacheKey(stream.CID, stream.MID)
	for {
		stream.Mutex.Lock()
		task := newTask()
		// 计算当前任务的下载范围
		if *stream.TaskStart == 0 {
			task.ContentStart = contentStart
		} else {
			task.ContentStart = *stream.TaskStart
		}
		// 处理偏移量, 确保分片按照 ChunkSize 对齐, 提高 Telegram 服务器读取效率
		task.Offset = task.ContentStart - (task.ContentStart/stream.ChunkSize)*stream.ChunkSize
		task.ContentStart = task.ContentStart - task.Offset
		task.ContentEnd = task.ContentStart + stream.ChunkSize - 1

		// 如果下载起点超过了请求范围, 则结束下载
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
				// 任务队列已满, 这里保持阻塞直到能存入或取消
				log.Printf("任务队列已满: cid=%d, mid=%d, name=%s", stream.CID, stream.MID, stream.FileName)
				stream.Tasks <- task
			}
		}
		// 更新流的状态, 为下一个任务做准备
		*stream.TaskStart = task.ContentEnd + 1
		*stream.TaskEnd = *stream.TaskStart + stream.ChunkSize - 1
		stream.Mutex.Unlock()

		// 尝试下载该分片
		maxCount := 3
		if task.ContentStart < int64(1048576) || (contentEnd-task.ContentEnd)/contentEnd*1000 < 2 {
			maxCount = 6
		}

		maxWait := 3
		for num := 1; num <= maxCount; num++ {
			// 从缓存读取
			found := stream.handleCache(task, cacheKey, contentEnd)
			if found {
				break
			}

			// 下载
			if waitUntil := infos.WaitUntil.Load(); waitUntil > 0 {
				if remaining := time.Until(time.Unix(waitUntil, 0)); remaining > 0 {
					log.Printf("协程%d: 检测到FloodWait, 等待 %.2f 秒", numTask, remaining.Seconds())
					time.Sleep(remaining)
				}
			}
			version := stream.Version.Load()
			stream.Mutex.Lock()
			src := *stream.Src
			stream.Mutex.Unlock()

			// 调用 Gogram 接口从 Telegram 下载特定范围的文件块
			// 首次请求给足冷启动时间, 后续固定超时
			timeout := 10 * time.Second
			if num == 1 {
				timeout = 15 * time.Second
			}

			content, fileName, err := stream.Client.DownloadChunk(src, int(task.ContentStart), int(task.ContentEnd), int(stream.ChunkSize), false, stream.Ctx, timeout)
			if err != nil {
				switch {
				// 如果 context 已经关闭（手动取消或整体超时），则彻底停止任务
				case stream.Ctx.Err() != nil, errors.Is(err, context.Canceled):
					task.Error = errors.New("已取消下载任务")
					close(task.Done)
					return
				case telegram.MatchError(err, "FILE_REFERENCE_EXPIRED"):
					// 如果报错文件引用过期, 则调用 refresh 重新获取消息并更新引用
					log.Printf("文件引用已过期: cid=%d, mid=%d, version=%d, name=%s, numTask=%d", stream.CID, stream.MID, version, fileName, numTask)
					if err := stream.refresh(numTask, version); err != nil {
						task.Error = err
						close(task.Done)
						return
					}
					// 刷新成功后继续重试当前分片
					continue
				default:
					if matches := infos.Rex.FindStringSubmatch(err.Error()); len(matches) > 0 {
						wait := 3
						if len(matches) > 1 {
							for _, match := range matches[1:] {
								if match != "" {
									if value, e := strconv.Atoi(match); e == nil {
										wait = value
										break
									}
								}
							}
						}
						log.Printf("协程%d: 访问太过频繁, 等待 %d 秒后重试", numTask, wait+1)
						waitUntil := time.Now().Add(time.Duration(wait+1) * time.Second)
						if currentWait := infos.WaitUntil.Load(); waitUntil.Unix() > currentWait {
							infos.WaitUntil.Store(waitUntil.Unix())
						}
						time.Sleep(time.Duration(wait+1) * time.Second)
						if num >= maxCount && maxWait > 0 {
							num = maxCount - 1
							maxWait--
						}
						continue
					} else {
						if num < maxCount {
							backoff := time.Duration(1<<num) * time.Second // 2s, 4s, 8s...
							log.Printf("协程%d: 网络错误重试 %d/%d, 等待 %v. 错误: %+v", numTask, num, maxCount, backoff, err)
							time.Sleep(backoff)
							continue
						} else {
							task.Error = err
							close(task.Done)
							return
						}
					}
				}
			}

			// 缓存
			if stream.HeadSize > 0 && stream.TailSize > 0 {
				infos.Mutex.Lock()
				switch {
				case task.ContentStart <= stream.HeadSize && task.ContentEnd <= stream.HeadSize:
					if values, ok := infos.HeadCache[cacheKey]; ok {
						values.Time = time.Now() // 指针类型，直接修改即生效
						found := false
						for _, c := range values.Contents {
							if c.Start == task.ContentStart && c.End == task.ContentEnd {
								found = true
								break
							}
						}
						if !found {
							// maxChunks = HeadSize / ChunkSize, 即头部最多能存几个分片
							maxChunks := int((stream.HeadSize+stream.ChunkSize-1)/stream.ChunkSize) + 1
							lenContents := len(values.Contents)
							if lenContents >= maxChunks {
								// 淡出策略：保留靠近文件头的分片, 删除 Start 最大的（距开头最远、最冗余）
								maxNum := 0
								for num := 1; num < lenContents; num++ {
									if values.Contents[num].Start > values.Contents[maxNum].Start {
										maxNum = num
									}
								}
								values.Contents[maxNum] = values.Contents[lenContents-1]
								values.Contents[lenContents-1] = MediaContent{}
								values.Contents = values.Contents[:lenContents-1]
							}
							values.Contents = append(values.Contents, MediaContent{
								Start:   task.ContentStart,
								End:     task.ContentEnd,
								Content: content,
							})
						}
					}
				case task.ContentStart >= stream.ContentSize-stream.TailSize:
					if values, ok := infos.TailCache[cacheKey]; ok {
						values.Time = time.Now() // 指针类型，直接修改即生效
						found := false
						for _, c := range values.Contents {
							if c.Start == task.ContentStart && c.End == task.ContentEnd {
								found = true
								break
							}
						}
						if !found {
							// maxChunks = TailSize / ChunkSize, 即尾部最多能存几个分片
							maxChunks := int((stream.TailSize+stream.ChunkSize-1)/stream.ChunkSize) + 1
							lenContents := len(values.Contents)
							if lenContents >= maxChunks {
								// 淡出策略：保留靠近文件尾的分片, 删除 Start 最小的（距结尾最远、最冗余）
								minNum := 0
								for num := 1; num < lenContents; num++ {
									if values.Contents[num].Start < values.Contents[minNum].Start {
										minNum = num
									}
								}
								values.Contents[minNum] = values.Contents[lenContents-1]
								values.Contents[lenContents-1] = MediaContent{}
								values.Contents = values.Contents[:lenContents-1]
							}
							values.Contents = append(values.Contents, MediaContent{
								Start:   task.ContentStart,
								End:     task.ContentEnd,
								Content: content,
							})
						}
					}
				}
				infos.Mutex.Unlock()
			}

			task.handleContent(content, contentEnd)
			break
		}

		// 检查循环退出后是否成功
		if task.Content == nil && task.Error == nil {
			task.Error = fmt.Errorf("下载失败, 已达最大重试次数: %d", maxCount)
			close(task.Done)
			return
		}
	}
}

// clean 清理未完成或已读取的任务管道, 防止内存泄漏
func (stream *Stream) clean() {
	// 创建计时器, 避免死循环
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()

	for {
		select {
		case task := <-stream.Tasks:
			if task != nil {
				// 等待任务完成后再释放, 避免与下载协程产生 data race
				// 使用 timer 替代 time.After 避免内存泄漏
				taskTimer := time.NewTimer(5 * time.Second)
				select {
				case <-task.Done:
					if !taskTimer.Stop() {
						<-taskTimer.C
					}
				case <-taskTimer.C:
					log.Printf("清理任务时遇到阻塞过长, 强制丢弃: start=%d end=%d", task.ContentStart, task.ContentEnd)
				}
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

	// 如果版本号已经变了, 说明其他协程已经完成了刷新
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

func (task *Task) handleContent(content []byte, contentEnd int64) {
	// 根据初始偏移量截取内容
	content = content[task.Offset:]
	actualStart := task.ContentStart + task.Offset

	// 裁剪末尾：最后一个分片可能超出实际请求范围（contentEnd）,
	// 防止写入 HTTP 响应时超过声明的 Content-Length
	if task.ContentEnd > contentEnd {
		wantedLen := contentEnd - actualStart + 1
		if wantedLen > 0 && int64(len(content)) > wantedLen {
			content = content[:wantedLen]
		}
		task.ContentEnd = contentEnd
	}

	task.Content = content
	close(task.Done) // 唤醒等待此分片的协程
}

func (stream *Stream) handleCache(task *Task, cacheKey string, contentEnd int64) (found bool) {
	infos.Mutex.Lock()
	defer infos.Mutex.Unlock()
	// 从缓存读取
	switch {
	case task.ContentStart <= stream.HeadSize && task.ContentEnd <= stream.HeadSize:
		if values, ok := infos.HeadCache[cacheKey]; ok {
			for _, value := range values.Contents {
				if value.Start == task.ContentStart && value.End == task.ContentEnd {
					log.Printf("命中头部缓存: cid=%d, mid=%d, name=%s, start=%d, end=%d", stream.CID, stream.MID, stream.FileName, task.ContentStart, task.ContentEnd)
					task.handleContent(value.Content, contentEnd)
					return true
				}
			}
		} else {
			evictOldestCache(infos.HeadCache, 4)
			contents := make([]MediaContent, 0, int(stream.HeadSize/stream.ChunkSize))
			infos.HeadCache[cacheKey] = &MediaCache{Contents: contents, Time: time.Now()}
			log.Printf("头部缓存已初始化: cid=%d, mid=%d", stream.CID, stream.MID)
			return false
		}
	case task.ContentStart >= stream.ContentSize-stream.TailSize:
		if values, ok := infos.TailCache[cacheKey]; ok {
			for _, value := range values.Contents {
				if value.Start == task.ContentStart && value.End == task.ContentEnd {
					log.Printf("命中尾部缓存: cid=%d, mid=%d, name=%s, start=%d, end=%d", stream.CID, stream.MID, stream.FileName, task.ContentStart, task.ContentEnd)
					task.handleContent(value.Content, contentEnd)
					return true
				}
			}
		} else {
			evictOldestCache(infos.TailCache, 4)
			contents := make([]MediaContent, 0, int(stream.TailSize/stream.ChunkSize))
			infos.TailCache[cacheKey] = &MediaCache{Contents: contents, Time: time.Now()}
			log.Printf("尾部缓存已初始化: cid=%d, mid=%d", stream.CID, stream.MID)
			return false
		}
	}
	return false
}
