---
name: case-web-payment-sandbox-false-positive
description: 任务复盘《目标 https://overchat.ai/web》渗透报告（未完成）：支付环节"无需鉴权即可不花钱开通高级套餐"为【误报·已撤回】——判定"支付沙盒的正常行为"而非越权漏洞的四因子门禁（部署环境 / 支付通道模式 / 商品 SKU / 真实影响）、真·支付绕过对照判据（civpn 案）、误报当杠杆污染下游结论的止损纪律与降级留证写法；关键词：支付绕过 误报 假阳性 测试沙盒 sandbox staging testMode cs_test_ pk_live_ apple_pay_test_ 未支付即开通订阅 结算与权益发放解耦 403 付费权益层 支付流程绕过 复核 订阅套餐 权益发放
---

# 目标 https://overchat.ai/web · 任务复盘手册 · 支付沙盒误报专项与真实越权链（case-web-payment-sandbox-false-positive）

> **技能定位**：本手册来自 ARTEX 外部渗透报告《overchat.ai 渗透报告（未完成）》的真实复盘。
> 第一价值不在"这个站有哪些漏洞"，而在**一条被 AI 误判为漏洞、并被当作杠杆使用后污染了下游结论的"支付绕过"误报**。
> 第二价值是本案中经复核确认成立的真实越权链（手法可直接复用）。
> **最重要的一条纪律**：**"未支付即拿到付费权益" ≠ 支付绕过漏洞**。绝大多数此类现象发生在支付沙盒 / 预发 / 测试 SKU 上，是为让 QA 走通流程而【刻意设计】的正常行为。**先过门禁，再谈漏洞。**

## 0. 三十秒版结论

- 报告中的 `[MEDIUM] staging 未支付即开通付费套餐（Apple Pay Test 零支付订阅发放，结算与权益发放解耦）` —— **撤回，不成立**。
  它是 staging 支付沙盒里一条**测试 SKU** 的正常发放行为：**测试通道（`cs_test_`）+ 测试套餐码（`apple_pay_test_1`）+ 预发域名（`staging-api.overchat.ai`）+ 无真实资金流 + 只影响测试者自己的账号** ⇒ 没有越权、没有资损、没有受害者。
- 这次误报的**实际损害不在报告里，而在推理链里**：它被当成"打开 403 付费权益层"的钥匙，随后"付费层已开、因此上游错误回显如何如何"的结论全部挂在这个钥匙上。**一条假结论会污染整条下游链**。
- 判定纪律见【门禁 G1–G6】；真·支付绕过的对照样例见【第 3 节】。

## 1. 案卷速览：被误报的那条 vs 真实支付绕过应有的样子

| 维度 | 本案被误报的"支付绕过" | 真实支付绕过应有的样子 |
| --- | --- | --- |
| 目标主机 | `staging-api.overchat.ai`（**预发**） | 生产域名 |
| 支付通道 | Stripe **test 模式**会话 `cs_test_b18XxBufUvrb54l17KTcQvIr5CVNqOraZF2DTaf2JfijcMnZbtIqR59DML` | live 通道（`cs_live_…` / `pk_live_…`） |
| 商品 SKU | `planCode=apple_pay_test_1`（**测试件**） | 真实价目表 SKU |
| 资金流 | 无（测试会话不产生真实扣款） | 真实金额被绕过 / 不对称 |
| 受益主体 | 测试者自己的 staging 账号 | 他人账号 / 规模化套利 |
| 受害方 | 无 | 站方资损 / 上游算力被白嫖 |
| 复现后果 | 一次性、+7 天测试权益 | 可重复、可无限续期 |
| 判定 | **误报，撤回** | **真漏洞** |

原报告记录的现象（作为反例事实保留，不作为漏洞）：
```
POST /v1/billing/buy-subscription {"subscriptionCode":"apple_pay_test_1","placement":"web","ui":"hosted","returnUrl":"https://staging-api.overchat.ai/"}
→ 201 {"url":"https://checkout.stripe.com/c/pay/cs_test_b18XxBufUvrb54l17KTcQvIr5CVNqOraZF2DTaf2JfijcMnZbtIqR59DML#..."}
GET /v1/billing/stripe-session?session_id=cs_test_b18X... → 200 {"payment_status":"unpaid","customer_id":"cus_VTXOXtWuJMlNZg","user_id":"f17ddd16-..."}
GET /v1/billing/subscription → 200 {"planCode":"apple_pay_test_1","status":3,"stripeId":"sub_1USaPbGWUebql6H99HbSZLPb","paidThroughPeriodEnd":"2026-10-27T10:36:55.000Z"}
POST /v1/billing/checkout/sync-package {"sessionId":"cs_test_b18X..."} → 201 {"ok":false,"reason":"session_not_paid"}
```
关键观察（决定了判定的**决定性旁证**）：**生产前端确认 Stripe 为 live 真实模式，无测试模式可复刻**（`pk_live_51TKGJqGWUebql6H9XYzhx7huu87JL5QUGbhI7oarA5Stur9o39SDWnOuTzXiOpyssk0tsBnZcCcmZxaiIVlpOhPp00ldyLirN1`，前端 `eu()→"PRODUCTION"`）⇒ 该测试套餐在生产**本来就不存在、也不需要存在**，"结算与权益发放解耦"只是测试通道的正常语义。

