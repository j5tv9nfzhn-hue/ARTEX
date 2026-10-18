---
name: wasm-cloud-saas-tactics
description: WASM 前端签名逆向、云存储 (OSS/STS) 预签名目录穿越与 AI 内容 SaaS 渗透防踩坑专家手册（源自 notegpt.io 实战报告沉淀）。涵盖 WASM 签名算法静态还原、未归一化原始路径 PUT 绕过 SignatureDoesNotMatch、利用截图转存 OSS 通道全回显读取阿里云 100.100.100.200 RAM 凭证、OAuth postMessage 与重置链接 JWT 泄密及 3-Strike 止损纪律。
---

# WASM 前端签名逆向、云存储穿透与 AI 内容 SaaS 渗透防踩坑手册 (wasm-cloud-saas-tactics)

> **核心定位**：本技能包由 `notegpt.io` 完整逆向与渗透报告深度提炼而成。针对采用前端 WebAssembly (`.wasm`) 接口签名保护、阿里云/AWS 对象存储 (OSS/S3) 预签名直传以及音视频/文档转换管线的现代 AI SaaS 平台，**最核心的突破口在于先逆向还原前端签名、识破客户端 URL 路径归一化陷阱，并区分无回显盲 SSRF 与基于 OSS 转存的全回显 SSRF 通道**。

---

## When to Use

- 目标前端加载 `.wasm` 模块（如 `crypto_util_bg.*.wasm`、`sign.wasm`）对 API 请求生成 `s` / `sign` / `t` / `nonce` 签名参数，直接修改请求参数会触发签名校验失败时。
- 目标提供文件上传预签名接口（如 `POST /api/v1/upload/sign-url`）或直接下发云存储临时凭证接口（如 `GET /api/v1/oss/sts-token`）时。
- 目标具备远程 URL 拉取能力，如网页截图转存（`/api/v2/upload-screenshot`）、文档/网页转换（`/api/v1/docx-convert`、`/api/v1/web-convert`）、音视频提取（`/api/v2/audio-extract/start`）等。
- 目标包含 OAuth 弹窗登录回调（`/auth/callback`）、邮件激活/密码重置 JWT 链接、AI 流式对话续传（`/api/v2/chat/agent-stream/continue`）或笔记分享导出功能时。
- **包内深度参考文档**：
  - `references/pitfalls-and-bypasses.md`：8 大实战踩坑陷阱、失败根因与底层绕过方案对照表（必读）
  - `references/attack-chains-and-pocs.md`：5 阶段完整突破推导链与脱敏验证探针模板

---

## Procedure

### 1. 第一阶段：前端 WASM 签名模块定位与静态逆向还原

1. **禁止在带签名接口上直接盲 Fuzz**：发现接口携带 `s`（签名）、`t`（时间戳）、`nonce`（随机串）且改动业务字段即返回 400/403 时，立即在前端打包产物中搜索 `.wasm` 胶水代码（如 `crypto_util`）。
2. **静态反编译提取盐值与拼接顺序**：
   - 下载 `.wasm` 二进制后，使用 `wasm2wat`、`wasm-decompile` 或 `strings` 检查数据段（Data Segment）中的硬编码字符串与常量盐值。
   - 还原签名生成公式（例如将业务参数 `biz`、`file_type`、`filename`、`nonce`、`t` 与固定盐值拼接后计算 MD5），用 Python/Go 封装本地签名构造函数，彻底解锁匿名签名接口的任意参数注入能力。

### 2. 第二阶段：预签名上传目录穿越与原始未归一化路径 PUT

1. **在预签名接口注入跨目录参数**：使用还原的签名算法调用 `POST /api/v1/upload/sign-url`，将业务目录参数设为 `biz: "../../../deploy"`（或目标高价值路径），使服务端用含 `../` 的 Object Key 计算出合法的 OSS 预签名 PUT URL。
2. **保持原始未归一化路径发包（避开最大踩坑点）**：
   - 拿到含 `/notegpt/web/../../../deploy/...` 的预签名 URL 后，**严禁使用会自动折叠 `../` 的普通 HTTP 客户端**；必须使用 `curl --path-as-is`、Go `url.URL{Opaque: rawPath}` 或原始 TLS Socket 发送 PUT 请求，使 OSS 服务端验签通过并在归档时完成跨目录任意文件覆盖。
3. **验证同源/CDN 存储型 XSS 与未授权 STS 令牌**：
   - 上传 `Content-Type: text/html` 或 `image/svg+xml` 的验证载荷至公网 CDN 域名（如 `cdn.ng-resource.com`），验证存储型 XSS；同时排查是否存在未授权直发临时凭证的 `GET /api/v1/oss/sts-token` 接口。

### 3. 第三阶段：全回显 SSRF 通道筛选与阿里云 RAM 元数据凭证窃取

1. **优先寻找「拉取远程 URL 并转存公开 OSS」的全回显 SSRF 通道**：
   - 对比所有接收 URL 的接口：文档/音视频转换接口往往只返回转换报错或异步任务状态（半回显/盲 SSRF），而截图转存接口（如 `POST /api/v2/upload-screenshot` 的 `uploaded_screen_shot_url`）会将远程 HTTP 响应体原样保存至公开 OSS 并返回可下载的 CDN 链接！
