---
name: reverse-engineering
description: 全栈逆向工程专家手册（前端 JS/Webpack/Vite/SourceMap 拆包、API 签名 sign/nonce/timestamp 与加密算法逆向、WASM/Protobuf/WebSocket 协议分析、PE/ELF/归档包静态逆向）。在遇到前端加密加签、混淆 JS、二进制文件或私有协议时调用。
---

# 全栈逆向工程专家手册 (reverse-engineering)

## 任务域分流（先判断，禁止串线）
若任务域为 software_re，**仅使用本节的软件静态分析工作流**，不要自动进入下方 Packet-First、改包或漏洞利用流程。
1. 用 binary_inspect(file_path=...) 读取样本身份、SHA-256、格式与基础头部。该工具不执行样本，也不反编译函数；unknown/unsupported 不代表文件安全。
2. 依据实际工具清单确认 REA/Ghidra/IDA/JADX 等 Provider 与目标架构支持；没有相应工具就记录环境缺口并停止盲猜。
3. 围绕用户指定的功能定位字符串引用、导入、函数、调用链和类型；每次只查询相关片段，不通读整个反编译结果。
4. 证据写入 record_fact：哈希、模块、地址类型与值、工具参数/输出；observed 表示看到的输出，行为解释和类型猜测使用 inferred。
5. 静态证据不足时列出待验证假设。执行、附加、Hook、安装或修改样本/分析库须另获授权；不得因任务目标存在就假定授权。
6. 在任务工作目录交付 Markdown 软件报告，说明功能位置、算法/协议/类型、证据与限制；普通软件结论无需上报漏洞。

下方保留的 Web 流程只用于 web_security 或明确的 Web 签名分析请求。


本技能包基于顶级安全专家的 **Packet-First（抓包反推）6 步逆向法**，将前端 JS/WASM 加密签名逆向、隐藏路由/权限树提取以及二进制/固件/归档包分析与内置 Go 原生工具（`web_recon` / `extract_data` / `codec_crypto` / `http_request` / `net_probe` / `Grep` / `Read`）深度整合。

---

## When to Use

- 接口请求包含 `sign`、`signature`、`x-signature`、`nonce`、`timestamp`、`encryptedData` 或请求体整体加密，直接改包返回 `签名错误 / 非法请求` 时。
- 前端采用 Webpack / Vite / Next.js 打包混淆，需快速还原源码（SourceMap）、提取全量隐藏路由、权限树（`perm_tree`）或硬编码密钥时。
- 目标使用 WebSocket 自定义帧、Protobuf、GraphQL 持久化查询（Persisted Queries）或 WASM 模块处理核心逻辑时。
- 下载或接触到 `.zip` / `.tar.gz` / `.jar` / `.apk` / `.whl` 归档包及 PE / ELF 二进制程序，需免解压提取敏感配置、符号表、加密常量或分析协议格式时。

---

## Procedure

### 1. 前端与 API 签名逆向：Packet-First 六步工作流

遇到请求加签或加密时，**严禁从零通读几兆字节的 `vendor.js`！** 必须严格按以下 6 步由果索因：

1. **Step 1 — 还原 SourceMap 与收割异步 Chunk（1 次原生调用）**：
   - 调用 `web_recon(mode="sourcemap", url="<目标站点URL>", output_dir="<当前工作目录>")`：自动探测 `.js.map`（含 `SourceMap` 响应头、行尾 `sourceMappingURL` 及常见路径）并还原原始 TypeScript/JavaScript 目录结构。
   - 若无 `.map`，调用 `web_recon(mode="spa_harvest", url="<目标站点URL>", output_dir="<当前工作目录>")` 下载全部首屏与异步懒加载 chunk。
2. **Step 2 — 精准锚定加签入口（`Grep` 1 秒定位）**：
   - 使用原生 `Grep` 在下载的 JS 目录中搜索独特特征词（优先级从高到低）：
     1. 自定义 Header 名（如 `X-Sign`、`X-Signature`、`X-Nonce`、`X-Request-Id`、`client-version`）；
     2. 拦截器关键字：`interceptors.request.use`、`Authorization`、`HmacSHA256`、`createHash`、`SubtleCrypto`、`crypto.subtle`、`AES.encrypt`、`pkcs7`、`serialize`；
     3. 特定 API 路径后缀（如 `/login-with-browser-check`、`browser_fingerprint`）。