## 2. 核心方法：支付类"免费拿到权益"四因子门禁（G1–G6）

### 2.1 为什么支付沙盒特别容易骗过 AI（本案机制复盘）

现象表面上完全符合经典漏洞模板：
- `buy-subscription` 返回后，订阅行**立即被改写为激活态**：`status=3`、`paidThroughPeriodEnd=+7 天`、`hasEverSubscribed=true`；
- 同一会话 `GET /v1/billing/stripe-session` 却回显 `payment_status="unpaid"`；
- `POST /v1/billing/checkout/sync-package` 判定 `session_not_paid` **却不回滚权益**；
- 403 付费权益层随之被打开。

"结算说没付钱、权益却发了" ⇒ 看上去就是教科书级的 **结算与权益发放解耦**（INV-01 / INV-08）。

掉坑的真正原因是**把"真钱链路的状态语义"套到了"测试链路上"**：
在 test 通道里，`payment_status=unpaid` 与 `session_not_paid` **不是缺陷，是必然**——测试会话根本不会产生支付确认事件，服务端在测试 SKU 上提前发权益，是为了让 QA 能进入付费态继续测下游功能。
**"状态机矛盾"只有在矛盾的两端都属于同一条真实业务链路时才是缺陷。**

第二个放大器：**杠杆心理**。这个发现恰好"打开了 403 付费层"，直接服务于任务主线（取上游凭据），于是它被当成重要战果；而一条帮不上忙的发现往往会被更严格地质疑。**有用不等于成立**——定级必须先于用途。

### 2.2 门禁 G1–G6（照着做，别凭感觉）

**G1 · 部署环境因子**
- 目标主机是**生产**还是预发/沙盒/预览/演示？判据：域名形态（`staging` / `dev` / `test` / `preview` / `sandbox` / `demo`）、证书、OpenAPI 里的 `base_url`、请求头 `x-project`、页面内环境标识。
- **本案**：`staging-api.overchat.ai`（生产是 `api.overchat.ai`）。
- 规则：现象**仅在非生产环境**成立 ⇒ 降级为「测试环境配置/暴露问题」，**不是生产越权漏洞**。若预发可达真实上游或真实用户数据，改报「预发环境暴露」——那是另一条链路，理由要换。

**G2 · 支付通道模式因子**
- 结算会话 / 订单 / 订阅 / 支付方式的前缀是否表明测试模式？Stripe：`cs_test_` / `pi_test_` / `sub_test_` / `in_test_` / `ch_test_`；PayPal：`sandbox`；或平台自研网关的 `testMode` 字段。
- 生产同名端点走的是什么模式？（`cs_live_` / `pk_live_`）
- **本案**：`cs_test_b18X…`；生产 `pk_live_…`，**无测试模式可复刻**。
- 规则：全程 test 会话 ⇒ **"未收到支付确认"是语义空转**（该事件在测试通道里永不发生），不能作为缺陷证据。

**G3 · 商品 SKU 因子**
- 订阅的套餐码 / 价格 ID 本身是不是测试件？命名含 `_test_` / `sandbox` / `free_trial` / `demo`，或价目表里查无此 SKU。
- **本案**：`planCode=apple_pay_test_1` —— 名字里就写着 `test`。
- 规则：测试 SKU 发放的"权益"是**假权益**，它的发放/回收/续期不构成任何业务资产损失。

**G4 · 真实影响因子**
- 真实支付绕过的**成立要件（缺一不可）**：
  1. **生产环境**；
  2. **live 支付通道 / 真实价目表 SKU**；
  3. 权益是**真实可消耗资产**（真上游算力、真金额、真额度、真时长、可用资源）；
  4. **可重复 / 可规模化**，或可作用于**他人账户** ⇒ 存在实际损失（资损、越权、服务滥用）。
- 若 1 / 2 / 3 任一不成立 ⇒ **不是支付绕过漏洞**；最多是「测试配置不当」或「预发暴露」类运维问题。

**G5 · 对照取证（必做，不能只读响应体）**
- 生产端点同款调用走的是 live 还是 test？生产是否有同名 `planCode`？
- "该行为是刻意设计"的**旁证**在哪？OpenAPI 的 `summary` / `description`、前端是否内置该测试套餐入口、是否只在 CI/内部可见、运营文档。
- **本案**（决定性旁证）：生产前端确认 Stripe live、无测试模式可复刻 ⇒ staging 的 `apple_pay_test_1` 不可能也不需要在生产复刻 ⇒ 该行为是**测试夹具**，不是缺陷。
- 若找不到任何"刻意设计"的旁证，才允许继续往缺陷方向走。

