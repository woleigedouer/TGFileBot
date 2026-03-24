# TGBot

TGBot 是一个 Telegram Bot 和 UserBot 结合的项目，旨在提供文件直链分享、媒体流式传输以及通过 Telegram 机器人管理 UserBot 的功能。

## 功能特性

- **文件直链分享**: 将 Telegram 中的媒体文件（图片、文档、视频）生成可直接访问的 HTTP 直链。
- **媒体流式传输**: 支持通过 HTTP Range 请求对视频等媒体文件进行流式传输，实现边下边播。
- **UserBot 管理**: 通过 Bot 客户端发送命令来管理 UserBot 的登录、白名单设置等。
- **多管理员支持**: 支持配置多个管理员 ID，共同管理 Bot。
- **密码保护**: 可选的密码保护，限制对直链的访问。
- **缓存清理**: 自动清理旧的 Bot 和 UserBot 缓存文件。

## 部署

### 1. 获取 API ID 和 API Hash

访问 [my.telegram.org](https://my.telegram.org/) 登录您的 Telegram 账号，创建一个新的应用以获取 `App ID` 和 `App Hash`。

### 2. 获取 Bot Token

在 Telegram 中搜索 `@BotFather`，创建一个新的 Bot 并获取 `Bot Token`。

### 3. 配置 `config.json`

在 `files` 目录下创建 `config.json` 文件，并根据 `config.json.example` 填写您的配置信息。

```json
{
  "port": 8080,               // 程序运行的 HTTP 端口
  "id": 0,                    // Telegram API ID
  "hash": "",                 // Telegram API Hash
  "site": "http://your_domain_or_ip", // 反代域名，用于生成直链
  "phone": "",                // User Bot 身份对应的手机号 (带国际区号，例如: +8613800138000)
  "botToken": "",             // 接收/phone等命令的Bot Token
  "password": "",             // 访问/link的密码 (可选)
  "userID": 0,                // User Bot 身份对应的账号ID (用于判断是否为管理员，建议填写UserBot的ID)
  "adminIDs": [],             // 支持多管理员的ID列表 (填写Telegram用户ID)
  "whiteIDs": []              // 支持多白名单的ID列表 (可选，用于限制/stream访问)
}
```

**注意**:
- `id` 和 `hash` 填写您在 [my.telegram.org](https://my.telegram.org/) 获取的 `App ID` 和 `App Hash`。
- `site` 填写您的服务器域名或 IP 地址，用于生成直链。
- `phone` 填写您用于 UserBot 登录的手机号，需要带国际区号。
- `botToken` 填写您从 `@BotFather` 获取的 Bot Token。
- `userID` 填写您的 Telegram 用户 ID，用于 Bot 接收管理命令。
- `adminIDs` 可以填写多个管理员的 Telegram 用户 ID。
- `password` 是可选的，如果设置，访问 `/stream` 和 `/link` 接口需要携带 `key` 参数。

### 4. 运行项目

#### 本地运行

```bash
go mod tidy
go run main.go -files files
```

#### Docker 部署

1. 构建 Docker 镜像:
   ```bash
   docker build -t tgfilebot .
   ```

2. 运行 Docker 容器:
   ```bash
   docker run -d --name tgfilebot -p 9981:8080 -v $(pwd)/files:/root/files tgfilebot
   ```
   或者使用 `docker-compose.yml`:
   ```bash
   docker-compose up -d
   ```

## 使用方法

### Bot 命令

通过您的 Bot 客户端向 Bot 发送以下命令：

- `/phone <手机号>`: 绑定 UserBot 手机号，例如 `/phone +8613800138000`。
- `/code <验证码>`: 输入 Telegram 发送给您的验证码，完成 UserBot 登录。
- `/allow <用户ID>`: 添加用户到白名单，允许其访问直链。
- `/disallow <用户ID>`: 从白名单中移除用户。

### 获取直链

- **通过 Bot 转发/发送媒体文件**: 将 Telegram 中的图片、文档、视频等媒体文件转发或直接发送给 Bot，Bot 会自动回复直链。
- **通过 Bot 发送 Telegram 链接**: 发送 `t.me/c/channel_id/message_id` 或 `t.me/username/message_id` 格式的链接给 Bot，Bot 会解析并回复直链。
- **通过 HTTP 接口**:
    - `GET /stream?cid=<chat_id>&mid=<message_id>&cate=<bot|user>&key=<password>`: 获取媒体文件流。
    - `GET /link?link=<telegram_link>&key=<password>`: 解析 Telegram 链接并重定向到直链。

## 注意事项

- `App ID` 和 `App Hash` 建议使用您自己的，以避免不必要的限制。
- UserBot 登录后，会缓存对话列表，这可能需要一些时间。
- 如果 UserBot 长时间未活动，可能会出现 Peer 缓存失效的问题，此时 Bot 会尝试刷新对话列表。
- 确保您的服务器防火墙允许外部访问配置的 `port`。