2. **两步提取阿里云 Kubernetes Worker 节点 RAM 临时凭证**：
   - 第 1 步：传入 `http://100.100.100.200/latest/meta-data/ram/security-credentials/`，从返回的公开 OSS 文件读取绑定的 RAM 角色名称（如 `KubernetesWorkerRole-...`）。
   - 第 2 步：传入 `http://100.100.100.200/latest/meta-data/ram/security-credentials/<RoleName>`，直接从返回的 OSS 链接下载完整的 `AccessKeyId`、`AccessKeySecret` 与 `SecurityToken`。

### 4. 第四阶段：认证令牌设计缺陷、OAuth 回调与 BOLA 矩阵排查

1. **审计邮件激活与密码重置 URL 中的 JWT 载荷**：
   - Base64 解码注册确认与密码重置链接中的 `token`（JWT Payload），检查服务端是否违规将用户明文 `password` 或 `argon2id` 密码哈希直接编码进 URL Token 中（导致邮件网关、Referer 与浏览器历史直接泄露凭据）。
2. **审计 OAuth 回调页 `postMessage` 目标源配置**：
   - 检查 `/auth/callback` 前端脚本是否调用 `window.opener.postMessage(payload, "*")` 无差别向任意打开者窗口广播 OAuth `access_token`。
3. **排查流式续传、大模型补全与内容分享的 BOLA 越权**：
   - 重点测试 `/api/v2/chat/agent-stream/continue`（跨用户对话续传越权）、`/api/v2/llm/completions`（越权消耗配额）以及 `/api/v2/share`（遍历递增整数 `content_id` 强制公开任意用户的私有笔记并读取全文）。

---

## Pitfalls

### 1. 陷阱一：拿到含 `../` 的 OSS 预签名 URL 后直接用普通客户端发包，因 `403 SignatureDoesNotMatch` 误判漏洞不存在

- **踩坑现象**：成功通过 `biz: "../../../deploy"` 拿到了包含 `/notegpt/web/../../../deploy/test.txt` 的阿里云 OSS 预签名 URL，但用 Python `requests.put(url)` 或普通 `curl` 发送时恒定返回 `403 SignatureDoesNotMatch`，AI 反复尝试不同路径层数均失败。
- **破局方案**：服务端是在**未折叠 `../` 的原始字符串**上计算的 OSS 签名；而标准 HTTP 客户端在发包前会自动将 URL 路径归一化为 `/deploy/test.txt`，导致 OSS 服务端收到的请求路径与签名不一致！必须使用 `curl --path-as-is` 或在代码中构造 `req.URL.Opaque = rawPath` 保持原始含 `../` 的路径原样发出。
- **止损红线**：遇到预签名 URL 报 `SignatureDoesNotMatch` 时，第一次失败即检查客户端是否归一化了 `../`，严禁盲目修改业务参数重试。

### 2. 陷阱二：在带 WASM 签名的接口上硬跑字典 Fuzz

- **踩坑现象**：对 `/api/v1/upload/sign-url` 直接修改 `biz` 或 `filename` 参数，网关因 `s` 签名不匹配直接拒绝，消耗数十次无效请求。
- **破局方案**：凡请求头或查询串带 `s`/`sign` + `t` + `nonce`，必须先暂停接口 Fuzz，下载前端 `.wasm` 逆向还原签名函数，用脚本实时重算签名后再发包。

### 3. 陷阱三：在异步音视频/文档转换盲 SSRF 上死磕云元数据读取

- **踩坑现象**：`/api/v1/docx-convert?type=2`、`/api/v1/web-convert`、`/api/v2/audio-extract/start` 虽存在 SSRF 且能触发调试栈泄露，但它们会对响应内容做格式解析（如校验是否为合法 DOCX/音频），无法原样回显 `100.100.100.200` 的 JSON 凭证。
- **破局方案**：在转换类接口仅提取内网探测结论与 Traceback 框架信息即可止损；读取云元数据 JSON 必须切换到「不做内容格式校验、直接将响应字节流转存 OSS」的接口（如 `/api/v2/upload-screenshot`）。

### 4. 陷阱四：测试私有笔记越权时只看 GET 详情接口，漏掉「强制分享 (`/api/v2/share`)」曲线越权

- **踩坑现象**：直接 GET 他人私有笔记详情被鉴权拦截，便认为笔记读取不存在越权。
- **破局方案**：测试 `POST /api/v2/share` 等状态变更接口——传入递增的整数 `content_id` 即可将任意他人的私有笔记强制转为公开分享状态并获得 `share_id`，随后通过公开分享链接直接读取完整私有内容（同时还可通过 `/api/v2/notes/shared` 导入触发跨用户存储型 XSS）。

---

## Verification

1. **最小无害化证明纪律**：
   - 验证 OSS 预签名目录穿越时，仅上传无害纯文本标记文件（如 `poc-check.txt`），**严禁覆盖线上真实的部署脚本、静态资源或业务数据**。
   - 通过 SSRF 获取阿里云 `100.100.100.200` RAM 临时凭证或通过 `/api/v1/oss/sts-token` 获取 STS Token 后，仅做只读身份核验（如 `sts:GetCallerIdentity`），严禁枚举删除存储桶内生产数据。
   - 验证 `/api/v2/share` 越权时，仅用自建测试账号的 `content_id` 完成验证闭环，严禁批量遍历生产用户 ID。
2. **查阅支撑文件**：
   - 完整 WASM 签名逆向流程、未归一化 HTTP PUT 发送代码与 8 大踩坑详表，请读取 `references/pitfalls-and-bypasses.md` 与 `references/attack-chains-and-pocs.md`。