**G6 · 杠杆污染检查（本案的实际教训）**
- 一旦把某发现当作"打开门禁的钥匙"，**所有基于它的结论都要挂上依赖标记**。
- 判定被推翻后，**必须回头重开**那些下游结论重新验证，而不是只删掉这一条。
- 本案原报告即在此处失守：它写下"finding #133 已经打开了 403 付费权益层……此前意图给出的『错误回显已彻底净化』『上游面被 403 门禁挡住』等结论是在【付费层未开】时取得的，属假阴性：在已激活订阅的 staging 会话上……"——把**沙盒开关**当成了**上游情报的门禁**，整段推理建立在一个测试权益上。
- 规则：**发现被降级/撤回时，其下游结论默认作废**，逐条重验。

### 2.3 止损纪律（3-Strike）

- **Strike 1**：查生产同名端点的通道模式（live vs test）→ 已足以定性的，**到此为止**。
- **Strike 2**：查 SKU 是否测试件 / 价目表是否收录 → 仍指向沙盒，**判定误报，收工**。
- **Strike 3（禁止）**：不要为了"让报告更饱满"或"找生产复刻路径"继续放大影响面、反复验证、或把下游结论往上搭。**沙盒性质一旦成立，扩大规模不会改变性质，只会让误报更可信。**

## 3. 真 vs 假支付绕过：必须能区分，而不是一律怀疑

### 3.1 判据要点（一句话版）

> **"测试模式"出现在哪里，决定了它的性质。**
> `预发环境 + 测试 SKU + cs_test_ 会话` ⇒ **沙盒的正常行为**；
> `生产环境 + 真实 SKU + 真实价格` ⇒ **真漏洞（生产残留 testMode）**。

### 3.2 反例（真阳性对照）：civpn.com 案（见 Skill `case-web-logic-flaw-bypass-payment-24`）
- **生产**域名下的**生产**支付网关 `pay.vpngap.com`，根路径直接回显 `{"service":"VPNGap Pay","version":"1.0.0","testMode":true}` —— **生产残留 testMode**，这是真缺陷。
- **真实 SKU + 真实价目表**：`plan_id=5`「24TB」，订单按原价 `3699` 记账，而提交金额被篡改为 `money=0.01`、`sign` 全零仍被接受。
- **可重复且无上限**：自建账号 `expired_at` 一路从 2026 推到 2036，连续 6 轮，余额全程为 0 ⇒ 站方真实资损。
- 与本案并列看，差别一目了然：**同是"测试模式"三个字，落点在生产真实 SKU 上就是漏洞，落点在预发测试 SKU 上就是沙盒。**

### 3.3 同案对照：**形状一样、判定相反**的两条（防止一刀切地"见免费就怀疑"或"见免费就放过"）
| 条目 | 现象 | 环境/成本 | 判定 |
| --- | --- | --- | --- |
| 被撤回的 staging `apple_pay_test_1` | 零支付拿到付费权益 | 预发 / 测试 SKU / 假钱 / 无受害方 | **误报** |
| 生产 `POST /v1/prompt/enhance` | 未订阅、未计费的游客即可拿到真实上游模型输出，且余额与负债无变化 | **生产 / 真上游算力 / 站方承担成本** | **真 MEDIUM**（付费门禁覆盖不全） |
| 生产 `POST /v1/chat/completions` | 匿名即可调用上游并原样透传 OpenRouter 响应体（含 `"provider":"OpenAI"`、`id=gen-<unix>-<hash>`） | **生产 / 真上游算力** | **真 MEDIUM** |

**结论**：同样是"免费访问"，判定完全取决于**环境 + 成本归属 + 受害方**三件事。**不要因为一次误报就学会"支付类都别信"，也不要因为"免费"就放过生产链路上的免费算力。**

## 4. 误报的正确留证写法（降级，不要删除）

被判为沙盒的行为，**不能简单删掉**（会被后来者重新"发现"并重复踩坑），应**降级并留证**：

```
[INFO][已撤回·非漏洞] staging 支付沙盒 apple_pay_test_1 测试套餐零支付发放（预期行为）
判定依据（G1–G5）：预发域名 staging-api.overchat.ai（G1 命中）｜会话前缀 cs_test_（G2 命中）｜
SKU 名含 _test_ 且不在生产价目表（G3 命中）｜仅影响测试者自有账号、无资金流无受害方（G4 命中）｜
生产前端确认为 Stripe live 模式、无测试模式可复刻（G5 命中）
处置：不可作为漏洞上报；不可作为"付费层已开启"的杠杆；基于其推导的下游结论需重新验证
```
这样后续任何 agent/人翻到这条时，看到的是"**已复核·非漏洞**"，而不是"AI 说这是漏洞"。

