package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/amarnathcjd/gogram/telegram"
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
	Filter bool   // 是否启用过滤, 只删除特定 ID 以外的文件
	ID     string // 过滤 ID（如账号 ID）
	Cate   string // 类型：bot 或 user
	Realm  string // 范围：cache 或 session
}

type OffSet struct {
	Offset int32     // 偏移量
	Time   time.Time // 时间
}

type OffSets struct {
	Mutex   *sync.Mutex       // 互斥锁, 保护并发安全
	OffSets map[string]OffSet // 偏移量映射
}

type Item struct {
	Name string `json:"name"`
	Text string `json:"text,omitempty"`
	MID  int32  `json:"mid"`
	CID  int64  `json:"cid"`
	Size int64  `json:"size"`
	Date int32  `json:"date,omitempty"`
}

type ChannelItem struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	CID  int64  `json:"cid,omitempty"`
}

type MediaContent struct {
	Start   int64
	End     int64
	Content []byte
	Time    time.Time
}

type MediaCache struct {
	Contents []MediaContent
	Time     time.Time
}

type Items struct {
	HasMore bool   `json:"more"`
	Channel string `json:"channel"`
	Item    []Item `json:"item"`
}

type ID struct {
	Hash    string
	IsAdmin bool
	IsWhite bool
}

// Infos 结构体保存了程序运行时的全局状态和资源句柄
type Infos struct {
	BotClient  *telegram.Client       // 独立的 Bot 客户端（用于与用户交互）
	UserClient *telegram.Client       // 全局 UserBot 客户端实例（用于读取私有内容和流式传输）
	Client     *telegram.Client       // 当前活跃客户端指针
	Mutex      *sync.RWMutex          // 全局互斥锁, 保护并发安全
	Cond       *sync.Cond             // 条件变量, 用于等待
	Conf       *Conf                  // 指向全局配置
	File       *os.File               // 日志文件句柄
	Rex        *regexp.Regexp         // 用于解析 Telegram FloodWait 错误的正则
	RexRules   []*regexp.Regexp       // 预编译的群管正则规则缓存
	FilesPath  string                 // 配置文件存放目录
	FilePath   string                 // 日志文件路径
	BotID      int64                  // Bot 自身的 ID
	Status     atomic.Int32           // UserBot 登录状态: 0 未登录, 1 等待验证码, 2 等待二步验证, 3 已登录
	WaitUntil  atomic.Int64           // 等待结束时间
	Code       chan string            // 用于接收异步提交的验证码
	Pass       chan string            // 用于接收异步提交的二步验证密码
	IDs        map[int64]ID           // 缓存用户 ID 到哈希的映射, 减少重复计算
	HeadCache  map[string]*MediaCache // 缓存文件头部数据
	TailCache  map[string]*MediaCache // 缓存文件尾部数据
}

var infos *Infos
var offSets *OffSets
var startTime time.Time
var version = "v1.0.10"

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
		if infos.File != nil {
			if err := infos.File.Close(); err != nil {
				log.Printf("关闭日志文件错误: %v", err)
			}
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

	// 4. 启动 Bot 客户端
	if err = infos.startBot(); err != nil {
		return
	}

	// 5. 初始化 UserBot 客户端（此时只是连接, 尚未完成登录流程）
	if err = infos.userBotClient(); err != nil {
		log.Printf("UserBot 启动失败: %+v", err)
		return
	}

	// 忽略 SIGPIPE 信号, 防止由于网络异常断开导致进程崩溃
	signal.Ignore(syscall.SIGPIPE)

	// 设置系统中断信号监听, 用于优雅退出
	statusChan := make(chan os.Signal, 1)
	signal.Notify(statusChan, os.Interrupt, syscall.SIGTERM)

	// 创建 HTTP 服务器
	server := &http.Server{
		Addr:              fmt.Sprintf(":%d", infos.Conf.Port),
		Handler:           http.HandlerFunc(handleMain),
		ReadTimeout:       30 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       600 * time.Second,
		MaxHeaderBytes:    1 << 20, // 最大头部字节数 (1MB)
	}

	// 6. 在独立协程中启动 HTTP 服务
	go func() {
		log.Printf("HTTP 服务运行在 %d 端口", infos.Conf.Port)

		if err := server.ListenAndServe(); err != nil {
			log.Printf("HTTP 服务启动失败: %+v", err)
			statusChan <- os.Interrupt
		}
	}()

	// 7. 发送程序启动通知
	sendMS(nil, "程序已启动", nil, 60)

	// 8. 检查 UserBot 登录状态, 尝试自动登录（若已存在 session）
	if err := infos.checkStatus(); err != nil {
		log.Printf("UserBot 登录失败: %+v", err)
		infos.resetStatus()
	}

	// 阻塞等待直到接收到退出信号
	status := <-statusChan
	log.Printf("收到信号: %v, 正在退出...", status)

	// 设置关闭的超时时间，例如 10 秒
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Printf("HTTP 服务关闭异常: %+v", err)
	} else {
		log.Printf("HTTP 服务已优雅关闭")
	}
	sendMS(nil, "程序已退出", nil, 60)
}

// newInfos 初始化全局 Infos 对象, 加载日志和配置
func newInfos(filePath, filesPath string) (*Infos, error) {
	mutex := new(sync.RWMutex)
	infos := &Infos{
		FilePath:  filePath,
		FilesPath: filesPath,
		Mutex:     mutex,
		Cond:      sync.NewCond(mutex),
		Code:      make(chan string, 1),
		Pass:      make(chan string, 1),
		HeadCache: make(map[string]*MediaCache, 4),
		TailCache: make(map[string]*MediaCache, 4),
		Rex:       regexp.MustCompile(`(?i)(?:FLOOD(?:_PREMIUM)?_WAIT_(\d+)|WAIT(?:\s+OF)?\s*(\d+))`),
	}
	// 启动配置自动保存监听
	//go infos.watchConf()

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
	infos.IDs = make(map[int64]ID, len(conf.AdminIDs)+len(conf.WhiteIDs)+1)
	infos.buildIDs()
	infos.buildRexRules()

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

// newOffSets 初始化全局翻页偏移量缓存
func newOffSets() *OffSets {
	return &OffSets{
		Mutex:   new(sync.Mutex),
		OffSets: make(map[string]OffSet),
	}
}

// buildRegex 预编译正则规则并缓存到 infos.RexRules
func (infos *Infos) buildRexRules() {
	infos.Mutex.Lock()
	defer infos.Mutex.Unlock()
	infos.RexRules = make([]*regexp.Regexp, 0, len(infos.Conf.Rules))
	for _, rule := range infos.Conf.Rules {
		if rule == "" {
			continue
		}
		r, err := regexp.Compile(rule)
		if err != nil {
			log.Printf("正则规则编译失败 [%s]: %+v", rule, err)
			continue
		}
		infos.RexRules = append(infos.RexRules, r)
	}
	log.Printf("成功预编译 %d 条正则规则", len(infos.RexRules))
}
