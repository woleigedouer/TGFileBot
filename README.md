# TGFileBot

TGFileBot 是一个 Telegram Bot 和 UserBot 深度结合的开源项目，旨在提供高性能的文件直链提取、媒体分片流式传输以及完善的远程机器人管理功能。

## 核心功能

- **🚀 高性能流式下载**: 基于协程并发的分片下载技术，支持 HTTP Range 请求，可实现视频在浏览器或播放器中的随拖随播（边看边播）。
- **🔗 智能链接提取**: 支持将 Telegram 消息（图片、文档、视频、音频）直接转换为 HTTP(s) 直链。支持私有频道和公开频道的链接解析。
- **🤖 双模式机器人管理**: 通过 Bot 客户端发送指令，远程管理 UserBot 的生命周期（登录、设置、白名单等），无需操作服务器控制台。
- **🛡️ 完善的权限控制**: 支持多管理员机制及白名单系统，所有敏感功能均受权限保护。
- **🔑 灵活的身份验证**: 支持通过密码（key）或动态哈希（hash）保护直链，防止链接被恶意滥用。
- **♻️ 自动引用刷新**: 针对 Telegram 资源引用过期（`FILE_REFERENCE_EXPIRED`）提供毫秒级自动重连和刷新机制，确保大文件下载不中断。
- **📝 伪静态与播控优化**: 提供 `/stream/{mid}/{filename}` 格式的伪静态链接，优化流媒体文件的识别与加载体验。

## 部署

### 1. 获取 API ID 和 API Hash

