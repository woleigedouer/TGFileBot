package main

import (
	"bufio"         // 用于读取文件流
	"encoding/json" // 用于处理 JSON 数据
	"errors"        // 用于处理错误
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

	"github.com/amarnathcjd/gogram/telegram" // 导入 gogram 客户端核心库
)

type CleanRealm struct {
	Filter bool   // 是否过滤
	ID     string // 过滤ID
	Cate   string // 过滤类型: bot 或者 user
	Realm  string // 过滤范围: cache 或者 session
}

type Infos struct {
	BotClient  *telegram.Client // 独立的 Bot 客户端（可选）
	UserClient *telegram.Client // 全局 Telegram 客户端实例
	Client     *telegram.Client // 当前客户端实例
	Mutex      *sync.Mutex      // 并发锁
	Conf       *Conf            // 全局配置指针
	HasNew     bool             // 是否有新配置
	FilesPath  string           // 配置目录路径
	FilePath   string           // 日志文件路径
	Status     int              // 登录状态: 0 未登录, 1 验证码, 2 密码, 3 已登录
	BotID      int64            // Bot 的 ID
	Code       chan string      // 验证码
	Pass       chan string      // 二次验证密码
	//UserHash  string      // 验证码所需的登录Hash
}

var infos *Infos
var startTime time.Time
var version = "v1.0.1"

func newInfos(filePath, filesPath string) (*Infos, error) {
	// 创建日志文件
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		log.Printf("无法打开日志文件: %v", err)
	}

	defer func() {
		if err := file.Close(); err != nil {
			log.Printf("关闭日志文件错误: %v", err)
		}
	}()

	// 设置日志输出
	multiWriter := io.MultiWriter(os.Stdout, file)
	log.SetOutput(multiWriter)

	// 加载配置文件
	conf, err := loadConf(filesPath)
	if err != nil {
		log.Fatalf("载入配置文件失败: %+v", err)
	}

	// 获取 BotID
	var botID int64
	if conf.BotToken != "" {
		parts := strings.Split(conf.BotToken, ":")
		if len(parts) < 1 {
			return nil, fmt.Errorf("BotToken 格式错误: %s", conf.BotToken)
		}
		result := strings.TrimSpace(parts[0])
		botID, err = strconv.ParseInt(result, 10, 64)
		if err != nil {
			log.Printf("解析 BotID 失败: %+v", err)
		}
	}

	return &Infos{
		FilePath:  filePath,
		FilesPath: filesPath,
		Conf:      conf,
		BotID:     botID,
		Mutex:     new(sync.Mutex),
		Code:      make(chan string, 1),
		Pass:      make(chan string, 1),
	}, nil
}

func main() {
	startTime = time.Now()
	// 加载配置文件
	files := flag.String("files", "files", "文件路径和名称")
	file := flag.String("log", "files/log.log", "日志文件路径")
	flag.Parse()

	// 初始化
	value, err := newInfos(*file, *files)
	if err != nil {
		log.Printf("初始化失败: %+v", err)
		return
	}
	infos = value

	// 退出时清理
	defer func() {
		if infos.BotClient != nil {
			if err := infos.BotClient.Disconnect(); err != nil {
				log.Printf("Bot 退出失败: %+v", err)
			}
		}
		if infos.UserClient != nil {
			if err := infos.UserClient.Disconnect(); err != nil {
				log.Printf("UserBot 退出失败: %+v", err)
			}
		}
	}()

	if infos.Conf.AppID == 0 || infos.Conf.AppHash == "" || infos.Conf.BotToken == "" {
		log.Panicf("配置文件缺少必要的参数: AppID、AppHash、BotToken")
		return
	}

	if infos.Conf.Port == 0 {
		infos.Conf.Port = 8080
	}

	// 启动 Bot
	err = infos.startBot()
	if err != nil {
		return
	}

	// 启动 UserBot 客户端
	err = infos.userBotClient()
	if err != nil {
		log.Printf("UserBot 启动失败: %+v", err)
		return
	}

	// 忽略 SIGPIPE 信号
	signal.Ignore(syscall.SIGPIPE)

	// 创建信号通道
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// 在协程中启动 HTTP 服务
	go func() {
		log.Printf("HTTP 服务运行在 %d 端口", infos.Conf.Port)
		server := &http.Server{
			Addr:              fmt.Sprintf(":%d", infos.Conf.Port),
			Handler:           http.HandlerFunc(handleMain),
			ReadTimeout:       30 * time.Second,  // 读取请求超时
			ReadHeaderTimeout: 10 * time.Second,  // 读取请求头超时
			WriteTimeout:      60 * time.Second,  // 写入响应超时
			IdleTimeout:       600 * time.Second, // 空闲连接超时
			MaxHeaderBytes:    1 << 20,           // 1MB
		}

		if err := server.ListenAndServe(); err != nil {
			log.Printf("HTTP 服务启动失败: %+v", err)
			sigChan <- os.Interrupt
		}
	}()

	if infos.BotClient != nil {
		if _, err := infos.BotClient.SendMessage(infos.Conf.UserID, "程序已启动"); err != nil {
			log.Printf("发送消息失败: %+v", err)
		}
		if err := infos.checkStatus(); err != nil {
			log.Printf("UserBot 登录失败: %+v", err)
			infos.resetStatus()
			sigChan <- os.Interrupt
		}
	}

	// 等待中断信号
	sig := <-sigChan
	log.Printf("收到信号: %v, 正在退出...", sig)
	if infos.BotClient != nil {
		if _, err := infos.BotClient.SendMessage(infos.Conf.UserID, "程序已退出"); err != nil {
			log.Printf("发送消息失败: %+v", err)
		}
	}
}

