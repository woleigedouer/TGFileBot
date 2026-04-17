package main

import (
	"bufio"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/amarnathcjd/gogram/telegram"
)

// handleTime 将秒数格式化为人类可读的时间字符串
func handleTime(secs uint64) string {
	if secs > 86400 {
		return fmt.Sprintf("%dd %dh %dm %ds", secs/86400, (secs%86400)/3600, (secs%3600)/60, secs%60)
	} else if secs > 3600 {
		return fmt.Sprintf("%dh %dm %ds", secs/3600, (secs%3600)/60, secs%60)
	} else if secs > 60 {
		return fmt.Sprintf("%dm %ds", secs/60, secs%60)
	}
	return fmt.Sprintf("%ds", secs)
}

// formatFileSize 将字节数格式化为 B/K/M 单位的字符串
func formatFileSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%dB", size)
	}

	units := []string{"B", "K", "M"}
	var count int
	var result = float64(size)

	for result >= unit && count < len(units)-1 {
		result /= unit
		count++
	}

	// 如果是整数则不保留小数, 有小数则保留两位
	if result == float64(int64(result)) {
		return fmt.Sprintf("%.0f%s", result, units[count])
	}
	return fmt.Sprintf("%.2f%s", result, units[count])
}

// convertMaxSize 将用户输入的缓存大小字符串（如 "32M"）转换为字节数
func convertMaxSize(str string) int64 {
	var unit int64 = 1
	src := strings.ToUpper(str)
	switch {
	case strings.HasSuffix(src, "B"), regexp.MustCompile(`\d$`).MatchString(src):
		src = strings.TrimSuffix(src, "B")
		unit = 1
	case strings.HasSuffix(src, "K"):
		src = strings.TrimSuffix(src, "K")
		unit = 1024
	case strings.HasSuffix(src, "M"):
		src = strings.TrimSuffix(src, "M")
		unit = 1024 * 1024
	default:
		return int64(128 * 1024)
	}

	value, err := strconv.ParseInt(src, 10, 64)
	if err != nil {
		return int64(128 * 1024)
	}
	return value * unit
}

// extractContent 从字符串中提取正文与可选的行数参数
// 例如 "error 20" 返回 ("error", &20)；"error" 返回 ("error", nil)；"20" 返回 ("", &20)
func extractContent(src string) (string, *int) {
	src = strings.TrimSpace(src)

	// 1. 如果整个字符串就是一个数字
	if num, err := strconv.Atoi(src); err == nil {
		return "", &num
	}

	// 2. 寻找主体部分最后一个空格
	count := strings.LastIndexByte(src, ' ')
	if count == -1 {
		return src, nil
	}

	// 3. 判断最后一个空格后面那一截是不是数字
	content := src[count+1:]
	if num, err := strconv.Atoi(content); err == nil {
		return src[:count], &num
	}

	return src, nil
}

// readLastLines 读取日志文件中匹配 src 正则的最后 count 行
func readLastLines(filePath, src string, count int) (lines []string, err error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := file.Close(); err != nil {
			log.Printf("关闭文件失败: %+v", err)
		}
	}()

	re := regexp.MustCompile(src)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if re.MatchString(scanner.Text()) {
			lines = append(lines, scanner.Text())
		}
		// 超过行数限制后, 舍弃旧行（滑动窗口效果）
		if len(lines) > count {
			lines = lines[1:]
		}
	}
	if err := scanner.Err(); err != nil {
		return lines, err
	}
	return lines, nil
}

// cleanFiles 清理指定类型的 session 或 cache 文件
func cleanFiles(realm CleanRealm) {
	switch strings.ToLower(realm.Realm) {
	case "cache":
		if files, err := os.ReadDir(infos.FilesPath); err == nil {
			src := fmt.Sprintf("%s_", strings.ToLower(realm.Cate))
			for _, file := range files {
				name := strings.TrimSpace(file.Name())
				if !file.IsDir() && strings.HasPrefix(name, src) && strings.HasSuffix(name, ".cache") {
					if realm.Filter {
						if realm.ID != "" && realm.ID != "0" {
							currentID := strings.TrimSuffix(strings.TrimPrefix(name, src), ".cache")
							if currentID != realm.ID {
								if err := os.Remove(filepath.Join(infos.FilesPath, name)); err != nil {
									log.Printf("删除缓存文件失败: %v", err)
								}
							}
						}
					} else {
						if err := os.Remove(filepath.Join(infos.FilesPath, name)); err != nil {
							log.Printf("删除缓存文件失败: %v", err)
						}
					}
				}
			}
		}
	case "session":
		name := fmt.Sprintf("%s.session", strings.ToLower(realm.Cate))
		if err := os.Remove(filepath.Join(infos.FilesPath, name)); err != nil {
			log.Printf("删除缓存文件失败: %v", err)
		}
	}
}

