package main

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/gob"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	handleUrl "net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/celestix/gotgproto"
	"github.com/celestix/gotgproto/dispatcher"
	"github.com/celestix/gotgproto/dispatcher/handlers"
	"github.com/celestix/gotgproto/ext"
	"github.com/celestix/gotgproto/sessionMaker"
	"github.com/celestix/gotgproto/storage"
	"github.com/celestix/gotgproto/types"
	"github.com/coocood/freecache"
	"github.com/glebarez/sqlite"
	"github.com/gotd/td/tg"
	"github.com/joho/godotenv"
	rangeParser "github.com/quantumsheep/range-parser"
)

// ============================================================================
// 版本相关
// ============================================================================
var version = "v1.0.0"

// ============================================================================
// 配置相关
// ============================================================================

type Config struct {
	ApiID        int32
	ApiHash      string
	BotToken     string
	LogChannelID int64
	Port         int
	Host         string
	HashLength   int
	AdminUsers   []int64
	TeleID       int64  // 机器人用户ID
	PhoneNumber  string // User Bot 手机号（可选）
	Password     string
}

var config *Config
var startTime time.Time
var UserBot *gotgproto.Client // User Bot 客户端

// ============================================================================
// 类型定义
// ============================================================================

type File struct {
	Location tg.InputFileLocationClass
	FileSize int64
	FileName string
	MimeType string
	ID       int64
}

type HashableFileStruct struct {
	FileName string
	FileSize int64
	MimeType string
	FileID   int64
}