func (infos *Infos) isAdmin(id int64) bool {
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

func (infos *Infos) isWhiteList(id int64) bool {
	for _, whiteID := range infos.Conf.WhiteIDs {
		if id == whiteID {
			return true
		}
	}
	return infos.isAdmin(id) || id == infos.BotID
}

func (infos *Infos) startBot() (err error) {
	botID := strconv.FormatInt(infos.BotID, 10)
	if botID != "" && botID != "0" {
		cleanFiles(CleanRealm{ID: botID, Cate: "bot", Realm: "cache", Filter: true})
	}

	// 如果配置文件中包含 BotToken, 优先让 Bot 上线用来交互与监听管理指令
	botConf := telegram.ClientConfig{
		AppID:        infos.Conf.AppID,
		AppHash:      infos.Conf.AppHash,
		LogLevel:     telegram.LogError,
		Session:      filepath.Join(infos.FilesPath, "bot.session"),
		Cache:        telegram.NewCache(filepath.Join(infos.FilesPath, "bot.cache")),
		CacheSenders: true,
		FloodHandler: func(err error) bool {
			wait := 3
			re := regexp.MustCompile(`(\d+).*?seconds`)
			match := re.FindStringSubmatch(err.Error())
			if len(match) > 1 {
				if wait, err = strconv.Atoi(match[1]); err != nil {
					log.Printf("解析等待时间失败: err=%v", err)
				}
			}
			log.Printf("下载太过频繁, 等待 %d 秒后重试", wait)
			time.Sleep(time.Duration(wait) * time.Second)
			return true
		},
	}

	// 创建 Bot 客户端
	client, err := telegram.NewClient(botConf)
	if err != nil {
		// 清理缓存
		cleanFiles(CleanRealm{Cate: "bot", Realm: "session"})
		cleanFiles(CleanRealm{Cate: "bot", Realm: "cache", Filter: false})
		log.Printf("创建 Bot 客户端失败: %+v", err)
		return
	}

	// 连接 Bot
	if err = client.Connect(); err != nil {
		// 清理缓存
		cleanFiles(CleanRealm{Cate: "bot", Realm: "session"})
		cleanFiles(CleanRealm{Cate: "bot", Realm: "cache", Filter: false})
		log.Printf("Bot 连接失败: %+v", err)
		return
	}

	// 登录 Bot
	if err = client.LoginBot(infos.Conf.BotToken); err != nil {
		// 清理缓存
		cleanFiles(CleanRealm{Cate: "bot", Realm: "session"})
		cleanFiles(CleanRealm{Cate: "bot", Realm: "cache", Filter: false})
		log.Printf("Bot 登录失败: %+v", err)
		return
	}

	// 注册 Bot 命令处理函数
	client.On(telegram.OnMessage, handleBotCommand)
	log.Printf("Bot 启动成功")

	infos.Mutex.Lock()
	infos.BotClient = client
	infos.Mutex.Unlock()
	return
}

func (infos *Infos) userBotClient() (err error) {
	// 清理缓存
	userID := strconv.FormatInt(infos.Conf.UserID, 10)
	if userID != "" && userID != "0" {
		cleanFiles(CleanRealm{ID: userID, Cate: "user", Realm: "cache", Filter: true})
	}

	botConf := telegram.ClientConfig{
		AppID:        infos.Conf.AppID,
		AppHash:      infos.Conf.AppHash,
		LogLevel:     telegram.LogError,
		Session:      filepath.Join(infos.FilesPath, "user.session"),
		Cache:        telegram.NewCache(filepath.Join(infos.FilesPath, "user.cache")),
		CacheSenders: true,
		FloodHandler: func(err error) bool {
			wait := 3
			re := regexp.MustCompile(`(\d+).*?seconds`)
			match := re.FindStringSubmatch(err.Error())
			if len(match) > 1 {
				if wait, err = strconv.Atoi(match[1]); err != nil {
					log.Printf("解析等待时间失败: err=%+v", err)
				}
			}
			log.Printf("下载太过频繁, 等待 %d 秒后重试, err=%+v", wait, err)
			time.Sleep(time.Duration(wait) * time.Second)
			return true
		},
	}
	if infos.Conf.DC != 0 {
		botConf.DataCenter = infos.Conf.DC
	}

	client, err := telegram.NewClient(botConf)
	if err != nil {
		// 清理缓存
		cleanFiles(CleanRealm{Cate: "user", Realm: "session"})
		cleanFiles(CleanRealm{Cate: "user", Realm: "cache", Filter: false})
		log.Printf("创建 UserBot 客户端失败: %+v", err)
		return
	}

	// 连接 Bot
	if err = client.Connect(); err != nil {
		// 清理缓存
		cleanFiles(CleanRealm{Cate: "user", Realm: "session"})
		cleanFiles(CleanRealm{Cate: "user", Realm: "cache", Filter: false})
		log.Printf("UserBot 连接失败: %+v", err)
		return
	}

	infos.Mutex.Lock()
	infos.UserClient = client
	infos.Mutex.Unlock()

	return err
}

func (infos *Infos) startUserBot(phone string) (err error) {
	infos.Mutex.Lock()
	switch infos.Status {
	case 1, 2:
		infos.Mutex.Unlock()
		err = errors.New("已有登录流程正在进行")
		log.Printf("UserBot 登录失败: %+v", err)
		return err
	case 3:
		infos.Mutex.Unlock()
		if infos.UserClient == nil {
			if err := infos.userBotClient(); err != nil {
				log.Printf("UserBot 登录失败: %+v", err)
				infos.resetStatus()
				return err
			}
		}
		return nil
	default:
		// infos.Status = 1
		infos.Mutex.Unlock()
		if infos.UserClient == nil {
			if err := infos.userBotClient(); err != nil {
				log.Printf("UserBot 登录失败: %+v", err)
				infos.resetStatus()
				return err
			}
		}
		if _, err := infos.BotClient.SendMessage(infos.Conf.UserID, fmt.Sprintf("收到手机号 %s, 正在尝试发送验证码...", phone)); err != nil {
			log.Printf("发送消息失败: %+v", err)
		}
		// 启动登录流程（会阻塞, 直到登录完成或失败）
		go func() {
			status, err := infos.UserClient.Login(phone, &telegram.LoginOptions{
				CodeCallback:     infos.code,
				PasswordCallback: infos.pass,
				MaxRetries:       3,
			})
			if err != nil {
				log.Printf("UserBot 登录失败: %+v", err)
				if _, err := infos.BotClient.SendMessage(infos.Conf.UserID, fmt.Sprintf("登录失败: %+v", err)); err != nil && infos.Status != 0 {
					log.Printf("发送消息失败: %+v", err)
				}
				infos.resetStatus()
				return
			}

			if status == true {
				log.Printf("UserBot 登录成功")
				if err := infos.checkStatus(); err != nil {
					log.Printf("UserBot 登录失败: %+v", err)
					infos.resetStatus()
					return
				}
			}
		}()
	}

	return nil
}

func (infos *Infos) checkStatus() (err error) {
	// 登录成功
	me, err := infos.UserClient.GetMe()
	if err != nil {
		log.Printf("获取用户信息失败: %v", err)
		infos.Mutex.Lock()
		infos.Status = 0
		infos.Mutex.Unlock()
		return nil
	}

	if me.ID == infos.Conf.UserID {
		log.Printf("登录成功! 用户: @%s", me.Username)
		if _, err := infos.BotClient.SendMessage(infos.Conf.UserID, fmt.Sprintf("登录成功! 用户: @%s", me.Username)); err != nil && infos.Status != 0 {
			log.Printf("发送消息失败: %+v", err)
		}
		infos.Mutex.Lock()
		infos.Status = 3
		infos.Mutex.Unlock()
		return nil
	} else {
		log.Printf("登录失败: 用户ID不匹配, 期望 %d, 实际 %d", infos.Conf.UserID, me.ID)
		if infos.UserClient != nil {
			if err := infos.UserClient.Disconnect(); err != nil {
				log.Printf("UserBot 退出失败: %+v", err)
			}
		}
		infos.resetStatus()
		return infos.userBotClient()
	}
}

func (infos *Infos) code() (code string, err error) {
	if infos.Status == 0 {
		log.Println("等待用户输入验证码...")
		infos.Mutex.Lock()
		infos.Status = 1
		infos.Mutex.Unlock()
		select {
		case code := <-infos.Code:
			log.Printf("收到验证码: %s", code)
			return code, nil
		case <-time.After(2 * time.Minute):
			err = errors.New("等待验证码超时")
			return "", err
		}
	} else {
		err = errors.New("当前状态不是等待验证码")
		log.Printf("提交验证码失败: %+v", err)
		return "", err
	}
}

func (infos *Infos) submitCode(code string) (err error) {
	infos.Mutex.Lock()
	defer infos.Mutex.Unlock()

	if infos.Status != 1 {
		err = errors.New("当前状态不是等待验证码")
		log.Printf("提交验证码失败: %+v", err)
		return err
	}
	infos.Code <- code
	return nil
}

func (infos *Infos) pass() (pass string, err error) {
	if infos.Status == 1 {
		log.Println("等待用户输入密码...")
		infos.Mutex.Lock()
		infos.Status = 2
		infos.Mutex.Unlock()
		select {
		case pass := <-infos.Pass:
			log.Printf("收到密码: %s", pass)
			return pass, nil
		case <-time.After(2 * time.Minute):
			err = errors.New("等待密码超时")
			return "", err
		}
	} else {
		err = errors.New("当前状态不是等待密码")
		log.Printf("提交密码失败: %+v", err)
		return "", err
	}
}

func (infos *Infos) submitPass(pass string) (err error) {
	infos.Mutex.Lock()
	defer infos.Mutex.Unlock()

	if infos.Status != 2 {
		err = errors.New("当前状态不是等待密码")
		log.Printf("提交密码失败: %+v", err)
		return err
	}
	infos.Pass <- pass
	return nil
}

func (infos *Infos) resetStatus() {
	// 断开连接
	if err := infos.UserClient.Disconnect(); err != nil {
		log.Printf("UserBot 断开连接失败: %+v", err)
	}
	// 清理缓存
	cleanFiles(CleanRealm{Cate: "user", Realm: "session"})
	cleanFiles(CleanRealm{Cate: "user", Realm: "cache", Filter: false})
	// 重置状态
	infos.Mutex.Lock()
	infos.UserClient = nil
	infos.Status = 0
	infos.Mutex.Unlock()
	return
}

func handleMain(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if path != "/" {
		path = strings.TrimSuffix(path, "/")
	}
	switch {
	case path == "/":
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		content := map[string]any{
			"message": "服务器正在运行。",
			"ok":      true,
			"uptime":  handleTime(uint64(time.Since(startTime).Seconds())),
			"version": version,
		}
		err := json.NewEncoder(w).Encode(content)
		if err != nil {
			log.Printf("发送网页失败: %+v", err)
		}
		return
	case path == "/log":
		// 1. 设置响应头
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Connection", "keep-alive")
		// 禁用 Nginx 等代理的缓冲 (可选, 但对流式传输很重要)
		w.Header().Set("X-Content-Type-Options", "nosniff")

		// 获取 Flusher 接口
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "浏览器不支持流式传输", http.StatusInternalServerError)
			return
		}

		// 2. 打开日志文件
		file, err := os.Open(infos.FilePath)
		if err != nil {
			if _, err := fmt.Fprintf(w, "无法打开日志文件: %v\n", err); err != nil {
				log.Printf("发送网页失败: %+v", err)
			}
			return
		}
		defer func() {
			err := file.Close()
			if err != nil {
				log.Printf("关闭日志文件错误: %v", err)
			}
		}()

		ctx := r.Context()
		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			select {
			case <-ctx.Done():
				return
			default:
				if _, err := fmt.Fprintf(w, "%s\n", scanner.Text()); err != nil {
					log.Printf("发送网页失败: %+v", err)
				}
				flusher.Flush()
			}
		}
		if err := scanner.Err(); err != nil {
			log.Printf("读取文件时发生错误: %v", err)
			if _, err := fmt.Fprintf(w, "\n[服务器端读取错误: %v]\n", err); err != nil {
				log.Printf("发送网页失败: %+v", err)
			}
		}
	case strings.HasPrefix(path, "/link"):
		handleLink(w, r)
		return
	case strings.HasPrefix(path, "/stream"):
		handleStream(w, r)
		return
	default:
		http.NotFound(w, r)
		return
	}
}

