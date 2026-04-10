package main

import (
	"fmt"
	"html"
	"log"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/amarnathcjd/gogram/telegram"
)

// handleBotCommand 是 Bot 的总消息分发入口，处理所有管理指令
func handleBotCommand(m *telegram.NewMessage) error {
	if m.Sender.ID == infos.BotID {
		return nil
	}

	text := strings.TrimSpace(m.Text())

	switch {
	case strings.HasPrefix(text, "/start"):
		if !infos.isWhite(m.SenderID()) {
			sendMS(m, "你没有使用此机器人的权限", nil, 60)
			return nil
		}

		var src string
		if m.SenderID() == infos.Conf.UserID {
			switch infos.Status.Load() {
			case 0:
				src = "userBot 未登录, 仅使用 Bot 或发送 /phone 手机号登录 userBot"
			case 1:
				src = "正在等待验证码, 请发送 /code 验证码"
			case 2:
				src = "正在等待密码, 请发送 /pass 密码"
			case 3:
				src = "userBot 已登录"
			}
		} else {
			src = "仅限内部使用, 请保管好你的HASH密码与UID"
		}
		sendMS(m, src, nil, 60)
		return nil

	case strings.HasPrefix(text, "/allow"):
		if !infos.isAdmin(m.SenderID()) {
			sendMS(m, "你没有使用此命令的权限", nil, 60)
			return nil
		}
		whiteID, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(text, "/allow")), 10, 64)
		if err != nil {
			sendMS(m, fmt.Sprintf("添加白名单失败: %+v", err), nil, 60)
			return nil
		}

		if whiteID != 0 {
			if slices.Contains(infos.Conf.WhiteIDs, whiteID) {
				sendMS(m, fmt.Sprintf("白名单中已存在: %d", whiteID), nil, 60)
				return nil
			}

			infos.Mutex.Lock()
			value := ID{
				IsWhite: true,
			}
			infos.IDs[whiteID] = value
			infos.Conf.WhiteIDs = append(infos.Conf.WhiteIDs, whiteID)
			if err := saveConf(infos.Conf, infos.FilesPath); err != nil {
				log.Printf("保存配置文件失败: %+v", err)
			}
			infos.Mutex.Unlock()
			sendMS(m, fmt.Sprintf("添加白名单成功: %d", whiteID), nil, 60)
		}
		return nil

	case strings.HasPrefix(text, "/disallow"):
		if !infos.isAdmin(m.SenderID()) {
			sendMS(m, "你没有使用此命令的权限", nil, 60)
			return nil
		}
		whiteID, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(text, "/disallow")), 10, 64)
		if err != nil {
			sendMS(m, fmt.Sprintf("移除白名单失败: %+v", err), nil, 60)
			return nil
		}

		if whiteID != 0 {
			if slices.Contains(infos.Conf.WhiteIDs, whiteID) {
				infos.Mutex.Lock()
				delete(infos.IDs, whiteID)
				infos.Conf.WhiteIDs = slices.DeleteFunc(infos.Conf.WhiteIDs, func(num int64) bool {
					return num == whiteID
				})
				if err := saveConf(infos.Conf, infos.FilesPath); err != nil {
					log.Printf("保存配置文件失败: %+v", err)
				}
				infos.Mutex.Unlock()
				sendMS(m, fmt.Sprintf("移除白名单成功: %d", whiteID), nil, 60)
				return nil
			}
			sendMS(m, fmt.Sprintf("用户 %d 不在白名单中", whiteID), nil, 60)
		}
		return nil

	case strings.HasPrefix(text, "/qr"):
		if m.SenderID() != infos.Conf.UserID {
			sendMS(m, "你没有使用此命令的权限", nil, 60)
			return nil
		}
		if err := infos.startUserBotQR(); err != nil {
			sendMS(m, fmt.Sprintf("启动 QR 登录失败: %+v", err), nil, 60)
		}
		return nil

	case strings.HasPrefix(text, "/phone"):
		if m.SenderID() != infos.Conf.UserID {
			sendMS(m, "你没有使用此命令的权限", nil, 60)
			return nil
		}
		content := strings.TrimSpace(strings.TrimPrefix(text, "/phone"))
		if content == "" {
			sendMS(m, "手机不能为空", nil, 60)
			return nil
		}
		if !strings.HasPrefix(content, "+") {
			content = "+" + content
		}
		if err := infos.startUserBot(content); err != nil {
			sendMS(m, fmt.Sprintf("启动 UserBot 失败: %+v", err), nil, 60)
		}
		return nil

	case strings.HasPrefix(text, "/code"):
		if m.SenderID() != infos.Conf.UserID {
			sendMS(m, "你没有使用此命令的权限", nil, 60)
			return nil
		}
		code := strings.TrimSpace(strings.TrimPrefix(text, "/code"))
		if code == "" {
			sendMS(m, "验证码不能为空", nil, 60)
			return nil
		}
		if err := infos.submitCode(code); err != nil {
			sendMS(m, fmt.Sprintf("提交验证码失败: %+v", err), nil, 60)
			return nil
		}
		sendMS(m, "提交验证码成功", nil, 60)
		return nil

	case strings.HasPrefix(text, "/pass") && !strings.HasPrefix(text, "/password"):
		if m.SenderID() != infos.Conf.UserID {
			sendMS(m, "你没有使用此命令的权限", nil, 60)
			return nil
		}
		pass := strings.TrimSpace(strings.TrimPrefix(text, "/pass"))
		if pass == "" {
			sendMS(m, "2FA密码不能为空", nil, 60)
			return nil
		}
		if err := infos.submitPass(pass); err != nil {
			sendMS(m, fmt.Sprintf("提交2FA密码失败: %+v", err), nil, 60)
			return nil
		}
		sendMS(m, "提交2FA密码成功", nil, 60)
		return nil

	case strings.HasPrefix(text, "/dc"):
		if !infos.isAdmin(m.SenderID()) {
			sendMS(m, "你没有使用此命令的权限", nil, 60)
			return nil
		}
		content := strings.TrimSpace(strings.TrimPrefix(text, "/dc"))
		if content == "" {
			if infos.Conf.DC != 0 {
				sendMS(m, fmt.Sprintf("当前DC: %d", infos.Conf.DC), nil, 60)
			} else {
				sendMS(m, "当前未手动指定DC", nil, 60)
			}
			return nil
		}
		value, err := strconv.Atoi(content)
		if err != nil {
			sendMS(m, fmt.Sprintf("DC格式错误: %+v", err), nil, 60)
			return nil
		}
		if value < 1 || value > 5 {
			sendMS(m, "DC必须在1-5之间", nil, 60)
			return nil
		}
		infos.Mutex.Lock()
		infos.Conf.DC = value
		if err := saveConf(infos.Conf, infos.FilesPath); err != nil {
			log.Printf("保存配置文件失败: %+v", err)
		}
		infos.Mutex.Unlock()
		sendMS(m, fmt.Sprintf("DC已设置为: %d, 重启后生效", value), nil, 60)
		return nil

	case strings.HasPrefix(text, "/site"):
		if !infos.isAdmin(m.SenderID()) {
			sendMS(m, "你没有使用此命令的权限", nil, 60)
			return nil
		}
		content := strings.TrimSpace(strings.TrimPrefix(text, "/site"))
		if content == "" {
			sendMS(m, fmt.Sprintf("当前反代地址: %s", infos.Conf.Site), nil, 60)
			return nil
		}
		if !strings.HasPrefix(content, "http") {
			sendMS(m, "反代地址格式错误", nil, 60)
			return nil
		}
		infos.Mutex.Lock()
		infos.Conf.Site = content
		if err := saveConf(infos.Conf, infos.FilesPath); err != nil {
			log.Printf("保存配置文件失败: %+v", err)
		}
		infos.Mutex.Unlock()
		sendMS(m, fmt.Sprintf("反代地址已设置为: %s", content), nil, 60)
		return nil

	case strings.HasPrefix(text, "/size"):
		if !infos.isAdmin(m.SenderID()) {
			sendMS(m, "你没有使用此命令的权限", nil, 60)
			return nil
		}
		content := strings.TrimSpace(strings.TrimPrefix(text, "/size"))
		if content == "" {
			sendMS(m, fmt.Sprintf("当前最大缓存: %s", formatFileSize(infos.Conf.MaxSize)), nil, 60)
			return nil
		}
		value := convertMaxSize(content)
		if value == 0 {
			sendMS(m, "最大缓存格式错误", nil, 60)
			return nil
		}
		infos.Mutex.Lock()
		infos.Conf.MaxSize = value
		if err := saveConf(infos.Conf, infos.FilesPath); err != nil {
			log.Printf("保存配置文件失败: %+v", err)
		}
		infos.Mutex.Unlock()
		src := fmt.Sprintf("最大缓存已设置为: %s", formatFileSize(value))
		if value > 128*1024*1024 {
			src += ", 当前缓存较大, 容易引起OOM, 请谨慎设置"
		}
		sendMS(m, src, nil, 60)
		return nil

	case strings.HasPrefix(text, "/password"):
		if !infos.isAdmin(m.SenderID()) {
			sendMS(m, "你没有使用此命令的权限", nil, 60)
			return nil
		}
		content := strings.TrimSpace(strings.TrimPrefix(text, "/password"))
		if content == "" {
			sendMS(m, fmt.Sprintf("当前密码: %s", infos.Conf.Password), nil, 60)
			return nil
		}
		infos.Mutex.Lock()
		infos.Conf.Password = content
		for key, value := range infos.IDs {
			value.Hash = ""
			infos.IDs[key] = value
		}
		if err := saveConf(infos.Conf, infos.FilesPath); err != nil {
			log.Printf("保存配置文件失败: %+v", err)
		}
		infos.Mutex.Unlock()
		sendMS(m, fmt.Sprintf("密码已设置为: %s", content), nil, 60)
		return nil

	case strings.HasPrefix(text, "/channel"):
		if !infos.isAdmin(m.SenderID()) {
			sendMS(m, "你没有使用此命令的权限", nil, 60)
			return nil
		}
		content := strings.TrimSpace(strings.TrimPrefix(text, "/channel"))
		if content == "" {
			sendMS(m, fmt.Sprintf("当前频道ID: %d", infos.Conf.ChannelID), nil, 60)
			return nil
		}
		if !strings.HasPrefix(content, "-100") {
			content = "-100" + content
		}
		value, err := strconv.ParseInt(content, 10, 64)
		if err != nil {
			sendMS(m, fmt.Sprintf("频道ID格式错误: %+v", err), nil, 60)
			return nil
		}
		infos.Mutex.Lock()
		infos.Conf.ChannelID = value
		if err := saveConf(infos.Conf, infos.FilesPath); err != nil {
			log.Printf("保存配置文件失败: %+v", err)
		}
		infos.Mutex.Unlock()
		sendMS(m, fmt.Sprintf("频道ID已设置为: %d", value), nil, 60)
		return nil

	case strings.HasPrefix(text, "/workers"):
		if !infos.isAdmin(m.SenderID()) {
			sendMS(m, "你没有使用此命令的权限", nil, 60)
			return nil
		}
		content := strings.TrimSpace(strings.TrimPrefix(text, "/workers"))
		if content == "" {
			sendMS(m, fmt.Sprintf("当前并发数: %d", infos.Conf.Workers), nil, 60)
			return nil
		}
		num, err := strconv.Atoi(content)
		if err != nil {
			sendMS(m, "并发数必须为数字", nil, 60)
			return nil
		}
		if num <= 0 {
			sendMS(m, "并发数必须大于 0", nil, 60)
			return nil
		}
		infos.Mutex.Lock()
		infos.Conf.Workers = num
		if err := saveConf(infos.Conf, infos.FilesPath); err != nil {
			log.Printf("保存配置文件失败: %+v", err)
		}
		infos.Mutex.Unlock()
		src := fmt.Sprintf("并发数已设置为: %d", num)
		if num > 4 {
			src += ", 当前并发数较大, 容易引起下载失败甚至封号, 请谨慎设置"
		}
		sendMS(m, src, nil, 60)
		return nil

	case strings.HasPrefix(text, "/check"):
		if !infos.isAdmin(m.SenderID()) {
			sendMS(m, "你没有使用此命令的权限", nil, 60)
			return nil
		}
		content := strings.TrimSpace(strings.TrimPrefix(text, "/check"))
		if content == "" {
			sendMS(m, "请提供要检查的哈希值", nil, 60)
			return nil
		}
		if uid := infos.checkHash(content); uid != 0 {
			user, err := infos.BotClient.GetUser(uid)
			if err != nil {
				log.Printf("获取用户信息失败: %+v", err)
				return nil
			}
			fullName := user.FirstName + user.LastName
			var values strings.Builder
			values.WriteString(fmt.Sprintf("• <b>用户 ID</b>: <code>%d</code>\n", uid))
			if fullName != "" {
				values.WriteString(fmt.Sprintf("• <b>显示名称</b>: %s\n", html.EscapeString(fullName)))
			}
			if user.Username != "" {
				values.WriteString(fmt.Sprintf("• <b>用户昵称</b>: @%s\n", user.Username))
			}
			sendMS(m, values.String(), nil, 60)
		}
		return nil

	case strings.HasPrefix(text, "/add"):
		if !infos.isAdmin(m.SenderID()) {
			sendMS(m, "你没有使用此命令的权限", nil, 60)
			return nil
		}
		channel := strings.TrimSpace(strings.TrimPrefix(text, "/add"))
		if channel == "" {
			sendMS(m, "请提供要添加的频道别名", nil, 60)
			return nil
		}
		if slices.Contains(infos.Conf.Channels, channel) {
			sendMS(m, fmt.Sprintf("频道 %s 已存在", channel), nil, 60)
			return nil
		}
		infos.Mutex.Lock()
		infos.Conf.Channels = append(infos.Conf.Channels, channel)
		if err := saveConf(infos.Conf, infos.FilesPath); err != nil {
			log.Printf("保存配置文件失败: %+v", err)
		}
		infos.Mutex.Unlock()
		sendMS(m, fmt.Sprintf("添加频道成功: %s", channel), nil, 60)
		return nil

	case strings.HasPrefix(text, "/del"):
		if !infos.isAdmin(m.SenderID()) {
			sendMS(m, "你没有使用此命令的权限", nil, 60)
			return nil
		}
		channel := strings.TrimSpace(strings.TrimPrefix(text, "/del"))
		if channel == "" {
			sendMS(m, "请提供要移除的频道别名", nil, 60)
			return nil
		}
		if !slices.Contains(infos.Conf.Channels, channel) {
			sendMS(m, fmt.Sprintf("频道 %s 不在搜索列表中", channel), nil, 60)
			return nil
		}
		infos.Mutex.Lock()
		infos.Conf.Channels = slices.DeleteFunc(infos.Conf.Channels, func(key string) bool {
			return key == channel
		})
		if err := saveConf(infos.Conf, infos.FilesPath); err != nil {
			log.Printf("保存配置文件失败: %+v", err)
		}
		infos.Mutex.Unlock()
		sendMS(m, fmt.Sprintf("移除频道成功: %s", channel), nil, 60)
		return nil

	case strings.HasPrefix(text, "/list"):
		if !infos.isAdmin(m.SenderID()) {
			sendMS(m, "你没有使用此命令的权限", nil, 60)
			return nil
		}
		content := strings.TrimSpace(strings.TrimPrefix(text, "/list"))
		if content == "" {
			sendMS(m, "请提供要列出的类别: <code>channels</code> | <code>ids</code>", nil, 60)
			return nil
		}
		switch content {
		case "channels":
			var values strings.Builder
			count := len(infos.Conf.Channels)
			if count == 0 {
				sendMS(m, "⚠️ <b>暂无搜索频道别名</b>", nil, 60)
				break
			}
			values.WriteString(fmt.Sprintf("🔍 <b>搜索频道别名列表</b> (共 %d 个)\n", count))
			values.WriteString("━━━━━━━━━━━━━━━\n")
			for _, ch := range infos.Conf.Channels {
				if !strings.HasPrefix(ch, "@") {
					ch = "@" + ch
				}
				values.WriteString(fmt.Sprintf("• %s\n", html.EscapeString(ch)))
			}
			sendMS(m, values.String(), nil, 60)
		case "ids":
			var values strings.Builder
			count := len(infos.Conf.WhiteIDs)
			if count == 0 {
				sendMS(m, "⚠️ <b>白名单目前为空</b>", nil, 60)
				break
			}
			values.WriteString(fmt.Sprintf("🛡️ <b>白名单 ID 列表</b> (共 %d 个)\n", count))
			values.WriteString("━━━━━━━━━━━━━━━\n")
			for _, whiteID := range infos.Conf.WhiteIDs {
				values.WriteString(fmt.Sprintf("• <code>%d</code>\n", whiteID))
			}
			sendMS(m, values.String(), nil, 60)
		default:
			sendMS(m, "类别错误", nil, 60)
		}
		return nil

	case strings.HasPrefix(text, "/port"):
		if !infos.isAdmin(m.SenderID()) {
			sendMS(m, "你没有使用此命令的权限", nil, 60)
			return nil
		}
		content := strings.TrimSpace(strings.TrimPrefix(text, "/port"))
		if content == "" {
			sendMS(m, "请提供要修改的端口", nil, 60)
			return nil
		}
		value, err := strconv.Atoi(content)
		if err != nil {
			sendMS(m, "端口格式错误", nil, 60)
			return nil
		}
		if value <= 0 || value > 65535 {
			sendMS(m, "端口必须在 1-65535 之间", nil, 60)
			return nil
		}
		infos.Mutex.Lock()
		infos.Conf.Port = value
		if err := saveConf(infos.Conf, infos.FilesPath); err != nil {
			log.Printf("保存配置文件失败: %+v", err)
		}
		infos.Mutex.Unlock()
		sendMS(m, fmt.Sprintf("端口已设置为: %d, 重启后生效", value), nil, 60)
		return nil

	case strings.HasPrefix(text, "/info"):
		if !infos.isAdmin(m.SenderID()) {
			sendMS(m, "你没有使用此命令的权限", nil, 60)
			return nil
		}

		num := 10
		content := strings.TrimSpace(strings.TrimPrefix(text, "/info"))
		if content != "" {
			src, value := extractContent(content)
			if value != nil {
				num = *value
			}
			content = src
		}

		if infos.FilePath == "" {
			sendMS(m, "暂未开启日志记录", nil, 60)
			return nil
		}

		lines, err := readLastLines(infos.FilePath, content, num)
		if err != nil {
			sendMS(m, fmt.Sprintf("读取日志失败: %+v", err), nil, 60)
			return nil
		}

		if len(lines) == 0 {
			sendMS(m, "暂无日志内容", nil, 60)
			return nil
		}

		const maxCount = 4000
		var values strings.Builder
		values.WriteString(fmt.Sprintf("<b>📜 系统日志 (最后 %d 行)</b>\n\n", len(lines)))
		values.WriteString("<pre>")

		for _, line := range lines {
			line = html.EscapeString(line) + "\n"
			if values.Len()+len(line)+len("</pre>") > maxCount {
				values.WriteString("</pre>")
				sendMS(m, values.String(), nil)
				values.Reset()
				values.WriteString("<pre>")
			}
			values.WriteString(line)
		}

		if values.Len() > len("<pre>") {
			values.WriteString("</pre>")
			sendMS(m, values.String(), nil)
		}
		return nil

	default:
		if !infos.isWhite(m.SenderID()) && m.SenderID() != 0 {
			sendMS(m, "你没有使用此机器人的权限", nil, 60)
			return nil
		}
		return handleMess(m)
	}
}

