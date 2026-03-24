package main

import (
	"encoding/json" // 处理 JSON 数据
	"flag"          // 用于处理命令行参数
	"fmt"           // 用于格式化字符串
	"html"          // 用于转义 HTML 字符
	"io"            // 用于处理文件流
	"log"           // 用于记录程序日志
	"net/http"      // 用于启动 HTTP 服务器和处理请求
	"os"            // 用于处理操作系统信号
	"os/signal"     // 用于处理操作系统信号
	"path/filepath" // 用于保存文件路径操作
	"regexp"        // 用于正则表达式匹配（链接解析）
	"slices"        // 用于切片操作
	"strconv"       // 用于字符串和数值的相互转换
	"strings"       // 用于字符串处理
	"sync"          // 用于并发锁
	"syscall"       // 用于处理操作系统信号
	"time"          // 用于处理时间相关逻辑

	mtproto "github.com/amarnathcjd/gogram"  // 导入 gogram 的 MTProto 协议核心库
	"github.com/amarnathcjd/gogram/telegram" // 导入 gogram 客户端核心库
)

type Infos struct {
	BotClient  *telegram.Client         // 独立的 Bot 客户端（可选）
	UserClient *telegram.Client         // 全局 Telegram 客户端实例
	Client     *telegram.Client         // 当前客户端实例
	Mutex      *sync.Mutex              // 并发锁
	Conf       *Conf                    // 全局配置指针
	FilesPath  string                   // 配置目录路径
	UserHash   string                   // 验证码所需的登录Hash
	Senders    map[int]*mtproto.MTProto // 独立的 Bot 客户端
}

var infos Infos
var startTime time.Time
var version = "v1.0.0"

func isAdmin(id int64) bool {
	if id == infos.Conf.UserID {
		return true
	}
	for _, admin := range infos.Conf.AdminIDs {
		if id == admin {
			return true
		}
	}
	return false
}