func (f *HashableFileStruct) Pack() string {
	hasher := md5.New()
	val := reflect.ValueOf(*f)
	for i := 0; i < val.NumField(); i++ {
		field := val.Field(i)
		var fieldValue []byte
		switch field.Kind() {
		case reflect.String:
			fieldValue = []byte(field.String())
		case reflect.Int64:
			fieldValue = []byte(strconv.FormatInt(field.Int(), 10))
		default:
			fieldValue = []byte{}
		}
		hasher.Write(fieldValue)
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

type RootResponse struct {
	Message string `json:"message"`
	Ok      bool   `json:"ok"`
	Uptime  string `json:"uptime"`
	Version string `json:"version"`
}

// ============================================================================
// 缓存相关
// ============================================================================

type Cache struct {
	cache *freecache.Cache
	mu    sync.RWMutex
}

var cache *Cache

func InitCache() {
	gob.Register(File{})
	gob.Register(tg.InputDocumentFileLocation{})
	gob.Register(tg.InputPhotoFileLocation{})
	cache = &Cache{cache: freecache.NewCache(10 * 1024 * 1024)}
	log.Println("缓存已初始化")
}

// ============================================================================
// 通知去重
// ============================================================================

// notifyDedupEntry 记录一条已发送的通知文本及其发送时间
type notifyDedupEntry struct {
	sentAt time.Time
}

var (
	notifyDedupMu  sync.Mutex
	notifyDedupMap = make(map[string]notifyDedupEntry)
)

// isDuplicateNotify 检查 text 是否在 1 分钟内已经发送过。
// 若未发送过（或已超时），则记录并返回 false；否则返回 true。
func isDuplicateNotify(text string) bool {
	// 用 MD5 作为 key 以节省内存
	h := md5.Sum([]byte(text))
	key := hex.EncodeToString(h[:])

	notifyDedupMu.Lock()
	defer notifyDedupMu.Unlock()

	// 清理过期条目（简单遍历，条目数量通常很少）
	now := time.Now()
	for k, v := range notifyDedupMap {
		if now.Sub(v.sentAt) >= 5*time.Minute {
			delete(notifyDedupMap, k)
		}
	}

	if entry, exists := notifyDedupMap[key]; exists {
		if now.Sub(entry.sentAt) < 5*time.Minute {
			return true // 重复
		}
	}
	notifyDedupMap[key] = notifyDedupEntry{sentAt: now}
	return false
}

func (c *Cache) Get(key string, value *File) error {
	c.mu.RLock()
	defer c.mu.RUnlock()
	data, err := c.cache.Get([]byte(key))
	if err != nil {
		return err
	}
	dec := gob.NewDecoder(bytes.NewReader(data))
	return dec.Decode(value)
}

func (c *Cache) Set(key string, value *File, expireSeconds int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	var buf bytes.Buffer
	enc := gob.NewEncoder(&buf)
	err := enc.Encode(value)
	if err != nil {
		return err
	}
	err = c.cache.Set([]byte(key), buf.Bytes(), expireSeconds)
	if err != nil {
		return err
	}
	return nil
}

// ============================================================================
// Telegram 客户端相关
// ============================================================================

var Bot *gotgproto.Client

// BotAuthConversator 实现自定义认证流程，等待机器人发送验证码
type BotAuthConversator struct {
	phoneNumber string
	codeChan    chan string
	passChan    chan string
}

// NewBotAuthConversator 创建一个新的 BotAuthConversator
func NewBotAuthConversator(phoneNumber string) *BotAuthConversator {
	return &BotAuthConversator{
		phoneNumber: phoneNumber,
		codeChan:    make(chan string, 1),
		passChan:    make(chan string, 1),
	}
}

// AskPhoneNumber 返回配置的手机号
func (b *BotAuthConversator) AskPhoneNumber() (string, error) {
	log.Printf("使用手机号: %s\n", maskPhone(b.phoneNumber))
	return b.phoneNumber, nil
}

// AskCode 等待用户通过机器人发送 /code 命令
func (b *BotAuthConversator) AskCode() (string, error) {
	log.Println("============================================")
	log.Println("等待验证码...")
	log.Println("请在 Telegram 机器人中发送: /code <验证码>")
	log.Println("例如: /code 12345")
	log.Println("============================================")

	// 等待从 channel 接收验证码，设置超时时间为 5 分钟
	select {
	case code := <-b.codeChan:
		log.Printf("已接收验证码: %s\n", code)
		return code, nil
	case <-time.After(5 * time.Minute):
		return "", errors.New("等待验证码超时（5分钟）")
	}
}

// AskPassword 请求两步验证密码
func (b *BotAuthConversator) AskPassword() (string, error) {
	log.Println("============================================")
	log.Println("需要两步验证密码")
	log.Println("请在 Telegram 机器人中发送: /pass <密码>")
	log.Println("例如: /pass mypassword")
	log.Println("============================================")

	// 等待从 channel 接收密码，设置超时时间为 5 分钟
	select {
	case password := <-b.passChan:
		log.Println("已接收两步验证密码")
		return password, nil
	case <-time.After(5 * time.Minute):
		return "", errors.New("等待密码超时（5分钟）")
	}
}

// AuthStatus 接收认证状态更新
func (b *BotAuthConversator) AuthStatus(authStatus gotgproto.AuthStatus) {
	log.Printf("认证状态更新: %+v (剩余尝试次数: %d)\n", authStatus.Event, authStatus.AttemptsLeft)
}

var userBotAuthConversator *BotAuthConversator // 全局变量，用于在命令处理器中访问

func StartClient() error {
	clientOpts := &gotgproto.ClientOpts{
		Session:          sessionMaker.SqlSession(sqlite.Open("files/fsb.session")),
		DisableCopyright: true,
	}

	type ClintChan struct {
		client *gotgproto.Client
		err    error
	}

	clintChan := make(chan ClintChan, 1)
	// 在后台执行可能阻塞的构造
	go func() {
		client, err := gotgproto.NewClient(
			int(config.ApiID),
			config.ApiHash,
			gotgproto.ClientTypeBot(config.BotToken),
			clientOpts,
		)
		clintChan <- ClintChan{client: client, err: err}
	}()

	// 等待结果或超时
	select {
	case result := <-clintChan:
		if result.err != nil {
			return result.err
		}
		Bot = result.client
		log.Printf("机器人已启动: @%s\n", Bot.Self.Username)
		LoadCommands(Bot.Dispatcher)
		return nil
	case <-time.After(15 * time.Second):
		// 超时：选择一 — 返回错误；或二 — 让后台继续并稍后再处理
		return fmt.Errorf("启动客户端超时(15s)")
	}
}

// StartUserBot 启动 User Bot 客户端，使用自定义认证流程
func StartUserBot() error {
	log.Println("正在启动 User Bot...")

	// 创建自定义认证会话处理器
	authConversator := NewBotAuthConversator(config.PhoneNumber)
	userBotAuthConversator = authConversator // 保存到全局变量

	clientOpts := &gotgproto.ClientOpts{
		Session:          sessionMaker.SqlSession(sqlite.Open("files/userbot.session")),
		DisableCopyright: true,
		AuthConversator:  authConversator, // 使用自定义认证处理器
	}

	client, err := gotgproto.NewClient(
		int(config.ApiID),
		config.ApiHash,
		gotgproto.ClientTypePhone(config.PhoneNumber),
		clientOpts,
	)
	if err != nil {
		return fmt.Errorf("启动 User Bot 失败: %v", err)
	}

	UserBot = client
	log.Printf("User Bot 已启动: @%s (ID: %d)\n", client.Self.Username, client.Self.ID)

	return nil
}

// ============================================================================
// 命令处理器
// ============================================================================

func LoadCommands(d dispatcher.Dispatcher) {
	d.AddHandler(handlers.NewCommand("start", handleStart))
	d.AddHandler(handlers.NewCommand("allow", handleAllow))
	d.AddHandler(handlers.NewCommand("disallow", handleDisallow))
	d.AddHandler(handlers.NewCommand("phone", handlePhone))
	d.AddHandler(handlers.NewCommand("code", handleCode))
	d.AddHandler(handlers.NewCommand("pass", handlePass))
	d.AddHandler(handlers.NewMessage(nil, handleMessage))
	log.Println("命令处理器已加载")
}

// 管理员判断（使用 ADMIN_USERS 作为管理员列表）
func isAdmin(userID int64) bool {
	return len(config.AdminUsers) > 0 && contains(config.AdminUsers, userID)
}

// /allow 命令：/allow <userID>
func handleAllow(ctx *ext.Context, u *ext.Update) error {
	adminID := u.EffectiveChat().GetID()
	if !isAdmin(adminID) {
		_, _ = ctx.Reply(u, ext.ReplyTextString("无权执行此命令（仅限管理员）"), nil)
		return dispatcher.EndGroups
	}

	args := strings.Fields(strings.TrimSpace(u.EffectiveMessage.Text))
	if len(args) < 2 {
		_, _ = ctx.Reply(u, ext.ReplyTextString("用法: /allow <用户ID>"), nil)
		return dispatcher.EndGroups
	}
	userID, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		_, _ = ctx.Reply(u, ext.ReplyTextString("无效的用户ID"), nil)
		return dispatcher.EndGroups
	}

	created := whitelist.Add(userID)
	if saveErr := whitelist.Save(); saveErr != nil {
		log.Printf("保存白名单失败: %v", saveErr)
	}

	if created {
		_, _ = ctx.Reply(u, ext.ReplyTextString(fmt.Sprintf("已将用户加入白名单: %d", userID)), nil)
	} else {
		_, _ = ctx.Reply(u, ext.ReplyTextString(fmt.Sprintf("用户已在白名单中: %d", userID)), nil)
	}
	return dispatcher.EndGroups
}

// /disallow 命令：/disallow <userID>
func handleDisallow(ctx *ext.Context, u *ext.Update) error {
	adminID := u.EffectiveChat().GetID()
	if !isAdmin(adminID) {
		_, _ = ctx.Reply(u, ext.ReplyTextString("无权执行此命令（仅限管理员）"), nil)
		return dispatcher.EndGroups
	}

	args := strings.Fields(strings.TrimSpace(u.EffectiveMessage.Text))
	if len(args) < 2 {
		_, _ = ctx.Reply(u, ext.ReplyTextString("用法: /disallow <用户ID>"), nil)
		return dispatcher.EndGroups
	}
	userID, err := strconv.ParseInt(args[1], 10, 64)
	if err != nil {
		_, _ = ctx.Reply(u, ext.ReplyTextString("无效的用户ID"), nil)
		return dispatcher.EndGroups
	}

	removed := whitelist.Remove(userID)
	if saveErr := whitelist.Save(); saveErr != nil {
		log.Printf("保存白名单失败: %v", saveErr)
	}
	if removed {
		_, _ = ctx.Reply(u, ext.ReplyTextString(fmt.Sprintf("已将用户从白名单移出: %d", userID)), nil)
	} else {
		_, _ = ctx.Reply(u, ext.ReplyTextString(fmt.Sprintf("用户不在白名单中: %d", userID)), nil)
	}
	return dispatcher.EndGroups
}

// /phone 命令：/phone <手机号>
func handlePhone(ctx *ext.Context, u *ext.Update) error {
	adminID := u.EffectiveChat().GetID()
	if !isAdmin(adminID) {
		_, _ = ctx.Reply(u, ext.ReplyTextString("无权执行此命令（仅限管理员）"), nil)
		return dispatcher.EndGroups
	}

	args := strings.Fields(strings.TrimSpace(u.EffectiveMessage.Text))
	if len(args) < 2 {
		masked := maskPhone(config.PhoneNumber)
		msg := "用法: /phone <手机号>\n示例: /phone +8613800138000"
		if masked != "" {
			msg += fmt.Sprintf("\n当前已保存: %s", masked)
		}
		_, _ = ctx.Reply(u, ext.ReplyTextString(msg), nil)
		return dispatcher.EndGroups
	}

	phone := strings.TrimSpace(args[1])
	if !validPhone(phone) {
		_, _ = ctx.Reply(u, ext.ReplyTextString("手机号格式不正确，请使用国际区号格式，例如 +8613800138000"), nil)
		return dispatcher.EndGroups
	}

	if err := savePhoneEncrypted(phone); err != nil {
		log.Printf("保存加密手机号失败: %v", err)
		_, _ = ctx.Reply(u, ext.ReplyTextString("保存失败，请查看服务端日志"), nil)
		return dispatcher.EndGroups
	}

	config.PhoneNumber = phone

	// 若启用了 User Bot 且尚未启动，则尝试立即启动
	if config.PhoneNumber != "" && UserBot == nil {
		if err := StartUserBot(); err != nil {
			log.Printf("设置手机号后启动 User Bot 失败: %v", err)
			_, _ = ctx.Reply(u, ext.ReplyTextString("号码已保存，但启动 User Bot 失败，请查看日志或稍后重试"), nil)
			return dispatcher.EndGroups
		}
		_, _ = ctx.Reply(u, ext.ReplyTextString("手机号已保存，User Bot 已启动。"), nil)
		return dispatcher.EndGroups
	}

	_, _ = ctx.Reply(u, ext.ReplyTextString("手机号已保存。若已在运行，将在下次重启后生效。"), nil)
	return dispatcher.EndGroups
}

// /code 命令：/code <验证码>
func handleCode(ctx *ext.Context, u *ext.Update) error {
	adminID := u.EffectiveChat().GetID()
	if !isAdmin(adminID) {
		_, _ = ctx.Reply(u, ext.ReplyTextString("无权执行此命令（仅限管理员）"), nil)
		return dispatcher.EndGroups
	}

	args := strings.Fields(strings.TrimSpace(u.EffectiveMessage.Text))
	if len(args) < 2 {
		_, _ = ctx.Reply(u, ext.ReplyTextString("用法: /code <验证码>\n例如: /code 12345"), nil)
		return dispatcher.EndGroups
	}

	code := strings.TrimSpace(args[1])
	if code == "" {
		_, _ = ctx.Reply(u, ext.ReplyTextString("验证码不能为空"), nil)
		return dispatcher.EndGroups
	}

	// 检查是否有等待验证码的 authConversator
	if userBotAuthConversator == nil {
		_, _ = ctx.Reply(u, ext.ReplyTextString("当前没有等待验证码的认证流程"), nil)
		return dispatcher.EndGroups
	}

	// 尝试发送验证码到 channel
	select {
	case userBotAuthConversator.codeChan <- code:
		_, _ = ctx.Reply(u, ext.ReplyTextString("✅ 验证码已提交"), nil)
		log.Printf("管理员提交验证码: %s\n", code)
	default:
		_, _ = ctx.Reply(u, ext.ReplyTextString("验证码 channel 已满或当前不需要验证码"), nil)
	}

	return dispatcher.EndGroups
}

// /pass 命令：/pass <密码>
func handlePass(ctx *ext.Context, u *ext.Update) error {
	adminID := u.EffectiveChat().GetID()
	if !isAdmin(adminID) {
		_, _ = ctx.Reply(u, ext.ReplyTextString("无权执行此命令（仅限管理员）"), nil)
		return dispatcher.EndGroups
	}

	args := strings.Fields(strings.TrimSpace(u.EffectiveMessage.Text))
	if len(args) < 2 {
		_, _ = ctx.Reply(u, ext.ReplyTextString("用法: /pass <密码>\n例如: /pass mypassword"), nil)
		return dispatcher.EndGroups
	}

	password := strings.TrimSpace(args[1])
	if password == "" {
		_, _ = ctx.Reply(u, ext.ReplyTextString("密码不能为空"), nil)
		return dispatcher.EndGroups
	}

	// 检查是否有等待密码的 authConversator
	if userBotAuthConversator == nil {
		_, _ = ctx.Reply(u, ext.ReplyTextString("当前没有等待密码的认证流程"), nil)
		return dispatcher.EndGroups
	}

	// 尝试发送密码到 channel
	select {
	case userBotAuthConversator.passChan <- password:
		_, _ = ctx.Reply(u, ext.ReplyTextString("✅ 密码已提交"), nil)
		log.Println("管理员提交了两步验证密码")
	default:
		_, _ = ctx.Reply(u, ext.ReplyTextString("密码 channel 已满或当前不需要密码"), nil)
	}

	return dispatcher.EndGroups
}

func validPhone(p string) bool {
	if p == "" {
		return false
	}
	// 简单校验：可选的+开头，后续为8-20位数字
	if p[0] == '+' {
		p = p[1:]
	}
	if len(p) < 8 || len(p) > 20 {
		return false
	}
	for _, ch := range p {
		if ch < '0' || ch > '9' {
			return false
		}
	}
	return true
}

func maskPhone(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	runes := []rune(p)
	if len(runes) <= 4 {
		return "****"
	}
	return string(runes[:2]) + strings.Repeat("*", len(runes)-4) + string(runes[len(runes)-2:])
}

// maskSecret 用于对敏感字符串做脱敏显示（保留前后各2位）
func maskSecret(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) <= 6 {
		return strings.Repeat("*", len(r))
	}
	return string(r[:2]) + strings.Repeat("*", len(r)-4) + string(r[len(r)-2:])
}