// handleMess 处理接收到的普通消息，解析其中的媒体文件或 Telegram 链接
func handleMess(m *telegram.NewMessage) error {
	// 如果是用户发送或转发来的、带有图片/文档/视频的消息，直接生成直链
	if m.IsMedia() && (m.Photo() != nil || m.Document() != nil || m.Video() != nil) {
		link := fmt.Sprintf("%s/stream?cid=%d&mid=%d&cate=bot", strings.TrimSuffix(infos.Conf.Site, "/"), m.ChatID(), m.ID)
		if infos.Conf.Password != "" {
			link += fmt.Sprintf("&hash=%s&uid=%d", infos.calculateHash(m.SenderID()), m.SenderID())
		}
		return sendLink(m, link)
	}

	if infos.Status.Load() != 3 {
		return nil
	}

	src := strings.TrimSpace(m.Text())
	if src == "" {
		return nil
	}

	// 匹配格式如：t.me/c/12345/678 或 t.me/username/678
	re := regexp.MustCompile(`t\.me\/(c\/(\d+)|([a-zA-Z0-9_]+))\/(\d+)(?:\?.*comment=(\d+))?`)
	matches := re.FindAllStringSubmatch(src, -1)

	if len(matches) == 0 {
		return nil
	}
	res := HackLink{
		M:       m,
		Matches: matches,
	}
	for _, link := range hackLink(res) {
		if err := sendLink(m, link); err != nil {
			log.Printf("发送消息失败: %+v", err)
		}
	}
	return nil
}