访问 [my.telegram.org](https://my.telegram.org/) 登录您的 Telegram 账号，创建一个新的应用以获取 `App ID` 和 `App Hash`。

### 2. 获取 Bot Token

在 Telegram 中搜索 `@BotFather`，创建一个新的 Bot 并获取 `Bot Token`。

### 3. 配置 `config.json`

在程序运行目录下或指定目录下创建 `config.json` 文件（可参考 `files/config.json.example`）。

```json
{
  "port": 8080,
  // 程序运行的 HTTP 端口
  "id": 0,
  // Telegram API ID
  "hash": "",
  // Telegram API Hash
  "site": "",
  // 反代域名或服务器 IP，用于生成直链
  "botToken": "",
  // 管理用 Bot 的 Token
  "userID": 0,
  // 主管理员 UserID (UserBot 对应的账号 ID)
  "password": "",
  // 接口访问密码 (可选，设置后需在 URL 携带 key 或 hash 参数)
  "dc": 0,
  // Datacenter ID (可选，默认为 0，如遇到连接问题可尝试指定 DC)
  "workers": 1,
  // 并发块下载数 (默认为 1，建议不大于 4)
  "channelID": 0,
  // 绑定的默认频道 ID (可选)
  "adminIDs": [],
  // 辅助管理员 ID 列表
  "whiteIDs": []
  // 白名单 ID 列表
}
```

**参数说明**:

- `id` 和 `hash`: 必填，从 [my.telegram.org](https://my.telegram.org/) 获取。
- `site`: 必填，用于生成直链。
- `userID`: 建议填写您的个人 Telegram ID。
- `password`: 如果设置了该项，访问资源时 URL 必须包含 `&key=您的密码` 或 `&hash=ID与密码拼接后的MD5前6位`。
- `workers`: 并发下载块数。提高该值可加速下载，但可能导致账号封禁，建议保持在 1-4 之间。

### 4. 命令行参数

程序支持以下命令行参数：

- `-files`: 指定存放配置文件、会话文件和缓存的目录（默认为 `files`）。
- `-log`: 指定日志文件路径（默认为 `files/log.log`）。
- `-version`, `-v`: 打印程序版本号。

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

容器默认使用 `/root/files/config.json`。首次启动时如果挂载目录中没有 `config.json`，会自动生成模板到宿主机的 `./files/config.json`，请填写 `id`、`hash`、`botToken` 等必要配置后重启容器。

## 使用方法

### Bot 管理命令

通过主管理员账号向 Bot 发送以下命令：

| 命令                   | 说明                         | 权限   |
|:---------------------|:---------------------------|:-----|
| `/start`             | 查看 UserBot 当前状态            | 白名单  |
| `/qr`                | **推荐方式：** 生成登录二维码，手机扫码即可登录 | 主管理员 |
| `/phone <手机号>`       | 发起手机号登录流程                  | 主管理员 |
| `/code <验证码>`        | 提交手机验证码（需混入非数字字符，详见注意事项）  | 主管理员 |
| `/pass <密码>`         | 提交账号的二次验证（2FA）密码           | 主管理员 |
| `/password <key>`    | 设置当前账号的密码                  | 管理员  |
| `/dc <ID>`           | 指定 UserBot 的DC             | 管理员  |
| `/allow <ID>`        | 将特定用户 ID 添加到白名单            | 管理员  |
| `/disallow <ID>`     | 从白名单中移除特定用户                | 管理员  |
| `/channel <ID>`      | 动态设置绑定的默认频道 ID             | 管理员  |
| `/workers <1-4>`     | 动态调整下载并发协程数                | 管理员  |
| `/site <URL>`        | 动态更新生成链接所用的域名/反代地址         | 管理员  |
| `/size <size>`       | 动态设置最大缓存大小                  | 管理员  |
| `/info <筛选关键字> <行数>` | 查看系统运行日志（默认 10 行）          | 管理员  |
| `/check <Hash>`      | 查看特定哈希值对应的用户信息             | 管理员  |
| `/port <端口>`         | 动态设置 HTTP 服务端口             | 管理员  |
| `/add <别名>`          | 添加搜索频道别名                   | 管理员  |
| `/del <别名>`          | 移除搜索频道别名                   | 管理员  |
| `/addrule <规则>`      | 添加正则过滤规则                   | 管理员  |
| `/delrule <索引/内容>` | 按索引或内容移除正则过滤规则           | 管理员  |
| `/list <类别>`         | 列出所有搜索频道别名、白名单ID、正则规则    | 管理员  |

### 获取直链

- **直接转发媒体**: 将媒体消息转发给 Bot，Bot 会返回支持分片流传输的直链。
- **解析原始链接**: 发送 `t.me/c/xxx/yyy` 格式链接，Bot 会代为解密并生成下载地址。

## HTTP API 接口

所有接口均由内置 HTTP 服务提供，默认监听 `8080` 端口。若配置了 `password`，则需在请求 URL 中附带鉴权参数（见下方鉴权说明）。

### `GET /`

返回服务器运行状态，无需鉴权。

**响应示例**:
```json
{
  "message": "服务器正在运行。",
  "ok": true,
  "uptime": "1d 2h 3m 4s",
  "version": "v1.0.5"
}
```

---

### `GET /stream` — 流媒体 / 下载接口

核心下载接口，支持 HTTP Range 分段请求，可在浏览器或播放器中实现随拖随播。

**URL 格式**:
```
/stream?cid={cid}&mid={mid}&cate={bot|user}&key={key}&download=true
```

**或使用伪静态格式（更好的播放器兼容性）**:
```
/stream/{mid}/{filename}?cid={cid}&key={key}
```

| 参数 | 必填 | 说明 |
|:---|:---:|:---|
| `cid` | 否 | 频道 ID（负数形式，如 `-1001234567890`）。若 `config.json` 中已设置 `channelID` 则可省略 |
| `mid` | 是 | 消息 ID（正整数）|
| `cate` | 否 | 下载客户端选择：`user`（使用 UserBot，可访问私有频道）或 `bot`（默认）。UserBot 未登录时自动回退到 Bot |
| `download` | 否 | 设为 `true` 时以附件模式下载（`Content-Disposition: attachment`），否则为内联播放 |
| `key` | 否* | 明文访问密码（设置了 `password` 时必填其一）|
| `hash` | 否* | 基于用户 ID 的哈希鉴权（设置了 `password` 时必填其一），需同时提供 `uid` |
| `uid` | 否* | 使用 `hash` 鉴权时必须提供对应用户 ID |

> 若消息为转发消息，程序会自动解析转发来源频道，确保分片下载稳定。

---

### `GET /link` — 链接解析跳转接口

将 Telegram 消息链接解析为直链，并以 **302 重定向** 返回。

**URL 格式**:
```
/link?link={TG_LINK}&key={key}&uid={uid}&hash={hash}
```

| 参数 | 必填 | 说明 |
|:---|:---:|:---|
| `link` | 是 | 完整的 Telegram 消息链接，格式为 `https://t.me/c/{cid}/{mid}` 或 `https://t.me/{username}/{mid}` |
| `key` | 否* | 明文访问密码(与hash二选一) |
| `hash` | 否* | 哈希鉴权(与key二选一) |
| `uid` | 否* | 使用 `hash` 时对应的用户 ID |

**支持的链接格式**:
- 私有频道: `https://t.me/c/1234567890/100`
- 公开频道: `https://t.me/channelname/100`
- 带评论区链接: `https://t.me/c/1234567890/100?comment=200`

---

### `GET /search` — 频道内容搜索接口

在已配置的搜索频道中并发全文检索，需 UserBot 已登录。

**URL 格式**:
```
/search?keywords={关键词}&page={页码}&limit={每页数量}&offset={偏移量}&key={key}
```

| 参数 | 必填 | 说明 |
|:---|:---:|:---|
| `keywords` | 是 | 搜索关键词 |
| `page` | 否 | 页码，默认 `1` |
| `limit` | 否 | 每页返回数量，默认 `20` |
| `offset` | 否 | 结果偏移量，默认 `0` |
| `key` / `hash` / `uid` | 否* | 鉴权参数（同上）|

**响应示例**:
```json
{
  "more": false,
  "items": [
    {
      "more": false,
      "channel": "channelname",
      "item": [
        { "name": "example.mp4", "mid": 100, "cid": -1001234567890, "size": 104857600 }
      ]
    }
  ]
}
```

> 搜索范围为 `config.json` 中 `/add` 命令添加的频道别名列表。接口超时时间为 **30 秒**。

---

### 💡 身份鉴权说明

若配置了 `password`，访问所有 HTTP 接口时须在 URL 中携带以下任意一种鉴权方式：

| 鉴权方式 | URL 参数 | 说明 |
|:---|:---|:---|
| 明文密码 | `&key=yourpassword` | 直接传入配置中的 `password` 值 |
| 哈希密码 | `&hash=xxxxxx&uid=888888` | 更安全的方式，避免明文暴露密码 |

**Hash 计算公式**: `md5(uid + password)` 的前 **6 位**十六进制字符串。

*示例*: `uid` 为 `8888`，`password` 为 `mypass`，则计算 `md5("8888mypass")` 并取前 6 位。

## 技术架构亮点

本项目采用了 **“生产者 - 消费者” (Producer-Consumer)** 模型处理文件流：

1. **生产者 (Streamer)**: 多个协程并发从 Telegram 服务器拉取数据块，存入由 `sync.Cond` 保护的有序任务管道。
2. **消费者 (HTTP Handler)**: 按照 Range 请求的字节序，精准地将数据块写入 HTTP 响应体。
3. **引用透明**: 针对 Telegram 内部的 `file_reference` 过期问题，程序实现了自动静默刷新机制，确保即便在数小时的流传输过程中，连接也不会因为
   Token 失效而中断。

## 注意事项

- **风控风险**: 频繁使用 UserBot 进行大文件下载可能触碰 Telegram 的 API 限制。建议将 `workers` 设置在合理范围（1-4）。

- **⚠️ 验证码输入限制（重要）**: 由于 Telegram 的安全策略，当通过 Bot 消息提交 `/code` 验证码时，Telegram 服务器会检测到验证码是通过自动化方式发送的，从而**立即使验证码失效**，导致 `PHONE_CODE_EXPIRED` 错误。

  **解决方法（必须手动操作）**：
  1. 收到 Telegram 发来的 6 位数字验证码，例如 `12345`。
  2. 在验证码数字之间**随机插入任意非数字字符**（字母、符号、空格均可），例如将 `12345` 改写为 `1a2b3c4d5` 或 `1-2-3-4-5`。
  3. **手动**（非复制粘贴自动化消息）将混淆后的字符串发送给 Bot，即 `/code 1a2b3c4d5`。
  4. 程序会自动过滤掉所有非数字字符，提取出真正的验证码 `12345` 进行登录。

  > 此方法的原理：Telegram 对「原始验证码字符串」的发送行为进行监控，而混入随机字符后，消息内容不再与验证码完全匹配，可绕过该限制。优先推荐使用 `/qr` 二维码扫码登录以完全避免此问题。

## Bot 与 UserBot 的区别及 UserBot 风险

本项目结合了 Telegram Bot 和 UserBot 的功能。理解它们之间的区别以及使用 UserBot 的潜在风险非常重要。

### Bot (机器人)

- **官方支持**: Bot 是 Telegram 官方提供的一种自动化工具，通过 BotFather 创建和管理。
- **API 限制**: Bot 的功能受到 Telegram Bot API 的限制，通常用于与用户交互、发送消息、管理群组等。
- **安全性**: Bot 账户相对安全，因为它们不能像普通用户一样登录 Telegram 客户端，也无法访问用户的私人聊天记录。
- **使用场景**: 适用于公开服务、自动化任务、信息推送等。

### UserBot (用户机器人)

- **模拟用户**: UserBot 是通过模拟普通 Telegram 用户行为来实现自动化操作的程序。它使用用户的 API ID 和 API Hash
  登录，拥有与普通用户相同的权限。
- **功能强大**: UserBot 可以执行普通用户能做的所有操作，包括访问私人聊天、发送任何类型的文件、加入频道等，因此功能比 Bot
  更强大。
- **潜在风险**:
    - **账号安全**: 使用 UserBot 意味着您将自己的 Telegram 账号凭据（API ID, API Hash,
      手机号）提供给程序。如果程序存在漏洞或被恶意利用，您的账号可能面临风险。
    - **违反服务条款**: Telegram 的服务条款可能禁止或限制 UserBot 的使用。过度或滥用 UserBot 功能可能导致您的账号被限制甚至封禁。
    - **隐私泄露**: UserBot 可以访问您的所有聊天记录 and 联系人信息。请确保您完全信任 UserBot 的开发者和代码，以防隐私泄露。
    - **稳定性问题**: UserBot 的实现通常依赖于逆向工程或非官方 API，可能不如官方 Bot API 稳定，容易受到 Telegram 更新的影响。
- **使用场景**: 适用于需要模拟用户行为、访问受限内容、进行高级自动化操作的场景，但需谨慎使用并承担相应风险。

**本项目中的 UserBot 主要用于获取普通 Bot 无法直接访问的媒体文件直链，以及通过 UserBot 身份进行流式传输。请务必了解并接受使用
UserBot 带来的潜在风险。**

## 许可证

本项目遵循 [MIT](LICENSE) 许可。

## Changelog / 更新日志
- **v1.0.1**
  - **稳定性:** 修复并发下载中 `stream.Src` 读取的 Data Race 问题。
  - **内存优化:** 修复 `clean()` 中 `time.After` 造成的潜在 Timer 内存泄漏。
  - **容错增强:** 对普通网络错误加入指数退避 (Exponential Backoff) 重试策略。
  - **重构:** `http.go` 核心请求解析逻辑抽离，提升可维护性。