func main() {
	startTime = time.Now()
	// 加载配置文件
	files := flag.String("files", "files", "文件路径和名称")
	flag.Parse()

	value, err := loadConf(*files)
	if err != nil {
		log.Fatalf("载入配置文件失败: %+v", err)
		return
	}
	if infos.Mutex == nil {
		infos.Mutex = new(sync.Mutex)
	}

	if infos.Senders == nil {
		infos.Mutex.Lock()
		infos.Senders = make(map[int]*mtproto.MTProto)
		infos.Mutex.Unlock()
	}
	infos.Mutex.Lock()
	infos.FilesPath = *files
	infos.Conf = value
	infos.Mutex.Unlock()
	if infos.Conf.AppID == 0 || infos.Conf.BotToken == "" {
		log.Fatalf("配置文件缺少必要的 AppID 或 BotToken")
		return
	}

	if infos.Conf.Port == 0 {
		infos.Conf.Port = 8080
	}

	// 如果 AppID 不为空，清理非当前 AppID 的 bot 缓存文件
	if files, err := os.ReadDir(infos.FilesPath); err == nil {
		targetID := strconv.FormatInt(int64(infos.Conf.AppID), 10)
		for _, file := range files {
			name := file.Name()
			if !file.IsDir() && strings.HasPrefix(name, "bot_") && strings.HasSuffix(name, ".cache") {
				currentID := strings.TrimSuffix(strings.TrimPrefix(name, "bot_"), ".cache")
				if currentID != targetID {
					err := os.Remove(filepath.Join(infos.FilesPath, name))
					if err != nil {
						log.Printf("删除缓存文件失败: %v", err)
					}
				}
			}
		}
	}

	// 如果配置文件中包含 BotToken，优先让 Bot 上线用来交互与监听管理指令
	botConf := telegram.ClientConfig{
		AppID:    infos.Conf.AppID,
		AppHash:  infos.Conf.AppHash,
		LogLevel: telegram.LogError,
		Session:  filepath.Join(infos.FilesPath, "bot.session"),
		Cache:    telegram.NewCache(filepath.Join(infos.FilesPath, "bot.cache")),
	}

	client, err := telegram.NewClient(botConf)
	if err != nil {
		log.Fatalf("创建Bot客户端失败: %+v", err)
		return
	}
	infos.Mutex.Lock()
	infos.BotClient = client
	infos.Mutex.Unlock()
	infos.BotClient.On(telegram.OnMessage, handleBotCommand)

	if err := infos.BotClient.Connect(); err != nil {
		log.Fatalf("Bot连接失败: %+v", err)
		return
	}
	if err := infos.BotClient.LoginBot(infos.Conf.BotToken); err != nil {
		log.Fatalf("Bot登录失败: %+v", err)
		return
	}
	log.Printf("Bot 启动成功")

	// 忽略 SIGPIPE 信号
	signal.Ignore(syscall.SIGPIPE)

	// 创建信号通道
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// 尝试一并启动 UserBot（可能会卡或由于未登而只进行 Connect 不做其它业务）
	err = startUserBot()
	if err != nil {
		log.Printf("UserBot 启动失败: %+v", err)
		sigChan <- os.Interrupt
	} else {
		// 设置 HTTP 流式传输路由
		http.HandleFunc("/stream", handleStream)
		http.HandleFunc("/link", handleLink)
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/" {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			resp := map[string]any{
				"message": "服务器正在运行。",
				"ok":      true,
				"uptime":  handleTime(uint64(time.Since(startTime).Seconds())),
				"version": version,
			}
			err = json.NewEncoder(w).Encode(resp)
			if err != nil {
				log.Printf("发送网页失败: %+v", err)
			}
		})
		// 在协程中启动 HTTP 服务
		go func() {
			log.Printf("HTTP 服务运行在 %d 端口", infos.Conf.Port)
			if err := http.ListenAndServe(fmt.Sprintf(":%d", infos.Conf.Port), nil); err != nil {
				log.Printf("HTTP 服务启动失败: %+v", err)
				sigChan <- os.Interrupt
			}
		}()
	}

	// 等待中断信号
	sig := <-sigChan
	log.Printf("收到信号: %v, 正在退出...", sig)
}

func startUserBot() error {
	// 判断能否建立基本连接和是否授权过
	if infos.Conf.Phone == "" {
		log.Print("UserBot 授权失败. 请发送 /phone 手机号")
		if infos.BotClient != nil {
			if _, err := infos.BotClient.SendMessage(infos.Conf.UserID, "UserBot 授权失败. 请发送 /phone 手机号"); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
		}
		return nil
	}

	// 如果 Phone 不为空，清理非当前 Phone 的 user 缓存文件
	if infos.Conf.Phone != "" {
		if files, err := os.ReadDir(infos.FilesPath); err == nil {
			targetID := infos.Conf.Phone
			for _, file := range files {
				name := file.Name()
				if !file.IsDir() && strings.HasPrefix(name, "user_") && strings.HasSuffix(name, ".cache") {
					currentID := strings.TrimSuffix(strings.TrimPrefix(name, "user_"), ".cache")
					if currentID != targetID {
						err := os.Remove(filepath.Join(infos.FilesPath, name))
						if err != nil {
							log.Printf("删除缓存文件失败: %v", err)
						}
					}
				}
			}
		}
	}

	userConf := telegram.ClientConfig{
		AppID:    infos.Conf.AppID,
		AppHash:  infos.Conf.AppHash,
		LogLevel: telegram.LogError,
		Session:  filepath.Join(infos.FilesPath, "user.session"),
		Cache:    telegram.NewCache(filepath.Join(infos.FilesPath, "user.cache")),
	}

	client, err := telegram.NewClient(userConf)
	if err != nil {
		log.Printf("创建UserBot客户端失败: %+v", err)
		return err
	}
	infos.Mutex.Lock()
	infos.UserClient = client
	infos.Mutex.Unlock()

	if err := client.Connect(); err != nil {
		log.Printf("UserBot连接失败: %+v", err)
		return err
	}

	if is, err := client.IsAuthorized(); !is {
		if err := handlePhone(); err != nil {
			log.Printf("发送验证码失败: %+v", err)
		}
		if err != nil {
			log.Printf("UserBot授权失败: %+v", err)
			return nil
		} else {
			log.Printf("UserBot未授权. 请重新发送 /phone 手机号")
			return nil
		}
	}
	return initUserBot()
}