// hackLink 是链接解析的核心逻辑，负责将 t.me 链接映射到具体的媒体消息并生成本程序的流地址
func hackLink(res HackLink) (links []string) {
	for _, match := range res.Matches {
		var cid any   // 用于 ResolvePeer 的标识项（可以是用户名或 chatID）
		var mid int32 // 消息 ID

		// 1. 解析 Chat ID 或 Username
		if match[2] != "" {
			// 如果是 c/(\d+)，代表私有频道链接，需要给 ID 补充前缀 -100
			value, err := strconv.ParseInt("-100"+match[2], 10, 64)
			if err != nil {
				log.Printf("解析频道ID失败: %+v", err)
				if res.M != nil {
					if _, err := res.M.Reply("解析频道ID失败"); err != nil {
						log.Printf("发送消息失败: %+v", err)
					}
				}
				continue
			}
			cid = value
		} else {
			// 否则匹配的是公开频道的 username
			cid = match[3]
		}

		// 2. 解析消息偏移 ID
		value, err := strconv.ParseInt(match[4], 10, 32)
		if err != nil {
			log.Printf("解析消息ID失败: %+v", err)
			if res.M != nil {
				if _, err := res.M.Reply("解析消息ID失败"); err != nil {
					log.Printf("发送消息失败: %+v", err)
				}
			}
			continue
		}
		mid = int32(value)

		// 3. 使用 UserBot 客户端尝试获取目标消息
		ms, err := infos.UserClient.GetMessages(cid, &telegram.SearchOption{IDs: []int32{mid}})
		if err != nil || len(ms) == 0 {
			log.Printf("获取消息失败: cid=%v, mid=%d, err=%v, count=%d", cid, mid, err, len(ms))
			if res.M != nil {
				if _, err := res.M.Reply("获取消息失败"); err != nil {
					log.Printf("发送消息失败: %+v", err)
				}
			}
			continue
		}

		src := ms[0] // 获取第一条目标消息

		// 4. 处理链接中的评论 (comment) 逻辑
		if match[5] != "" {
			commentID, err := strconv.ParseInt(match[5], 10, 32)
			if err != nil {
				continue
			}

			if src.Message.Replies != nil && src.Message.Replies.ChannelID != 0 {
				discussionID := src.Message.Replies.ChannelID
				commentMs, err := infos.UserClient.GetMessages(discussionID, &telegram.SearchOption{IDs: []int32{int32(commentID)}})
				if err == nil && len(commentMs) > 0 {
					src = commentMs[0]
					src.ID = int32(commentID)
					src.Chat.ID = discussionID
				}
			}
		}

		// 5. 校验消息是否包含可流式传输的媒体
		if !src.IsMedia() || (src.Photo() == nil && src.Document() == nil && src.Video() == nil) {
			log.Printf("消息不包含媒体: cid=%v, mid=%d", cid, mid)
			if res.M != nil {
				if _, err := res.M.Reply("消息不包含媒体"); err != nil {
					log.Printf("发送消息失败: %+v", err)
				}
			}
			continue
		}

		// 6. 为该媒体构造本程序的下载直链 (流地址)
		link := fmt.Sprintf("%s/stream?cid=%v&mid=%d&cate=user", strings.TrimSuffix(infos.Conf.Site, "/"), src.ChatID(), src.ID)

		// 7. 处理 URL 的权限参数附加
		if infos.Conf.Password != "" {
			if res.M != nil {
				link += fmt.Sprintf("&hash=%s&uid=%d", infos.calculateHash(res.M.SenderID()), res.M.SenderID())
			} else {
				switch {
				case res.Hash != "" && res.UID != 0:
					link += fmt.Sprintf("&hash=%s&uid=%d", res.Hash, res.UID)
				case res.Pass != "":
					link += fmt.Sprintf("&key=%s", res.Pass)
				default:
					log.Printf("未提供密码或哈希")
				}
			}
		}
		links = append(links, link)
	}
	return links
}