// 恢复 /start 处理器
func handleStart(ctx *ext.Context, u *ext.Update) error {
	chatId := u.EffectiveChat().GetID()
	peerChatId := ctx.PeerStorage.GetPeerById(chatId)

	if peerChatId.Type != int(storage.TypeUser) {
		return dispatcher.EndGroups
	}

	// 白名单拦截 (包含管理员)
	if !whitelist.IsAllowed(chatId) {
		_, _ = ctx.Reply(u, ext.ReplyTextString("您不在白名单中，无法使用该机器人。"), nil)
		return dispatcher.EndGroups
	}

	_, err := ctx.Reply(u, ext.ReplyTextString("您好，发送任意文件即可获取该文件的直链。"), nil)
	if err != nil {
		log.Printf("发送欢迎消息给用户 %d 失败: %v", chatId, err)
	}
	return dispatcher.EndGroups
}

// 恢复媒体过滤函数
func supportedMediaFilter(m *types.Message) (bool, error) {
	if m.Media == nil {
		return false, dispatcher.EndGroups
	}
	switch m.Media.(type) {
	case *tg.MessageMediaDocument, *tg.MessageMediaPhoto:
		return true, nil
	default:
		return false, dispatcher.EndGroups
	}
}

// 恢复通用消息处理
func handleMessage(ctx *ext.Context, u *ext.Update) error {
	chatId := u.EffectiveChat().GetID()
	peerChatId := ctx.PeerStorage.GetPeerById(chatId)

	if peerChatId.Type != int(storage.TypeUser) {
		return dispatcher.EndGroups
	}

	// 白名单拦截 (包含管理员)
	if !whitelist.IsAllowed(chatId) {
		_, _ = ctx.Reply(u, ext.ReplyTextString("您不在白名单中，无法使用该机器人。"), nil)
		return dispatcher.EndGroups
	}

	// 检查是否是 Telegram 链接
	messageText := u.EffectiveMessage.Text
	if messageText != "" {
		switch {
		case strings.Contains(messageText, "t.me/c/"):
			channelID, messageID, err := parseTelegramLink(messageText)
			if err == nil {
				// 处理 t.me/c/<id>/<msg>
				return handleTelegramLink(ctx, u, channelID, messageID)
			}
		default:
			// 尝试解析 t.me/<username>/<msg>
			if username, mid, err := parseUsernameLink(messageText); err == nil {
				return handleTelegramUsernameLink(ctx, u, username, mid)
			}
		}
	}

	supported, err := supportedMediaFilter(u.EffectiveMessage)
	if err != nil {
		return err
	}
	if !supported {
		return dispatcher.EndGroups
	}

	// 转发消息到日志频道
	update, err := forwardMessage(ctx, chatId, u.EffectiveMessage.ID)
	if err != nil {
		_, err := ctx.Reply(u, ext.ReplyTextString(fmt.Sprintf("错误: %s", err.Error())), nil)
		if err != nil {
			log.Printf("发送错误消息给用户 %d 失败: %v", chatId, err)
		}
		return dispatcher.EndGroups
	}

	messageID := update.Updates[0].(*tg.UpdateMessageID).ID
	doc := update.Updates[1].(*tg.UpdateNewChannelMessage).Message.(*tg.Message).Media

	file, err := fileFromMedia(doc)
	if err != nil {
		_, err := ctx.Reply(u, ext.ReplyTextString(fmt.Sprintf("错误: %s", err.Error())), nil)
		if err != nil {
			log.Printf("发送错误消息给用户 %d 失败: %v", chatId, err)
		}
		return dispatcher.EndGroups
	}

	fullHash := packFile(file.FileName, file.FileSize, file.MimeType, file.ID)
	hash := getShortHash(fullHash)
	link := fmt.Sprintf("%s/stream/%d?hash=%s", config.Host, messageID, hash)

	// 创建按钮
	row := tg.KeyboardButtonRow{
		Buttons: []tg.KeyboardButtonClass{
			&tg.KeyboardButtonURL{
				Text: "下载",
				URL:  link + "&d=true",
			},
			&tg.KeyboardButtonURL{
				Text: "在线",
				URL:  link,
			},
		},
	}

	markup := &tg.ReplyInlineMarkup{
		Rows: []tg.KeyboardButtonRow{row},
	}

	_, err = ctx.Reply(u, ext.ReplyTextString(fmt.Sprintf("直链: %s", link)), &ext.ReplyOpts{
		Markup:           markup,
		ReplyToMessageId: u.EffectiveMessage.ID,
	})
	if err != nil {
		log.Printf("发送直链消息给用户 %d 失败: %v", chatId, err)
	}

	// 向管理员（日志频道）发送通知
	if notifyErr := notifyAdminWithUserAndLink(ctx, chatId, link, fmt.Sprintf("来自用户文件直链 (messageID: %d)", messageID)); notifyErr != nil {
		log.Printf("发送管理员通知失败: %v", notifyErr)
	}

	return dispatcher.EndGroups
}

// ============================================================================
// 工具函数
// ============================================================================

func contains(slice []int64, item int64) bool {
	for _, v := range slice {
		if v == item {
			return true
		}
	}
	return false
}

// parseTelegramLink 解析 Telegram 链接，提取频道 ID 和消息 ID
// 支持格式: https://t.me/c/1683088671/36831
func parseTelegramLink(text string) (channelID int64, messageID int, err error) {
	// 查找 t.me/c/ 链接
	if !strings.Contains(text, "t.me/c/") {
		return 0, 0, errors.New("不是有效的 Telegram 频道链接")
	}

	// 提取链接部分
	parts := strings.Split(text, "t.me/c/")
	if len(parts) < 2 {
		return 0, 0, errors.New("链接格式错误")
	}

	// 分割频道 ID 和消息 ID
	pathParts := strings.Split(strings.TrimSpace(parts[1]), "/")
	if len(pathParts) < 2 {
		return 0, 0, errors.New("链接格式错误，缺少消息 ID")
	}

	// 解析频道 ID
	cID, err := strconv.ParseInt(pathParts[0], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("无效的频道 ID: %v", err)
	}

	// 解析消息 ID
	mID, err := strconv.Atoi(pathParts[1])
	if err != nil {
		return 0, 0, fmt.Errorf("无效的消息 ID: %v", err)
	}

	// 转换频道 ID（添加 -100 前缀）
	channelID = -1000000000000 - cID

	return channelID, mID, nil
}

// 解析 t.me/<username>/<messageID> 链接
func parseUsernameLink(text string) (username string, messageID int, err error) {
	var part string
	if strings.Contains(text, "t.me/") {
		parsedURL, err := handleUrl.Parse(text)
		if err != nil {
			return "", 0, fmt.Errorf("解析 %s 错误无效的 URL %+v", text, err)
		}
		part = strings.Trim(parsedURL.Path, "/")
	} else if strings.HasPrefix(text, "@") {
		part = text[1:]
	} else {
		return "", 0, errors.New("不是有效的用户名链接")
	}

	path := strings.Split(strings.TrimSpace(part), "/")
	if len(path) < 2 {
		return "", 0, errors.New("链接格式错误，缺少消息 ID")
	}

	if path[0] == "c" { // 这是 /c/ 链接，交给另一个解析
		return "", 0, errors.New("非用户名链接")
	}

	uname := strings.TrimSpace(path[0])
	if uname == "" {
		return "", 0, errors.New("用户名为空")
	}
	mid, err := strconv.Atoi(path[1])
	if err != nil {
		return "", 0, fmt.Errorf("无效的消息 ID: %v", err)
	}
	return uname, mid, nil
}