// 供登陆成功或者检测已授权时调用，初始化正式监听逻辑
func initUserBot() error {
	me, err := infos.UserClient.GetMe()
	if err != nil {
		log.Printf("获取用户信息失败: %v", err)
		return err
	}
	log.Printf("UserBot 登录成功: %s", me.Username)

	// 预先取一次对话列表（缓存 AccessHash），解决长时间闲置后无法 ResolvePeer 的问题
	log.Printf("UserBot 正在缓存对话列表...")
	if _, err := infos.UserClient.GetDialogs(&telegram.DialogOptions{Limit: 100}); err != nil {
		log.Printf("UserBot 缓存对话列表失败: %v", err)
	}

	infos.UserClient.On(telegram.OnMessage, handleMess)
	return nil
}

func handlePhone() error {
	for count := 0; count < 3; count++ {
		sendCode, err := infos.UserClient.AuthSendCode(infos.Conf.Phone, infos.Conf.AppID, infos.Conf.AppHash, &telegram.CodeSettings{})
		if err != nil {
			if strings.Contains(err.Error(), "DC_MIGRATE") {
				time.Sleep(time.Second) // 等待一秒后重试
				continue
			} else {
				if _, err := infos.BotClient.SendMessage(infos.Conf.UserID, "发送验证码失败: "+err.Error()); err != nil {
					log.Printf("发送消息失败: %+v", err)
				}
				return err
			}
		} else {
			infos.Mutex.Lock()
			infos.UserHash = sendCode.(*telegram.AuthSentCodeObj).PhoneCodeHash
			infos.Mutex.Unlock()
			if _, err := infos.BotClient.SendMessage(infos.Conf.UserID, "验证码已发送, 请发送 /code 验证码 来完成登录"); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			return nil
		}
	}
	return nil
}