// isDigit 判断 rune 是否为数字字符（供 submitCode 过滤验证码使用）
func isDigit(r rune) bool {
	return r >= '0' && r <= '9'
}

// search 在指定频道中搜索关键词并返回匹配的媒体文件列表
func (infos *Infos) search(channel, keywords string, page, limit int, offset int32) (items Items, err error) {
	if waitUntil := infos.WaitUntil.Load(); waitUntil > 0 {
		if remaining := time.Until(time.Unix(waitUntil, 0)); remaining > 0 {
			log.Printf("搜索: 检测到FloodWait, 等待 %.2f 秒", remaining.Seconds())
			time.Sleep(remaining)
		}
	}

	ch, err := infos.UserClient.ResolvePeer(fmt.Sprintf("@%s", channel))
	if err != nil {
		log.Printf("频道解析失败: %+v", err)
		return items, err
	}
	if offset == 0 {
		offSets.Mutex.Lock()
		key := fmt.Sprintf("%s|%s|%d", channel, keywords, page)
		if values, ok := offSets.OffSets[key]; ok && time.Since(values.Time) < time.Hour {
			offset = values.Offset
		}
		offSets.Mutex.Unlock()
		if page > 1 && offset == 0 {
			return items, errors.New("未找到匹配消息")
		}
	}

	ms, err := infos.UserClient.GetMessages(ch, &telegram.SearchOption{
		Query:  keywords,
		Limit:  int32(limit),
		Offset: offset,
		Filter: &telegram.InputMessagesFilterVideo{},
	})

	if err != nil {
		return items, err
	}
	if len(ms) == 0 {
		return items, errors.New("未找到匹配消息")
	}

	if len(ms) == limit {
		items.HasMore = true
		key := fmt.Sprintf("%s|%s|%d", channel, keywords, page+1)
		offSets.Mutex.Lock()
		offSets.OffSets[key] = OffSet{
			Offset: ms[len(ms)-1].ID,
			Time:   time.Now(),
		}
		offSets.Mutex.Unlock()
	}

	slices.Reverse(ms)
	maxCount := 3
	rids := make(map[int64]bool)
	mids := make([]int32, 0, len(ms)*maxCount)
	seen := make(map[int32]bool)
	for _, m := range ms {
		if m.File == nil {
			continue
		}
		if m.Message.GroupedID != 0 {
			for num := 0; num < maxCount; num++ {
				mid := m.ID + int32(num)
				if value, ok := seen[mid]; ok && value {
					continue
				}
				seen[mid] = true
				mids = append(mids, mid)
				rids[m.Message.GroupedID] = true
			}
		} else {
			mids = append(mids, m.ID)
		}
	}

	results := [][]telegram.NewMessage{ms}

	if len(rids) > 0 {
		results = make([][]telegram.NewMessage, 0, (len(mids)/100)+1)
		for chunk := range slices.Chunk(mids, 100) {
			ms, err = infos.UserClient.GetMessages(ch, &telegram.SearchOption{
				IDs:    chunk,
				Limit:  100,
				Filter: &telegram.InputMessagesFilterVideo{},
			})
			if err != nil {
				continue
			}
			results = append(results, ms)
		}
	}

	for _, ms := range results {
		for _, m := range ms {
			if m.File == nil {
				continue
			}
			if len(rids) > 0 {
				if value, ok := rids[m.Message.GroupedID]; !ok || !value {
					continue
				}
			}

			if items.Channel == "" {
				items.Channel = strings.TrimSpace(m.Channel.Title)
			}

			name := strings.TrimSpace(m.File.Name)
			if name == "" {
				name = strings.TrimSpace(m.Text())
			}
			items.Item = append(items.Item, Item{
				Name: name,
				Size: m.File.Size,
				CID:  m.Channel.ID,
				MID:  m.ID,
			})
		}
	}
	return items, nil
}