// 通过用户名解析频道并返回 InputChannel 以及内部频道ID（-100前缀形式）
func getChannelPeerByUsername(ctx context.Context, api *tg.Client, peerStorage *storage.PeerStorage, username string) (*tg.InputChannel, int64, error) {
	uname := strings.TrimPrefix(username, "@")
	res, err := api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: uname})
	if err != nil {
		return nil, 0, fmt.Errorf("解析用户名失败: %v", err)
	}

	// 从 Peer 中提取目标 ChannelID（如果 Peer 指向频道）
	var targetChannelID int64
	if pc, ok := res.Peer.(*tg.PeerChannel); ok {
		targetChannelID = pc.ChannelID
	}

	// 在返回的 Chats 中查找频道
	var ch *tg.Channel
	for _, chat := range res.GetChats() {
		if c, ok := chat.(*tg.Channel); ok {
			if strings.EqualFold(c.Username, uname) || (targetChannelID != 0 && c.GetID() == targetChannelID) {
				ch = c
				break
			}
		}
	}
	if ch == nil {
		return nil, 0, errors.New("未找到对应频道")
	}

	// 保存到缓存
	peerStorage.AddPeer(ch.GetID(), ch.AccessHash, storage.TypeChannel, "")
	input := ch.AsInput()

	// 计算 -100 前缀的内部频道ID（用于直链）
	internalID := int64(-1000000000000) - input.ChannelID
	return input, internalID, nil
}

// handleTelegramLink 处理从 Telegram /c 链接获取文件（不做转发）
func handleTelegramLink(ctx *ext.Context, u *ext.Update, channelID int64, messageID int) error {
	chatId := u.EffectiveChat().GetID()
	log.Printf("开始处理 Telegram 链接: 频道ID=%d, 消息ID=%d\n", channelID, messageID)

	message, err := getTGMessageFromChannel(ctx, Bot, channelID, messageID)
	if err != nil {
		_, _ = ctx.Reply(u, ext.ReplyTextString(fmt.Sprintf("❌ 获取消息失败: %s\n\n💡 提示：请将机器人加入该频道，或开启 User Bot 仅用于读取以提升兼容性。", err.Error())), nil)
		return dispatcher.EndGroups
	}
	if message.Media == nil {
		_, _ = ctx.Reply(u, ext.ReplyTextString("❌ 该消息不包含文件"), nil)
		return dispatcher.EndGroups
	}

	file, err := fileFromMedia(message.Media)
	if err != nil {
		_, _ = ctx.Reply(u, ext.ReplyTextString(fmt.Sprintf("❌ 提取文件失败: %s", err.Error())), nil)
		return dispatcher.EndGroups
	}

	fullHash := packFile(file.FileName, file.FileSize, file.MimeType, file.ID)
	hash := getShortHash(fullHash)
	link := fmt.Sprintf("%s/stream/%d_%d?hash=%s", config.Host, channelID, messageID, hash)

	row := tg.KeyboardButtonRow{Buttons: []tg.KeyboardButtonClass{
		&tg.KeyboardButtonURL{Text: "下载", URL: link + "&d=true"},
		&tg.KeyboardButtonURL{Text: "复制", URL: link},
	}}
	markup := &tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{row}}

	_, err = ctx.Reply(u, ext.ReplyTextString(fmt.Sprintf("直链: %s", link)), &ext.ReplyOpts{Markup: markup, ReplyToMessageId: u.EffectiveMessage.ID})
	if err != nil {
		log.Printf("发送直链消息给用户 %d 失败: %v", chatId, err)
	}

	// 通知管理员
	if notifyErr := notifyAdminWithUserAndLink(ctx, chatId, link, fmt.Sprintf("来自 /c 链接 (channelID: %d, messageID: %d)", channelID, messageID)); notifyErr != nil {
		log.Printf("发送管理员通知失败: %v", notifyErr)
	}
	return dispatcher.EndGroups
}

// 处理用户名链接
func handleTelegramUsernameLink(ctx *ext.Context, u *ext.Update, username string, messageID int) error {
	chatId := u.EffectiveChat().GetID()

	log.Printf("开始处理 Telegram 用户名链接: @%s/%d\n", username, messageID)

	message, internalID, err := getTGMessageFromUsername(ctx, Bot, username, messageID)
	if err != nil {
		_, _ = ctx.Reply(u, ext.ReplyTextString(fmt.Sprintf("❌ 获取消息失败: %s\n\n💡 提示：请将机器人加入该频道，或开启 User Bot 仅用于读取以提升兼容性。", err.Error())), nil)
		return dispatcher.EndGroups
	}
	if message.Media == nil {
		_, _ = ctx.Reply(u, ext.ReplyTextString("❌ 该消息不包含文件"), nil)
		return dispatcher.EndGroups
	}

	file, err := fileFromMedia(message.Media)
	if err != nil {
		_, _ = ctx.Reply(u, ext.ReplyTextString(fmt.Sprintf("❌ 提取文件失败: %s", err.Error())), nil)
		return dispatcher.EndGroups
	}

	fullHash := packFile(file.FileName, file.FileSize, file.MimeType, file.ID)
	hash := getShortHash(fullHash)
	link := fmt.Sprintf("%s/stream/%d_%d?hash=%s", config.Host, internalID, messageID, hash)

	row := tg.KeyboardButtonRow{Buttons: []tg.KeyboardButtonClass{
		&tg.KeyboardButtonURL{Text: "下载", URL: link + "&d=true"},
		&tg.KeyboardButtonURL{Text: "复制", URL: link},
	}}

	markup := &tg.ReplyInlineMarkup{Rows: []tg.KeyboardButtonRow{row}}

	_, err = ctx.Reply(u, ext.ReplyTextString(fmt.Sprintf("直链: %s", link)), &ext.ReplyOpts{Markup: markup, ReplyToMessageId: u.EffectiveMessage.ID})
	if err != nil {
		log.Printf("发送直链消息给用户 %d 失败: %v", chatId, err)
	}

	// 通知管理员
	if notifyErr := notifyAdminWithUserAndLink(ctx, chatId, link, fmt.Sprintf("来自 @%s/%d 链接", username, messageID)); notifyErr != nil {
		log.Printf("发送管理员通知失败: %v", notifyErr)
	}
	return dispatcher.EndGroups
}

// 从用户名定位的频道获取消息
func getTGMessageFromUsername(ctx context.Context, client *gotgproto.Client, username string, messageID int) (*tg.Message, int64, error) {
	// 如果启用了 User Bot，优先使用 User Bot 获取消息
	var useClient *gotgproto.Client
	if config.PhoneNumber != "" && UserBot != nil {
		useClient = UserBot
		log.Printf("使用 User Bot 获取消息 (username)\n")
	} else {
		useClient = client
		log.Printf("使用 Bot 获取消息 (username)\n")
	}

	channel, internalID, err := getChannelPeerByUsername(ctx, useClient.API(), useClient.PeerStorage, username)
	if err != nil {
		return nil, 0, err
	}

	inputMessageID := tg.InputMessageClass(&tg.InputMessageID{ID: messageID})
	msgReq := tg.ChannelsGetMessagesRequest{Channel: channel, ID: []tg.InputMessageClass{inputMessageID}}
	res, err := useClient.API().ChannelsGetMessages(ctx, &msgReq)
	if err != nil {
		return nil, 0, err
	}

	messages := res.(*tg.MessagesChannelMessages)
	if len(messages.Messages) == 0 {
		return nil, 0, fmt.Errorf("消息未找到")
	}
	msg, ok := messages.Messages[0].(*tg.Message)
	if !ok {
		return nil, 0, fmt.Errorf("该文件已被删除")
	}
	return msg, internalID, nil
}

// Telegram 辅助函数
func getTGMessage(ctx context.Context, client *gotgproto.Client, messageID int) (*tg.Message, error) {
	inputMessageID := tg.InputMessageClass(&tg.InputMessageID{ID: messageID})
	channel, err := getLogChannelPeer(ctx, client.API(), client.PeerStorage)
	if err != nil {
		return nil, err
	}

	messageRequest := tg.ChannelsGetMessagesRequest{
		Channel: channel,
		ID:      []tg.InputMessageClass{inputMessageID},
	}
	res, err := client.API().ChannelsGetMessages(ctx, &messageRequest)
	if err != nil {
		return nil, err
	}

	messages := res.(*tg.MessagesChannelMessages)
	if len(messages.Messages) == 0 {
		return nil, fmt.Errorf("消息未找到")
	}

	message, ok := messages.Messages[0].(*tg.Message)
	if !ok {
		return nil, fmt.Errorf("该文件已被删除")
	}
	return message, nil
}

