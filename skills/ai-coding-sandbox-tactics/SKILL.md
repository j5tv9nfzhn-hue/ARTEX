---
name: ai-coding-sandbox-tactics
description: AI 编程沙箱、GPU 推理控制面与 BYOK Agent 渗透及防踩坑专家手册（源自 free.ai 实战报告沉淀）。涵盖受限沙箱 RCE 突破、renameat2(RENAME_EXCHANGE) 符号链接 TOCTOU 宿主机读写穿透、共享 UID 与环境变量内部密钥接管、BYOK 伪造 tool_calls 反向劫持 Agent 及 3-Strike 止损纪律。
---

# AI 编程沙箱、GPU 控制面与 BYOK Agent 渗透防踩坑手册 (ai-coding-sandbox-tactics)

> **核心定位**：本技能包由 `free.ai` 完整渗透与漏洞复盘报告深度提炼而成。在测试 AI Coding 在线沙箱、GPU 模型调度网关与支持 BYOK（自带模型密钥/地址）的 Agent 平台时，**最终漏洞产物只是结果，真正决定成败的是如何绕过沙箱白名单、静态路径检查与鉴权假象，并避免在盲 SSRF 或单次静态软链接上陷入死循环**。

---

## When to Use

- 目标提供在线代码执行、AI 编程工作区（如 `/coder/session`、`/coder/command`、`/coder/file-save`、`/coder/session/{sid}/download`）、Jupyter/容器终端或文件打包下载功能时。
- 目标包含 AI 模型推理路由、GPU 状态监控（如 `/v1/gpu/status/`、`/v1/gpu/reset/`）、内部计费扣费（`/api/internal/verify/`、`/api/internal/deduct/`）或多服务共享密钥架构时。
- 目标支持用户自定义模型配置（BYOK，如 `byok_base_url`、`byok_api_key`）、RAG 知识库检索（`/v1/context/query/`）或 AI 图像/音视频媒体处理接口时。
- **包内深度参考文档**：
  - `references/pitfalls-and-bypasses.md`：8 大实战踩坑陷阱、失败根因与底层绕过方案对照表（必读）
  - `references/attack-chains-and-pocs.md`：6 阶段完整突破推导链与脱敏验证探针模板

---

## Procedure

### 1. 第一阶段：沙箱会话生命周期与受限命令执行突破

1. **先盘点沙箱全生命周期接口**：定位会话创建（`POST /coder/session`）、命令执行（`POST /coder/command`）、文件读写（`POST /coder/file-save`）与打包导出（`GET /coder/session/{sid}/download`），优先测试不带 `Authorization` 头时的匿名可访问性。
2. **双路径突破 `safe_mode` 命令限制**：
   - **路径 A（显式参数降级）**：在创建会话或执行命令请求体中显式传入 `"safe_mode": false`，直接关闭命令白名单守卫。
   - **路径 B（复合命令分隔符注入）**：当处于默认安全模式、仅放行白名单前缀（如 `ls`、`python`、`cat`）时，测试在合法前缀后拼接 `; id`、`&& env` 或换行符——许多轻量级沙箱仅用前缀匹配校验第一个 Token，随后整串交由 `/bin/sh -c` 执行。

### 2. 第二阶段：沙箱隔离边界定性与 `renameat2` TOCTOU 宿主机穿透

1. **先做进程与 UID 边界定性（严禁盲目打内核提权）**：
   - 拿到命令执行后立即检查 `id`、`ps aux`、`/proc/self/cgroup` 与 `ls -ld ..`：确认沙箱是独立容器还是直接以宿主机普通用户（如 `uid=1001(free)`）运行在宿主机文件系统中。
2. **利用 `renameat2(RENAME_EXCHANGE)` 原子交换突破路径校验**：
   - 当导出下载（Zip 打包）或文件保存接口存在前置 `realpath`/`lstat` 软链接拦截时，在沙箱内编译运行 C 竞争程序，调用 `syscall(SYS_renameat2, AT_FDCWD, "normal_dir", AT_FDCWD, "symlink_target", RENAME_EXCHANGE)` 在合法目录与符号链接之间高速原子切换：
     - **宿主机任意文件读取**：竞争 `GET /coder/session/{sid}/download`，穿透读取 `/proc/self/environ` 与宿主机后端源码配置（如 `/free/gpu_api/config.py`）。
     - **宿主机与跨沙箱任意文件写入**：竞争 `POST /coder/file-save`，将文件以宿主机进程 UID 写入其他租户沙箱目录或宿主机 `/home/<user>/`。

### 3. 第三阶段：环境变量与内部共享密钥横向接管控制面

1. **从 `/proc/self/environ` 与宿主机配置提取四类核心凭证**：
   - 内部服务间共享密钥：`WEB_SERVER_SECRET`、`TRUSTED_SERVICE_SECRETS`（如 `content_engine`、`code_arena` 等服务令牌）。
   - 第三方高价值付费 API Key：`OPENROUTER_API_KEY`、`FALAI_KEY` 等。
   - 云基础设施元数据：探测 `http://169.254.169.254/metadata/v1/`（DigitalOcean/AWS/阿里云实例元数据与 VPC 内网拓扑）。