func handleBotCommand(m *telegram.NewMessage) error {
	text := strings.TrimSpace(m.Text())
	if !isAdmin(m.SenderID()) {
		log.Printf("收到非管理员消息: %d", m.SenderID())
		return nil
	}

	switch {
	case strings.HasPrefix(text, "/allow "):
		whiteID, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(text, "/allow ")), 10, 64)
		if err != nil {
			if _, err := m.Reply("添加白名单失败: " + err.Error()); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			return err
		}
		infos.Mutex.Lock()
		infos.Conf.WhiteIDs = append(infos.Conf.WhiteIDs, whiteID)
		infos.Mutex.Unlock()
		if _, err := m.Reply(fmt.Sprintf("添加白名单成功: %d", whiteID)); err != nil {
			log.Printf("发送消息失败: %+v", err)
		}
		return nil
	case strings.HasPrefix(text, "/disallow "):
		whiteID, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(text, "/disallow ")), 10, 64)
		if err != nil {
			if _, err := m.Reply("移除白名单失败: " + err.Error()); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			return err
		}
		infos.Mutex.Lock()
		infos.Conf.WhiteIDs = slices.DeleteFunc(infos.Conf.WhiteIDs, func(num int64) bool {
			return num == whiteID
		})
		infos.Mutex.Unlock()
		if _, err := m.Reply(fmt.Sprintf("移除白名单成功: %d", whiteID)); err != nil {
			log.Printf("发送消息失败: %+v", err)
		}
		return nil
	case strings.HasPrefix(text, "/phone "):
		infos.Mutex.Lock()
		infos.Conf.Phone = strings.TrimSpace(strings.TrimPrefix(text, "/phone "))
		infos.Mutex.Unlock()
		if _, err := m.Reply(fmt.Sprintf("收到手机号 %s, 正在尝试发送验证码...", infos.Conf.Phone)); err != nil {
			log.Printf("发送消息失败: %+v", err)
		}

		if infos.UserClient == nil {
			err := startUserBot()
			if err != nil {
				_, err := m.Reply("UserBot 启动失败: " + err.Error())
				if err != nil {
					log.Printf("发送消息失败: %+v", err)
				}
				return err
			}
		}
		return handlePhone()
	case strings.HasPrefix(text, "/code "):
		if infos.Conf.Phone == "" || infos.UserHash == "" {
			if _, err := m.Reply("请先发送 /phone 手机号"); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			return nil
		}

		code := strings.TrimSpace(strings.TrimPrefix(text, "/code "))
		if _, err := m.Reply("正在登录..."); err != nil {
			log.Printf("发送消息失败: %+v", err)
		}

		if infos.UserClient == nil {
			err := startUserBot()
			if err != nil {
				if _, err := m.Reply("UserBot 启动失败: " + err.Error()); err != nil {
					log.Printf("发送消息失败: %+v", err)
				}
				return err
			}
		}

		if _, err := infos.UserClient.AuthSignIn(infos.Conf.Phone, infos.UserHash, code, nil); err != nil {
			if _, err := m.Reply("登录失败: " + err.Error()); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			infos.Mutex.Lock()
			infos.Conf.Phone = ""
			infos.UserHash = ""
			infos.Mutex.Unlock()
			return err
		}

		// 登录成功，更新配置并保存，因为设置了 SessionFile 也会一同持久化

		if err := saveConf(infos.Conf, infos.FilesPath); err != nil {
			if _, err := m.Reply("登录成功, 但保存配置失败: " + err.Error()); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
		} else {
			if _, err := m.Reply("登录成功"); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
		}
		return initUserBot()
	default:
		return handleMess(m)
	}
}

// handleM 处理接收到的新消息，解析其中的媒体文件或 tg 链接
func handleMess(m *telegram.NewMessage) error {
	// 如果是用户发送或转发来的、带有图片/文档/视频的消息，直接生成直链
	if m.IsMedia() && (m.Photo() != nil || m.Document() != nil || m.Video() != nil) {
		link := fmt.Sprintf("%s:%d/stream?cid=%d&mid=%d&cate=bot", strings.TrimSuffix(infos.Conf.Site, "/"), infos.Conf.Port, m.ChatID(), m.ID)
		return sendLink(m, link)
	}

	text := m.Text() // 获取文本内容
	if text == "" {
		log.Printf("消息为空")
		return nil
	}

	// 编译正则表达式匹配 Telegram 链接格式
	// 匹配格式如：t.me/c/12345/678 或 t.me/username/678
	re := regexp.MustCompile(`t\.me\/(c\/(\d+)|([a-zA-Z0-9_]+))\/(\d+)`)
	matches := re.FindAllStringSubmatch(text, -1)

	// 遍历所有匹配到的链接
	for _, match := range matches {
		var cid any   // 用于 ResolvePeer 的标识项（可以是用户名或 chatID）
		var mid int32 // 消息 ID

		// 解析逻辑
		if match[2] != "" {
			// 如果是 c/(\d+)，代表私有频道链接，需要给 ID 补充前缀 -100
			value, err := strconv.ParseInt("-100"+match[2], 10, 64)
			if err != nil {
				if _, err := m.Reply("解析频道ID失败"); err != nil {
					log.Printf("发送消息失败: %+v", err)
				}
				continue
			}
			cid = value
		} else {
			// 否则匹配的是公开频道的 username
			cid = match[3]
		}

		// 解析消息偏移 ID
		value, err := strconv.ParseInt(match[4], 10, 32)
		if err != nil {
			if _, err := m.Reply("解析消息ID失败"); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			continue
		}
		mid = int32(value)

		// 使用 GetMessages 尝试获取目标消息，gogram 会自动映射 peer 为 InputPeer
		ms, err := infos.UserClient.GetMessages(cid, &telegram.SearchOption{IDs: []int32{mid}})
		if err != nil || len(ms) == 0 {
			if _, err := m.Reply("找不到指定的消息, 或者 User Bot 没有权限(请确认已加入频道)"); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			continue
		}

		src := ms[0] // 获取第一条目标消息
		// 判断该消息是否包含可下载的媒体内容
		if !src.IsMedia() || (src.Photo() == nil && src.Document() == nil && src.Video() == nil) {
			if _, err := m.Reply("链接对应的消息不包含媒体文件"); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			continue
		}

		// 为媒体文件构造下载直链
		link := fmt.Sprintf("%s:%d/stream?cid=%d&mid=%d&cate=user", strings.TrimSuffix(infos.Conf.Site, "/"), infos.Conf.Port, src.ChatID(), src.ID)
		err = sendLink(m, link)
		if err != nil {
			log.Printf("推送直链失败: %+v", err)
		}
	}
	return nil
}

