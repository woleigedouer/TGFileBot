# TGBot

TGBot 是一个 Telegram Bot 和 UserBot 结合的项目，旨在提供文件直链分享、媒体流式传输以及通过 Telegram 机器人管理 UserBot 的功能。

## 暂存问题

由于 MTProto 协议更新导致**部分**账号输入 /code 验证码后出现 [PHONE_CODE_EXPIRED] 错误。等待上游 AmarnathCJD/gogram 更新解决。此时仅可使用 Bot，**通过 Telegram 链接获取直链**相关功能将无法使用。

## 功能特性

- **文件直链分享**: 将 Telegram 中的媒体文件（图片、文档、视频、音频等）生成可直接访问的 HTTP 直链。
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
  // 程序运行的 HTTP 端口
  "port": 8080,
  // Telegram API ID
  "id": 0,
  // Telegram API Hash
  "hash": "",
  // 反代域名，用于生成直链
  "site": "",
  // 并发数
  "workers": 1,
  // 接收/phone等命令的Bot Token
  "botToken": "",
  // 浏览器访问授权密码 (可选)
  "password": "",
  // User Bot 身份对应的账号ID (用于判断是否为管理员，建议填写UserBot的ID)
  "userID": 0,
  // 绑定的频道ID
  "channelID": 0,
  // 支持多管理员的ID列表 (填写Telegram用户ID)
  "adminIDs": [],
  // 支持多白名单的ID列表 (可选，用于限制/stream访问)
  "whiteIDs": []
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

### 4. 命令行参数

程序支持通过命令行参数进行配置：

- `-files`: 指定配置和数据文件的存放目录（默认为 `files`）。该目录下应包含 `config.json`，程序运行产生的 `session` 和 `cache` 文件也会存放在此目录中。

### 5. 运行项目

#### 本地运行

```bash
go mod tidy
# 默认使用当前目录下的 files 文件夹
go run main.go
# 或者指定其它文件夹
go run main.go -files 自定义目录
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
- **通过 Bot 发送 Telegram 链接**: 发送 `t.me/c/channel_id/message_id` 或 `t.me/username/message_id` 格式的链接给 Bot，Bot 会解析并回复直链，支持带 `comment` 参数的评论链接。
- **通过 HTTP 接口**:
    - `GET /stream?cid=<chat_id>&mid=<message_id>&cate=<bot|user>&key=<password>`: 获取媒体文件流。
    - `GET /link?link=<telegram_link>&key=<password>`: 解析 Telegram 链接并重定向到直链。

## 注意事项

- `App ID` 和 `App Hash` 建议使用您自己的，以避免不必要的限制。
- UserBot 登录后，会缓存对话列表，这可能需要一些时间。
- 如果 UserBot 长时间未活动，可能会出现 Peer 缓存失效的问题，此时 Bot 会尝试刷新对话列表。
- 确保您的服务器防火墙允许外部访问配置的 `port`。

## Bot 与 UserBot 的区别及 UserBot 风险

本项目结合了 Telegram Bot 和 UserBot 的功能。理解它们之间的区别以及使用 UserBot 的潜在风险非常重要。

### Bot (机器人)

- **官方支持**: Bot 是 Telegram 官方提供的一种自动化工具，通过 BotFather 创建和管理。
- **API 限制**: Bot 的功能受到 Telegram Bot API 的限制，通常用于与用户交互、发送消息、管理群组等。
- **安全性**: Bot 账户相对安全，因为它们不能像普通用户一样登录 Telegram 客户端，也无法访问用户的私人聊天记录。
- **使用场景**: 适用于公开服务、自动化任务、信息推送等。

### UserBot (用户机器人)

- **模拟用户**: UserBot 是通过模拟普通 Telegram 用户行为来实现自动化操作的程序。它使用用户的 API ID 和 API Hash 登录，拥有与普通用户相同的权限。
- **功能强大**: UserBot 可以执行普通用户能做的所有操作，包括访问私人聊天、发送任何类型的文件、加入频道等，因此功能比 Bot 更强大。
- **潜在风险**:
    - **账号安全**: 使用 UserBot 意味着您将自己的 Telegram 账号凭据（API ID, API Hash, 手机号）提供给程序。如果程序存在漏洞或被恶意利用，您的账号可能面临风险。
    - **违反服务条款**: Telegram 的服务条款可能禁止或限制 UserBot 的使用。过度或滥用 UserBot 功能可能导致您的账号被限制甚至封禁。
    - **隐私泄露**: UserBot 可以访问您的所有聊天记录和联系人信息。请确保您完全信任 UserBot 的开发者和代码，以防隐私泄露。
    - **稳定性问题**: UserBot 的实现通常依赖于逆向工程或非官方 API，可能不如官方 Bot API 稳定，容易受到 Telegram 更新的影响。
- **使用场景**: 适用于需要模拟用户行为、访问受限内容、进行高级自动化操作的场景，但需谨慎使用并承担相应风险。

**本项目中的 UserBot 主要用于获取普通 Bot 无法直接访问的媒体文件直链，以及通过 UserBot 身份进行流式传输。请务必了解并接受使用 UserBot 带来的潜在风险。**

本项目遵循 MIT 许可 - 详见 [LICENSE](LICENSE)。