// 从指定频道获取消息
func getTGMessageFromChannel(ctx context.Context, client *gotgproto.Client, channelID int64, messageID int) (*tg.Message, error) {
	inputMessageID := tg.InputMessageClass(&tg.InputMessageID{ID: messageID})

	// 如果启用了 User Bot，优先使用 User Bot 获取消息
	var useClient *gotgproto.Client
	if config.PhoneNumber != "" && UserBot != nil {
		useClient = UserBot
		log.Printf("使用 User Bot 获取消息\n")
	} else {
		useClient = client
		log.Printf("使用 Bot 获取消息\n")
	}

	// 获取频道的 InputChannel
	channel, err := getChannelPeer(ctx, useClient.API(), useClient.PeerStorage, channelID)
	if err != nil {
		return nil, err
	}

	messageRequest := tg.ChannelsGetMessagesRequest{
		Channel: channel,
		ID:      []tg.InputMessageClass{inputMessageID},
	}
	res, err := useClient.API().ChannelsGetMessages(ctx, &messageRequest)
	if err != nil {
		return nil, err
	}

	messages := res.(*tg.MessagesChannelMessages)
	if len(messages.Messages) == 0 {
		return nil, fmt.Errorf("消息未找到")
	}

	message, ok := messages.Messages[0].(*tg.Message)
	if !ok {
		return nil, fmt.Errorf("该文件已被删除")
	}
	return message, nil
}

// 获取指定频道的 InputChannel
func getChannelPeer(ctx context.Context, api *tg.Client, peerStorage *storage.PeerStorage, channelID int64) (*tg.InputChannel, error) {
	// 先尝试从缓存中获取
	cachedInputPeer := peerStorage.GetInputPeerById(channelID)

	switch peer := cachedInputPeer.(type) {
	case *tg.InputPeerChannel:
		return &tg.InputChannel{
			ChannelID:  peer.ChannelID,
			AccessHash: peer.AccessHash,
		}, nil
	case *tg.InputPeerEmpty:
		// 继续调用 API 获取
	default:
		return nil, errors.New("unexpected type of input peer")
	}

	// 移除 -100 前缀（如果存在）
	actualChannelID := channelID
	if actualChannelID < -1000000000000 {
		actualChannelID = actualChannelID + 1000000000000
		actualChannelID = -actualChannelID
	} else if actualChannelID < 0 {
		actualChannelID = -actualChannelID
	}

	inputChannel := &tg.InputChannel{ChannelID: actualChannelID}
	log.Printf("尝试访问频道 ID: %d (原始: %d)\n", actualChannelID, channelID)

	channels, err := api.ChannelsGetChannels(ctx, []tg.InputChannelClass{inputChannel})
	if err != nil {
		log.Printf("获取频道失败: %v\n", err)
		return nil, fmt.Errorf("获取频道失败（确保机器人已加入该频道）：%v", err)
	}
	if len(channels.GetChats()) == 0 {
		return nil, errors.New("未找到频道 - 请确保机器人已加入该频道")
	}

	channel, ok := channels.GetChats()[0].(*tg.Channel)
	if !ok {
		return nil, errors.New("类型断言失败，无法转换为 *tg.Channel")
	}

	peerStorage.AddPeer(channel.GetID(), channel.AccessHash, storage.TypeChannel, "")
	log.Printf("成功访问频道: %s (ID: %d)\n", channel.Title, channel.ID)
	return channel.AsInput(), nil
}

// 将用户消息转发到日志频道（与 User Bot 无关）
func forwardMessage(ctx *ext.Context, fromChatId int64, messageID int) (*tg.Updates, error) {
	fromPeer := ctx.PeerStorage.GetInputPeerById(fromChatId)
	if fromPeer.Zero() {
		return nil, fmt.Errorf("fromChatId: %d 不是有效的对等体", fromChatId)
	}

	toPeer, err := getLogChannelPeer(ctx, ctx.Raw, ctx.PeerStorage)
	if err != nil {
		return nil, err
	}

	update, err := ctx.Raw.MessagesForwardMessages(ctx, &tg.MessagesForwardMessagesRequest{
		RandomID: []int64{time.Now().UnixNano()},
		FromPeer: fromPeer,
		ID:       []int{messageID},
		ToPeer:   &tg.InputPeerChannel{ChannelID: toPeer.ChannelID, AccessHash: toPeer.AccessHash},
	})
	if err != nil {
		return nil, err
	}
	return update.(*tg.Updates), nil
}

// 从消息媒体提取文件
func fileFromMedia(media tg.MessageMediaClass) (*File, error) {
	switch media := media.(type) {
	case *tg.MessageMediaDocument:
		document, ok := media.Document.AsNotEmpty()
		if !ok {
			return nil, fmt.Errorf("unexpected type %T", media)
		}

		var fileName string
		for _, attribute := range document.Attributes {
			if name, ok := attribute.(*tg.DocumentAttributeFilename); ok {
				fileName = name.FileName
				break
			}
		}

		return &File{
			Location: document.AsInputDocumentFileLocation(),
			FileSize: document.Size,
			FileName: fileName,
			MimeType: document.MimeType,
			ID:       document.ID,
		}, nil

	case *tg.MessageMediaPhoto:
		photo, ok := media.Photo.AsNotEmpty()
		if !ok {
			return nil, fmt.Errorf("unexpected type %T", media)
		}

		sizes := photo.Sizes
		if len(sizes) == 0 {
			return nil, errors.New("照片没有尺寸信息")
		}

		photoSize := sizes[len(sizes)-1]
		size, ok := photoSize.AsNotEmpty()
		if !ok {
			return nil, errors.New("照片尺寸信息为空")
		}

		location := &tg.InputPhotoFileLocation{
			ID:            photo.GetID(),
			AccessHash:    photo.GetAccessHash(),
			FileReference: photo.GetFileReference(),
			ThumbSize:     size.GetType(),
		}

		return &File{
			Location: location,
			FileSize: 0,
			FileName: fmt.Sprintf("photo_%d.jpg", photo.GetID()),
			MimeType: "image/jpeg",
			ID:       photo.GetID(),
		}, nil
	}
	return nil, fmt.Errorf("unexpected type %T", media)
}

// 从日志频道消息ID获取文件（带缓存）
func fileFromMessage(ctx context.Context, client *gotgproto.Client, messageID int) (*File, error) {
	key := fmt.Sprintf("file:%d:%d", messageID, client.Self.ID)
	var cachedMedia File
	err := cache.Get(key, &cachedMedia)
	if err == nil {
		return &cachedMedia, nil
	}

	message, err := getTGMessage(ctx, client, messageID)
	if err != nil {
		return nil, err
	}

	file, err := fileFromMedia(message.Media)
	if err != nil {
		return nil, err
	}

	err = cache.Set(key, file, 3600)
	if err != nil {
		log.Printf("缓存消息 %d 的文件失败: %v", messageID, err)
	}
	return file, nil
}

// 获取日志频道的 InputChannel（用于 Bot 作为日志存储）
func getLogChannelPeer(ctx context.Context, api *tg.Client, peerStorage *storage.PeerStorage) (*tg.InputChannel, error) {
	cachedInputPeer := peerStorage.GetInputPeerById(config.LogChannelID)

	switch peer := cachedInputPeer.(type) {
	case *tg.InputPeerChannel:
		return &tg.InputChannel{ChannelID: peer.ChannelID, AccessHash: peer.AccessHash}, nil
	case *tg.InputPeerEmpty:
		// 继续调用 API 获取
	default:
		return nil, errors.New("unexpected type of input peer")
	}

	// 移除 -100 前缀（如果存在）
	channelID := config.LogChannelID
	if channelID < -1000000000000 {
		channelID = channelID + 1000000000000
		channelID = -channelID
	} else if channelID < 0 {
		channelID = -channelID
	}

	inputChannel := &tg.InputChannel{ChannelID: channelID}
	log.Printf("尝试访问频道 ID: %d (原始: %d)\n", channelID, config.LogChannelID)

	channels, err := api.ChannelsGetChannels(ctx, []tg.InputChannelClass{inputChannel})
	if err != nil {
		log.Printf("获取频道失败: %v\n", err)
		return nil, fmt.Errorf("获取频道失败（确保机器人已作为管理员添加）：%v", err)
	}
	if len(channels.GetChats()) == 0 {
		return nil, errors.New("未找到频道 - 请将机器人添加为频道管理员")
	}

	channel, ok := channels.GetChats()[0].(*tg.Channel)
	if !ok {
		return nil, errors.New("类型断言失败，无法转换为 *tg.Channel")
	}

	peerStorage.AddPeer(channel.GetID(), channel.AccessHash, storage.TypeChannel, "")
	log.Printf("成功访问频道: %s (ID: %d)\n", channel.Title, channel.ID)
	return channel.AsInput(), nil
}

