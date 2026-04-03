package main

import (
	"bufio" // 用于读取文件流
	"context"
	"crypto/md5"        // 用于计算哈希值
	"encoding/hex"      // 用于进行十六进制编码
	"encoding/json"     // 用于处理 JSON 数据
	"errors"            // 用于处理错误
	"flag"              // 用于处理命令行参数
	"fmt"               // 用于格式化字符串
	"html"              // 用于转义 HTML 字符
	"io"                // 用于处理文件流
	"log"               // 用于记录程序日志
	"net/http"          // 用于启动 HTTP 服务器和处理请求
	handleUrl "net/url" // 用于处理 URL 相关操作
	"os"                // 用于处理操作系统信号
	"os/signal"         // 用于处理操作系统信号
	"path/filepath"     // 用于保存文件路径操作
	"regexp"            // 用于正则表达式匹配（链接解析）
	"slices"            // 用于切片操作
	"strconv"           // 用于字符串和数值的相互转换
	"strings"           // 用于字符串处理
	"sync"              // 用于并发锁
	"sync/atomic"
	"syscall" // 用于处理操作系统信号
	"time"    // 用于处理时间相关逻辑

	"github.com/amarnathcjd/gogram/telegram" // 导入 gogram 客户端核心库
)

// HackLink 结构体用于在处理提取链接时传递中间数据
type HackLink struct {
	M       *telegram.NewMessage // 原始消息对象
	UID     int64                // 发起请求的用户 ID
	Pass    string               // 可选密码
	Hash    string               // 验证哈希
	Matches [][]string           // 正则匹配到的链接信息
}

// CleanRealm 结构体用于定义清理缓存和会话的范围
type CleanRealm struct {
	Filter bool   // 是否启用过滤，只删除特定 ID 以外的文件
	ID     string // 过滤 ID（如账号 ID）
	Cate   string // 类型：bot 或 user
	Realm  string // 范围：cache 或 session
}

type OffSet struct {
	Offset int32     // 偏移量
	Time   time.Time // 时间
}

type OffSets struct {
	Mutex   *sync.Mutex       // 互斥锁，保护并发安全
	OffSets map[string]OffSet // 偏移量映射
}

type Item struct {
	Name string `json:"name"`
	MID  int32  `json:"mid"`
	CID  int64  `json:"cid"`
	Size int64  `json:"size"`
}

type Items struct {
	HasMore bool   `json:"more"`
	Channel string `json:"channel"`
	Item    []Item `json:"item"`
}

// Infos 结构体保存了程序运行时的全局状态和资源句柄
type Infos struct {
	BotClient  *telegram.Client // 独立的 Bot 客户端（用于与用户交互）
	UserClient *telegram.Client // 全局 UserBot 客户端实例（用于读取私有内容和流式传输）
	Client     *telegram.Client // 当前活跃客户端指针
	Mutex      *sync.Mutex      // 全局互斥锁，保护并发安全
	Conf       *Conf            // 指向全局配置
	HasNew     bool             // 标记配置是否被动态修改需要持久化
	FilesPath  string           // 配置文件存放目录
	FilePath   string           // 日志文件路径
	File       *os.File         // 日志文件句柄
	Status     int              // UserBot 登录状态: 0 未登录, 1 等待验证码, 2 等待二步验证, 3 已登录
	BotID      int64            // Bot 自身的 ID
	Code       chan string      // 用于接收异步提交的验证码
	Pass       chan string      // 用于接收异步提交的二步验证密码
	IDs        map[int64]string // 缓存用户 ID 到哈希的映射，减少重复计算
	Rex        *regexp.Regexp   // 用于解析 Telegram FloodWait 错误的正则
}

var infos *Infos
var offSets *OffSets
var startTime time.Time
var version = "v1.0.5"

