---
name: web-vuln-tactics
description: Web 核心漏洞深度挖掘与绕过专家手册（BOLA/IDOR 越权矩阵、登录/MFA/密码重置/OAuth 状态机绕过、SSRF 与云元数据、并发条件竞争 Race、强类型框架注入快速证伪）。在测试 API 鉴权、业务逻辑、越权提权或参数注入时调用。
---

# Web 核心漏洞深度挖掘与专家绕过手册 (web-vuln-tactics)

本技能包将顶级安全专家在 Web/API 渗透中的**槽位定性、最小探针验证、高频绕过手法与及时止损纪律**固化为标准作业流，并优先绑定内置 Go 原生工具（`http_request` / `http_sequence` / `http_fuzz` / `web_recon` / `codec_crypto` / `extract_data`）。

---

## When to Use

- 发现业务 API 端点（用户资料、项目/团队/租户资源、订阅/计费/套餐、管理后台接口）需测试水平/垂直越权（BOLA / BFLA / IDOR）时。
- 测试登录、注册、邮箱验证、MFA/2FA 绑定与步进验证（Step-Up Auth）、密码重置、OAuth/SSO 回调等身份认证状态机时。
- 需要判断某个输入参数是否存在 SQLi / NoSQLi / SSTI / 命令注入 / SSRF，避免在强类型框架上盲目穷举 Payload 时。
- 测试优惠券核销、积分划转、免费额度扣减、异步任务触发、邀请码兑换等有状态操作的并发条件竞争（Race Condition）时。

---

## Procedure

### 1. 第一步：技术栈与输入槽位定性（Tech-Stack & Slot Triage）

在发送任何攻击载荷前，先收集首包响应头、错误体结构与状态码作为待核实的技术栈线索，不把格式相似当作机制证明：

| 响应特征 | 技术栈/校验层线索（待验证） | 专家决策（打什么 / 停什么） |
|---|---|---|
| `422 Unprocessable Entity` + `{"detail":[{"type":"string_type","loc":["body","..."]}]}` | **观察到类似参数校验的错误格式，技术栈尚待验证** | **立即停止**对该字段做类型混淆（已观察到的类型变化被拒绝）和大量语法型 SQLi 变体；若测试 2~3 个字符串探针恒返回固定字节数的 `500` 或 `400` 且耗时无差异，仅支持停止低收益重复尝试，不证明 ORM 参数化查询；**立即转向业务逻辑与跨账号凭据错配**。 |
| `400 Bad Request` + `JSON parse error: Cannot deserialize value of type...` | **类似 Spring/Jackson 绑定错误；需独立验证** | 关注 Mass Assignment（额外传 `role`/`isAdmin`/`tenantId`）、SpEL 表达式点、Actuator 端点及权限注解绕过（末尾 `/`、`;`、路径矩阵参数）。 |
| `200 OK` + `{"errors":[{"message":"...","locations":[...]}]}` | **可能的 GraphQL 响应结构** | 直接调用 `web_recon(mode="graphql")` 提取 Schema 全量 Query/Mutation，重点测未授权 Mutation、嵌套深度、Batching 数组绕速率限制与 IDOR。 |
| 包含 `x-amz-*` / `supabase` / `postgrest` / ` Hasura` 响应头 | **可能的 BaaS/PostgREST/Supabase 线索** | 重点测 JWT `aal`（`aal1` vs `aal2`）门禁遗漏、Anon Key 直接调 `/rest/v1/<table>` RLS 策略缺失、`/auth/v1/user` 元数据自篡改（`user_metadata` vs `app_metadata`）。 |

---

### 2. BOLA / BFLA / 多租户隔离穿透（最高产出方向）

**严禁只测未授权（不带 Token 返回 401 就放弃）！** 还应在授权范围内检查**已认证低权限会话 A 访问会话 B 资源**或**低级别令牌调用高权限动作**：

1. **双会话矩阵重放（使用 `http_sequence` 的 `bola` 模式）**：
   - 准备两个独立测试账号（或同租户普通成员 vs 管理员，或 `aal1` 令牌 vs `aal2` 令牌，或 `email_verified=false` 令牌 vs 正常令牌）。
   - 调用 `http_sequence(mode="bola", url=..., auth_a={"Authorization":"Bearer <Token_B_or_Low>"}, auth_b={"Authorization":"Bearer <Token_A_Owner>"})`。
   - **判定金标准**：对比两组响应的状态码、JSON 核心数据键与文本相似度（Similarity `> 0.85` 且包含账号 A 私有数据即确认越权；绝不可仅凭 `200 OK` 空列表误报）。
2. **复合标识符与间接引用绕过**：
   - 若路径 UUID 鉴权严格，检查请求体 JSON 内是否同时携带 `user_id`、`org_id`、`team_id`、`project_id`、`workspace_id`（路径用自己的 ID 过网关鉴权，Body 填受害者 ID 触发业务读写）。
   - 检查批量接口（`POST /batch`、`DELETE /items?ids=1,2`、导出接口 `/export`、克隆/转移接口 `/transfer`），往往单条接口有归属校验而批量/导出接口漏检。

