# TGFileBot

TGBot 是一个 Telegram Bot 和 UserBot 结合的项目，旨在提供文件直链分享、媒体流式传输以及通过 Telegram 机器人管理 UserBot 的功能。

## 暂存问题

由于 MTProto 协议更新导致**部分**账号输入 /code 验证码后出现 [PHONE_CODE_EXPIRED] 错误。此时仅可使用 Bot，**通过 Telegram 链接获取直链**相关功能将无法使用。如有遇到此类问题建议更换账号登录或等待上游框架更新。

## 功能特性

-   **文件直链分享**: 将 Telegram 中的媒体文件（图片、文档、视频、音频等）生成可直接访问的 HTTP 直链。
-   **媒体流式传输**: 支持通过 HTTP Range 请求对视频等媒体文件进行流式传输，实现边看边播。支持多线程并发下载。
-   **UserBot 管理**: 通过 Bot 客户端发送命令来管理 UserBot 的登录、设置、白名单等。
-   **URL 伪静态支持**: 支持 `/stream/消息ID/文件名` 格式的资源链接。
-   **多管理员与白名单**: 支持配置多个管理员 ID，以及设置白名单限制访问。
-   **动态配置更新**: 部分配置项（如频道 ID、并发数）可通过机器人命令在线修改。
-   **日志实时查看**: 通过机器人命令可远程查看程序的运行日志。
-   **自动刷新引用**: 针对 Telegram 资源引用过期（`FILE_REFERENCE_EXPIRED`）提供自动刷新功能，确保链接长期有效。

## 部署

### 1. 获取 API ID 和 API Hash