func handleMediaCate(fileName string) string {
	lowerFileName := strings.ToLower(fileName)
	switch {
	case strings.HasSuffix(lowerFileName, ".webm"):
		return "video/webm"
	case strings.HasSuffix(lowerFileName, ".avi"):
		return "video/x-msvideo"
	case strings.HasSuffix(lowerFileName, ".wmv"):
		return "video/x-ms-wmv"
	case strings.HasSuffix(lowerFileName, ".flv"):
		return "video/x-flv"
	case strings.HasSuffix(lowerFileName, ".mov"):
		return "video/quicktime"
	case strings.HasSuffix(lowerFileName, ".mkv"):
		return "video/x-matroska"
	case strings.HasSuffix(lowerFileName, ".ts"):
		return "video/mp2t"
	case strings.HasSuffix(lowerFileName, ".mpeg"), strings.HasSuffix(lowerFileName, ".mpg"):
		return "video/mpeg"
	case strings.HasSuffix(lowerFileName, ".3gpp"), strings.HasSuffix(lowerFileName, ".3gp"):
		return "video/3gpp"
	case strings.HasSuffix(lowerFileName, ".mp4"), strings.HasSuffix(lowerFileName, ".m4s"):
		return "video/mp4"
	default:
		return "application/octet-stream"
	}
}

