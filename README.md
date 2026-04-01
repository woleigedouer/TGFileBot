# TGFileBot

TGFileBot 是一个 Telegram Bot 和 UserBot 深度结合的开源项目，旨在提供高性能的文件直链提取、媒体分片流式传输以及完善的远程机器人管理功能。

## 核心功能

-   **🚀 高性能流式下载**: 基于协程并发的分片下载技术，支持 HTTP Range 请求，可实现视频在浏览器或播放器中的随拖随播（边看边播）。
-   **🔗 智能链接提取**: 支持将 Telegram 消息（图片、文档、视频、音频）直接转换为 HTTP(s) 直链。支持私有频道和公开频道的链接解析。
-   **🤖 双模式机器人管理**: 通过 Bot 客户端发送指令，远程管理 UserBot 的生命周期（登录、设置、白名单等），无需操作服务器控制台。
-   **🛡️ 完善的权限控制**: 支持多管理员机制及白名单系统，所有敏感功能均受权限保护。
-   **🔑 灵活的身份验证**: 支持通过密码（key）或动态哈希（hash）保护直链，防止链接被恶意滥用。
-   **♻️ 自动引用刷新**: 针对 Telegram 资源引用过期（`FILE_REFERENCE_EXPIRED`）提供毫秒级自动重连和刷新机制，确保大文件下载不中断。
-   **📝 伪静态与播控优化**: 提供 `/stream/{mid}/{filename}` 格式的伪静态链接，优化流媒体文件的识别与加载体验。

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

| 命令 | 说明 | 权限 |
| :--- | :--- | :--- |
| `/start` | 查看 UserBot 当前状态 | 白名单 |
| `/qr` | **推荐方式：** 生成登录二维码，手机扫码即可登录 | 主管理员 |
| `/phone <手机号>` | 发起手机号登录流程 | 主管理员 |
| `/code <验证码>` | 提交手机验证码 | 主管理员 |
| `/pass <密码>` | 提交账号的二次验证（2FA）密码 | 主管理员 |
| `/password <key>` | 设置当前账号的密码 | 主管理员 |
| `/dc <ID>` | 指定 UserBot 的DC | 管理员 |
| `/allow <ID>` | 将特定用户 ID 添加到白名单 | 管理员 |
| `/disallow <ID>` | 从白名单中移除特定用户 | 管理员 |
| `/channel <ID>` | 动态设置绑定的默认频道 ID | 管理员 |
| `/workers <1-4>` | 动态调整下载并发协程数 | 管理员 |
| `/site <URL>` | 动态更新生成链接所用的域名/反代地址 | 管理员 |
| `/info <行数>` | 查看系统运行日志（默认 10 行） | 管理员 |
| `/check <Hash>` | 查看特定哈希值对应的用户信息 | 管理员 |
| `/port <端口>` | 动态设置 HTTP 服务端口 | 管理员 |
| `/add <别名>` | 添加搜索频道别名 | 管理员 |
| `/del <别名>` | 移除搜索频道别名 | 管理员 |
| `/list <类别>` | 列出所有搜索频道别名或白名单ID | 管理员 |

### 获取直链

-   **直接转发媒体**: 将媒体消息转发给 Bot，Bot 会返回支持分片流传输的直链。
-   **解析原始链接**: 发送 `t.me/c/xxx/yyy` 格式链接，Bot 会代为解密并生成下载地址。
-   **API 接口详情**:
    -   **流媒体接口**: `/stream?cid={cid}&mid={mid}&cate={bot|user}&key={key}&download=true`
    -   **跳转接口**: `/link?link={TG_LINK}&hash={hash}&uid={uid}`
    -   **伪静态地址**: `/stream/{mid}/{filename}?cid={cid}`

### 💡 身份鉴权算法 (开发者参考)
如果您在配置中设置了 `password`，则访问敏感文件时必须携带 `key`（明文密码）或 `hash`。
**Hash 计算公式**: `md5(userID + password)` 的前 6 位十六进制字符串。
*示例*: userID 为 `8888`，密码为 `mypass`。则需计算 `md5("8888mypass")`。

## 技术架构亮点

本项目采用了 **“生产者 - 消费者” (Producer-Consumer)** 模型处理文件流：
1.  **生产者 (Streamer)**: 多个协程并发从 Telegram 服务器拉取数据块，存入由 `sync.Cond` 保护的有序任务管道。
2.  **消费者 (HTTP Handler)**: 按照 Range 请求的字节序，精准地将数据块写入 HTTP 响应体。
3.  **引用透明**: 针对 Telegram 内部的 `file_reference` 过期问题，程序实现了自动静默刷新机制，确保即便在数小时的流传输过程中，连接也不会因为 Token 失效而中断。

## 注意事项

-   **风控风险**: 频繁使用 UserBot 进行大文件下载可能触碰 Telegram 的 API 限制。建议将 `workers` 设置在合理范围（1-4）。
-   **账号异常**: 由于 Telegram 协议不断迭代，部分账号可能出现 `PHONE_CODE_EXPIRED` 错误，此时推荐优先尝试 `/qr` 二维码登录。

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