// main 是程序的入口函数
func main() {
	startTime = time.Now()
	// 解析命令行参数
	files := flag.String("files", "files", "配置文件所属目录路径（包含 config.json, session 等）")
	file := flag.String("log", "", "日志文件的存放路径")
	var ver bool
	flag.BoolVar(&ver, "version", false, "显示程序版本号并退出")
	flag.BoolVar(&ver, "v", false, "显示程序版本号并退出")
	flag.Parse()

	// 版本检查逻辑
	if ver {
		fmt.Println(version)
		return
	}

	// 1. 初始化全局 Infos 对象并加载配置
	value, err := newInfos(*file, *files)
	if err != nil {
		log.Printf("初始化失败: %+v", err)
		return
	}
	infos = value

	offSets = newOffSets()

	// 2. 退出时的资源清理（延迟执行）
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

	// 3. 校验关键配置参数
	if infos.Conf.AppID == 0 || infos.Conf.AppHash == "" || infos.Conf.BotToken == "" {
		log.Panicf("配置文件缺少必要的参数: AppID、AppHash、BotToken")
		return
	}

	if infos.Conf.Port == 0 {
		infos.Conf.Port = 8080 // 默认端口 8080
	}

	// 4. 启动 Bot 客户端（用于接收管理指令和进行提取链接交互）
	err = infos.startBot()
	if err != nil {
		return
	}

	// 5. 初始化 UserBot 客户端（此时只是连接，尚未完成登录流程）
	err = infos.userBotClient()
	if err != nil {
		log.Printf("UserBot 启动失败: %+v", err)
		return
	}

	// 忽略 SIGPIPE 信号，防止由于网络异常断开导致进程崩溃
	signal.Ignore(syscall.SIGPIPE)

	// 设置系统中断信号监听，用于优雅退出
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)

	// 6. 在独立协程中启动 HTTP HTTP 服务
	go func() {
		log.Printf("HTTP 服务运行在 %d 端口", infos.Conf.Port)
		server := &http.Server{
			Addr:              fmt.Sprintf(":%d", infos.Conf.Port),
			Handler:           http.HandlerFunc(handleMain), // 主路由处理函数
			ReadTimeout:       30 * time.Second,             // 读取请求超时
			ReadHeaderTimeout: 10 * time.Second,             // 读取请求头超时
			WriteTimeout:      60 * time.Second,             // 写入响应超时
			IdleTimeout:       600 * time.Second,            // 空闲连接超时
			MaxHeaderBytes:    1 << 20,                      // 最大头部字节数 (1MB)
		}

		if err := server.ListenAndServe(); err != nil {
			log.Printf("HTTP 服务启动失败: %+v", err)
			sigChan <- os.Interrupt
		}
	}()

	// 7. 发送程序启动通知（如果 Bot 已连接且有管理员配置）
	sendMS(nil, "程序已启动", nil, 60)

	// 8. 检查 UserBot 登录状态，尝试自动登录（若已存在 session）
	if err := infos.checkStatus(); err != nil {
		log.Printf("UserBot 登录失败: %+v", err)
		infos.resetStatus()
	}

	// 阻塞等待直到接收到退出信号
	sig := <-sigChan
	log.Printf("收到信号: %v, 正在退出...", sig)
	sendMS(nil, "程序已退出", nil, 60)
}

func newInfos(filePath, filesPath string) (*Infos, error) {
	infos := &Infos{
		FilePath:  filePath,
		FilesPath: filesPath,
		Mutex:     new(sync.Mutex),
		Code:      make(chan string, 1),
		Pass:      make(chan string, 1),
		Rex:       regexp.MustCompile(`(?:FLOOD_PREMIUM_WAIT_|A WAIT OF |FLOOD_WAIT_)(\d+)`),
	}
	// 创建日志文件
	if filePath != "" {
		filePath = filepath.Clean(filePath)
		file, err := os.OpenFile(filePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0666)
		if err != nil {
			log.Printf("无法打开日志文件: %v", err)
		}
		infos.File = file
		// 设置日志输出
		multiWriter := io.MultiWriter(os.Stdout, file)
		log.SetOutput(multiWriter)
	}

	// 加载配置文件
	conf, err := loadConf(filesPath)
	if err != nil {
		log.Fatalf("载入配置文件失败: %+v", err)
	}
	if conf.Workers == 0 {
		conf.Workers = 1
	}
	if conf.MaxSize == 0 {
		conf.MaxSize = 32 * 1024 * 1024
	}
	infos.Conf = conf
	infos.IDs = make(map[int64]string, len(conf.AdminIDs)+len(conf.WhiteIDs)+1)

	// 获取 BotID
	if conf.BotToken != "" {
		parts := strings.Split(conf.BotToken, ":")
		if len(parts) < 1 {
			return nil, fmt.Errorf("BotToken 格式错误: %s", conf.BotToken)
		}
		result := strings.TrimSpace(parts[0])
		infos.BotID, err = strconv.ParseInt(result, 10, 64)
		if err != nil {
			log.Printf("解析 BotID 失败: %+v", err)
		}
	}

	return infos, nil
}

func newOffSets() *OffSets {
	return &OffSets{
		Mutex:   new(sync.Mutex),
		OffSets: make(map[string]OffSet),
	}
}