---

### 3. 认证、MFA 与密码重置状态机攻击链

针对登录、MFA、密码重置流程，按以下 6 条专家检验清单逐项验证（每项仅需 1~2 次请求，调用 `http_sequence(mode="chain")`）：

1. **AAL 降级与备选登录入口绕 MFA**：
   - 当账号开启 MFA 后，对比主登录口（`/auth/login`）与旁路登录口（如 `/browser-auth/login-with-browser-check`、`/oauth/token`、`grant_type=refresh_token`、设备码 `/oauth/device`、Magic Link）。
   - 解码返回的 JWT（调用 `codec_crypto(operation="jwt_decode")`）检查 `aal` 声明，并用 `aal1` 令牌实测核心业务面（特别是写操作 `POST/PUT/DELETE` 与异步任务触发口），确认是否存在“读接口拦了 `aal1` 但某个写接口/子服务漏拦 `aal1`”。
2. **跨账号凭据错配（Cross-Account Token Binding）**：
   - 账号 A 申请密码重置 Token / 邮箱验证码 / MFA `factor_id` + `challenge_id`，在确认请求中携带**账号 B 的 Bearer Token + 账号 A 的重置/验证参数**（或反之），观察是否导致跨账号绑定或接管。
3. **JWT 签名与声明篡改**：
   - 调用 `codec_crypto(operation="jwt_none")` 测试 `alg: none`；
   - 调用 `codec_crypto(operation="jwt_crack")` 跑内置高频弱密钥字典；
   - 检查 Header 是否含 `jwk` / `jku` / `kid`（路径遍历 `../../dev/null` 加空密钥签名）。
4. **注册与资料修改的批量赋值（Mass Assignment）**：
   - 在 `POST /register`、`PATCH /profile`、`PUT /users/me` 请求 JSON 中注入 `{"role":"admin","is_admin":true,"plan":"pro","credits":99999,"email_verified":true}`，观察响应或再次 `GET /auth/me` 是否生效。

---

### 4. 并发条件竞争（Race Condition / TOCTOU）

凡涉及**“先检查状态/余额/次数，再扣减/发放/绑定”**的单次动作，一律使用原生单包同步屏障齐射验证：

- **调用方式**：`http_sequence(mode="race", race_count=15, steps=[{"method":"POST","url":"...","headers":...,"body":...}])`。
- **重点目标**：
  - 免费试用/积分兑换/红包领取/邀请奖励接口；
  - 一次性备份码（Backup Codes）生成或核销、一次性重置 Token 消费；
  - 创建受配额限制的资源（如免费版最多创建 1 个项目/1 个团队/1 个 API Key，并发 15 个请求看是否创建出多个）。

---

### 5. SSRF 与云元数据探测

凡遇到 `webhook_url`、`avatar_url`、`import_url`、`callback`、`endpoint`、` baseUrl`（如自定义模型 Provider 测试连接）、PDF/截图渲染等服务端外联参数：

1. 先用 `net_probe(mode="oast_callback")` 或无害内外网地址差分量测响应报错信息与耗时；
2. 探测环回与云元数据：`http://127.0.0.1:<port>`、`http://169.254.169.254/latest/meta-data/`（AWS）、`http://metadata.google.internal/computeMetadata/v1/`（GCP）、`http://100.100.100.200/latest/meta-data/`（阿里云）；
3. 若存在域名白名单或内网 IP 过滤，仅测 **3 种正交绕过**：`302` 重定向跳转、IP 十进制/八进制/IPv6（`[::ffff:127.0.0.1]`）、DNS Rebinding。若 3 种均被网络层阻断则立即止损。

---

## Pitfalls

1. **严禁在强类型或固定错误接口上跑 50 个 SQLi/编码变体**：同一参数测试 **3 个正交探针**（单引号语法破坏、布尔/时间延迟、类型变化）若均返回完全相同的静态错误体且无时延差分，**基于预算与已得证据停止同类低收益尝试，记录未验证范围，而非宣称不存在问题**。
2. **严禁编写临时 Python 脚本发包或做并发爆破**：单发请求一律用 `http_request`，多步链/BOLA/并发竞争一律用 `http_sequence`，字典探测一律用 `http_fuzz`。
3. **严禁把信息级差异当高危漏洞上报**：例如“不存在的 Token 返回 500、已过期的 Token 返回 400”单凭该差异不足以证明高危，应结合可复现影响；先记入 `record_fact` 即可，不得调用 `report_finding`。

---

## Verification

调用 `report_finding` 前必须通过以下验证：
- 具备完整的真实请求与响应证据（优先带 `http_request` 自动返回的 `exchange_id`）；
- 越权漏洞必须证明返回了**另一账号的私有业务数据**或成功修改了另一账号的状态；
- 注入/SSRF 漏洞必须有明确的数据回显、布尔可复现差分或带外回调证据。