// 获取用户信息（用户名与显示名）
func getUserInfo(ctx context.Context, api *tg.Client, peerStorage *storage.PeerStorage, userID int64) (username string, displayName string) {
	ip := peerStorage.GetInputPeerById(userID)
	switch p := ip.(type) {
	case *tg.InputPeerUser:
		inUser := &tg.InputUser{UserID: p.UserID, AccessHash: p.AccessHash}
		uf, err := api.UsersGetFullUser(ctx, inUser)
		if err != nil || uf == nil {
			return "", ""
		}
		var u *tg.User
		for _, usr := range uf.Users {
			if tu, ok := usr.(*tg.User); ok && tu.GetID() == p.UserID {
				u = tu
				break
			}
		}
		if u != nil {
			uname := strings.TrimSpace(u.Username)
			name := strings.TrimSpace(strings.TrimSpace(u.FirstName + " " + u.LastName))
			if uname != "" {
				username = "@" + uname
			}
			displayName = name
		}
	case *tg.InputPeerSelf:
		uf, err := api.UsersGetFullUser(ctx, &tg.InputUserSelf{})
		if err != nil || uf == nil {
			return "", ""
		}
		for _, usr := range uf.Users {
			if tu, ok := usr.(*tg.User); ok {
				uname := strings.TrimSpace(tu.Username)
				name := strings.TrimSpace(strings.TrimSpace(tu.FirstName + " " + tu.LastName))
				if uname != "" {
					username = "@" + uname
				}
				displayName = name
				break
			}
		}
	default:
		// 不支持的类型，返回空
	}
	return
}

// 向日志频道发送管理员通知
func notifyAdminWithUserAndLink(ctx *ext.Context, userID int64, link string, note string) error {
	// 获取用户信息
	uname, name := getUserInfo(ctx, ctx.Raw, ctx.PeerStorage, userID)
	userLine := fmt.Sprintf("UserID: %d", userID)
	if uname != "" {
		userLine += fmt.Sprintf(" (username: %s)", uname)
	}
	if name != "" {
		userLine += fmt.Sprintf(", name: %s", name)
	}

	text := fmt.Sprintf("%s\n%s\n直链: %s", userLine, note, link)

	params := map[string]any{
		"chat_id": config.TeleID,
		"text":    text,
	}
	body, err := json.Marshal(params)
	if err != nil {
		return err
	}
	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", config.BotToken)
	req, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("向机器人 %d 发送通知失败: %v", config.TeleID, err)
		return err
	}

	defer func() {
		err := resp.Body.Close()
		if err != nil {
			log.Printf("关闭响应体失败: %v", err)
		}
	}()

	return nil
}

// ====== 辅助函数：hash 与时间格式 ======
func packFile(fileName string, fileSize int64, mimeType string, fileID int64) string {
	return (&HashableFileStruct{FileName: fileName, FileSize: fileSize, MimeType: mimeType, FileID: fileID}).Pack()
}

func getShortHash(fullHash string) string {
	if len(fullHash) < config.HashLength {
		return fullHash
	}
	return fullHash[:config.HashLength]
}

func checkHash(inputHash string, expectedHash string) bool {
	return inputHash == getShortHash(expectedHash)
}

func timeFormat(seconds uint64) string {
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

// ============================================================================
// HTTP 路由（基于 net/http）
// ============================================================================

// getClientIP 获取真实的客户端 IP
func getClientIP(r *http.Request) string {
	ip := r.Header.Get("X-Real-IP")
	if ip == "" {
		ip = r.Header.Get("X-Forwarded-For")
	}
	if ip == "" {
		ip = r.RemoteAddr
	}
	// X-Forwarded-For 可能是逗号分隔的多个 IP，取第一个
	if idx := strings.Index(ip, ","); idx != -1 {
		ip = strings.TrimSpace(ip[:idx])
	}
	// 去除可能包含的端口号 (如 127.0.0.1:12345)
	if strings.Contains(ip, ":") {
		// 考虑到 IPv6 含有冒号，使用 net.SplitHostPort 更加健壮
		if host, _, err := net.SplitHostPort(ip); err == nil {
			ip = host
		}
	}
	return ip
}

func setupRouter() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		resp := RootResponse{
			Message: "服务器正在运行。",
			Ok:      true,
			Uptime:  timeFormat(uint64(time.Since(startTime).Seconds())),
			Version: version,
		}
		_ = json.NewEncoder(w).Encode(resp)
	})
	// link 路由
	mux.HandleFunc("/link", handleTGLink)

	// stream 路由: 形如 /stream/{messageID 或 channelID_messageID}
	mux.HandleFunc("/stream/", handleStream)

	return mux
}