// sendLink 发送美化后的下载链接消息
func sendLink(m *telegram.NewMessage, link string) error {
	text := fmt.Sprintf("<b>🔗 链接提取成功</b>\n\n<code>%s</code>\n\n👆 <i>上方链接复制, 下方按钮下载</i> 👇", html.EscapeString(link))
	markup := telegram.InlineURL(
		"🚀 直接下载", fmt.Sprintf("%s&download=true", link),
	)

	_, err := m.Reply(text, &telegram.SendOptions{
		ParseMode:   "html",
		ReplyMarkup: markup,
	})

	if err != nil {
		log.Printf("发送下载链接失败: %+v", err)
	}
	return err
}

// sendMS 统一发送消息，支持回复或主动推送给管理员，可设置自动删除延时
func sendMS(m *telegram.NewMessage, src any, params *telegram.SendOptions, wait ...int) {
	switch {
	case m != nil:
		ms, err := m.Reply(src, params)
		if err != nil {
			log.Printf("发送消息失败: %+v", err)
		}
		if len(wait) > 0 && wait[0] > 0 {
			go func() {
				time.Sleep(time.Duration(wait[0]) * time.Second)
				if _, err = ms.Delete(); err != nil {
					log.Printf("删除消息失败: %+v", err)
				}
			}()
		}
		return
	case infos.BotClient != nil:
		ms, err := infos.BotClient.SendMessage(infos.Conf.UserID, src, params)
		if err != nil {
			log.Printf("发送消息失败: %+v", err)
		}
		if len(wait) > 0 && wait[0] > 0 && ms != nil {
			go func() {
				time.Sleep(time.Duration(wait[0]) * time.Second)
				if _, err = ms.Delete(); err != nil {
					log.Printf("删除消息失败: %+v", err)
				}
			}()
		}
		return
	}
}
