package main

import (
	"bufio"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
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

func cleanChannelID(channel string) string {
	channel = strings.TrimSpace(channel)
	channel = strings.TrimPrefix(channel, "@")
	return strings.Trim(channel, "/")
}

func messageChannelTitle(m telegram.NewMessage, fallback string) string {
	if m.Channel != nil {
		if title := strings.TrimSpace(m.Channel.Title); title != "" {
			return title
		}
	}
	return cleanChannelID(fallback)
}

func messageDate(m telegram.NewMessage) int32 {
	if m.Message == nil {
		return 0
	}
	return m.Message.Date
}

func floodWaitError(action string) error {
	if waitUntil := infos.WaitUntil.Load(); waitUntil > 0 {
		if remaining := time.Until(time.Unix(waitUntil, 0)); remaining > 0 {
			return fmt.Errorf("%s: Telegram 限流中, %.0f 秒后重试", action, remaining.Seconds())
		}
	}
	return nil
}

func recordFloodWaitFromError(err error) {
	if err == nil {
		return
	}
	wait := 0
	matches := infos.Rex.FindStringSubmatch(err.Error())
	if len(matches) > 1 {
		for _, match := range matches {
			if value, err := strconv.Atoi(match); err == nil {
				wait = value + 1
				break
			}
		}
	}
	if wait <= 0 {
		return
	}
	waitUntil := time.Now().Add(time.Duration(wait) * time.Second)
	if currentWait := infos.WaitUntil.Load(); waitUntil.Unix() > currentWait {
		infos.WaitUntil.Store(waitUntil.Unix())
	}
}

func packMessagesResult(client *telegram.Client, result telegram.MessagesMessages) []telegram.NewMessage {
	var raw []telegram.Message
	switch result := result.(type) {
	case *telegram.MessagesChannelMessages:
		client.Cache.UpdatePeersToCache(result.Users, result.Chats)
		raw = result.Messages
	case *telegram.MessagesMessagesObj:
		client.Cache.UpdatePeersToCache(result.Users, result.Chats)
		raw = result.Messages
	case *telegram.MessagesMessagesSlice:
		client.Cache.UpdatePeersToCache(result.Users, result.Chats)
		raw = result.Messages
	}

	packed := telegram.PackMessages(client, raw)
	messages := make([]telegram.NewMessage, 0, len(packed))
	for _, message := range packed {
		if message != nil {
			messages = append(messages, *message)
		}
	}
	return messages
}

func inputMessageIDs(ids []int32) []telegram.InputMessage {
	inputIDs := make([]telegram.InputMessage, 0, len(ids))
	for _, id := range ids {
		inputIDs = append(inputIDs, &telegram.InputMessageID{ID: id})
	}
	return inputIDs
}

func (infos *Infos) getMessagesByIDsFast(peer telegram.InputPeer, ids []int32) ([]telegram.NewMessage, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	if err := floodWaitError("读取消息"); err != nil {
		return nil, err
	}

	inputIDs := inputMessageIDs(ids)
	var (
		result telegram.MessagesMessages
		err    error
	)
	switch peer := peer.(type) {
	case *telegram.InputPeerChannel:
		result, err = infos.UserClient.ChannelsGetMessages(&telegram.InputChannelObj{ChannelID: peer.ChannelID, AccessHash: peer.AccessHash}, inputIDs)
	case *telegram.InputPeerChat, *telegram.InputPeerUser, *telegram.InputPeerSelf:
		result, err = infos.UserClient.MessagesGetMessages(inputIDs)
	default:
		return nil, errors.New("不支持的 peer 类型")
	}
	if err != nil {
		recordFloodWaitFromError(err)
		return nil, err
	}
	return packMessagesResult(infos.UserClient, result), nil
}

func (infos *Infos) searchMessagesFast(peer telegram.InputPeer, action, keywords string, filter telegram.MessagesFilter, limit int, offset int32) ([]telegram.NewMessage, error) {
	if err := floodWaitError(action); err != nil {
		return nil, err
	}
	if filter == nil {
		filter = &telegram.InputMessagesFilterEmpty{}
	}
	result, err := infos.UserClient.MessagesSearch(&telegram.MessagesSearchParams{
		Peer:     peer,
		Q:        keywords,
		OffsetID: offset,
		Filter:   filter,
		Limit:    int32(limit),
	})
	if err != nil {
		recordFloodWaitFromError(err)
		return nil, err
	}
	return packMessagesResult(infos.UserClient, result), nil
}

func messageGroupedID(m telegram.NewMessage) int64 {
	if m.Message == nil {
		return 0
	}
	return m.Message.GroupedID
}

func isVideoMessage(m telegram.NewMessage) bool {
	if m.Message == nil {
		return false
	}
	return m.Video() != nil
}

func mediaItemFromMessage(m telegram.NewMessage) (Item, bool) {
	return mediaItemFromMessageText(m, m.Text())
}

func mediaItemFromMessageText(m telegram.NewMessage, text string) (Item, bool) {
	if m.File == nil || m.Channel == nil || !isVideoMessage(m) {
		return Item{}, false
	}
	text = strings.TrimSpace(text)
	name := strings.TrimSpace(m.File.Name)
	if name == "" {
		name = text
	}
	if name == "" {
		name = fmt.Sprintf("Telegram %d", m.ID)
	}
	return Item{
		Name: name,
		Text: text,
		Size: m.File.Size,
		CID:  m.Channel.ID,
		MID:  m.ID,
		Date: messageDate(m),
	}, true
}

type groupedMediaContext struct {
	Text   string
	Videos []telegram.NewMessage
}

func (ctx groupedMediaContext) textFor(m telegram.NewMessage) string {
	if text := strings.TrimSpace(m.Text()); text != "" {
		return text
	}
	return ctx.Text
}

func collectGroupedMediaContext(messages []telegram.NewMessage, groupID int64) groupedMediaContext {
	var ctx groupedMediaContext
	var textID int32
	seenVideos := make(map[string]bool)
	for _, m := range messages {
		if groupID != 0 && messageGroupedID(m) != groupID {
			continue
		}
		if text := strings.TrimSpace(m.Text()); text != "" && (ctx.Text == "" || m.ID < textID) {
			ctx.Text = text
			textID = m.ID
		}
		if isVideoMessage(m) {
			key := fmt.Sprintf("%d:%d", messageChannelID(m), m.ID)
			if seenVideos[key] {
				continue
			}
			seenVideos[key] = true
			ctx.Videos = append(ctx.Videos, m)
		}
	}
	return ctx
}

func messageChannelID(m telegram.NewMessage) int64 {
	if m.Channel != nil {
		return m.Channel.ID
	}
	return m.ChannelID()
}

func appendGroupedVideoItems(items *Items, channel string, groupCache map[int64]groupedMediaContext, m telegram.NewMessage, seen map[string]bool) {
	ctx := collectGroupedMediaContext([]telegram.NewMessage{m}, messageGroupedID(m))
	if groupID := messageGroupedID(m); groupID != 0 {
		if cached, ok := groupCache[groupID]; ok {
			ctx = cached
		}
	}
	videos := ctx.Videos
	if len(videos) == 0 && isVideoMessage(m) {
		videos = []telegram.NewMessage{m}
	}
	for _, video := range videos {
		if video.Channel == nil {
			continue
		}
		if items.Channel == "" {
			items.Channel = messageChannelTitle(video, channel)
		}
		key := fmt.Sprintf("%d:%d", video.Channel.ID, video.ID)
		if seen[key] {
			continue
		}
		if item, ok := mediaItemFromMessageText(video, ctx.textFor(video)); ok {
			seen[key] = true
			items.Item = append(items.Item, item)
		}
	}
}

func (infos *Infos) loadGroupedMediaContexts(peer telegram.InputPeer, seeds []telegram.NewMessage) map[int64]groupedMediaContext {
	seedByGroup := make(map[int64][]telegram.NewMessage)
	ids := make([]int32, 0, len(seeds)*3)
	seenIDs := make(map[int32]bool)

	for _, seed := range seeds {
		groupID := messageGroupedID(seed)
		if groupID == 0 {
			continue
		}
		if isVideoMessage(seed) && strings.TrimSpace(seed.Text()) != "" {
			continue
		}
		seedByGroup[groupID] = append(seedByGroup[groupID], seed)
		for id := seed.ID - 9; id <= seed.ID+9; id++ {
			if id <= 0 || seenIDs[id] {
				continue
			}
			seenIDs[id] = true
			ids = append(ids, id)
		}
	}

	contexts := make(map[int64]groupedMediaContext, len(seedByGroup))
	if len(seedByGroup) == 0 {
		return contexts
	}

	groupMessages := make(map[int64][]telegram.NewMessage, len(seedByGroup))
	for groupID, groupSeeds := range seedByGroup {
		groupMessages[groupID] = append(groupMessages[groupID], groupSeeds...)
	}

	for start := 0; start < len(ids); start += 100 {
		end := start + 100
		if end > len(ids) {
			end = len(ids)
		}
		ms, err := infos.getMessagesByIDsFast(peer, ids[start:end])
		if err != nil {
			log.Printf("批量读取媒体组失败: ids=%d-%d, err=%v", ids[start], ids[end-1], err)
			continue
		}
		for _, m := range ms {
			groupID := messageGroupedID(m)
			if _, ok := seedByGroup[groupID]; ok {
				groupMessages[groupID] = append(groupMessages[groupID], m)
			}
		}
	}

	for groupID, messages := range groupMessages {
		contexts[groupID] = collectGroupedMediaContext(messages, groupID)
	}
	return contexts
}

// search 在指定频道中搜索关键词并返回匹配的媒体文件列表
func (infos *Infos) search(channel, keywords string, page, limit int, offset int32) (items Items, err error) {
	if err := floodWaitError("搜索"); err != nil {
		return items, err
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

	filters := []telegram.MessagesFilter{
		&telegram.InputMessagesFilterEmpty{},
		&telegram.InputMessagesFilterVideo{},
	}
	groupCache := make(map[int64]groupedMediaContext)
	seen := make(map[string]bool)
	matched := make([]telegram.NewMessage, 0, limit*2)
	var nextOffset int32
	var lastErr error

	for _, filter := range filters {
		ms, err := infos.searchMessagesFast(ch, "搜索", keywords, filter, limit, offset)
		if err != nil {
			lastErr = err
			continue
		}
		if len(ms) == 0 {
			continue
		}
		if len(ms) == limit {
			items.HasMore = true
			nextOffset = ms[len(ms)-1].ID
		} else if nextOffset == 0 {
			nextOffset = ms[len(ms)-1].ID
		}
		matched = append(matched, ms...)
	}

	for groupID, ctx := range infos.loadGroupedMediaContexts(ch, matched) {
		groupCache[groupID] = ctx
	}
	for _, m := range matched {
		appendGroupedVideoItems(&items, channel, groupCache, m, seen)
	}

	if len(items.Item) == 0 {
		if lastErr != nil {
			return items, lastErr
		}
		return items, errors.New("未找到匹配消息")
	}
	if items.Channel == "" {
		items.Channel = channel
	}
	if len(items.Item) > limit {
		items.Item = items.Item[:limit]
	}
	if items.HasMore && nextOffset != 0 {
		key := fmt.Sprintf("%s|%s|%d", channel, keywords, page+1)
		offSets.Mutex.Lock()
		offSets.OffSets[key] = OffSet{
			Offset: nextOffset,
			Time:   time.Now(),
		}
		offSets.Mutex.Unlock()
	}
	return items, nil
}

// latest 在指定频道中按发布时间读取最新视频消息。
func (infos *Infos) latest(channel string, page, limit int, offset int32) (items Items, err error) {
	if err := floodWaitError("最新列表"); err != nil {
		return items, err
	}

	channel = cleanChannelID(channel)
	ch, err := infos.UserClient.ResolvePeer(fmt.Sprintf("@%s", channel))
	if err != nil {
		log.Printf("频道解析失败: %+v", err)
		return items, err
	}

	if offset == 0 {
		offSets.Mutex.Lock()
		key := fmt.Sprintf("latest|%s|%d", channel, page)
		if values, ok := offSets.OffSets[key]; ok && time.Since(values.Time) < time.Hour {
			offset = values.Offset
		}
		offSets.Mutex.Unlock()
		if page > 1 && offset == 0 {
			return items, errors.New("未找到更多消息")
		}
	}

	ms, err := infos.searchMessagesFast(ch, "最新列表", "", &telegram.InputMessagesFilterVideo{}, limit, offset)
	if err != nil {
		return items, err
	}
	if len(ms) == 0 {
		return items, errors.New("未找到视频消息")
	}

	groupCache := infos.loadGroupedMediaContexts(ch, ms)
	seen := make(map[string]bool)
	for _, m := range ms {
		appendGroupedVideoItems(&items, channel, groupCache, m, seen)
	}

	if items.Channel == "" {
		items.Channel = channel
	}
	if len(items.Item) == 0 {
		return items, errors.New("未找到视频消息")
	}
	if len(items.Item) > limit {
		items.Item = items.Item[:limit]
	}
	if len(ms) == limit {
		items.HasMore = true
		offSets.Mutex.Lock()
		offSets.OffSets[fmt.Sprintf("latest|%s|%d", channel, page+1)] = OffSet{
			Offset: ms[len(ms)-1].ID,
			Time:   time.Now(),
		}
		offSets.Mutex.Unlock()
	}
	return items, nil
}
