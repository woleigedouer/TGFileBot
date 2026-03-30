package main

import (
	"bufio"         // 用于读取文件流
	"crypto/md5"    // 用于计算哈希值
	"encoding/hex"  // 用于进行十六进制编码
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

type HackLink struct {
	M       *telegram.NewMessage
	UID     int64
	Pass    string
	Hash    string
	Matches [][]string
}

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
	File       *os.File         // 日志文件对象
	Status     int              // 登录状态: 0 未登录, 1 验证码, 2 密码, 3 已登录
	BotID      int64            // Bot 的 ID
	Code       chan string      // 验证码
	Pass       chan string      // 二次验证密码
	IDs        map[int64]string // 授权ID列表
	Rex        *regexp.Regexp   // 正则表达式
}

var infos *Infos
var startTime time.Time
var version = "v1.0.4"

func newInfos(filePath, filesPath string) (*Infos, error) {
	// 创建日志文件
	filePath = filepath.Clean(filePath)
	file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
	if err != nil {
		log.Printf("无法打开日志文件: %v", err)
	}

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
		File:      file,
		FilePath:  filePath,
		FilesPath: filesPath,
		Conf:      conf,
		BotID:     botID,
		Mutex:     new(sync.Mutex),
		Code:      make(chan string, 1),
		Pass:      make(chan string, 1),
		IDs:       make(map[int64]string, len(conf.AdminIDs)+len(conf.WhiteIDs)+1),
		Rex:       regexp.MustCompile(`(?:FLOOD_PREMIUM_WAIT_|A WAIT OF |FLOOD_WAIT_)(\d+)`),
	}, nil
}

