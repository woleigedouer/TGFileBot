package main

import (
	"encoding/json" // 用于解析 JSON 配置文件
	"io"            // 用于读取文件内容
	"log"           // 用于日志记录
	"os"            // 用于文件操作
	"path/filepath" // 用于处理文件路径
)

// Config 结构体定义了程序所需的配置项
type Conf struct {
	Port     int     `json:"port"`               // 程序运行的 HTTP 端口
	AppID    int32   `json:"id"`                 // Telegram API ID
	AppHash  string  `json:"hash"`               // Telegram API Hash
	Site     string  `json:"site"`               // 反代域名
	Phone    string  `json:"phone"`              // User Bot 身份对应的手机号
	BotToken string  `json:"botToken"`           // 接收/phone等命令的Bot Token
	Password string  `json:"password,omitempty"` // 访问/link的密码
	UserID   int64   `json:"userID"`             // User Bot 身份对应的账号ID
	AdminIDs []int64 `json:"adminIDs,omitempty"` // 支持多管理员的ID列表
	WhiteIDs []int64 `json:"whiteIDs,omitempty"` // 支持多白名单的ID列表
}

// loadConf 用于从命令行参数指定的目录中加载 config.json
func loadConf(filesPath string) (*Conf, error) {
	// 构建 config.json 的完整路径
	confPath := filepath.Join(filesPath, "config.json")
	// 打开配置文件
	file, err := os.Open(confPath)
	if err != nil {
		log.Printf("打开 config.json 文件错误: %+v", err)
		return nil, err
	}
	// 确保在函数返回前关闭文件
	defer func() {
		if err := file.Close(); err != nil {
			log.Printf("关闭 config.json 文件错误: %+v", err)
		}
	}()

	// 读取文件的所有字节
	bytes, err := io.ReadAll(file)
	if err != nil {
		log.Printf("读取 config.json 文件错误: %+v", err)
		return nil, err
	}

	var conf Conf
	// 将 JSON 字节解析到 Config 结构体中
	if err := json.Unmarshal(bytes, &conf); err != nil {
		log.Printf("解析 config.json 文件错误: %+v", err)
		return nil, err
	}

	return &conf, nil // 返回配置对象
}

// saveConf 保存配置信息到 config.json
func saveConf(conf *Conf, filesPath string) error {
	configPath := filepath.Join(filesPath, "config.json")
	bytes, err := json.MarshalIndent(conf, "", "  ")
	if err != nil {
		log.Printf("解析 config.json 文件错误: %+v", err)
		return err
	}
	// 确保在函数返回前关闭文件
	defer func() {
		if err := os.WriteFile(configPath, bytes, 0644); err != nil {
			log.Printf("写入 config.json 文件错误: %+v", err)
		}
	}()
	return nil
}