func handleTGLink(w http.ResponseWriter, r *http.Request) {
	params := r.URL.Query()
	password := params.Get("key")
	if password != config.Password {
		http.Error(w, "无效的密码", http.StatusUnauthorized)
		return
	}

	// 获取访问者 IP
	clientIP := getClientIP(r)

	targetLink := params.Get("link")

	var streamPath string // 形如 channelID_messageID 或 messageID
	var streamHash string

	switch {
	case strings.Contains(targetLink, "/c/"):
		channelID, messageID, err := parseTelegramLink(targetLink)
		if err != nil {
			http.Error(w, fmt.Sprintf("解析链接失败: %v", err), http.StatusBadRequest)
			return
		}
		message, err := getTGMessageFromChannel(r.Context(), Bot, channelID, messageID)
		if err != nil {
			http.Error(w, fmt.Sprintf("获取消息失败: %v", err), http.StatusBadRequest)
			return
		}
		if message.Media == nil {
			http.Error(w, "该消息不包含文件", http.StatusBadRequest)
			return
		}
		file, err := fileFromMedia(message.Media)
		if err != nil {
			http.Error(w, fmt.Sprintf("提取文件失败: %v", err), http.StatusBadRequest)
			return
		}
		fullHash := packFile(file.FileName, file.FileSize, file.MimeType, file.ID)
		streamHash = getShortHash(fullHash)
		streamPath = fmt.Sprintf("%d_%d", channelID, messageID)

	default:
		username, mid, err := parseUsernameLink(targetLink)
		if err != nil {
			http.Error(w, fmt.Sprintf("解析链接失败: %v", err), http.StatusBadRequest)
			return
		}
		message, internalID, err := getTGMessageFromUsername(r.Context(), Bot, username, mid)
		if err != nil {
			http.Error(w, fmt.Sprintf("获取消息失败: %v", err), http.StatusBadRequest)
			return
		}
		if message.Media == nil {
			http.Error(w, "该消息不包含文件", http.StatusBadRequest)
			return
		}
		file, err := fileFromMedia(message.Media)
		if err != nil {
			http.Error(w, fmt.Sprintf("提取文件失败: %v", err), http.StatusBadRequest)
			return
		}
		fullHash := packFile(file.FileName, file.FileSize, file.MimeType, file.ID)
		streamHash = getShortHash(fullHash)
		streamPath = fmt.Sprintf("%d_%d", internalID, mid)
	}

	// 构造最终直链
	finalLink := fmt.Sprintf("%s/stream/%s?hash=%s", config.Host, streamPath, streamHash)

	// 异步通知机器人（1 分钟内相同内容只通知一次）
	go func() {
		notifyText := fmt.Sprintf("已处理来自 %s 的转发 %s 链接请求，直链: %s", clientIP, targetLink, finalLink)
		if isDuplicateNotify(notifyText) {
			log.Printf("跳过重复通知（1 分钟内已发送）: %s", notifyText)
			return
		}
		notifyParams := map[string]any{
			"chat_id": config.TeleID,
			"text":    notifyText,
		}
		body, err := json.Marshal(notifyParams)
		if err != nil {
			log.Printf("序列化通知消息失败: %v", err)
			return
		}
		url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", config.BotToken)
		req, err := http.NewRequest("POST", url, bytes.NewBuffer(body))
		if err != nil {
			log.Printf("创建通知请求失败: %v", err)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		client := &http.Client{Timeout: 10 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			log.Printf("发送转发链接通知失败: %v", err)
			return
		}
		defer func() {
			_ = resp.Body.Close()
		}()
	}()

	// 302 跳转到直链
	http.Redirect(w, r, finalLink, http.StatusFound)
}

func handleStream(w http.ResponseWriter, r *http.Request) {
	// 解析 messageID 路径部分
	path := strings.TrimPrefix(r.URL.Path, "/stream/")
	if path == "" || strings.Contains(path, "/") {
		http.Error(w, "无效的路径", http.StatusNotFound)
		return
	}

	messageIDParam := path

	// 检查是否包含频道ID (格式: channelID_messageID)
	var channelID int64
	var messageID int
	var err error

	if strings.Contains(messageIDParam, "_") {
		// 新格式: channelID_messageID，直接从原频道读取
		parts := strings.Split(messageIDParam, "_")
		if len(parts) != 2 {
			http.Error(w, "无效的消息ID格式", http.StatusBadRequest)
			return
		}

		channelID, err = strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			http.Error(w, "无效的频道ID", http.StatusBadRequest)
			return
		}

		messageID, err = strconv.Atoi(parts[1])
		if err != nil {
			http.Error(w, "无效的消息ID", http.StatusBadRequest)
			return
		}

		log.Printf("从原频道读取文件: 频道ID=%d, 消息ID=%d\n", channelID, messageID)
	} else {
		// 旧格式: 仅消息ID，从日志频道读取
		messageID, err = strconv.Atoi(messageIDParam)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	authHash := r.URL.Query().Get("hash")
	if authHash == "" {
		http.Error(w, "缺少 hash 参数", http.StatusBadRequest)
		return
	}

	var file *File

	if channelID != 0 {
		// 从原频道获取文件
		message, err := getTGMessageFromChannel(r.Context(), Bot, channelID, messageID)
		if err != nil {
			http.Error(w, fmt.Sprintf("获取消息失败: %v", err), http.StatusBadRequest)
			return
		}

		file, err = fileFromMedia(message.Media)
		if err != nil {
			http.Error(w, fmt.Sprintf("提取文件失败: %v", err), http.StatusBadRequest)
			return
		}
	} else {
		// 从日志频道获取文件（兼容旧逻辑）
		file, err = fileFromMessage(r.Context(), Bot, messageID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
	}

	expectedHash := packFile(file.FileName, file.FileSize, file.MimeType, file.ID)
	if !checkHash(authHash, expectedHash) {
		http.Error(w, "无效的 hash", http.StatusBadRequest)
		return
	}

	// 处理照片
	if file.FileSize == 0 {
		res, err := Bot.API().UploadGetFile(r.Context(), &tg.UploadGetFileRequest{
			Location: file.Location,
			Offset:   0,
			Limit:    1024 * 1024,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		result, ok := res.(*tg.UploadFile)
		if !ok {
			http.Error(w, "意外的响应", http.StatusInternalServerError)
			return
		}
		fileBytes := result.GetBytes()
		w.Header().Set("Content-Disposition", fmt.Sprintf("inline; filename=\"%s\"", file.FileName))
		if r.Method != http.MethodHead {
			w.Header().Set("Content-Type", file.MimeType)
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(fileBytes)
		}
		return
	}

	w.Header().Set("Accept-Ranges", "bytes")
	var start, end int64
	rangeHeader := r.Header.Get("Range")

	if rangeHeader == "" {
		start = 0
		end = file.FileSize - 1
		w.WriteHeader(http.StatusOK)
	} else {
		ranges, err := rangeParser.Parse(file.FileSize, rangeHeader)
		if err != nil || len(ranges) == 0 {
			http.Error(w, "无效的 Range", http.StatusBadRequest)
			return
		}
		start = ranges[0].Start
		end = ranges[0].End
		w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, file.FileSize))
		w.WriteHeader(http.StatusPartialContent)
	}

	contentLength := end - start + 1
	mimeType := file.MimeType
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}

	w.Header().Set("Content-Type", mimeType)
	w.Header().Set("Content-Length", strconv.FormatInt(contentLength, 10))

	disposition := "inline"
	if r.URL.Query().Get("d") == "true" {
		disposition = "attachment"
	}
	w.Header().Set("Content-Disposition", fmt.Sprintf("%s; filename=\"%s\"", disposition, file.FileName))

	if r.Method != http.MethodHead {
		var reader io.ReadCloser
		if channelID != 0 {
			// 从原频道读取，优先使用 User Bot 下载（其拥有源频道访问权限），并支持自动刷新文件引用
			readerClient := Bot
			if config.PhoneNumber != "" && UserBot != nil {
				readerClient = UserBot
			}
			reader = newTelegramReaderWithRefresh(r.Context(), readerClient, file.Location, start, end, contentLength, channelID, messageID)
		} else {
			// 从日志频道读取，使用 Bot 即可
			reader = newTelegramReader(r.Context(), Bot, file.Location, start, end, contentLength)
		}
		defer func() {
			err := reader.Close()
			if err != nil {
				log.Printf("关闭 telegram reader 失败: %v", err)
			}
		}()
		_, err := io.CopyN(w, reader, contentLength)
		if err != nil && err != io.EOF {
			log.Printf("流式传输文件时出错: %v", err)
		}
	}
}

// ============================================================================
// 黑名单（持久化）
// ============================================================================

type Whitelist struct {
	mu   sync.RWMutex
	set  map[int64]struct{}
	file string
}

func NewWhitelist(file string) *Whitelist {
	return &Whitelist{set: make(map[int64]struct{}), file: file}
}

func (b *Whitelist) Load() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	data, err := os.ReadFile(b.file)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var ids []int64
	if err := json.Unmarshal(data, &ids); err != nil {
		return err
	}
	for _, id := range ids {
		b.set[id] = struct{}{}
	}
	return nil
}