访问 [my.telegram.org](https://my.telegram.org/) 登录您的 Telegram 账号，创建一个新的应用以获取 `App ID` 和 `App Hash`。

### 2. 获取 Bot Token

在 Telegram 中搜索 `@BotFather`，创建一个新的 Bot 并获取 `Bot Token`。

### 3. 配置 `config.json`

在程序运行目录下或指定目录下创建 `config.json` 文件（可参考 `files/config.json.example`）。

```json
{
  "port": 8080,        // 程序运行的 HTTP 端口
  "id": 0,             // Telegram API ID
  "hash": "",          // Telegram API Hash
  "site": "",          // 反代域名或服务器 IP，用于生成直链
  "botToken": "",      // 管理用 Bot 的 Token
  "userID": 0,         // 主管理员 UserID (UserBot 对应的账号 ID)
  "password": "",      // 接口访问密码 (可选，设置后需在 URL 携带 key 或 hash 参数)
  "dc": 0,             // Datacenter ID (可选，默认为 0，如遇到连接问题可尝试指定 DC)
  "workers": 1,        // 并发块下载数 (默认为 1，建议不大于 4)
  "channelID": 0,      // 绑定的默认频道 ID (可选)
  "adminIDs": [],      // 辅助管理员 ID 列表
  "whiteIDs": []       // 白名单 ID 列表
}
```

**参数说明**:

-   `id` 和 `hash`: 必填，从 [my.telegram.org](https://my.telegram.org/) 获取。
-   `site`: 必填，用于生成直链。
-   `userID`: 建议填写您的个人 Telegram ID。
-   `password`: 如果设置了该项，访问资源时 URL 必须包含 `&key=您的密码` 或 `&hash=ID与密码拼接后的MD5前6位`。
-   `workers`: 并发下载块数。提高该值可加速下载，但可能导致账号封禁，建议保持在 1-4 之间。

### 4. 命令行参数

程序支持以下命令行参数：

-   `-files`: 指定存放配置文件、会话文件和缓存的目录（默认为 `files`）。
-   `-log`: 指定日志文件路径（默认为 `files/log.log`）。
-   `-version`, `-v`: 打印程序版本号。

### 5. 运行项目

#### 本地运行

```bash
go mod tidy
go run main.go -files files
```

#### Docker 部署

```bash
# 使用 Docker Compose
docker-compose up -d

# 或者手动构建与运行
docker build -t tgfilebot .
docker run -d --name tgfilebot -p 8080:8080 -v $(pwd)/files:/root/files tgfilebot
```

## 使用方法

### Bot 管理命令

通过主管理员账号向 Bot 发送以下命令：

-   `/start`: 查看 UserBot 当前状态。
-   `/phone <手机号>`: 绑定 UserBot，例如 `/phone +8613800138000`。
-   `/code <验证码>`: 提交登录验证码。
-   `/pass <密码>`: 提交账号的二次验证（2FA）密码。
-   `/channel <频道ID>`: 设置当前绑定的频道 ID，私有频道 ID 需以 `-100` 开头。
-   `/workers <数字>`: 设置下载并发数（建议 1-4）。
-   `/info [行数]`: 查看最近的系统运行日志，默认为 10 行。
-   `/allow <用户ID>`: 将用户添加到白名单。
-   `/disallow <用户ID>`: 从白名单中移除用户。
-   `/check <哈希值>`: 查看哈希值对应的用户信息。

### 获取直链

-   **转发媒体消息**: 将媒体文件（图片、视频、文档）直接转发给 Bot，Bot 会返回该文件的流式下载链接。
-   **发送 Telegram 消息链接**: 发送 `t.me/c/xxx/yyy` 格式的链接，Bot 会代为解析并寻找其中的媒体文件生成直链。
-   **API 接口**:
    -   `/stream?cid=<频道ID>&mid=<消息ID>&cate=<bot|user>&key=<密码>&hash=<密码哈希>&download=true`: 获取媒体流。
    -   `/stream/<消息ID>/<文件名>?cid=<频道ID>&...`: 伪静态链接支持。
    -   `/link?link=<Telegram链接>&key=<密码>&...`: 解析电报链接并重定向至直链。

## 注意事项

-   **账号风险**: UserBot 模拟用户行为，请勿短时间内大量下载，以免被 Telegram 限制或封禁账号。
-   **引用过期**: 程序会自动处理 `FILE_REFERENCE_EXPIRED` 错误并刷新文件标识符。
-   **资源加载**: UserBot 已登录状态下，可以解析频道和讨论组消息。支持带评论参数的链接解析。

## Bot 与 UserBot 的区别及 UserBot 风险

本项目结合了 Telegram Bot 和 UserBot 的功能。理解它们之间的区别以及使用 UserBot 的潜在风险非常重要。

### Bot (机器人)

-   **官方支持**: Bot 是 Telegram 官方提供的一种自动化工具，通过 BotFather 创建和管理。
-   **API 限制**: Bot 的功能受到 Telegram Bot API 的限制，通常用于与用户交互、发送消息、管理群组等。
-   **安全性**: Bot 账户相对安全，因为它们不能像普通用户一样登录 Telegram 客户端，也无法访问用户的私人聊天记录。
-   **使用场景**: 适用于公开服务、自动化任务、信息推送等。

### UserBot (用户机器人)

-   **模拟用户**: UserBot 是通过模拟普通 Telegram 用户行为来实现自动化操作的程序。它使用用户的 API ID 和 API Hash 登录，拥有与普通用户相同的权限。
-   **功能强大**: UserBot 可以执行普通用户能做的所有操作，包括访问私人聊天、发送任何类型的文件、加入频道等，因此功能比 Bot 更强大。
-   **潜在风险**:
    -   **账号安全**: 使用 UserBot 意味着您将自己的 Telegram 账号凭据（API ID, API Hash, 手机号）提供给程序。如果程序存在漏洞或被恶意利用，您的账号可能面临风险。
    -   **违反服务条款**: Telegram 的服务条款可能禁止或限制 UserBot 的使用。过度或滥用 UserBot 功能可能导致您的账号被限制甚至封禁。
    -   **隐私泄露**: UserBot 可以访问您的所有聊天记录 and 联系人信息。请确保您完全信任 UserBot 的开发者和代码，以防隐私泄露。
    -   **稳定性问题**: UserBot 的实现通常依赖于逆向工程或非官方 API，可能不如官方 Bot API 稳定，容易受到 Telegram 更新的影响。
-   **使用场景**: 适用于需要模拟用户行为、访问受限内容、进行高级自动化操作的场景，但需谨慎使用并承担相应风险。

**本项目中的 UserBot 主要用于获取普通 Bot 无法直接访问的媒体文件直链，以及通过 UserBot 身份进行流式传输。请务必了解并接受使用 UserBot 带来的潜在风险。**

## 许可证

本项目遵循 [MIT](LICENSE) 许可。