2. **使用内部 Header 反打主站计费与 GPU 控制面**：
   - 携带 `X-Internal-Secret: <WEB_SERVER_SECRET>` 验证内部用户校验与余额扣减接口（`/api/internal/verify/`、`/api/internal/deduct/`）及 GPU 模型重置接口（`/v1/gpu/reset/{model_id}/`）。
   - 携带 `X-Trusted-Service: <TRUSTED_SERVICE_SECRET>` 调用模型对话接口，验证配额与付费墙绕过。

### 4. 第四阶段：BYOK 模型端点劫持与跨会话 BOLA 串联

1. **BYOK 自定义模型网关双向利用**：
   - 将 `byok_base_url` 指向受控的 Mock OpenAI 服务，一次请求即可截获服务端完整 System Prompt（如 5.9 KB 内部提示词）与内部工具定义。
   - 在 Mock 响应中返回伪造的 `tool_calls`（如调用 `run_command` / `write_file`），验证服务端 Agent 是否无条件信任并执行外部模型下发的工具调用。
2. **从公开状态接口收集活跃 UUID 串联未授权 BOLA**：
   - 轮询 `/v1/gpu/status/` 等公开监控接口提取其他用户正在运行的 `session_id` / `request_id`，再代入未授权的 `/coder/*` 或 `/v1/context/query/` 接口实现跨租户接管。

---

## Pitfalls

### 1. 陷阱一：静态软链接被拒后直接放弃，或反复静态创建不同软链接

- **踩坑现象**：在沙箱内 `ln -s /etc/passwd leak` 后调用 `/coder/session/{sid}/download`，服务端发现是软链接直接跳过或报错；AI 连续换 5 个目标文件名静态软链接均失败。
- **破局方案**：服务端在打开文件前做了单次 `lstat`/`realpath` 校验，但校验与实际 `open()`/`write()` 之间无文件描述符绑定（非 `openat2(RESOLVE_NO_SYMLINKS)`）。必须改用 `renameat2(RENAME_EXCHANGE)` 原子交换正常目录与软链接并并发请求接口。
- **止损红线**：静态软链接测试失败 1 次即停止静态尝试，立即切入 TOCTOU 条件竞争或转查进程权限。

### 2. 陷阱二：被 `safe_mode` 首词白名单欺骗，在受限命令里盲猜二进制参数

- **踩坑现象**：`/coder/command` 报错提示当前处于安全模式仅允许特定命令，AI 试图只用单个白名单命令的参数读取全盘。
- **破局方案**：先检查 `POST /coder/session` 请求体是否支持 `"safe_mode": false`；其次测试 `ls; id` 或 `python3 -c "..."`——白名单往往只检查命令字符串前缀而未禁用 Shell 元字符 `;`。

### 3. 陷阱三：把 `byok_base_url` 当作普通内网盲 SSRF 跑端口扫描

- **踩坑现象**：发现 `byok_base_url` 可发起请求后，用它慢速遍历内网端口，既慢又容易超时，还漏掉了最高危的 Agent 反向劫持。
- **破局方案**：`byok_base_url` 是大模型客户端调用点！第一优先级是指向受控 HTTP 服务完整接收一次 POST 请求以获取 System Prompt 与工具 Schema；第二优先级是返回构造好的 `choices[0].message.tool_calls` JSON 反向驱动服务端 Agent 执行命令。

### 4. 陷阱四：在无回显媒体处理盲 SSRF 端点死磕读取文件内容

- **踩坑现象**：图像放大、视频合成、3D 生成等 7 个媒体接口接收图片/音频 URL，但只返回处理失败或二进制结果，无法直接回显文本型元数据；AI 在这些接口反复尝试读取 `/etc/passwd` 或云元数据。
- **破局方案**：媒体 URL 盲 SSRF 仅用 DNSLog/HTTP 回调确认出网与内网连通性即可止损；读取宿主机文件与云元数据应优先走沙箱命令执行、TOCTOU 下载归档或全回显接口。

### 5. 陷阱五：因 `session_id` 是 UUIDv4 认为无法利用 BOLA

- **踩坑现象**：发现 `/coder/session/{sid}` 等 6 个接口完全无鉴权，但因 `sid` 为随机 UUIDv4 便判为低危不可利用。
- **破局方案**：检查全局状态、GPU 队列监控（`/v1/gpu/status/`）、公开排行榜等接口，确认是否实时回显活跃任务的 UUID，从而形成「状态接口泄露 UUID → 未授权 BOLA 接管活跃沙箱」的完整高危链条。

---

## Verification

1. **最小无害化证明纪律**：
   - 验证沙箱 RCE 与 TOCTOU 时，仅读取 `/proc/self/environ`、`id` 输出或写入无害标记文件（验证后清理），严禁修改宿主机关键系统文件或破坏其他用户会话。
   - 提取到 `OPENROUTER_API_KEY`、`FALAI_KEY` 等第三方付费凭证后，仅调用只读额度/密钥信息查询接口（如 `/api/v1/auth/key`）确认有效性与余额，**严禁发起消耗额度的生成请求**。
   - 验证 `/api/internal/deduct/` 或 `/v1/gpu/reset/` 时，优先使用不造成生产中断的只读/最小参数探测。
2. **查阅支撑文件**：
   - 完整 C 语言 `renameat2` TOCTOU 竞争模板、BYOK Mock `tool_calls` 响应结构与 8 大踩坑详表，请读取 `references/pitfalls-and-bypasses.md` 与 `references/attack-chains-and-pocs.md`。
