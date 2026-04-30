package main

import (
	_ "embed"       // 用于内嵌默认配置模板
	"encoding/json" // 用于解析 JSON 配置文件
	"errors"        // 用于判断文件错误类型
	"io"            // 用于读取文件内容
	"log"           // 用于日志记录
	"os"            // 用于文件操作
	"path/filepath" // 用于处理文件路径
)

//go:embed files/config.json.example
var defaultConfigJSON []byte

// Conf 结构体定义了程序运行所需的各项配置参数
// 通过 json 标签与 config.json 文件进行映射
type Conf struct {
	Site      string   `json:"site"`                // 反代域名, 用于生成公开访问链接
	AppHash   string   `json:"hash"`                // Telegram API Hash, 从 my.telegram.org 获取
	BotToken  string   `json:"botToken"`            // Telegram Bot Token, 用于交互和管理
	Password  string   `json:"password,omitempty"`  // 访问 /link 接口时可选的身份验证密码
	Channels  []string `json:"channels,omitempty"`  // 频道列表, 用于搜索
	DC        int      `json:"dc,omitempty"`        // 指定连接的 Telegram 数据中心 (Data Center) ID
	Port      int      `json:"port"`                // 本地 HTTP 服务监听的端口
	Workers   int      `json:"workers,omitempty"`   // 文件下载/串流时的并发协程数
	AppID     int32    `json:"id"`                  // Telegram API ID, 从 my.telegram.org 获取
	MaxSize   int64    `json:"maxSize,omitempty"`   // 最大缓存大小
	UserID    int64    `json:"userID"`              // 管理员的 Telegram 用户 ID
	ChannelID int64    `json:"channelID,omitempty"` // 默认关联的频道 ID
	AdminIDs  []int64  `json:"adminIDs,omitempty"`  // 管理员 ID 列表, 拥有管理权限
	WhiteIDs  []int64  `json:"whiteIDs,omitempty"`  // 白名单 ID 列表, 允许使用部分功能
	Rules     []string `json:"rules,omitempty"`     // 群管正则规则列表
}

// loadConf 从指定路径加载 config.json 配置文件
// 如果文件不存在或解析失败, 将返回错误
func loadConf(filesPath string) (*Conf, error) {
	// 拼接完整的配置文件路径
	confPath := filepath.Join(filesPath, "config.json")

	// 打开配置文件
	file, err := os.Open(confPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if createErr := createDefaultConf(confPath); createErr != nil {
				log.Printf("生成 config.json 模板失败: %+v", createErr)
				return nil, createErr
			}
			log.Printf("config.json 不存在, 已生成模板: %s", confPath)
			file, err = os.Open(confPath)
			if err != nil {
				log.Printf("打开 config.json 文件错误: %+v", err)
				return nil, err
			}
		} else {
			log.Printf("打开 config.json 文件错误: %+v", err)
			return nil, err
		}
	}
	// 确保在函数退出时关闭文件句柄
	defer func() {
		if err := file.Close(); err != nil {
			log.Printf("关闭 config.json 文件错误: %+v", err)
		}
	}()

	// 读取文件的全部内容
	bytes, err := io.ReadAll(file)
	if err != nil {
		log.Printf("读取 config.json 文件错误: %+v", err)
		return nil, err
	}

	var conf Conf
	// 将 JSON 数据解析到 Conf 结构体中
	if err := json.Unmarshal(bytes, &conf); err != nil {
		log.Printf("解析 config.json 文件错误: %+v", err)
		return nil, err
	}

	return &conf, nil // 返回解析后的配置对象
}

// createDefaultConf 在配置目录中生成默认 config.json 模板。
func createDefaultConf(confPath string) error {
	if err := os.MkdirAll(filepath.Dir(confPath), 0755); err != nil {
		return err
	}
	return os.WriteFile(confPath, defaultConfigJSON, 0644)
}

// saveConf 将当前的配置信息序列化并保存到 config.json 文件中
// 常用于在程序运行过程中动态更新配置（如通过 Bot 命令添加白名单）
func saveConf(conf *Conf, filesPath string) error {
	configPath := filepath.Join(filesPath, "config.json")

	// 将结构体转换为格式化的 JSON 字节数组
	bytes, err := json.MarshalIndent(conf, "", "  ")
	if err != nil {
		log.Printf("解析 config.json 文件错误: %+v", err)
		return err
	}

	// 将字节数组写入到配置文件并返回结果
	if err := os.WriteFile(configPath, bytes, 0644); err != nil {
		log.Printf("写入 config.json 文件错误: %+v", err)
		return err
	}
	return nil
}