func handleBotCommand(m *telegram.NewMessage) error {
	defer func() {
		if infos.HasNew {
			if err := saveConf(infos.Conf, infos.FilesPath); err != nil {
				log.Printf("保存配置文件失败: %+v", err)
			}
		}
		infos.Mutex.Lock()
		infos.HasNew = false
		infos.Mutex.Unlock()
	}()

	if m.Sender.ID == infos.BotID {
		return nil
	}

	text := strings.TrimSpace(m.Text())

	switch {
	case strings.HasPrefix(text, "/start"):
		if m.SenderID() != infos.Conf.UserID {
			if _, err := m.Reply("你没有使用此机器人的权限"); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			return nil
		}
		var src string
		switch infos.Status {
		case 0:
			src = "userBot 未登录, 请发送 /phone 手机号"
		case 1:
			src = "正在等待验证码, 请发送 /code 验证码"
		case 2:
			src = "正在等待密码, 请发送 /pass 密码"
		case 3:
			src = "userBot 已登录"
		}
		if _, err := m.Reply(src); err != nil {
			log.Printf("发送消息失败: %+v", err)
		}
		return nil
	case strings.HasPrefix(text, "/allow"):
		if !infos.isAdmin(m.SenderID()) {
			log.Printf("收到非管理员消息: %d", m.SenderID())
			if _, err := m.Reply("你没有使用此机器人的权限"); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			return nil
		}
		whiteID, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(text, "/allow")), 10, 64)
		if err != nil {
			if _, err := m.Reply("添加白名单失败: " + err.Error()); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			return err
		}
		if whiteID != 0 {
			infos.Mutex.Lock()
			infos.Conf.WhiteIDs = append(infos.Conf.WhiteIDs, whiteID)
			infos.HasNew = true
			infos.Mutex.Unlock()
			if _, err := m.Reply(fmt.Sprintf("添加白名单成功: %d", whiteID)); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
		}
		return nil
	case strings.HasPrefix(text, "/disallow"):
		if !infos.isAdmin(m.SenderID()) {
			log.Printf("收到非管理员消息: %d", m.SenderID())
			if _, err := m.Reply("你没有使用此机器人的权限"); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			return nil
		}
		whiteID, err := strconv.ParseInt(strings.TrimSpace(strings.TrimPrefix(text, "/disallow")), 10, 64)
		if err != nil {
			if _, err := m.Reply("移除白名单失败: " + err.Error()); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			return err
		}
		if whiteID != 0 {
			infos.Mutex.Lock()
			oldLen := len(infos.Conf.WhiteIDs)
			infos.Conf.WhiteIDs = slices.DeleteFunc(infos.Conf.WhiteIDs, func(num int64) bool {
				return num == whiteID
			})
			newLen := len(infos.Conf.WhiteIDs)
			infos.Mutex.Unlock()
			if oldLen > newLen {
				infos.Mutex.Lock()
				infos.HasNew = true
				infos.Mutex.Unlock()
				if _, err := m.Reply(fmt.Sprintf("移除白名单成功: %d", whiteID)); err != nil {
					log.Printf("发送消息失败: %+v", err)
				}
			} else {
				if _, err := m.Reply(fmt.Sprintf("用户 %d 不在白名单中", whiteID)); err != nil {
					log.Printf("发送消息失败: %+v", err)
				}
			}
		}
		return nil
	case strings.HasPrefix(text, "/phone"):
		if !infos.isAdmin(m.SenderID()) {
			log.Printf("收到非管理员消息: %d", m.SenderID())
			if _, err := m.Reply("你没有使用此机器人的权限"); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			return nil
		}
		content := strings.TrimSpace(strings.TrimPrefix(text, "/phone"))
		if content == "" {
			if _, err := m.Reply("手机不能为空"); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			return errors.New("手机不能为空")
		}

		if !strings.HasPrefix(content, "+") {
			content = "+" + content
		}

		if err := infos.startUserBot(content); err != nil {
			if _, err := m.Reply("启动 UserBot 失败: " + err.Error()); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			return err
		}
		return nil
	case strings.HasPrefix(text, "/code"):
		if !infos.isAdmin(m.SenderID()) {
			log.Printf("收到非管理员消息: %d", m.SenderID())
			if _, err := m.Reply("你没有使用此机器人的权限"); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			return nil
		}

		code := strings.TrimSpace(strings.TrimPrefix(text, "/code"))
		if code == "" {
			if _, err := m.Reply("验证码不能为空"); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			return nil
		}

		if err := infos.submitCode(code); err != nil {
			if _, err := m.Reply("提交验证码失败: " + err.Error()); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			return err
		}
		if _, err := m.Reply("提交验证码成功"); err != nil {
			log.Printf("发送消息失败: %+v", err)
		}
		return nil

	case strings.HasPrefix(text, "/pass"):
		if !infos.isAdmin(m.SenderID()) {
			log.Printf("收到非管理员消息: %d", m.SenderID())
			if _, err := m.Reply("你没有使用此机器人的权限"); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			return nil
		}

		pass := strings.TrimSpace(strings.TrimPrefix(text, "/pass"))
		if pass == "" {
			if _, err := m.Reply("密码不能为空"); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			return nil
		}

		if err := infos.submitPass(pass); err != nil {
			if _, err := m.Reply("提交密码失败: " + err.Error()); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			return err
		}
		if _, err := m.Reply("提交密码成功"); err != nil {
			log.Printf("发送消息失败: %+v", err)
		}
		return nil
	default:
		if !infos.isWhiteList(m.SenderID()) && m.SenderID() != 0 {
			return nil
		}
		return handleMess(m)
	}
}