func main() {
	startTime = time.Now()
	// 加载配置文件
	files := flag.String("files", "files", "文件路径和名称")
	file := flag.String("log", "files/log.log", "日志文件路径")
	var ver bool
	flag.BoolVar(&ver, "version", false, "打印版本号并退出")
	flag.BoolVar(&ver, "v", false, "打印版本号并退出")
	flag.Parse()

	// 如果请求显示版本则直接输出并退出，避免初始化其他资源
	if ver {
		fmt.Println(version)
		return
	}

	// 初始化
	value, err := newInfos(*file, *files)
	if err != nil {
		log.Printf("初始化失败: %+v", err)
		return
	}
	infos = value

	// 退出时清理
	defer func() {
		if err := infos.File.Close(); err != nil {
			log.Printf("关闭日志文件错误: %v", err)
		}
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
		DeviceConfig: telegram.DeviceConfig{
			DeviceModel:   "Android",
			SystemVersion: "Android 14",
			AppVersion:    "10.14.3",
		},
		FloodHandler: func(err error) bool {
			wait := 3
			matches := infos.Rex.FindStringSubmatch(strings.ToUpper(err.Error()))
			if len(matches) > 1 {
				if value, err := strconv.Atoi(matches[1]); err == nil {
					wait = value
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
		DeviceConfig: telegram.DeviceConfig{
			DeviceModel:   "Android",
			SystemVersion: "Android 14",
			AppVersion:    "10.14.3",
		},
		FloodHandler: func(err error) bool {
			wait := 3
			matches := infos.Rex.FindStringSubmatch(strings.ToUpper(err.Error()))
			if len(matches) > 1 {
				if value, err := strconv.Atoi(matches[1]); err == nil {
					wait = value
				}
			}
			log.Printf("下载太过频繁, 等待 %d 秒后重试", wait)
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

func (infos *Infos) startUserBotQR() (err error) {
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
		infos.Mutex.Unlock()
		if infos.UserClient == nil {
			if err := infos.userBotClient(); err != nil {
				log.Printf("UserBot 登录失败: %+v", err)
				infos.resetStatus()
				return err
			}
		}
		if ms, err := infos.BotClient.SendMessage(infos.Conf.UserID, "正在请求登录二维码..."); err != nil {
			log.Printf("发送消息失败: %+v", err)
		} else {
			go func() {
				time.Sleep(35 * time.Second)
				_, err := ms.Delete()
				if err != nil {
					log.Printf("删除消息失败: %+v", err)
				}
			}()
		}
		// 启动登录流程（会阻塞, 直到登录完成或失败）
		go func() {
			qr, err := infos.UserClient.QRLogin(telegram.QrOptions{
				PasswordCallback: infos.pass,
			})
			if err != nil {
				log.Printf("获取 QR 登录失败: %+v", err)
				if _, err := infos.BotClient.SendMessage(infos.Conf.UserID, fmt.Sprintf("获取 QR 登录失败: %+v", err)); err != nil {
					log.Printf("发送消息失败: %+v", err)
				}
				infos.resetStatus()
				return
			}

			png, err := qr.ExportAsPng()
			if err != nil {
				log.Printf("导出 QR PNG 失败: %+v", err)
				return
			}

			inputFile, err := infos.BotClient.UploadFile(png, &telegram.UploadOptions{
				FileName: "qr.png",
			})
			if err != nil {
				log.Printf("上传 QR 文件失败: %+v", err)
				return
			}

			if ms, err := infos.BotClient.SendMessage(infos.Conf.UserID, inputFile, &telegram.SendOptions{
				Caption: "请使用手机 Telegram 扫描此二维码登录。二维码有效期 30 秒，如失效请重新发送 /qr",
			}); err != nil {
				log.Printf("发送 QR 图片失败: %+v", err)
			} else {
				log.Printf("二维码已发送给管理员")
				go func() {
					time.Sleep(35 * time.Second)
					_, err := ms.Delete()
					if err != nil {
						log.Printf("删除消息失败: %+v", err)
					}
				}()
			}

			err = qr.WaitLogin()
			if err != nil {
				log.Printf("QR 登录失败: %+v", err)
				if _, err := infos.BotClient.SendMessage(infos.Conf.UserID, fmt.Sprintf("QR 登录失败: %+v", err)); err != nil && infos.Status != 0 {
					log.Printf("发送消息失败: %+v", err)
				}
				infos.resetStatus()
				return
			}

			if err := infos.checkStatus(); err != nil {
				log.Printf("UserBot 登录失败: %+v", err)
				infos.resetStatus()
				return
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
		name := me.FirstName + me.LastName
		if me.Username != "" {
			name = "@" + me.Username
		}
		log.Printf("登录成功! 用户: %s", name)
		if _, err := infos.BotClient.SendMessage(infos.Conf.UserID, fmt.Sprintf("登录成功! 用户: %s", name)); err != nil && infos.Status != 0 {
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
}

func (infos *Infos) calculateHash(userID int64) string {
	if infos.Conf.Password == "" {
		return ""
	}
	res := fmt.Sprintf("%d%s", userID, infos.Conf.Password)
	src := md5.Sum([]byte(res))
	return hex.EncodeToString(src[:])[:6]
}

func (infos *Infos) checkHash(hash string) int64 {
	if hash == "" {
		return 0
	}
	if value, ok := infos.IDs[infos.Conf.UserID]; ok && value != "" {
		if value == hash {
			return infos.Conf.UserID
		}
	} else {
		infos.IDs[infos.Conf.UserID] = infos.calculateHash(infos.Conf.UserID)
	}

	for _, id := range infos.Conf.AdminIDs {
		if value, ok := infos.IDs[id]; ok && value != "" {
			if value == hash {
				return id
			}
		} else {
			infos.IDs[id] = infos.calculateHash(id)
		}
	}

	for _, id := range infos.Conf.WhiteIDs {
		if value, ok := infos.IDs[id]; ok && value != "" {
			if value == hash {
				return id
			}
		} else {
			infos.IDs[id] = infos.calculateHash(id)
		}
	}
	return 0
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
			src = "userBot 未登录, 仅使用 Bot 或发送 /phone 手机号登录 userBot"
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
			if _, err := m.Reply(fmt.Sprintf("添加白名单失败: %+v", err)); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			return nil
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
			if _, err := m.Reply(fmt.Sprintf("移除白名单失败: %+v", err)); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			return nil
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
			return nil
		}

		if !strings.HasPrefix(content, "+") {
			content = "+" + content
		}

		if err := infos.startUserBot(content); err != nil {
			if _, err := m.Reply(fmt.Sprintf("启动 UserBot 失败: %+v", err)); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			return nil
		}
		return nil
	case strings.HasPrefix(text, "/qr"):
		if !infos.isAdmin(m.SenderID()) {
			log.Printf("收到非管理员消息: %d", m.SenderID())
			if _, err := m.Reply("你没有使用此机器人的权限"); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			return nil
		}

		if err := infos.startUserBotQR(); err != nil {
			if _, err := m.Reply(fmt.Sprintf("启动 QR 登录失败: %+v", err)); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			return nil
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
			if _, err := m.Reply(fmt.Sprintf("提交验证码失败: %+v", err)); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			return nil
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
			if _, err := m.Reply(fmt.Sprintf("提交密码失败: %+v", err)); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			return nil
		}
		if _, err := m.Reply("提交密码成功"); err != nil {
			log.Printf("发送消息失败: %+v", err)
		}
		return nil
	case strings.HasPrefix(text, "/channel"):
		if !infos.isAdmin(m.SenderID()) {
			log.Printf("收到非管理员消息: %d", m.SenderID())
			if _, err := m.Reply("你没有使用此机器人的权限"); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			return nil
		}
		content := strings.TrimSpace(strings.TrimPrefix(text, "/channel"))
		if content == "" {
			log.Print("频道ID不能为空")
			return nil
		}
		if !strings.HasPrefix(content, "-100") {
			content = "-100" + content
		}
		value, err := strconv.ParseInt(content, 10, 64)
		if err != nil {
			if _, err := m.Reply(fmt.Sprintf("频道ID格式错误: %+v", err)); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			return nil
		}
		infos.Mutex.Lock()
		infos.Conf.ChannelID = value
		infos.HasNew = true
		infos.Mutex.Unlock()
		log.Printf("频道ID已设置为: %d", value)
		if _, err := m.Reply(fmt.Sprintf("频道ID已设置为: %d", value)); err != nil {
			log.Printf("发送消息失败: %+v", err)
		}
		return nil
	case strings.HasPrefix(text, "/workers"):
		if !infos.isAdmin(m.SenderID()) {
			log.Printf("收到非管理员消息: %d", m.SenderID())
			if _, err := m.Reply("你没有使用此机器人的权限"); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			return nil
		}
		content := strings.TrimSpace(strings.TrimPrefix(text, "/workers"))
		if content == "" {
			log.Print("并发数不能为空")
			return nil
		}

		num, err := strconv.Atoi(content)
		if err != nil {
			log.Print("并发数必须为数字")
			return nil
		}
		if num <= 0 {
			log.Print("并发数必须大于 0")
			return nil
		}
		if num > 4 {
			log.Print("并发数不建议超过 4, 否则可能导致下载失败甚至封号")
			if _, err := m.Reply("并发数不建议超过 4, 否则可能导致下载失败甚至封号"); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			return nil
		}
		infos.Mutex.Lock()
		infos.Conf.Workers = num
		infos.HasNew = true
		infos.Mutex.Unlock()
		log.Printf("并发数已设置为: %d", num)
		if _, err := m.Reply(fmt.Sprintf("并发数已设置为: %d", num)); err != nil {
			log.Printf("发送消息失败: %+v", err)
		}
		return nil
	case strings.HasPrefix(text, "/info"):
		if !infos.isAdmin(m.SenderID()) {
			log.Printf("收到非管理员消息: %d", m.SenderID())
			if _, err := m.Reply("你没有使用此机器人的权限"); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			return nil
		}

		num := 10
		content := strings.TrimSpace(strings.TrimPrefix(text, "/info"))
		if content != "" {
			if value, err := strconv.Atoi(content); err == nil && value > 0 {
				num = value
			}
		}

		// 读取日志
		lines, err := readLastLines(infos.FilePath, num)
		if err != nil {
			if _, err := m.Reply(fmt.Sprintf("读取日志失败: %+v", err)); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			return nil
		}

		if len(lines) == 0 {
			if _, err := m.Reply("暂无日志内容"); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			return nil
		}

		const maxCount = 4000
		var values strings.Builder
		header := fmt.Sprintf("<b>📜 系统日志 (最后 %d 行)</b>\n\n", len(lines))
		values.WriteString(header)
		values.WriteString("<pre>")

		for _, line := range lines {
			line = html.EscapeString(line) + "\n"
			// 如果当前消息加上这行和 </pre> 后超过限制，则先发送当前部分
			if values.Len()+len(line)+len("</pre>") > maxCount {
				values.WriteString("</pre>")
				if _, err := m.Reply(values.String()); err != nil {
					log.Printf("发送消息失败: %+v", err)
				}
				// 重置 Builder 开启下一个分片
				values.Reset()
				values.WriteString("<pre>")
			}
			values.WriteString(line)
		}

		// 发送剩余内容
		if values.Len() > len("<pre>") {
			values.WriteString("</pre>")
			if _, err := m.Reply(values.String()); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
		}
		return nil
	case strings.HasPrefix(text, "/check"):
		if !infos.isAdmin(m.SenderID()) {
			log.Printf("收到非管理员消息: %d", m.SenderID())
			if _, err := m.Reply("你没有使用此机器人的权限"); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			return nil
		}
		content := strings.TrimSpace(strings.TrimPrefix(text, "/check"))
		if content == "" {
			if _, err := m.Reply("请输入要检查的哈希值"); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			return nil
		}
		if uid := infos.checkHash(content); uid != 0 {
			// 注意：infos.BotClient.GetUser 返回的通常是 *telegram.User 对象
			user, err := infos.BotClient.GetUser(uid)
			if err != nil {
				log.Printf("获取用户信息失败: %+v", err)
				return nil
			}
			// 构造平衡的名字显示（处理姓和名）
			fullName := user.FirstName + user.LastName
			var values strings.Builder
			values.WriteString(fmt.Sprintf("• <b>用户 ID</b>: <code>%d</code>\n", uid))

			if fullName != "" {
				values.WriteString(fmt.Sprintf("• <b>显示名称</b>: %s\n", html.EscapeString(fullName)))
			}
			if user.Username != "" {
				values.WriteString(fmt.Sprintf("• <b>用户昵称</b>: @%s\n", user.Username))
			}
			// 使用 HTML 解析模式发送
			if _, err := m.Reply(values.String(), &telegram.SendOptions{ParseMode: "html"}); err != nil {
				log.Printf("发送消息失败: %+v", err)
			}
			return nil
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
			link += fmt.Sprintf("&hash=%s&uid=%d", infos.calculateHash(m.SenderID()), m.SenderID())
		}
		return sendLink(m, link)
	}

	if infos.Status != 3 {
		return nil
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
	if infos.Conf.Password != "" {
		hash := params.Get("hash")
		password := params.Get("key")
		switch {
		case password != "":
			if password != infos.Conf.Password {
				http.Error(w, "无效的密码", http.StatusUnauthorized)
				return
			}
		case hash != "":
			value := params.Get("uid")
			uid, err := strconv.ParseInt(value, 10, 64)
			if err == nil && uid != 0 {
				if hash != infos.calculateHash(uid) {
					http.Error(w, "无效的密码", http.StatusUnauthorized)
					return
				}
			} else {
				log.Printf("UID无效: %s", value)
				http.Error(w, "无效的密码", http.StatusUnauthorized)
				return
			}
		}
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
	if cate == "user" && infos.Status == 3 {
		infos.Client = infos.UserClient
	} else {
		infos.Client = infos.BotClient
	}

	// 获取消息内容 (增加一次重试逻辑, 解决由于长时间闲置导致 Peer 缓存失效的问题)
	ms, err := infos.Client.GetMessages(cid, &telegram.SearchOption{IDs: []int32{mid}})

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

	size := src.File.Size
	fileName := src.File.Name
	stream := newStream(r.Context(), infos.UserClient, src.Media(), infos.Conf.Workers, mid, cid, fileName)
	if src.Message.FwdFrom != nil {
		if ch, ok := src.Message.FwdFrom.FromID.(*telegram.PeerChannel); ok {
			stream.CID = ch.ChannelID
			stream.MID = src.Message.FwdFrom.ChannelPost
		}
	}

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
				log.Printf("流式传输文件已取消: cid=%d, mid=%d, fileName=%s", cid, mid, fileName)
				return
			case task := <-stream.Tasks:
				if task == nil {
					log.Printf("流式传输文件出错: cid=%d, mid=%d, fileName=%s, error=任务为空", cid, mid, fileName)
					continue
				}
				task.Cond.L.Lock()
				for !*task.Done {
					task.Cond.Wait()
				}
				task.Cond.L.Unlock()
				if task.Error != nil {
					log.Printf("切片下载出错: cid=%d, mid=%d, start=%d, end=%d, fileName=%s, error=%+v", cid, mid, task.ContentStart, task.ContentEnd, fileName, task.Error)
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
	res := HackLink{}
	params := r.URL.Query()
	if infos.Conf.Password != "" {
		hash := params.Get("hash")
		password := params.Get("key")
		switch {
		case password != "":
			res.Pass = password
			if password != infos.Conf.Password {
				http.Error(w, "无效的密码", http.StatusUnauthorized)
				return
			}
		case hash != "":
			res.Hash = hash
			value := params.Get("uid")
			uid, err := strconv.ParseInt(value, 10, 64)
			if err == nil && uid != 0 {
				res.UID = uid
				if hash != infos.calculateHash(uid) {
					http.Error(w, "无效的密码", http.StatusUnauthorized)
					return
				}
			} else {
				log.Printf("UID无效: %s", value)
				http.Error(w, "无效的密码", http.StatusUnauthorized)
				return
			}
		}
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
	res.Matches = matches

	for _, link := range hackLink(res) {
		http.Redirect(w, r, link, http.StatusFound)
		return
	}

	http.Error(w, "未找到可下载的媒体", http.StatusNotFound)
}

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

func readLastLines(filePath string, count int) (lines []string, err error) {
	file, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err := file.Close(); err != nil {
			log.Printf("关闭文件失败: %+v", err)
		}
	}()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
		if len(lines) > count {
			lines = lines[1:]
		}
	}
	if err := scanner.Err(); err != nil {
		return lines, err
	}
	return lines, nil
}

func hackLink(res HackLink) (links []string) {
	// 遍历所有匹配到的链接
	for _, match := range res.Matches {
		var cid any   // 用于 ResolvePeer 的标识项（可以是用户名或 chatID）
		var mid int32 // 消息 ID

		// 解析逻辑
		if match[2] != "" {
			// 如果是 c/(\d+), 代表私有频道链接, 需要给 ID 补充前缀 -100
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

		// 解析消息偏移 ID
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

		// 使用 GetMessages 尝试获取目标消息, gogram 会自动映射 peer 为 InputPeer
		ms, err := infos.UserClient.GetMessages(cid, &telegram.SearchOption{IDs: []int32{mid}})
		if err != nil || len(ms) == 0 {
			log.Printf("获取消息失败: cid=%d, mid=%d, err=%v, count=%d", cid, mid, err, len(ms))
			if res.M != nil {
				if _, err := res.M.Reply("获取消息失败"); err != nil {
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
			if res.M != nil {
				if _, err := res.M.Reply("消息不包含媒体"); err != nil {
					log.Printf("发送消息失败: %+v", err)
				}
			}
			continue
		}

		// 为媒体文件构造下载直链
		link := fmt.Sprintf("%s/stream?cid=%v&mid=%d&cate=user", strings.TrimSuffix(infos.Conf.Site, "/"), src.ChatID(), src.ID)
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