// handleStream 处理来自 HTTP 的文件流式读取请求
func handleStream(w http.ResponseWriter, r *http.Request) {
	// 获取 URL 传参
	params := r.URL.Query()
	password := params.Get("key")
	if infos.Conf.Password != "" && password != infos.Conf.Password {
		http.Error(w, "无效的密码", http.StatusUnauthorized)
		return
	}

	cid, err := strconv.ParseInt(params.Get("cid"), 10, 64)
	if err != nil || cid == 0 {
		http.Error(w, "频道ID无效", http.StatusBadRequest)
		return
	}
	value, err := strconv.ParseInt(params.Get("mid"), 10, 32)
	if err != nil || value == 0 {
		http.Error(w, "消息ID无效", http.StatusBadRequest)
		return
	}
	mid := int32(value)

	cate := params.Get("cate")
	if cate == "bot" {
		infos.Client = infos.BotClient
	} else {
		infos.Client = infos.UserClient
	}

	// 获取消息内容 (增加一次重试逻辑，解决由于长时间闲置导致 Peer 缓存失效的问题)
	ms, err := infos.Client.GetMessages(cid, &telegram.SearchOption{IDs: []int32{mid}})
	if (err != nil || len(ms) == 0) && infos.UserClient != nil && cate != "bot" {
		log.Printf("首次获取消息失败，尝试刷新对话列表后重试: cid=%d, mid=%d, err=%v", cid, mid, err)
		// 刷新一次对话列表（触发库内部自动解析 Entity 并更新 AccessHash 缓存）
		if _, err := infos.UserClient.GetDialogs(&telegram.DialogOptions{Limit: 100}); err == nil {
			ms, err = infos.UserClient.GetMessages(cid, &telegram.SearchOption{IDs: []int32{mid}})
		}
	}

	if err != nil || len(ms) == 0 {
		log.Printf("获取消息失败: cid=%d, mid=%d, err=%v, count=%d", cid, mid, err, len(ms))
		http.Error(w, fmt.Sprintf("获取消息失败: cid=%d, mid=%d, err=%v, count=%d", cid, mid, err, len(ms)), http.StatusNotFound)
		return
	}
	src := ms[0]

	// 确保消息包含媒体文件
	if !src.IsMedia() {
		log.Printf("消息不包含媒体: cid=%d, mid=%d", cid, mid)
		http.Error(w, fmt.Sprintf("消息不包含媒体: cid=%d, mid=%d", cid, mid), http.StatusBadRequest)
		return
	}

	// 通过 gogram 获取媒体文件的位置信息及详细数据
	loc, dc, size, fileName, err := telegram.GetFileLocation(src.Media(), telegram.FileLocationOptions{})
	if err != nil {
		log.Printf("获取文件位置失败: cid=%d, mid=%d, err=%v", cid, mid, err)
		http.Error(w, fmt.Sprintf("获取文件位置失败: cid=%d, mid=%d, err=%v", cid, mid, err), http.StatusInternalServerError)
		return
	}
	log.Printf("开始推流: cid=%d, mid=%d, file=%s, size=%d, dc=%d", cid, mid, fileName, size, dc)

	w.Header().Set("Accept-Ranges", "bytes")
	mimeType := handleMediaCate(fileName)
	w.Header().Set("Content-Type", mimeType)

	disposition := "inline"
	if r.URL.Query().Get("download") == "true" {
		disposition = "attachment"
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("%s; filename=\"%s\"", disposition, fileName))

	var start, end int64
	rangeHeader := r.Header.Get("Range")

	if rangeHeader == "" {
		start = 0
		end = size - 1
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		w.WriteHeader(http.StatusOK)
	} else {
		matches := regexp.MustCompile(`bytes= *([0-9]+) *- *([0-9]*)`).FindStringSubmatch(rangeHeader)
		if matches != nil {
			start, _ = strconv.ParseInt(matches[1], 10, 64)
			if matches[2] != "" {
				end, _ = strconv.ParseInt(matches[2], 10, 64)
			} else {
				end = size - 1
			}
		} else {
			start = 0
			end = size - 1
		}
		if end >= size {
			end = size - 1
		}
		if start > end {
			start = end
		}
		contentLength := end - start + 1
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, size))
		w.Header().Set("Content-Length", strconv.FormatInt(contentLength, 10))
		w.WriteHeader(http.StatusPartialContent)
	}

	if r.Method != http.MethodHead {
		reader := newReader(r.Context(), infos.Client, loc, dc, start, end, end-start+1, cid, mid, cate)
		defer func() {
			err := reader.Close()
			if err != nil {
				log.Printf("关闭 telegram reader 失败: %v", err)
			}
		}()
		_, err := io.CopyN(w, reader, end-start+1)
		if err != nil && err != io.EOF {
			log.Printf("流式传输文件时出错: %v", err)
		}
	}
}