func botConf(cate string) (botConf telegram.ClientConfig) {
	return telegram.ClientConfig{
		AppID:        infos.Conf.AppID,
		AppHash:      infos.Conf.AppHash,
		LogLevel:     telegram.LogError,
		Session:      filepath.Join(infos.FilesPath, fmt.Sprintf("%s.session", cate)),
		Cache:        telegram.NewCache(filepath.Join(infos.FilesPath, fmt.Sprintf("%s.cache", cate))),
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

func (infos *Infos) isWhite(id int64) bool {
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

	// 创建 Bot 客户端
	client, err := telegram.NewClient(botConf("bot"))
	if err != nil {
		// 清理缓存
		cleanFiles(CleanRealm{Cate: "bot", Realm: "session"})
		cleanFiles(CleanRealm{Cate: "bot", Realm: "cache", Filter: false})
		log.Printf("创建 Bot 客户端失败: %+v", err)
		return err
	}

	// 连接 Bot
	if err = client.Connect(); err != nil {
		// 清理缓存
		cleanFiles(CleanRealm{Cate: "bot", Realm: "session"})
		cleanFiles(CleanRealm{Cate: "bot", Realm: "cache", Filter: false})
		log.Printf("Bot 连接失败: %+v", err)
		return err
	}

	// 登录 Bot
	if err = client.LoginBot(infos.Conf.BotToken); err != nil {
		// 清理缓存
		cleanFiles(CleanRealm{Cate: "bot", Realm: "session"})
		cleanFiles(CleanRealm{Cate: "bot", Realm: "cache", Filter: false})
		log.Printf("Bot 登录失败: %+v", err)
		return err
	}

	// 注册 Bot 命令处理函数
	client.On(telegram.OnMessage, handleBotCommand)
	userID, err := client.ResolvePeer(infos.Conf.UserID)
	if err != nil {
		log.Printf("解析用户 ID 失败: %v", err)
		return
	}
	commands := []*telegram.BotCommand{
		{
			Command:     "qr",
			Description: "获取登录二维码",
		},
		{
			Command:     "phone",
			Description: "输入手机号登录",
		},
		{
			Command:     "code",
			Description: "输入验证码登录",
		},
		{
			Command:     "pass",
			Description: "输入2FA密码登录",
		},
	}
	commonCommands := []*telegram.BotCommand{
		{
			Command:     "dc",
			Description: "设置客户端默认DC",
		},
		{
			Command:     "allow",
			Description: "添加白名单",
		},
		{
			Command:     "disallow",
			Description: "移除白名单",
		},
		{
			Command:     "add",
			Description: "添加搜索频道",
		},
		{
			Command:     "del",
			Description: "移除搜索频道",
		},
		{
			Command:     "list",
			Description: "列出搜索频道或白名单",
		},
		{
			Command:     "info",
			Description: "获取程序运行信息",
		},
		{
			Command:     "size",
			Description: "设置程序缓存大小",
		},
		{
			Command:     "site",
			Description: "设置反代域名",
		},
		{
			Command:     "port",
			Description: "设置HTTP服务端口",
		},
		{
			Command:     "check",
			Description: "查找HASH对应的用户信息",
		},
		{
			Command:     "workers",
			Description: "设置并发数",
		},
		{
			Command:     "channel",
			Description: "设置绑定频道",
		},
		{
			Command:     "password",
			Description: "设置接口访问密码",
		},
	}
	commands = append(commands, commonCommands...)

	client.SetBotCommands(commands, &userID)
	client.SetBotCommands(commonCommands, nil)

	log.Printf("Bot 启动成功")

	infos.Mutex.Lock()
	infos.BotClient = client
	infos.Mutex.Unlock()
	return nil
}

func (infos *Infos) userBotClient() (err error) {
	// 清理缓存
	userID := strconv.FormatInt(infos.Conf.UserID, 10)
	if userID != "" && userID != "0" {
		cleanFiles(CleanRealm{ID: userID, Cate: "user", Realm: "cache", Filter: true})
	}

	conf := botConf("user")
	if infos.Conf.DC != 0 {
		conf.DataCenter = infos.Conf.DC
	}

	client, err := telegram.NewClient(conf)
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

// startUserBot 发起手机号登录流程
func (infos *Infos) startUserBot(phone string) (err error) {
	infos.Mutex.Lock()
	switch infos.Status {
	case 1, 2:
		// 正在进行验证码或密码输入状态，不允许重复发起
		infos.Mutex.Unlock()
		err = errors.New("已有登录流程正在进行")
		log.Printf("UserBot 登录失败: %+v", err)
		return err
	case 3:
		// 已登录状态，若客户端实例丢失则尝试重建
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
		// 未登录状态，开始新的登录流程
		infos.Mutex.Unlock()
		if infos.UserClient == nil {
			if err := infos.userBotClient(); err != nil {
				log.Printf("UserBot 登录失败: %+v", err)
				infos.resetStatus()
				return err
			}
		}
		sendMS(nil, fmt.Sprintf("收到手机号 %s, 正在尝试发送验证码...", phone), nil, 60)

		// 在协程中执行阻塞的登录命令
		go func() {
			status, err := infos.UserClient.Login(phone, &telegram.LoginOptions{
				CodeCallback:     infos.code, // 指定验证码回调函数
				PasswordCallback: infos.pass, // 指定二步验证回调函数
				MaxRetries:       3,
			})
			if err != nil {
				log.Printf("UserBot 登录失败: %+v", err)
				sendMS(nil, fmt.Sprintf("UserBot 登录失败: %+v", err), nil, 60)
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
		infos.Status = 1
		infos.Mutex.Unlock()
		if infos.UserClient == nil {
			if err := infos.userBotClient(); err != nil {
				log.Printf("UserBot 登录失败: %+v", err)
				infos.resetStatus()
				return err
			}
		}
		sendMS(nil, "正在请求登录二维码...", nil, 60)

		// 启动登录流程（会阻塞, 直到登录完成或失败）
		go func() {
			qr, err := infos.UserClient.QRLogin(telegram.QrOptions{
				PasswordCallback: infos.pass,
			})
			if err != nil {
				log.Printf("获取 QR 登录失败: %+v", err)
				if !telegram.MatchError(err, "SESSION_PASSWORD_NEEDED]") {
					sendMS(nil, fmt.Sprintf("获取 QR 登录失败: %+v", err), nil, 60)
					infos.resetStatus()
					return
				}
			}

			png, err := qr.ExportAsPng()
			if err != nil {
				log.Printf("导出 QR PNG 失败: %+v", err)
				return
			}

			src, err := infos.BotClient.UploadFile(png, &telegram.UploadOptions{
				FileName: "qr.png",
			})
			if err != nil {
				log.Printf("上传 QR 文件失败: %+v", err)
				return
			}
			sendMS(nil, src, &telegram.SendOptions{Caption: "请使用手机 Telegram 扫描此二维码登录。二维码有效期 30 秒，如失效请重新发送 /qr"}, 35)
			err = qr.WaitLogin()
			if err != nil {
				if !strings.Contains(err.Error(), "scanning again") {
					sendMS(nil, fmt.Sprintf("QR 登录失败: %+v", err), nil, 60)
					infos.resetStatus()
					return
				}
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
		sendMS(nil, fmt.Sprintf("登录成功! 用户: %s", name), nil)
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
		infos.Mutex.Lock()
		infos.Status = 1
		infos.Mutex.Unlock()
		sendMS(nil, "等待用户输入 /code 验证码...", nil, 120)
		select {
		case code := <-infos.Code:
			return code, nil
		case <-time.After(2 * time.Minute):
			err = errors.New("等待验证码超时")
			sendMS(nil, err.Error(), nil, 60)
			return "", err
		}
	} else {
		err = errors.New("当前状态不是等待验证码")
		sendMS(nil, err.Error(), nil, 60)
		return "", err
	}
}

func (infos *Infos) submitCode(code string) (err error) {
	infos.Mutex.Lock()
	defer infos.Mutex.Unlock()

	if infos.Status != 1 {
		err = errors.New("当前状态不是等待验证码")
		sendMS(nil, err.Error(), nil, 60)
		return err
	}
	infos.Code <- code
	return nil
}

func (infos *Infos) pass() (pass string, err error) {
	if infos.Status == 1 {
		infos.Mutex.Lock()
		infos.Status = 2
		infos.Mutex.Unlock()
		sendMS(nil, "等待用户输入 /pass 2FA密码...", nil, 120)
		select {
		case pass := <-infos.Pass:
			return pass, nil
		case <-time.After(2 * time.Minute):
			err = errors.New("等待2FA密码超时")
			sendMS(nil, err.Error(), nil, 60)
			return "", err
		}
	} else {
		err = errors.New("当前状态不是等待2FA密码")
		sendMS(nil, err.Error(), nil, 60)
		return "", err
	}
}

func (infos *Infos) submitPass(pass string) (err error) {
	infos.Mutex.Lock()
	defer infos.Mutex.Unlock()

	if infos.Status != 2 {
		err = errors.New("当前状态不是等待2FA密码")
		sendMS(nil, err.Error(), nil, 60)
		return err
	}
	infos.Pass <- pass
	return nil
}

func (infos *Infos) resetStatus() {
	// 1. 断开连接并清理句柄
	if err := infos.UserClient.Disconnect(); err != nil {
		log.Printf("UserBot 断开连接失败: %+v", err)
	}
	// 2. 清理磁盘上的 Session 和 Cache 文件（防止因文件损坏导致的下次循环失败）
	cleanFiles(CleanRealm{Cate: "user", Realm: "session"})
	cleanFiles(CleanRealm{Cate: "user", Realm: "cache", Filter: false})

	// 3. 重置内存状态
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

func (infos *Infos) search(channel, keywords string, page, limit int, offset int32) (items Items, err error) {
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
		Query:  keywords,                             // 搜索关键字
		Limit:  int32(limit),                         // 条数限制
		Offset: offset,                               // 偏移量
		Filter: &telegram.InputMessagesFilterVideo{}, // 过滤视频
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
	for _, m := range ms {
		if m.File == nil {
			continue
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
	return items, nil
}

// handleMain 是 HTTP 服务的主分发函数，根据路径路由到不同的处理器
func handleMain(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	// 标准化路径处理，移除尾部斜杠
	if path != "/" {
		path = strings.TrimSuffix(path, "/")
	}
	switch {
	case path == "/":
		// 返回服务器状态 JSON
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		content := map[string]any{
			"message": "服务器正在运行。",
			"ok":      true,
			"uptime":  handleTime(uint64(time.Since(startTime).Seconds())), // 运行时间
			"version": version,
		}
		err := json.NewEncoder(w).Encode(content)
		if err != nil {
			log.Printf("发送网页失败: %+v", err)
		}
		return
	case strings.HasPrefix(path, "/link"):
		// 处理链接直链提取并跳转
		handleLink(w, r)
		return
	case strings.HasPrefix(path, "/stream"):
		// 处理文件分片流式下载（串流播放）核心接口
		handleStream(w, r)
		return
	case strings.HasPrefix(path, "/search"):
		// 处理搜索
		handleSearch(w, r)
		return
	default:
		// 404
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
		if !infos.isWhite(m.SenderID()) {
			sendMS(m, "你没有使用此机器人的权限", nil, 60)
			return nil
		}

		var src string
		if m.SenderID() == infos.Conf.UserID {
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
			} else {
				infos.Mutex.Lock()
				infos.Conf.WhiteIDs = append(infos.Conf.WhiteIDs, whiteID)
				infos.HasNew = true
				infos.Mutex.Unlock()
				sendMS(m, fmt.Sprintf("添加白名单成功: %d", whiteID), nil, 60)
			}
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
				infos.Conf.WhiteIDs = slices.DeleteFunc(infos.Conf.WhiteIDs, func(num int64) bool {
					return num == whiteID
				})
				infos.HasNew = true
				infos.Mutex.Unlock()
				sendMS(m, fmt.Sprintf("移除白名单成功: %d", whiteID), nil, 60)
				return nil
			} else {
				sendMS(m, fmt.Sprintf("用户 %d 不在白名单中", whiteID), nil, 60)
				return nil
			}
		}
		return nil
	case strings.HasPrefix(text, "/qr"):
		if m.SenderID() != infos.Conf.UserID {
			sendMS(m, "你没有使用此命令的权限", nil, 60)
			return nil
		}

		if err := infos.startUserBotQR(); err != nil {
			sendMS(m, fmt.Sprintf("启动 QR 登录失败: %+v", err), nil, 60)
			return nil
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
			return nil
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
		infos.HasNew = true
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
		infos.HasNew = true
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
		infos.HasNew = true
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
		infos.HasNew = true
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
		infos.HasNew = true
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
		infos.HasNew = true
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
			sendMS(m, values.String(), nil, 60)
			return nil
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
		} else {
			infos.Mutex.Lock()
			infos.Conf.Channels = append(infos.Conf.Channels, channel)
			infos.HasNew = true
			infos.Mutex.Unlock()
			sendMS(m, fmt.Sprintf("添加频道成功: %s", channel), nil, 60)
		}

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
		} else {
			infos.Mutex.Lock()
			infos.Conf.Channels = slices.DeleteFunc(infos.Conf.Channels, func(key string) bool {
				return key == channel
			})
			infos.HasNew = true
			infos.Mutex.Unlock()
			sendMS(m, fmt.Sprintf("移除频道成功: %s", channel), nil, 60)
		}

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
			for _, channel := range infos.Conf.Channels {
				if !strings.HasPrefix(channel, "@") {
					channel = "@" + channel
				}
				values.WriteString(fmt.Sprintf("• %s\n", html.EscapeString(channel)))
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
		infos.HasNew = true
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

		// 读取日志
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
		header := fmt.Sprintf("<b>📜 系统日志 (最后 %d 行)</b>\n\n", len(lines))
		values.WriteString(header)
		values.WriteString("<pre>")

		for _, line := range lines {
			line = html.EscapeString(line) + "\n"
			// 如果当前消息加上这行和 </pre> 后超过限制，则先发送当前部分
			if values.Len()+len(line)+len("</pre>") > maxCount {
				values.WriteString("</pre>")
				sendMS(m, values.String(), nil)
				// 重置 Builder 开启下一个分片
				values.Reset()
				values.WriteString("<pre>")
			}
			values.WriteString(line)
		}

		// 发送剩余内容
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

func handleSearch(w http.ResponseWriter, r *http.Request) {
	if infos.UserClient == nil {
		http.Error(w, "userBot 未登录, 无法使用搜索功能", http.StatusUnauthorized)
		return
	}
	params := r.URL.Query()
	err := checkPass(params)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	keywords := params.Get("keywords")
	if keywords == "" {
		http.Error(w, "缺少关键词", http.StatusBadRequest)
		return
	}
	value := params.Get("page")
	if value == "" {
		value = "1"
	}
	page, err := strconv.Atoi(value)
	if err != nil || page <= 0 {
		page = 1
	}
	value = params.Get("offset")
	offset, err := strconv.ParseInt(value, 10, 32)
	if err != nil || offset <= 0 {
		offset = 0
	}

	limit, err := strconv.Atoi(value)
	if err != nil || limit <= 0 {
		limit = 20
	}

	ctx, cannel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cannel()
	count := atomic.Int64{}
	results := make(chan Items, len(infos.Conf.Channels))

	for _, channel := range infos.Conf.Channels {
		count.Add(1)
		channel = strings.TrimPrefix(channel, "@")
		go func(channel string) {
			defer count.Add(-1)
			result, err := infos.search(channel, keywords, page, limit, int32(offset))
			if err != nil {
				// log.Printf("搜索失败: %+v", err)
				return
			}
			select {
			case <-ctx.Done():
				return
			case results <- result:
			default:
				log.Print("搜索通道已满")
			}
		}(channel)
	}

	var items struct {
		HasMore bool    `json:"more"`
		Items   []Items `json:"items"`
	}

	items.Items = make([]Items, 0, len(infos.Conf.Channels))
	defer func() {
		content, err := json.Marshal(items)
		if err != nil {
			log.Printf("JSON序列化失败: %+v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		n, err := w.Write(content)
		if err != nil {
			log.Printf("写入长度 %d 的响应体失败: %+v", n, err)
			return
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case result := <-results:
			if len(result.Item) > 0 {
				items.Items = append(items.Items, result)
			}
			if !items.HasMore && result.HasMore {
				items.HasMore = result.HasMore
			}
		default:
			if count.Load() == 0 {
				return
			}
		}
	}
}

// handleStream 处理来自 HTTP 的文件流式读取请求
// 该函数实现了 Range 分段下载支持，允许像播放普通 mp4 文件一样拖动进度条
func handleStream(w http.ResponseWriter, r *http.Request) {
	// 1. 获取 URL 参数并完成身份校验
	params := r.URL.Query()
	err := checkPass(params)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	// 2. 解析频道 ID 和 消息 ID
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
		// 尝试从路径中提取 (兼容模式)
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

	// 3. 选择下载客户端 (Bot 或 UserBot)
	cate := params.Get("cate")
	if cate == "user" && infos.Status == 3 {
		infos.Client = infos.UserClient
	} else {
		infos.Client = infos.BotClient
	}

	// 4. 从 Telegram 获取指定消息
	ms, err := infos.Client.GetMessages(cid, &telegram.SearchOption{IDs: []int32{mid}})
	if err != nil || len(ms) == 0 {
		log.Printf("获取消息失败: cid=%d, mid=%d, err=%v, count=%d", cid, mid, err, len(ms))
		http.Error(w, fmt.Sprintf("获取消息失败: cid=%d, mid=%d, err=%v, count=%d", cid, mid, err, len(ms)), http.StatusNotFound)
		return
	}
	src := ms[0]

	// 5. 确保消息包含媒体文件并获取元数据
	if !src.IsMedia() {
		log.Printf("消息不包含媒体: cid=%d, mid=%d", cid, mid)
		http.Error(w, fmt.Sprintf("消息不包含媒体: cid=%d, mid=%d", cid, mid), http.StatusBadRequest)
		return
	}

	size := src.File.Size
	fileName := src.File.Name

	// 创建新的 Stream 流管理对象
	stream := newStream(r.Context(), infos.Client, src.Media(), infos.Conf.Workers, mid, cid, fileName)

	// 如果是转发的消息，重定向源频道以确保分片下载稳定性
	if src.Message.FwdFrom != nil {
		if ch, ok := src.Message.FwdFrom.FromID.(*telegram.PeerChannel); ok {
			stream.CID = ch.ChannelID
			stream.MID = src.Message.FwdFrom.ChannelPost
		}
	}

	// 6. 设置 HTTP 响应头
	w.Header().Set("Accept-Ranges", "bytes") // 启用 Range 支持
	w.Header().Set("Content-Type", handleMediaCate(fileName))

	disposition := "inline"
	if r.URL.Query().Get("download") == "true" {
		disposition = "attachment" // 附件模式下载
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("%s; filename=\"%s\"", disposition, fileName))

	// 7. 处理 HTTP Range 请求（分段读取的核心逻辑）
	var start, end int64
	rangeHeader := r.Header.Get("Range")

	if rangeHeader == "" {
		// 全量读取
		start = 0
		end = size - 1
		w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
		w.WriteHeader(http.StatusOK)
	} else {
		// 处理 Range 范围，例如：bytes=0-499
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

	// 8. 启动并发下载协程
	stream.ContentSize = end - start + 1
	go stream.start(start, end)
	defer stream.clean() // 结束时清理

	// 9. 循环从下载管道读取分片并写入 HTTP 响应体
	if r.Method == http.MethodGet {
		for {
			select {
			case <-r.Context().Done():
				// 客户端断开连接（如浏览器关闭或拖动进度条导致旧请求作废）
				log.Printf("流式传输文件已取消: cid=%d, mid=%d, fileName=%s", cid, mid, fileName)
				return
			case task := <-stream.Tasks:
				// 读取一个下载好的分片任务
				if task == nil {
					log.Printf("流式传输文件出错: cid=%d, mid=%d, fileName=%s, error=任务为空", cid, mid, fileName)
					continue
				}
				// 等待任务标记为 Done
				task.Cond.L.Lock()
				for !*task.Done {
					task.Cond.Wait()
				}
				task.Cond.L.Unlock()

				if task.Error != nil {
					log.Printf("切片下载出错: cid=%d, mid=%d, start=%d, end=%d, fileName=%s, error=%+v", cid, mid, task.ContentStart, task.ContentEnd, fileName, task.Error)
					return
				}
				// 写入响应
				if _, err := w.Write(*task.Content); err != nil {
					log.Printf("写入文件流时出错: cid=%d, mid=%d, err=%v", cid, mid, err)
				}

				// 检查是否已经写完当前请求的所有范围
				if task.ContentEnd >= end {
					log.Printf("流式传输文件已完成: cid=%d, mid=%d", cid, mid)
					return
				}
				task = nil
			}
		}

	} else {
		// HEAD 请求只返回头部，不执行循环体
		http.Error(w, fmt.Sprintf("不支持的请求方法: %s", r.Method), http.StatusMethodNotAllowed)
		return
	}
}

// handleLink 处理链接提取请求，将 Telegram 消息链接转换为直链下载地址并执行重定向
func handleLink(w http.ResponseWriter, r *http.Request) {
	res := HackLink{}
	params := r.URL.Query()

	// 1. 验证访问权限 (密码或哈希)
	err := checkPass(params)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	// 2. 获取目标 Telegram 链接
	src := params.Get("link")
	if src == "" || !strings.HasPrefix(src, "http") {
		http.Error(w, "无效的链接", http.StatusBadRequest)
		return
	}

	// 3. 正则匹配并解析链接
	re := regexp.MustCompile(`t\.me\/(c\/(\d+)|([a-zA-Z0-9_]+))\/(\d+)(?:\?.*comment=(\d+))?`)
	matches := re.FindAllStringSubmatch(src, -1)
	res.Matches = matches
	value := params.Get("uid")
	res.UID, err = strconv.ParseInt(value, 10, 64)
	if err != nil {
		log.Printf("转换UID错误: %+v", err)
	}
	res.Pass = params.Get("key")
	res.Hash = params.Get("hash")

	// 4. 调用解析核心逻辑提取直链
	for _, link := range hackLink(res) {
		// 成功提取到直链后执行 302 重定向
		http.Redirect(w, r, link, http.StatusFound)
		return
	}

	http.Error(w, "未找到可下载的媒体", http.StatusNotFound)
}

func checkPass(params handleUrl.Values) error {
	if infos.Conf.Password != "" {
		hash := params.Get("hash") // 基于用户 ID 的哈希校验
		password := params.Get("key")
		switch {
		case password != "":
			if password != infos.Conf.Password {
				return errors.New("无效的密码")
			}
		case hash != "":
			value := params.Get("uid")
			uid, err := strconv.ParseInt(value, 10, 64)
			if err == nil && uid != 0 {
				if hash != infos.calculateHash(uid) {
					return errors.New("无效的哈希密码")
				}
			} else {
				log.Printf("UID无效: %s", value)
				return errors.New("无效的UID")
			}
		default:
			return errors.New("您没有权限访问此链接")
		}
	}
	return nil
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

// readLastLines 读取日志文件的最后指定行数，供管理员查看
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
	// 使用 Scanner 遍历文件行
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		if re.MatchString(scanner.Text()) {
			lines = append(lines, scanner.Text())
		}
		// 超过行数限制后，舍弃旧行（滑动窗口效果）
		if len(lines) > count {
			lines = lines[1:]
		}
	}
	if err := scanner.Err(); err != nil {
		return lines, err
	}
	return lines, nil
}

// hackLink 是链接解析的核心逻辑，负责将 t.me 链接映射到具体的媒体消息并生成本程序的流地址
func hackLink(res HackLink) (links []string) {
	// 遍历所有匹配到的链接
	for _, match := range res.Matches {
		var cid any   // 用于 ResolvePeer 的标识项（可以是用户名或 chatID）
		var mid int32 // 消息 ID

		// 1. 解析 Chat ID 或 Username
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
		// 注意：只有 UserBot 才能访问非机器人所在或未授权的私密链接
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

		// 4. 处理链接中的评论 (comment) 逻辑，如果存在则寻找对应的评论消息
		if match[5] != "" {
			commentID, err := strconv.ParseInt(match[5], 10, 32)
			if err != nil {
				continue
			}

			// a. 检查该消息是否有讨论功能并获取讨论组 ID
			if src.Message.Replies != nil && src.Message.Replies.ChannelID != 0 {
				discussionID := src.Message.Replies.ChannelID // 讨论组的 ID

				// b. 从讨论组中获取真正的评论消息
				commentMs, err := infos.UserClient.GetMessages(discussionID, &telegram.SearchOption{IDs: []int32{int32(commentID)}})
				if err == nil && len(commentMs) > 0 {
					src = commentMs[0] // 将目标消息切换为评论内容
					src.ID = int32(commentID)
					src.Chat.ID = discussionID
				}
			}
		}

		// 5. 校验消息是否包含可流式传输的媒体（图片、文档、视频）
		if !src.IsMedia() || (src.Photo() == nil && src.Document() == nil && src.Video() == nil) {
			log.Printf("消息不包含媒体: cid=%d, mid=%d", cid, mid)
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
				// 自动为发起请求的用户生成哈希链接
				link += fmt.Sprintf("&hash=%s&uid=%d", infos.calculateHash(res.M.SenderID()), res.M.SenderID())
			} else {
				// 手动提供 hash/pass 模式
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
				_, err = ms.Delete()
				if err != nil {
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
				_, err = ms.Delete()
				if err != nil {
					log.Printf("删除消息失败: %+v", err)
				}
			}()
		}
		return
	}
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

func extractContent(src string) (string, *int) {
	src = strings.TrimSpace(src)

	// 1. 如果整个字符串就是一个数字 (对应情况：纯数字)
	if num, err := strconv.Atoi(src); err == nil {
		return "", &num
	}

	// 3. 寻找主体部分最后一个空格
	count := strings.LastIndexByte(src, ' ')
	if count == -1 {
		// 主体中没有空格，说明只有一种内容。
		// 由于第一步已经排除了整个是纯数字的情况，所以如果是 "re 123"，123 到底是XXX还是数字？
		// 按照通常逻辑 "re XXX"，这个单词就是 XXX
		return src, nil
	}

	// 4. 判断最后一个空格后面那一截是不是数字
	content := src[count+1:]
	if num, err := strconv.Atoi(content); err == nil {
		// 解析数字成功，说明最后一个空格前面的是 XXX，后面的是数字
		return src[:count], &num
	}

	// 5. 最后一个空格后面不是数字，说明整个 body 都是 XXX（例如 "re hello world"）
	return src, nil
}

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

	// 如果是整数则不保留小数，有小数则保留两位
	if result == float64(int64(result)) {
		return fmt.Sprintf("%.0f%s", result, units[count])
	}
	return fmt.Sprintf("%.2f%s", result, units[count])
}