// handleM 处理接收到的新消息, 解析其中的媒体文件或 tg 链接
func handleMess(m *telegram.NewMessage) error {
	// 如果是用户发送或转发来的、带有图片/文档/视频的消息, 直接生成直链
	if m.IsMedia() && (m.Photo() != nil || m.Document() != nil || m.Video() != nil) {
		link := fmt.Sprintf("%s/stream?cid=%d&mid=%d&cate=bot", strings.TrimSuffix(infos.Conf.Site, "/"), m.ChatID(), m.ID)
		if infos.Conf.Password != "" {
			link += fmt.Sprintf("&key=%s", infos.Conf.Password)
		}
		return sendLink(m, link)
	}

	src := strings.TrimSpace(m.Text())
	if src == "" {
		if _, err := m.Reply("消息为空"); err != nil {
			log.Printf("发送消息失败: %+v", err)
		}
		return nil
	}

	// 编译正则表达式匹配 Telegram 链接格式
	// 匹配格式如：t.me/c/12345/678 或 t.me/username/678
	re := regexp.MustCompile(`t\.me\/(c\/(\d+)|([a-zA-Z0-9_]+))\/(\d+)(?:\?.*comment=(\d+))?`)
	matches := re.FindAllStringSubmatch(src, -1)

	if len(matches) == 0 {
		// log.Printf("收到非链接消息: %s", src)
		return nil
	}

	for _, link := range hackLink(matches, m) {
		if err := sendLink(m, link); err != nil {
			log.Printf("发送消息失败: %+v", err)
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
		if infos.Conf.ChannelID != 0 {
			cid = infos.Conf.ChannelID
		} else {
			log.Printf("频道ID无效: %s", params.Get("cid"))
			http.Error(w, "频道ID无效", http.StatusBadRequest)
			return
		}
	}

	value, err := strconv.ParseInt(params.Get("mid"), 10, 32)
	if err != nil || value == 0 {
		re := regexp.MustCompile(`/stream/(\d+)/[a-zA-Z0-9]+`)
		matches := re.FindStringSubmatch(r.URL.Path)
		if len(matches) == 2 {
			value, err = strconv.ParseInt(matches[1], 10, 32)
			if err != nil || value == 0 {
				http.Error(w, "消息ID无效", http.StatusBadRequest)
				return
			}
		} else {
			http.Error(w, "消息ID无效", http.StatusBadRequest)
			return
		}
	}
	mid := int32(value)

	cate := params.Get("cate")
	if cate == "bot" {
		infos.Client = infos.BotClient
	} else {
		infos.Client = infos.UserClient
	}

	// 获取消息内容 (增加一次重试逻辑, 解决由于长时间闲置导致 Peer 缓存失效的问题)
	ms, err := infos.Client.GetMessages(cid, &telegram.SearchOption{IDs: []int32{mid}})
	if err != nil || len(ms) == 0 {
		log.Printf("首次获取消息失败, 尝试刷新对话列表后重试: cid=%d, mid=%d, err=%v", cid, mid, err)
		// 刷新一次对话列表（触发库内部自动解析 Entity 并更新 AccessHash 缓存）
		if _, err := infos.Client.GetDialogs(&telegram.DialogOptions{Limit: 100}); err == nil {
			ms, err = infos.Client.GetMessages(cid, &telegram.SearchOption{IDs: []int32{mid}})
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

	stream := newStream(r.Context(), infos.Client, src.Media(), infos.Conf.Workers, mid, cid)
	fileName := src.File.Name
	size := src.File.Size

	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", handleMediaCate(fileName))

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
			start, err = strconv.ParseInt(matches[1], 10, 64)
			if err != nil {
				start = 0
			}
			if matches[2] != "" {
				end, err = strconv.ParseInt(matches[2], 10, 64)
				if err != nil {
					end = size - 1
				}
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

	stream.ContentSize = end - start + 1
	go stream.start(start, end)
	defer stream.clean()

	if r.Method == http.MethodGet {
		for {
			select {
			case <-r.Context().Done():
				log.Printf("流式传输文件时已取消: cid=%d, mid=%d", cid, mid)
				return
			case task := <-stream.Tasks:
				if task == nil {
					log.Printf("流式传输文件时出错: cid=%d, mid=%d, err=任务为空", cid, mid)
					continue
				}
				task.Cond.L.Lock()
				for !*task.Done {
					task.Cond.Wait()
				}
				task.Cond.L.Unlock()
				if task.Error != nil {
					log.Printf("流式传输文件时出错: cid=%d, mid=%d, err=%v", cid, mid, task.Error)
					return
				}
				if _, err := w.Write(*task.Content); err != nil {
					log.Printf("写入文件流时出错: cid=%d, mid=%d, err=%v", cid, mid, err)
				}

				// 判断是否下载完成
				if task.ContentEnd >= end {
					log.Printf("流式传输文件已完成: cid=%d, mid=%d", cid, mid)
					return
				}
				task = nil
			}
		}

	} else {
		http.Error(w, fmt.Sprintf("不支持的请求方法: %s", r.Method), http.StatusMethodNotAllowed)
		return
	}
}

func handleLink(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	password := params.Get("key")
	if infos.Conf.Password != "" && password != infos.Conf.Password {
		http.Error(w, "无效的密码", http.StatusUnauthorized)
		return
	}

	src := params.Get("link")
	if src == "" || !strings.HasPrefix(src, "http") {
		http.Error(w, "无效的链接", http.StatusBadRequest)
		return
	}

	// 编译正则表达式匹配 Telegram 链接格式
	// 匹配格式如：t.me/c/12345/678 或 t.me/username/678
	re := regexp.MustCompile(`t\.me\/(c\/(\d+)|([a-zA-Z0-9_]+))\/(\d+)(?:\?.*comment=(\d+))?`)
	matches := re.FindAllStringSubmatch(src, -1)

	for _, link := range hackLink(matches, nil) {
		http.Redirect(w, r, link, http.StatusFound)
		return
	}

	http.Error(w, "未找到可下载的媒体", http.StatusNotFound)
}

func hackLink(matches [][]string, m *telegram.NewMessage) (links []string) {
	// 遍历所有匹配到的链接
	for _, match := range matches {
		var cid any   // 用于 ResolvePeer 的标识项（可以是用户名或 chatID）
		var mid int32 // 消息 ID

		// 解析逻辑
		if match[2] != "" {
			// 如果是 c/(\d+), 代表私有频道链接, 需要给 ID 补充前缀 -100
			value, err := strconv.ParseInt("-100"+match[2], 10, 64)
			if err != nil {
				log.Printf("解析频道ID失败: %+v", err)
				if m != nil {
					if _, err := m.Reply("解析频道ID失败"); err != nil {
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

		// 解析消息偏移 ID
		value, err := strconv.ParseInt(match[4], 10, 32)
		if err != nil {
			log.Printf("解析消息ID失败: %+v", err)
			if m != nil {
				if _, err := m.Reply("解析消息ID失败"); err != nil {
					log.Printf("发送消息失败: %+v", err)
				}
			}
			continue
		}
		mid = int32(value)

		// 使用 GetMessages 尝试获取目标消息, gogram 会自动映射 peer 为 InputPeer
		ms, err := infos.UserClient.GetMessages(cid, &telegram.SearchOption{IDs: []int32{mid}})
		if err != nil || len(ms) == 0 {
			log.Printf("获取消息失败: cid=%d, mid=%d, err=%v, count=%d", cid, mid, err, len(ms))
			if m != nil {
				if _, err := m.Reply("获取消息失败"); err != nil {
					log.Printf("发送消息失败: %+v", err)
				}
			}
			continue
		}

		src := ms[0] // 获取第一条目标消息
		if match[5] != "" {
			commentID, err := strconv.ParseInt(match[5], 10, 32)
			if err != nil {
				continue
			}

			// a. 检查该消息是否有讨论功能并获取讨论组 ID
			// gogram 的 NewMessage 对象通过 .Message.Replies 获取元数据
			if src.Message.Replies != nil && src.Message.Replies.ChannelID != 0 {
				discussionID := src.Message.Replies.ChannelID // 讨论组的 ID

				// b. 从讨论组中获取真正的评论消息
				commentMs, err := infos.UserClient.GetMessages(discussionID, &telegram.SearchOption{IDs: []int32{int32(commentID)}})
				if err == nil && len(commentMs) > 0 {
					src = commentMs[0] // 将目标消息切换为评论消息
					src.ID = int32(commentID)
					src.Chat.ID = discussionID
				}
			}
		}
		// 判断该消息是否包含可下载的媒体内容
		if !src.IsMedia() || (src.Photo() == nil && src.Document() == nil && src.Video() == nil) {
			log.Printf("消息不包含媒体: cid=%d, mid=%d", cid, mid)
			if m != nil {
				if _, err := m.Reply("消息不包含媒体"); err != nil {
					log.Printf("发送消息失败: %+v", err)
				}
			}
			continue
		}

		// 为媒体文件构造下载直链
		link := fmt.Sprintf("%s/stream?cid=%v&mid=%d&cate=user", strings.TrimSuffix(infos.Conf.Site, "/"), src.ChatID(), src.ID)
		if infos.Conf.Password != "" {
			link += fmt.Sprintf("&key=%s", infos.Conf.Password)
		}
		links = append(links, link)
	}
	return links
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
	text := fmt.Sprintf("<b>🔗 链接提取成功</b>\n\n<code>%s</code>\n\n👆 <i>上方链接复制, 下方按钮下载</i> 👇", html.EscapeString(link))
	markup := telegram.InlineURL(
		"🚀 直接下载", fmt.Sprintf("%s&download=true", link),
	)

	// 发送消息并设置解析模式为 HTML, 附带内联键盘
	_, err := m.Reply(text, &telegram.SendOptions{
		ParseMode:   "html",
		ReplyMarkup: markup,
	})

	if err != nil {
		log.Printf("发送下载链接失败: %+v", err)
	}
	return err
}

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
								err := os.Remove(filepath.Join(infos.FilesPath, name))
								if err != nil {
									log.Printf("删除缓存文件失败: %v", err)
								}
							}

						}
					} else {
						err := os.Remove(filepath.Join(infos.FilesPath, name))
						if err != nil {
							log.Printf("删除缓存文件失败: %v", err)
						}
					}
				}
			}
		}
	case "session":
		name := fmt.Sprintf("%s.session", strings.ToLower(realm.Cate))
		err := os.Remove(filepath.Join(infos.FilesPath, name))
		if err != nil {
			log.Printf("删除缓存文件失败: %v", err)
		}
	}
}

/*
func extractDC(err error) int {
	src := strings.ToUpper(err.Error())
	switch {
	case strings.Contains(src, "DC_MIGRATE"):
		re := regexp.MustCompile(`DC.*?(\d+).`)
		match := re.FindStringSubmatch(src)
		if len(match) == 2 {
			if dc, err := strconv.Atoi(match[1]); err == nil {
				return dc
			}
		}
	case strings.Contains(src, "PHONE_MIGRATE"):
		re := regexp.MustCompile(`DC.*?(\d+).`)
		match := re.FindStringSubmatch(src)
		if len(match) == 2 {
			if dc, err := strconv.Atoi(match[1]); err == nil {
				return dc
			}
		}
	}
	return 0
}
*/
