package main

import (
	"errors"
	"fmt"
	"log"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/amarnathcjd/gogram/telegram"
)

// startBot 创建并连接 Bot 客户端, 注册消息处理器并设置命令菜单
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

	go func() {
		// 先清空默认的命令列表, 确保没有权限的用户什么也看不到
		_, err := client.SetBotCommands([]*telegram.BotCommand{}, nil)
		if err != nil {
			log.Printf("清空默认命令失败: %+v", err)
		}

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
				Description: "输入验证码登录(需混入非数字字符)",
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

		_, err = client.SetBotCommands(commands, &userID)
		if err != nil {
			log.Printf("设置 Bot 超级管理员命令失败: %+v", err)
			return
		}

		for _, adminID := range infos.Conf.AdminIDs {
			if adminID == infos.Conf.UserID {
				continue
			}
			userID, err := client.ResolvePeer(adminID)
			if err != nil {
				log.Printf("解析用户 ID 失败: %v", err)
				continue
			}
			_, err = client.SetBotCommands(commonCommands, &userID)
			if err != nil {
				log.Printf("设置 Bot 管理员命令失败: %+v", err)
				continue
			}
		}
	}()

	log.Printf("Bot 启动成功")

	infos.Mutex.Lock()
	infos.BotClient = client
	infos.Mutex.Unlock()
	return nil
}

// userBotClient 创建并连接 UserBot 客户端（不执行登录, 仅建立连接）
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

	// 连接 UserBot
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
	switch infos.Status.Load() {
	case 1, 2:
		// 正在进行验证码或密码输入状态, 不允许重复发起
		infos.Mutex.Unlock()
		err = errors.New("已有登录流程正在进行")
		log.Printf("UserBot 登录失败: %+v", err)
		return err
	case 3:
		// 已登录状态, 若客户端实例丢失则尝试重建
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
		// 未登录状态, 开始新的登录流程
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

// startUserBotQR 发起二维码登录流程
func (infos *Infos) startUserBotQR() (err error) {
	infos.Mutex.Lock()
	switch infos.Status.Load() {
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
		infos.Status.Store(1)
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
			sendMS(nil, src, &telegram.SendOptions{Caption: "请使用手机 Telegram 扫描此二维码登录。二维码有效期 30 秒, 如失效请重新发送 /qr"}, 35)
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

// checkStatus 获取当前 UserBot 登录状态并校验 ID 是否合法
func (infos *Infos) checkStatus() (err error) {
	// 登录成功
	me, err := infos.UserClient.GetMe()
	if err != nil {
		log.Printf("获取用户信息失败: %v", err)
		infos.Mutex.Lock()
		infos.Status.Store(0)
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
		infos.Status.Store(3)
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

// resetStatus 断开 UserBot 连接并清理 session/cache, 将状态重置为未登录
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
	infos.Status.Store(0)
	infos.Mutex.Unlock()
}

// code 是登录回调, 暂停协程等待用户通过 Bot 发送验证码
func (infos *Infos) code() (code string, err error) {
	if infos.Status.Load() == 0 {
		infos.Mutex.Lock()
		infos.Status.Store(1)
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

// submitCode 接收用户通过 Bot 发送的验证码并写入通道
func (infos *Infos) submitCode(str string) (err error) {
	infos.Mutex.Lock()
	defer infos.Mutex.Unlock()

	if infos.Status.Load() != 1 {
		err = errors.New("当前状态不是等待验证码")
		sendMS(nil, err.Error(), nil, 60)
		return err
	}

	// 过滤非数字字符
	var sb strings.Builder
	for _, r := range str {
		if isDigit(r) {
			sb.WriteRune(r)
		}
	}

	code := sb.String()
	infos.Code <- code
	return nil
}

// pass 是登录回调, 暂停协程等待用户通过 Bot 发送 2FA 密码
func (infos *Infos) pass() (pass string, err error) {
	if infos.Status.Load() == 1 {
		infos.Mutex.Lock()
		infos.Status.Store(2)
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

// submitPass 接收用户通过 Bot 发送的 2FA 密码并写入通道
func (infos *Infos) submitPass(pass string) (err error) {
	infos.Mutex.Lock()
	defer infos.Mutex.Unlock()

	if infos.Status.Load() != 2 {
		err = errors.New("当前状态不是等待2FA密码")
		sendMS(nil, err.Error(), nil, 60)
		return err
	}
	infos.Pass <- pass
	return nil
}

// botConf 构造 Telegram 客户端所需的通用配置
func botConf(cate string) (conf telegram.ClientConfig) {
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
			matches := infos.Rex.FindStringSubmatch(err.Error())
			if len(matches) > 1 {
				for _, match := range matches {
					if value, err := strconv.Atoi(match); err == nil {
						wait = value
						break
					}
				}
			}
			log.Printf("访问太过频繁, 等待 %d 秒后重试", wait+1)
			waitUntil := time.Now().Add(time.Duration(wait+1) * time.Second)
			infos.WaitUntil.Store(waitUntil.Unix())
			time.Sleep(time.Duration(wait+1) * time.Second)
			return true
		},
	}
}
