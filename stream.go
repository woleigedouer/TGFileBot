package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"sync"

	"github.com/amarnathcjd/gogram/telegram"
)

type Reader struct {
	Ctx           context.Context
	Cancel        context.CancelFunc
	Client        *telegram.Client
	Location      telegram.InputFileLocation
	DC            int32
	Start         int64
	End           int64
	ChunkSize     int64
	ContentLength int64
	ChannelID     int64
	MessageID     int32
	Cate          string
	Buffers       chan []byte
	Errs          chan error
	CurrBuffer    []byte
	Pos           int
	ReadBytes     int64
	Once          sync.Once
}

func (reader *Reader) Close() error {
	if reader.Cancel != nil {
		reader.Cancel()
	}
	return nil
}

func newReader(
	ctx context.Context,
	client *telegram.Client,
	location telegram.InputFileLocation,
	dc int32,
	start int64,
	end int64,
	contentLength int64,
	channelID int64,
	messageID int32,
	cate string,
) io.ReadCloser {
	ctx, cancel := context.WithCancel(ctx)
	reader := &Reader{
		Ctx:           ctx,
		Cancel:        cancel,
		Client:        client,
		Location:      location,
		DC:            dc,
		Start:         start,
		End:           end,
		ChunkSize:     int64(1024 * 1024),
		ContentLength: contentLength,
		ChannelID:     channelID,
		MessageID:     messageID,
		Cate:          cate,
		Buffers:       make(chan []byte, 8), // Buffer up to 8MB
		Errs:          make(chan error, 1),
	}
	return reader
}

func (reader *Reader) startFetching() {
	go func() {
		defer close(reader.Buffers)

		workers := 4
		type task struct {
			index  int
			offset int64
		}
		type result struct {
			index int
			data  []byte
			err   error
		}

		tasks := make(chan task, workers)
		results := make(chan result, workers)

		// Start workers
		for i := 0; i < workers; i++ {
			go func() {
				for t := range tasks {
					data, err := reader.fetchChunk(t.offset)
					select {
					case results <- result{index: t.index, data: data, err: err}:
					case <-reader.Ctx.Done():
						return
					}
				}
			}()
		}

		totalChunks := int((reader.End - (reader.Start - (reader.Start % reader.ChunkSize)) + reader.ChunkSize) / reader.ChunkSize)
		if reader.End < reader.Start {
			totalChunks = 0
		}

		go func() {
			defer close(tasks)
			startOffset := reader.Start - (reader.Start % reader.ChunkSize)
			for i := 0; i < totalChunks; i++ {
				select {
				case tasks <- task{index: i, offset: startOffset + int64(i)*reader.ChunkSize}:
				case <-reader.Ctx.Done():
					return
				}
			}
		}()

		// Collector
		pendingResults := make(map[int][]byte)
		nextIndex := 0
		for nextIndex < totalChunks {
			select {
			case res := <-results:
				if res.err != nil {
					select {
					case reader.Errs <- res.err:
					default:
					}
					return
				}
				pendingResults[res.index] = res.data
				for {
					content, ok := pendingResults[nextIndex]
					if !ok {
						break
					}

					// Handle cuts for first and last chunks
					if totalChunks == 1 {
						firstCut := reader.Start % reader.ChunkSize
						lastCut := (reader.End % reader.ChunkSize) + 1
						if int64(len(content)) > lastCut {
							content = content[:lastCut]
						}
						if int64(len(content)) > firstCut {
							content = content[firstCut:]
						} else {
							content = []byte{}
						}
					} else if nextIndex == 0 {
						firstCut := reader.Start % reader.ChunkSize
						if int64(len(content)) > firstCut {
							content = content[firstCut:]
						} else {
							content = []byte{}
						}
					} else if nextIndex == totalChunks-1 {
						lastCut := (reader.End % reader.ChunkSize) + 1
						if int64(len(content)) > lastCut {
							content = content[:lastCut]
						}
					}

					if len(content) > 0 {
						select {
						case reader.Buffers <- content:
						case <-reader.Ctx.Done():
							return
						}
					}
					delete(pendingResults, nextIndex)
					nextIndex++
				}
			case <-reader.Ctx.Done():
				return
			}
		}
	}()
}

func (reader *Reader) fetchChunk(offset int64) ([]byte, error) {
	params := &telegram.UploadGetFileParams{
		Location: reader.Location,
		Offset:   offset,
		Limit:    int32(reader.ChunkSize),
	}

	res, err := reader.Client.UploadGetFile(params)
	if err != nil {
		// Attempt refreshcate
		if reader.ChannelID != 0 && reader.MessageID != 0 && reader.Cate != "bot" {
			log.Printf("Chunk fetch failed, attempting refresh: %v", err)
			ms, refreshErr := reader.Client.GetMessages(reader.ChannelID, &telegram.SearchOption{IDs: []int32{reader.MessageID}})
			if refreshErr == nil && len(ms) > 0 {
				src := ms[0]
				if src.IsMedia() {
					if loc, dc, _, _, locErr := telegram.GetFileLocation(src.Media(), telegram.FileLocationOptions{}); locErr == nil {
						reader.Location = loc
						reader.DC = dc
						params.Location = loc
						res, err = reader.Client.UploadGetFile(params)
					}
				}
			}
		}
	}

	if err != nil {
		return nil, err
	}

	if obj, ok := res.(*telegram.UploadFileObj); ok {
		return obj.Bytes, nil
	}
	return nil, fmt.Errorf("unexpected response type: %T", res)
}

func (reader *Reader) Read(content []byte) (num int, err error) {
	reader.Once.Do(reader.startFetching)

	if reader.ReadBytes >= reader.ContentLength {
		return 0, io.EOF
	}

	if reader.Pos >= len(reader.CurrBuffer) {
		select {
		case data, ok := <-reader.Buffers:
			if !ok {
				select {
				case err := <-reader.Errs:
					return 0, err
				default:
					return 0, io.EOF
				}
			}
			reader.CurrBuffer = data
			reader.Pos = 0
		case err := <-reader.Errs:
			return 0, err
		case <-reader.Ctx.Done():
			return 0, reader.Ctx.Err()
		}
	}

	num = copy(content, reader.CurrBuffer[reader.Pos:])
	reader.Pos += num
	reader.ReadBytes += int64(num)
	return num, nil
}