3. **Step 3 — 提取上下游 40 行代码切片还原算法**：
   - 用 `Read` 读取命中位置前后 40~80 行，理清三要素：
     - **待签串拼接规则（Canonical String）**：参数按字典序排序？`key=value&...` 还是 `method + path + timestamp + body + secret`？
     - **标准密码学原语**：MD5 / SHA256 / HMAC-SHA256 / AES-CBC / AES-GCM / RSA-PKCS1v15？
     - **密钥/盐（Secret/IV）来源**：静态硬编码常量、登录态动态下发、还是由用户信息派生？
4. **Step 4 — 使用原生 `codec_crypto` 零脚本验算签名**：
   - 拿一个真实抓到的成功请求（从 `traffic_search` / `traffic_get` 获取），把它的原始字段代入你还原的拼接公式，直接调用 `codec_crypto`（如 `operation="hmac_sha256"` / `"sha256"` / `"aes_cbc_encrypt"` / `"aes_gcm_encrypt"`）。
   - **验算门禁**：只有当你算出的签名/密文与真实抓包中的值 **100% 字节级一致** 时，才算逆向成功！若不一致，立刻回查大小写、URL 编码、空值剔除或时间戳精度（秒 vs 毫秒），禁止带着错误签名去盲目发包。
5. **Step 5 — 批量提取硬编码凭据与隐藏管理路由**：
   - 调用 `extract_data(mode="js_secrets", file_path="<js文件>")` 提取 AK/SK、JWT、API Key、Webhook、内网地址。
   - 调用 `web_recon(mode="route_map")` 与 `web_recon(mode="perm_tree")` 提取前端角色门禁，识别前端隐藏但后端实际暴露的 `/admin/*`、`/internal/*`、`/debug/*` 接口。
6. **Step 6 — 挂载重放与改包攻击**：
   - 用 `codec_crypto` 对篡改后的越权/注入参数重新计算合法签名，再通过 `http_request` 或 `http_sequence` 发送，彻底穿透前端签名防护。

---

### 2. 二进制、归档包与协议逆向流程

当目标是本地二进制（PE/ELF）、Java `.jar`、Python/Node 包或压缩归档时：

1. **免解压透视归档结构**：
   - 调用 `extract_data(mode="archive_list", file_path="<压缩包或jar路径>")` 列出所有条目、大小与修改时间。
   - 重点关注：`application.yml` / `.env` / `config.json` / `web.xml` / `*Controller.class` / `*.proto` / `*.wasm`。
   - 调用 `extract_data(mode="archive_extract", file_path="...", pattern="...")` 按需提取关键文件到当前工作目录。
2. **静态特征与字符串提取**：
   - 对二进制或大数据文件调用 `extract_data(mode="regex", file_path="...", pattern="[\\x20-\\x7E]{6,}")` 或特定正则提取可读字符串、URL、SQL 语句、格式化字符串、魔数（Magic Bytes）与硬编码密钥。
3. **私有 TCP / WebSocket 协议交互探测**：
   - 对自定义 TCP 端口或二进制协议，调用 `net_probe(mode="raw_http")` 发送自定义字节/Hex 报文并读取原始回包；
   - 对 WebSocket 端点，调用 `net_probe(mode="websocket", url="wss://...")` 完成握手、发送探针帧并解析服务端推送。

---

## Pitfalls

1. **严禁未验证签名一致性就盲目改包 Fuzz**：若逆向出的签名算法连原始正样本都无法复现，后续所有改包请求都会在网关验签层被直接丢弃（返回固定 401/403），造成无意义的 Token 浪费。
2. **严禁直接把整个混淆后的 `app.xxx.js`（数 MB）读入上下文**：必须先用 `web_recon` 或 `Grep` 缩小到具体文件与行号，再用 `Read` 指定 `offset` 和 `limit` 读取局部代码段。
3. **不要被前端路由守卫欺骗**：前端 `if (user.role !== 'admin') return redirect('/403')` 只是 UI 遮挡，提取出其底层调用的 `/api/v1/admin/...` URL 后，直接用普通用户的 Token 调 `http_request` 测后端是否真正校验了角色。

---

## Verification

- 逆向结论写回 `record_fact` 时，必须附上 **① 算法公式**、**② 密钥/IV 具体值及所在 JS 文件名与行号**、**③ 正样本验算一致的对照证据**，以便后续任何 Worker 直接复用该签名规则。