func handleLink(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	password := params.Get("key")
	if infos.Conf.Password != "" && password != infos.Conf.Password {
		http.Error(w, "无效的密码", http.StatusUnauthorized)
		return
	}
	link := params.Get("link")
	if link == "" || !strings.HasPrefix(link, "http") {
		http.Error(w, "无效的链接", http.StatusBadRequest)
		return
	}
	// 编译正则表达式匹配 Telegram 链接格式
	// 匹配格式如：t.me/c/12345/678 或 t.me/username/678
	re := regexp.MustCompile(`t\.me\/(c\/(\d+)|([a-zA-Z0-9_]+))\/(\d+)`)
	matches := re.FindAllStringSubmatch(link, -1)

	// 遍历所有匹配到的链接
	for _, match := range matches {
		var cid any   // 用于 ResolvePeer 的标识项（可以是用户名或 chatID）
		var mid int32 // 消息 ID

		// 解析逻辑
		if match[2] != "" {
			// 如果是 c/(\d+)，代表私有频道链接，需要给 ID 补充前缀 -100
			value, err := strconv.ParseInt("-100"+match[2], 10, 64)
			if err != nil {
				log.Printf("解析频道ID失败: %+v", err)
				continue
			}
			cid = value
		} else {
			// 否则匹配的是公开频道的 username
			cid = match[3]
		}

		// 解析消息偏移 ID
		value, err := strconv.ParseInt(match[4], 10, 32)
		if err != nil {
			log.Printf("解析消息ID失败: %+v", err)
			continue
		}
		mid = int32(value)

		// 使用 GetMessages 尝试获取目标消息，gogram 会自动映射 peer 为 InputPeer
		ms, err := infos.UserClient.GetMessages(cid, &telegram.SearchOption{IDs: []int32{mid}})
		if err != nil || len(ms) == 0 {
			log.Printf("获取消息失败: cid=%d, mid=%d, err=%v, count=%d", cid, mid, err, len(ms))
			continue
		}

		src := ms[0] // 获取第一条目标消息
		// 判断该消息是否包含可下载的媒体内容
		if !src.IsMedia() || (src.Photo() == nil && src.Document() == nil && src.Video() == nil) {
			log.Printf("消息不包含媒体: cid=%d, mid=%d", cid, mid)
			continue
		}

		// 为媒体文件构造下载直链
		if infos.Conf.Password != "" {
			link = fmt.Sprintf("%s:%d/stream?cid=%d&mid=%d&key=%s&cate=user", strings.TrimSuffix(infos.Conf.Site, "/"), infos.Conf.Port, src.ChatID(), src.ID, infos.Conf.Password)
		} else {
			link = fmt.Sprintf("%s:%d/stream?cid=%d&mid=%d&cate=user", strings.TrimSuffix(infos.Conf.Site, "/"), infos.Conf.Port, src.ChatID(), src.ID)
		}
		http.Redirect(w, r, link, http.StatusFound)
	}

}

func handleTime(seconds uint64) string {
	days := seconds / 86400
	hours := (seconds % 86400) / 3600
	minutes := (seconds % 3600) / 60
	secs := seconds % 60

	if days > 0 {
		return fmt.Sprintf("%dd %dh %dm %ds", days, hours, minutes, secs)
	} else if hours > 0 {
		return fmt.Sprintf("%dh %dm %ds", hours, minutes, secs)
	} else if minutes > 0 {
		return fmt.Sprintf("%dm %ds", minutes, secs)
	}
	return fmt.Sprintf("%ds", secs)
}

// sendLink 发送美化后的下载链接消息
func sendLink(m *telegram.NewMessage, link string) error {
	if infos.Conf.Password != "" {
		link += fmt.Sprintf("&key=%s", infos.Conf.Password)
	}
	text := fmt.Sprintf("<b>🔗 链接提取成功</b>\n\n<code>%s</code>\n\n👆 <i>点击上方链接复制，下方按钮下载</i> 👇", html.EscapeString(link))
	markup := telegram.InlineURL(
		"🚀 直接下载", fmt.Sprintf("%s&download=true", link),
	)

	// 发送消息并设置解析模式为 HTML，附带内联键盘
	_, err := m.Reply(text, &telegram.SendOptions{
		ParseMode:   "html",
		ReplyMarkup: markup,
	})
	if err != nil {
		log.Printf("发送下载链接失败: %+v", err)
	}
	return err
}