## 5. 本案经复核成立的真实漏洞（速查，手法见 `references/attack-chains-and-pocs.md`）

- **CRITICAL** 生产 `POST /v1/auth/apple/mobile` 不验 Apple idToken 签名 ⇒ `alg=none` 伪造 JWT 登录/创建任意账号（等价任意邮箱 ATO）。同款伪造 token 打 Google 路径恒 401（对照取证）。
- **CRITICAL / HIGH** `GET /v1/s3/download?url=` 零鉴权用站点自有 AWS 凭据签发 SigV4 预签名 URL；**忽略输入 host、只取 path 当 S3 key** ⇒ 站点自有桶任意 key 盲签 + 存在性 oracle（404 `NoSuchKey` vs 200）。
- **HIGH** `widget-api.overchat.ai` 匿名可读全站用户 PII（`/v1/users`、`/v1/chats`、`/v1/personas/paginated`、`/v1/canvas`、`/v1/dashboards`），会话总量报告为 `1808365` 条。
- **HIGH** 生产 `GET /v1/chat/{userId}/{chatId}` 及其 `/messages`、`/media` 未做归属校验 ⇒ 匿名 BOLA 读取他人完整对话。
- **HIGH** 生产 `POST /v1/chat/upload` 无 bearer 亦 201 ⇒ 匿名写生产桶（与预签名读回构成"匿名写 + 匿名读"闭环）；OpenAPI 却声明 `security:[{"bearer":[]}]`。
- **HIGH** `overchat-light`（eu-north-1）/ `amigopanda`（us-east-1）桶对象匿名直读，跨用户读取上传的 PDF/DOCX/PNG。
- **HIGH** 预发 `POST /v1/auth/google/mock` 免鉴权，且隐藏字段 `code` 按 `<code>@overchat.ai` 反查用户库 ⇒ 可铸造既有账号（含 `admin@overchat.ai`）的有效 HS256 JWT（生产同路径 404 ⇒ 影响面限预发）。
- **MEDIUM** `widget-api.overchat.ai` 把 `x-device-uuid` 客户端头当唯一身份凭据（缺头 401，带他人 deviceId 即 200 读其档案与订阅）⇒ 配合上条 PII 泄露形成"公开 PII → 身份冒充 → 读他人账户"ATO 读链。
- **MEDIUM** `api.vixio.ai`（≡ `staging-api.amigochat.io`，同一 NestJS 进程）匿名暴露 `/metrics`（92626 字节 Prometheus 全量内部路由面）、`/health`、`/echo`、`/v1/config/global`、`/v1/models/list`。
- **MEDIUM** 生产匿名调用上游 + `/v1/prompt/enhance` 缺订阅与额度门禁 + 匿名提示词注入泄露服务端 system prompt。

## 6. 复用清单（下次遇到"免费拿到付费权益"照着做）

1. **先看域名**：生产还是预发？→ G1
2. **再看会话/订单前缀**：`cs_test_` / `pi_test_` / `testMode` / `sandbox`？→ G2
3. **再看套餐码**：名字含 `test`/`sandbox`/`trial`？价目表里有吗？→ G3
4. **再问影响**：真钱吗？真算力吗？别人的账号吗？能重复吗？→ G4
5. **再做对照**：生产同名端点走 live 还是 test？有没有"刻意设计"的旁证？→ G5
6. **任一命中 ⇒ 不是支付绕过漏洞**，降级留证、停止论证。
7. **如果已经用它当过杠杆** → 逐条回头重验下游结论，别只删这一条。
8. **反过来也要用同一把尺**：生产链路上"未订阅/未计费却能吃真上游算力"（本案 `/v1/prompt/enhance`）**是真漏洞**，不要放过。

## 7. 包内参考文件

- `references/pitfalls-and-bypasses.md`：误报深度复盘（本案为什么"诱人"）、G1–G6 门禁的完整检查单、真/假支付绕过对照表、同案对照、杠杆污染机制、本案其余 13 类踩坑（部署差分、客户端头当凭据、预签名预言机、假阴性外推、mock 路由定域）与 3-Strike 止损矩阵。
- `references/attack-chains-and-pocs.md`：本案**经复核成立**的六条真实攻击链（alg=none JWT 伪造、零鉴权 SigV4 预签名器 + 存在性 oracle、widget 匿名 PII → deviceId 冒用、预发 mock 认证、匿名写生产桶、生产匿名上游调用 + 提示词注入）与被撤回的支付沙盒负例。

> 复用前必须重新核对授权范围、目标版本与环境差异；本案中大量端点位于预发环境，生产行为可能已加固。