func (b *Whitelist) Save() error {
	b.mu.RLock()
	defer b.mu.RUnlock()
	ids := make([]int64, 0, len(b.set))
	for id := range b.set {
		ids = append(ids, id)
	}
	data, err := json.MarshalIndent(ids, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(b.file, data, 0644)
}

func (b *Whitelist) IsAllowed(id int64) bool {
	b.mu.RLock()
	defer b.mu.RUnlock()
	// 管理员自动放行
	if isAdmin(id) {
		return true
	}
	_, ok := b.set[id]
	return ok
}

func (b *Whitelist) Add(id int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, existed := b.set[id]
	b.set[id] = struct{}{}
	return !existed
}

func (b *Whitelist) Remove(id int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	_, existed := b.set[id]
	delete(b.set, id)
	return existed
}

var whitelist = NewWhitelist("files/whitelist.json")

// ============================================================================
// 加密存储：User Bot 手机号
// ============================================================================

const phoneFile = "files/phone.enc"

func derivePhoneKey() ([]byte, error) {
	// 由 API_HASH + BOT_TOKEN + TELE_ID 派生密钥（32字节）
	if config == nil {
		return nil, errors.New("配置未初始化")
	}
	data := fmt.Sprintf("%s:%s:%d", strings.TrimSpace(config.ApiHash), strings.TrimSpace(config.BotToken), config.TeleID)
	sum := sha256.Sum256([]byte(data))
	return sum[:], nil
}

func savePhoneEncrypted(phone string) error {
	key, err := derivePhoneKey()
	if err != nil {
		return err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return err
	}
	ciphertext := gcm.Seal(nil, nonce, []byte(phone), nil)
	// 文件格式: 4字节魔数 + 1字节版本 + nonce + ciphertext
	buf := bytes.NewBuffer(nil)
	buf.Write([]byte{'P', 'H', 'O', 'N'})
	buf.WriteByte(1)
	buf.Write(nonce)
	buf.Write(ciphertext)
	return os.WriteFile(phoneFile, buf.Bytes(), 0600)
}

func loadPhoneEncrypted() (string, error) {
	data, err := os.ReadFile(phoneFile)
	if err != nil {
		return "", err
	}
	if len(data) < 5 {
		return "", errors.New("phone.enc 文件损坏")
	}
	if string(data[:4]) != "PHON" {
		return "", errors.New("phone.enc 魔数不匹配")
	}
	ver := data[4]
	if ver != 1 {
		return "", fmt.Errorf("不支持的加密版本: %d", ver)
	}
	key, err := derivePhoneKey()
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(data) < 5+gcm.NonceSize() {
		return "", errors.New("phone.enc 文件长度错误")
	}
	nonce := data[5 : 5+gcm.NonceSize()]
	ciphertext := data[5+gcm.NonceSize():]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// ============================================================================
// 配置加载
// ============================================================================

func loadConfig() error {
	// 使用 defer 在函数结束时输出配置（仅在成功时）
	success := false
	defer func() {
		if success && config != nil {
			log.Println("配置详情:")
			log.Printf("  Host: %s", config.Host)
			log.Printf("  Port: %d", config.Port)
			log.Printf("  ApiID: %d", config.ApiID)
			log.Printf("  ApiHash: %s", maskSecret(config.ApiHash))
			log.Printf("  BotToken: %s", maskSecret(config.BotToken))
			log.Printf("  LogChannelID: %d", config.LogChannelID)
			log.Printf("  TeleID: %d", config.TeleID)
			log.Printf("  HashLength: %d", config.HashLength)
			if len(config.AdminUsers) > 0 {
				log.Printf("  AdminUsers: %v", config.AdminUsers)
			}
			if strings.TrimSpace(config.PhoneNumber) != "" {
				log.Printf("  PhoneNumber: %s", maskPhone(config.PhoneNumber))
			}
		}
	}()

	// 尝试加载 .env 文件
	err := godotenv.Load("files/.env")
	if err != nil {
		log.Println("未找到 .env 文件，继续使用环境变量")
	}

	config = &Config{
		HashLength: 6,
		Port:       9981,
	}

	//TELE_ID
	if teleID := os.Getenv("TELE_ID"); teleID != "" {
		id, err := strconv.ParseInt(teleID, 10, 64)
		if err != nil {
			return fmt.Errorf("无效的 TELE_ID: %v", err)
		}
		config.TeleID = id
	} else {
		return errors.New("TELE_ID 是必需的")
	}

	// API_ID
	if apiID := os.Getenv("API_ID"); apiID != "" {
		id, err := strconv.ParseInt(apiID, 10, 32)
		if err != nil {
			return fmt.Errorf("无效的 API_ID: %v", err)
		}
		config.ApiID = int32(id)
	} else {
		return errors.New("API_ID 是必需的")
	}

	// API_HASH
	if apiHash := os.Getenv("API_HASH"); apiHash != "" {
		config.ApiHash = apiHash
	} else {
		return errors.New("API_HASH 是必需的")
	}

	// BOT_TOKEN
	if botToken := os.Getenv("BOT_TOKEN"); botToken != "" {
		config.BotToken = botToken
	} else {
		return errors.New("BOT_TOKEN 是必需的")
	}

	// LOG_CHANNEL
	if logChannel := os.Getenv("LOG_CHANNEL"); logChannel != "" {
		id, err := strconv.ParseInt(logChannel, 10, 64)
		if err != nil {
			return fmt.Errorf("无效的 LOG_CHANNEL: %v", err)
		}
		config.LogChannelID = id
	} else {
		return errors.New("LOG_CHANNEL 是必需的")
	}

	// PORT (可选)
	if port := os.Getenv("PORT"); port != "" {
		p, err := strconv.Atoi(port)
		if err == nil {
			config.Port = p
		}
	}

	// HOST (可选)
	if host := os.Getenv("HOST"); host != "" {
		config.Host = host
	} else {
		config.Host = fmt.Sprintf("http://localhost:%d", config.Port)
	}

	// HASH_LENGTH (可选)
	if hashLen := os.Getenv("HASH_LENGTH"); hashLen != "" {
		l, err := strconv.Atoi(hashLen)
		if err == nil && l > 0 {
			config.HashLength = l
		}
	}

	// ADMIN_USERS (可选)
	if adminUsers := os.Getenv("ADMIN_USERS"); adminUsers != "" {
		ids := strings.Split(adminUsers, ",")
		for _, id := range ids {
			userID, err := strconv.ParseInt(strings.TrimSpace(id), 10, 64)
			if err == nil {
				config.AdminUsers = append(config.AdminUsers, userID)
			}
		}
	}

	// Password (可选)
	if password := os.Getenv("PASSWORD"); password != "" {
		config.Password = password
	}

	success = true
	return nil
}

// ============================================================================
// 主函数
// ============================================================================

func main() {
	startTime = time.Now()

	log.Println("正在启动 Telegram 文件流机器人...")

	// 加载配置
	if err := loadConfig(); err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	log.Print("配置已加载\n")

	// 初始化缓存
	InitCache()

	// 加载白名单
	if err := whitelist.Load(); err != nil {
		log.Printf("加载白名单失败: %v", err)
	} else {
		log.Printf("白名单已加载，共 %d 个用户", len(whitelist.set))
	}

	// 启动 Telegram 客户端
	if err := StartClient(); err != nil {
		log.Fatalf("启动客户端失败: %v", err)
	}

	// 启动 User Bot 客户端（若未设置手机号则会被跳过）
	phone, err := loadPhoneEncrypted()
	if err != nil {
		if !os.IsNotExist(err) {
			log.Printf("获取 PhoneNumber 失败: %v\n", err)
		}
	}
	if phone != "" {
		config.PhoneNumber = strings.TrimSpace(phone)
		if err := StartUserBot(); err != nil {
			log.Fatalf("启动 User Bot 客户端失败: %v", err)
		}
	} else {
		log.Println("未设置 User Bot 手机号，跳过启动（可用 /phone 设置）")
	}

	// 设置 HTTP 路由
	handler := setupRouter()

	log.Printf("服务器正在 %s 运行\n", config.Host)
	log.Printf("监听端口 %d\n", config.Port)

	// 启动 HTTP 服务器
	if err := http.ListenAndServe(fmt.Sprintf(":%d", config.Port), handler); err != nil {
		log.Fatalf("启动服务器失败: %v", err)
	}
}

// ============================================================================
// Telegram Reader
// ============================================================================

type telegramReader struct {
	ctx           context.Context
	client        *gotgproto.Client
	location      tg.InputFileLocationClass
	start         int64
	end           int64
	next          func() ([]byte, error)
	buffer        []byte
	bytesread     int64
	chunkSize     int64
	pos           int64
	contentLength int64
	channelID     int64
	messageID     int
}

func (r *telegramReader) Close() error {
	return nil
}

func newTelegramReader(
	ctx context.Context,
	client *gotgproto.Client,
	location tg.InputFileLocationClass,
	start int64,
	end int64,
	contentLength int64,
) io.ReadCloser {
	r := &telegramReader{
		ctx:           ctx,
		client:        client,
		location:      location,
		start:         start,
		end:           end,
		chunkSize:     int64(1024 * 1024),
		contentLength: contentLength,
	}
	r.next = r.partStream()
	return r
}

func newTelegramReaderWithRefresh(
	ctx context.Context,
	client *gotgproto.Client,
	location tg.InputFileLocationClass,
	start int64,
	end int64,
	contentLength int64,
	channelID int64,
	messageID int,
) io.ReadCloser {
	r := &telegramReader{
		ctx:           ctx,
		client:        client,
		location:      location,
		start:         start,
		end:           end,
		chunkSize:     int64(1024 * 1024),
		contentLength: contentLength,
		channelID:     channelID,
		messageID:     messageID,
	}
	r.next = r.partStream()
	return r
}

func (r *telegramReader) Read(p []byte) (n int, err error) {
	if r.bytesread == r.contentLength {
		return 0, io.EOF
	}

	if r.pos >= int64(len(r.buffer)) {
		r.buffer, err = r.next()
		if err != nil {
			// If we have channel info, try to refresh the file reference
			if r.channelID != 0 && r.messageID != 0 {
				log.Printf("文件读取失败，尝试刷新文件引用...")
				message, refreshErr := getTGMessageFromChannel(r.ctx, r.client, r.channelID, r.messageID)
				if refreshErr == nil && message.Media != nil {
					file, fileErr := fileFromMedia(message.Media)
					if fileErr == nil {
						r.location = file.Location
						log.Printf("文件引用已刷新，重试读取...")
						r.buffer, err = r.next()
						if err != nil {
							return 0, err
						}
					}
				}
			}
			if err != nil {
				return 0, err
			}
		}
		if len(r.buffer) == 0 {
			r.next = r.partStream()
			r.buffer, err = r.next()
			if err != nil {
				return 0, err
			}
		}
		r.pos = 0
	}
	n = copy(p, r.buffer[r.pos:])
	r.pos += int64(n)
	r.bytesread += int64(n)
	return n, nil
}

func (r *telegramReader) chunk(offset int64, limit int64) ([]byte, error) {
	req := &tg.UploadGetFileRequest{
		Offset:   offset,
		Limit:    int(limit),
		Location: r.location,
	}

	res, err := r.client.API().UploadGetFile(r.ctx, req)
	if err != nil {
		return nil, err
	}

	switch result := res.(type) {
	case *tg.UploadFile:
		return result.Bytes, nil
	default:
		return nil, fmt.Errorf("unexpected type %T", result)
	}
}

func (r *telegramReader) partStream() func() ([]byte, error) {
	start := r.start
	end := r.end
	offset := start - (start % r.chunkSize)

	firstPartCut := start - offset
	lastPartCut := (end % r.chunkSize) + 1
	partCount := int((end - offset + r.chunkSize) / r.chunkSize)
	currentPart := 1

	readData := func() ([]byte, error) {
		if currentPart > partCount {
			return make([]byte, 0), nil
		}
		res, err := r.chunk(offset, r.chunkSize)
		if err != nil {
			return nil, err
		}
		if len(res) == 0 {
			return res, nil
		} else if partCount == 1 {
			res = res[firstPartCut:lastPartCut]
		} else if currentPart == 1 {
			res = res[firstPartCut:]
		} else if currentPart == partCount {
			res = res[:lastPartCut]
		}

		currentPart++
		offset += r.chunkSize
		return res, nil
	}
	return readData
}
