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

type Task struct {
	Offset       int64      // 任务偏移
	ContentStart int64      // 任务起点
	ContentEnd   int64      // 任务终点
	Version      int64      // 版本号
	Error        error      // 错误
	Done         *bool      // 当前任务完成
	Cond         *sync.Cond // 条件变量
	Content      *[]byte    // 任务内容
}

type Stream struct {
	Ctx          context.Context        // 上下文
	Client       *telegram.Client       // 客户端
	Src          *telegram.MessageMedia // 消息媒体
	Workers      int                    // 并发数
	MID          int32                  // 消息ID
	CID          int64                  // 频道ID
	ChunkSize    int64                  // 任务块大小
	ContentSize  int64                  // 内容大小
	MaxCacheSize int64                  // 缓存最大值
	TaskStart    *int64                 // 任务起点
	TaskEnd      *int64                 // 任务终点
	Error        error                  // 错误
	Count        atomic.Int64           // 任务数量
	Version      atomic.Int64           // 版本号
	Mutex        *sync.Mutex            // 互斥锁
	Tasks        chan *Task             // 任务队列
}

func newTask() *Task {
	return &Task{
		Error:   nil,
		Done:    new(bool),
		Content: new([]byte),
		Cond:    sync.NewCond(new(sync.Mutex)),
	}
}

func newStream(ctx context.Context, client *telegram.Client, media telegram.MessageMedia, workers int, mid int32, cid int64) *Stream {
	return &Stream{
		Ctx:          ctx,
		Client:       client,
		Src:          &media,
		Workers:      workers,
		MID:          mid,
		CID:          cid,
		ChunkSize:    512 * 1024,
		MaxCacheSize: 32 * 1024 * 1024,
		Tasks:        make(chan *Task, 256),
		Mutex:        new(sync.Mutex),
		TaskStart:    new(int64),
		TaskEnd:      new(int64),
		Count:        atomic.Int64{},
		Version:      atomic.Int64{},
	}
}

func (stream *Stream) start(contentStart, contentEnd int64) {
	maxTasks := int(math.Ceil(float64(stream.ContentSize) / float64(stream.ChunkSize)))
	if maxTasks > stream.Workers {
		maxTasks = stream.Workers
	}
	if stream.Workers == 1 {
		stream.ChunkSize = 1024 * 1024
	}

	for numTask := 1; numTask <= maxTasks; numTask++ {
		stream.Count.Add(1)
		go func() {
			defer stream.Count.Add(-1)
			stream.download(contentStart, contentEnd)
		}()
	}
}

func (stream *Stream) download(contentStart, contentEnd int64) {
	for {
		start := time.Now()
		stream.Mutex.Lock()
		task := newTask()
		if *stream.TaskStart == 0 {
			task.ContentStart = contentStart
		} else {
			task.ContentStart = *stream.TaskStart
		}
		task.Offset = task.ContentStart - (task.ContentStart/stream.ChunkSize)*stream.ChunkSize
		task.ContentStart = task.ContentStart - task.Offset
		task.ContentEnd = task.ContentStart + stream.ChunkSize - 1

		if task.ContentStart > contentEnd {
			stream.Mutex.Unlock()
			return
		}

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
				// 任务队列已满
				log.Printf("任务队列已满: cid=%d, mid=%d", stream.CID, stream.MID)
				stream.Tasks <- task
			}
		}
		// 更新任务起点和终点
		*stream.TaskStart = task.ContentEnd + 1
		*stream.TaskEnd = *stream.TaskStart + stream.ChunkSize - 1
		stream.Mutex.Unlock()

		for num := 1; num <= 3; num++ {
			version := stream.Version.Load()
			content, fileName, err := stream.Client.DownloadChunk(*stream.Src, int(task.ContentStart), int(task.ContentEnd), int(stream.ChunkSize), stream.Ctx)
			if err != nil {
				switch {
				case telegram.MatchError(err, "FILE_REFERENCE_EXPIRED"):
					// 4. 检测 FILE_REFERENCE_EXPIRED 错误，重试
					log.Printf("文件引用已过期: cid=%d, mid=%d, version=%d", stream.CID, stream.MID, version)
					stream.refresh(version)
					continue
				}
				task.Error = err
				task.Cond.L.Lock()
				*task.Done = true
				task.Cond.Signal()
				task.Cond.L.Unlock()
				return
			} else {
				duration := time.Since(start)
				log.Printf("下载完成: cid=%d, mid=%d, start=%d, end=%d, content=%d, fileName=%s, duration=%.2fs", stream.CID, stream.MID, task.ContentStart, task.ContentEnd, len(content), fileName, duration.Seconds())
				task.Cond.L.Lock()
				content = content[task.Offset:]
				if task.Content == nil {
					task.Content = &content
				} else {
					*task.Content = content
				}
				*task.Done = true
				task.Cond.Signal()
				task.Cond.L.Unlock()
			}
		}
	}
}

func (stream *Stream) clean() {
	// 创建计时器
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()

	// 清理任务队列
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

func (stream *Stream) refresh(version int64) {
	stream.Mutex.Lock()
	defer stream.Mutex.Unlock()

	if version != stream.Version.Load() {
		log.Printf("文件引用已刷新: cid=%d, mid=%d, version=%d, newVersion=%d", stream.CID, stream.MID, version, stream.Version.Load())
		return
	}

	ms, err := stream.Client.GetMessages(stream.CID, &telegram.SearchOption{IDs: []int32{stream.MID}})
	if err != nil {
		stream.Error = fmt.Errorf("获取消息失败: cid=%d, mid=%d, err=%v", stream.CID, stream.MID, err)
		return
	}
	if len(ms) == 0 {
		stream.Error = fmt.Errorf("获取消息失败: cid=%d, mid=%d, err=未获取到消息", stream.CID, stream.MID)
		return
	}
	src := ms[0]

	// 确保消息包含媒体文件
	if !src.IsMedia() {
		stream.Error = fmt.Errorf("消息不包含媒体: cid=%d, mid=%d", stream.CID, stream.MID)
		return
	}
	*stream.Src = src.Media()
	stream.Version.Add(1)
	log.Printf("文件引用已刷新: cid=%d, mid=%d, version=%d", stream.CID, stream.MID, stream.Version.Load())
}
