# 自主 AI 攻击防御与检测指南

> 本指南帮助防御者了解 ARTEX 等自主 AI 渗透代理，建立检测与阻断能力，不是攻击教程。仅用于保护自有或书面明确授权系统；未授权探测/攻击他人信息通信网络可能违法，参见[根 README 的安全告知](../README.md)。
>
> English: **[Defense and Detection Guide](defense-en.md)**.

自主 AI 工具将人工渗透流程变为可持续运行的自动过程：LLM 分解目标、执行真实工具、积累发现。防御者面对的不再只是单个操作人员，也可能是持续工作的代理群；本指南整理检测与响应所需变化。

---

## 1. 自主 AI 攻击与传统扫描器的区别

传统固定签名扫描器按预设项目依次检测。自主代理结合[系统架构](../README.md#系统架构)中的组件，支持无人介入的多阶段链路：

- **多角色代理：** goals 分解目标，planner 是唯一 intent 生成者，多 worker 并行执行，mainagent 提供人工交互。
- **双图积累状态：** 资产图记录“有什么”，探索图记录“测到哪里”，通过锚点连接；可从不同角度重访、基于旧观察推进。
- **事件闭环：** 图变更唤醒 planner，worker 写入又触发下一轮，向目标证明迭代。
- **有序链路：** planner 在共享 todolist 记录依赖，仅为前置完成的步骤分配 intent，避免无状态会话使顺序漂移。
- **上下文变体：** 工具与载荷并非固定常量，LLM 按环境生成，请求形态可变化。

### 检测为何更难

- **固定签名容易漏检：** 按上下文变化的请求可能绕过精确恶意字符串匹配。
- **可低速且间歇：** 轮间暂停、依据发现改向，仅看短时请求量不足。
- **侦察与后续动作连续：** 人工分析间隔缩短，首次接触到数据外流可更快。

### 仍可检测：行为比静态指纹难隐藏

UA 和固定字符串可以修改，但多阶段自适应行为更难完全改变。第 2–4 节重点采用行为视角。

- 同一来源以连续会话完成性质不同的多阶段请求。
- 在人工通常会暂停的时段仍持续探索。
- 依据失败响应系统性调整请求，而非随机变异。

---

## 2. 防御者可观察的指纹（IoC 与签名）

区分**目标侧**收到的流量与**运营者/取证侧**实际运行或中转主机的证据，不能混用。目标侧静态指纹有限，行为关联更关键。

### 目标侧：面向本系统的流量

- **补全 UA artex-enrich/1.0：** enrich/enrich.go 在 DNS/HTTP 补全直接发送 GET，非 LLM 生成；不跟随重定向、关闭 keep-alive、只读响应前部提取 title，默认并发 4。多资产短连接、单 GET、读取标题即断开是线索，但 UA 可改，缺失不代表安全。
- **主测试流量遵循工具默认：** worker 执行 Bash 外部工具或 HTTP，平台只注入 HTTP_PROXY/CA 环境尝试记录，不强制独有 UA。工具默认或自定义头可类似浏览器，不能仅靠 UA，须结合行为。
- **内置 WebFetch 留下 norma/0.4：** norma SDK 的默认 UA，不等同 Bash 外部工具。可在明文或 TLS 终结处观察，[Suricata sid 1000003](../detections/suricata/README.ko.md)匹配 norma/ 前缀。其他 SDK 使用者也有该 UA，只是辅助线索，缺失不排除 ARTEX。
- **无目标专用内置限速：** 速率由外部工具决定，默认每任务 3 worker 并行，既可能低速单会话，也可能多角度并发，单一阈值不足。
- **关键行为为组合出现：** 同一/少量轮换来源短间隔跨侦察、枚举、参数探测与认证/注入阶段；同端点请求按状态码/长度系统调整；非工作时段长时连续；401/403/429 后仍持续变换尝试。

### 运营者与取证侧：运行 ARTEX 的主机

用于调查疑似安装或执行 ARTEX 的中转主机。

- **默认 :8787：** main.go --addr 可改；该端口出现仪表盘/任务/资产图 UI 是线索。
- **记录 MITM 127.0.0.1:8788：** --proxy 默认 loopback，生成 mitmproxy-ca-cert.pem 以解密记录。检查信任 CA 及注入 HTTP_PROXY/CA 的痕迹。worker.go 固定变量名，代理与 mitmproxy CA 同时存在比单端口更特异；[triage](../detections/triage/README.ko.md)读取 /proc 或离线环境转储。
- **更新 UA artex-selfupdate：** selfupdate/ 查询 GitHub Release 的出站流量辅助指示二进制存在。
- **PostgreSQL 双图：** exploration_nodes、assets、companies、activity 及 agent_prompts 种子是数据结构线索。
- **DB 规则与审计：** intercept/ 按优先级匹配 DB 正则；GET /api/audit 中被阻止调用以 `【ARTEX 平台管控·非目标防御】` framing 记录，是运行证据。
- **破坏性命令狩猎：** rm -rf、mkfs、dd of=/dev/、shred/wipe、DROP DATABASE/TABLE、TRUNCATE、MongoDB drop/dropDatabase、Redis FLUSHALL/FLUSHDB、curl/wget -X DELETE 等是通用线索。`curl … | nc …` 属于外流而非破坏；平台因正常逆向连接/传输误报默认关闭此规则，[部署狩猎规则](../detections/sigma/destructive_command_hunting.yml)也仅含破坏范围，不含该管道。外流须单独研判调优，不能直接等同恶意或立即阻断。

> 目标侧以行为为主，artex-enrich/1.0、norma/0.4 为辅助。端口、更新 UA、DB 与审计 framing 主要用于实际运行/中转主机的取证，不能误当目标侧指标。

### 为什么 IP 阻断只是较弱的第一道防线

事件后常出现社区分享的“攻击 IP 清单”。不建议直接把来源不明、未经验证的非官方清单加入防火墙，尤其面对可轮换来源的自主代理。

- **难以验证来源：** 无可靠采集依据，可能混入无关地址。
- **很快过期：** VPN、云实例、受控中转可改变来源，阻断昨日地址不等于停止今日行为。
- **误封风险：** 共享网段/CDN/正常云 IP 可能伤及客户或内部服务，自动阻断会加速扩大影响。

IP 阻断有用的前提是可信机构/情报来源、有效期审查和误封评估；它仍是追踪轮换起点的临时措施。持续有效的是第 2–4 节行为检测和第 3、5 节攻击面缩减、补丁、MFA。

---

## 3. 常见入口与加固

自主代理针对与人工相同的弱点，但更持续、更快速重复；以下按防御优先级加固。

### 3.1 外部暴露面与已知 n-day 漏洞

最稳定入口常是暴露且未修补的已知漏洞，而非复杂 0-day：VPN/防火墙、管理控制台、应用服务器/中间件/框架如 WebLogic、Struts，以及合作、招聘、员工辅助系统。资产枚举与公开 PoC 的自动化匹配可早于补丁覆盖，形成速度不对称。

- **缩减暴露面：** 持续维护互联网资产/控制台/辅助系统清单，不必外露的置于内网、VPN 或允许列表之后。
- **迅速修补：** 优先边界、Web、应用与中间件的已知漏洞，特别是公开 PoC 流行组件。以持续更新的 [CISA KEV](https://www.cisa.gov/known-exploited-vulnerabilities-catalog)为优先级输入，交叉查看第 7.1 节韩国 KISA 官方建议，而非把某个 CVE 写死。
- **限制必要入口：** 不可关闭的管理界面加来源限制、MFA、VPN，减少未认证枚举。
- **同等管理辅助系统：** 合作/招聘/员工渠道保持与主服务相同补丁与监控水平；认证流见 3.2。

已知漏洞 URL/参数的短间隔连续请求是 n-day 扫描线索，结合第 2 节行为与第 4 节同来源多阶段关联更有效。

### 3.2 辅助认证与身份核验流程

附加、合作、招聘等不同认证路径可能校验较弱。代理可枚举并通过响应差异寻找条件，因此必须全量审查而不只保护主入口。

- 辅助与主服务认证强度一致，审查全部可替代路径。
- 服务器重新验证未登录→登录、普通→特权等状态转换，不信任客户端 cookie/header/参数标记。
- 审查身份 token/一次性码的寿命、重用与可猜测性。

### 3.3 API 认证与授权（IDOR、权限提升）

- 所有对象访问强制服务器所有权/权限检查，防止仅换 ID 读取他人资源。
- 全面审查横向与纵向权限边界；自动化标识符遍历可能发现单次人工测试遗漏的问题。

### 3.4 凭据填充（credential stuffing）

泄露账号/密码的重用尝试可能被自主代理的速度与分布放大。

- 登录/核验接口按 IP、账户、设备和行为实施自适应限速。
- 敏感操作强制 MFA，不能只依赖密码。
- 对照可信泄露清单，监控异常登录位置/速度。
- 对失败/成功分布变化，包括低速但广泛尝试，设置告警。

### 3.5 会话、令牌与秘密管理

- 最小化会话范围、寿命与刷新能力，敏感转换重新认证。
- 不在响应、日志或错误暴露 API key/内部 token；代理会利用错误线索。

---

## 4. 检测规则与日志模式（实践）

以下为产品无关伪规则，须映射到自身 WAF/IPS/SIEM。源码有依据的静态规则及补全速率/目标数、guard burst、guard 与破坏命令同主机共现等关联见 [Sigma](../detections/README.ko.md)。纯 Web 多阶段行为无独有 UA，4.2 提供通用模板。网络 UA 见 [Suricata](../detections/suricata/README.ko.md)：artex-enrich/1.0 的存在性 sid 1000001、速率 sid 1000002，norma/0.4 的 sid 1000003。

### 4.1 WAF 与 IPS（行为检测）

- 同来源会话中枚举/参数探测/认证请求高占比、静态资源低占比时增加风险分。
- 按状态码/正文长度系统性调整而非随机的连续请求增加权重。
- 已知自动化 UA 如 artex-enrich/1.0 可标记高风险，但缺失不表示安全。

### 4.2 SIEM 关联规则

- **同来源多阶段：** 同 IP/ASN/会话在短窗内同时出现端点枚举、参数探测、认证/注入尝试。
- **时间异常：** 单会话长时间连续，偏离正常业务时段分布。
- **失败后持续：** 收到 403/429 仍反复调整请求。

以下单文件含三条子规则与 temporal 关联，同客户端在窗口内满足全部三阶段才触发。它是**通用行为模板**，没有 ARTEX 指纹，区别于 detections/sigma/ 的源码固定规则。

```yaml
# ── 通用行为模板，并非 ARTEX 独有签名 ──
# 第 4.2 节同来源多阶段 Web 行为：枚举 → 探测 → 认证。
# 不以 ARTEX 源码固定指标为依据，区别于 detections/sigma/。
# 字段名采用 SigmaHQ webserver 分类；阈值和窗口须按本地日志调优。
# 自包含：三条子规则，以及同客户端在窗口内全部满足的 temporal 关联。
title: Web Endpoint Enumeration Burst From One Source
id: f03c360c-dc33-4a8a-afa8-821b1ff5c4e3
status: experimental
description: |
    Stage 1 of the same-source multi-stage pattern in the ARTEX defense guide section 4.2: a
    burst of endpoint or directory enumeration from a single client, seen as a high rate of 404
    and 400 responses in a short window. This is generic behaviour, not an ARTEX-specific
    signature; tune the count and window to your own baseline. On its own this leg is low signal
    and earns weight only inside the correlation below.
references:
    - https://github.com/jiwoochris/artex-ko/blob/main/docs/defense-ko.md
    - https://github.com/jiwoochris/artex-ko/blob/main/docs/defense-en.md
author: artex-ko defense guide (generic template)
date: 2026-10-07
tags:
    - attack.reconnaissance
    - attack.t1595
logsource:
    category: webserver
detection:
    enum_misses:
        sc-status:
            - 404
            - 400
    condition: enum_misses
falsepositives:
    - Broken links, authorised vulnerability scanners, or misconfigured clients that generate
      many 404 responses.
level: low
---
title: Web Parameter Or Path Injection Probe From One Source
id: f9296e55-6b7a-4030-b5f7-5f7b146233be
status: experimental
description: |
    Stage 2 of the same-source multi-stage pattern: parameter or path probing, matched here as
    query strings carrying common injection or traversal markers. This leg is unavoidably
    signature-like and noisy on its own, so it is scored low and earns weight only inside the
    correlation below. Extend the marker list to your own probe corpus and WAF categories; it is
    a coarse proxy for the broader "adapts requests to responses" behaviour the guide describes.
references:
    - https://github.com/jiwoochris/artex-ko/blob/main/docs/defense-ko.md
    - https://github.com/jiwoochris/artex-ko/blob/main/docs/defense-en.md
author: artex-ko defense guide (generic template)
date: 2026-10-07
tags:
    - attack.initial-access
    - attack.t1190
logsource:
    category: webserver
detection:
    probe_markers:
        cs-uri-query|contains:
            - '../'
            - "' or "
            - ' union select '
            - '<script'
            - '; drop '
    condition: probe_markers
falsepositives:
    - Legitimate request payloads that resemble probe markers; tune the marker list to your
      application.
level: low
---
title: Authentication Or Identity-Verification Attempt From One Source
id: 2614cacb-7455-46ad-9af2-7b9633f12d7b
status: experimental
description: |
    Stage 3 of the same-source multi-stage pattern: requests to login, authentication, or
    identity-verification endpoints, or 401 and 403 responses. Map the paths and your own
    authentication-event fields to your application; auxiliary, affiliate, and broker channels
    often expose weaker identity-verification endpoints than the main service and belong here
    too. This leg is broad by design and is only meaningful inside the correlation below.
references:
    - https://github.com/jiwoochris/artex-ko/blob/main/docs/defense-ko.md
    - https://github.com/jiwoochris/artex-ko/blob/main/docs/defense-en.md
author: artex-ko defense guide (generic template)
date: 2026-10-07
tags:
    - attack.credential-access
    - attack.t1110
logsource:
    category: webserver
detection:
    auth_path:
        cs-uri-stem|contains:
            - '/login'
            - '/auth'
            - '/verify'
            - '/otp'
    auth_deny:
        sc-status:
            - 401
            - 403
    condition: auth_path or auth_deny
falsepositives:
    - Ordinary users signing in; this leg is broad and only meaningful inside the correlation.
level: low
---
title: Same-Source Multi-Stage Web Attack (Enumeration, Probe, Auth)
id: 9b7c7b86-702f-42b8-be99-3e60a188ec5b
status: experimental
description: |
    The behaviour-based core of ARTEX defense guide section 4.2 as a deployable template: one
    client runs endpoint enumeration, parameter or path probing, and an authentication or
    identity-verification attempt within the same short window. This is the pattern an autonomous
    agent drives at machine speed and keeps driving past 403 and 429 responses. It is UA-free and
    carries no ARTEX fingerprint, so it is a GENERIC behavioural rule, not one of the
    ARTEX-source-grounded rules under detections/sigma/. Normalise the client field (c-ip, or a
    session identifier if you have one) and tune the window to your baseline. If three legs are
    too strict and miss cases, relax to any two of the three.
references:
    - https://github.com/jiwoochris/artex-ko/blob/main/docs/defense-ko.md
    - https://github.com/jiwoochris/artex-ko/blob/main/docs/defense-en.md
author: artex-ko defense guide (generic template)
date: 2026-10-07
tags:
    - attack.initial-access
    - attack.t1190
correlation:
    type: temporal
    rules:
        - f03c360c-dc33-4a8a-afa8-821b1ff5c4e3
        - f9296e55-6b7a-4030-b5f7-5f7b146233be
        - 2614cacb-7455-46ad-9af2-7b9633f12d7b
    group-by:
        - c-ip
    timespan: 10m
falsepositives:
    - An authorised vulnerability scan or QA run from a single source; allow-list its address.
level: high
```

使用模板时注意：

- 原文验证基线使用与检测包相同工具：全 SigmaHQ check 零错误/问题，Splunk 查询把三阶段按 c-ip、10 分钟聚合。因不是 ARTEX 源码固定指标，故不纳入 detections/ 规则树。[模板门禁](../detections/tests/check-defense-template.sh)在指南变更时由 CI 复查 check、splunk/eql/loki 转换、lucene/kusto 关联拒绝及中英文规则主体字节一致，防止上游升级令承诺过期。
- 原文 sigma-cli 3.1.0 基线中，全模板可转换为 splunk/eql/loki；kusto/lucene 报 `Backend does not support correlation rules`。后两者可仅转换三子规则，在产品原生功能表达同 c-ip、10 分钟窗口，例如 Sentinel summarize/bin(TimeGenerated, 10m)。见 [后端可移植性](../detections/README.ko.md#sigma-后端可移植性)。本次语言重构未重新执行这些容器验证。
- 第二阶段依赖注入/遍历标记，单独噪声较高，故子规则 low、关联 high。
- CDN/代理后需将 c-ip 映射为可信转发链恢复的客户端或会话标识；若三阶段过严，可按本地验证放宽为三选二。

### 4.3 认证日志

- 账户/IP 失败率突变、低速广泛账户尝试、异常快速失败→成功转换。
- 身份核验/一次性码接口的枚举式访问。

### 4.4 出站流量与取证

- 内部主机以 artex-selfupdate UA 访问代码仓库。
- 绑定 :8787 管理 UI、127.0.0.1:8788 记录代理的进程。
- artex-enrich/1.0 短时批量查询外部资产的 DNS/HTTP 补全模式。

---

## 5. 加固检查清单

防御组织可据此逐项审查。

- [ ] 登录/身份核验/敏感 API 自适应限速。
- [ ] 敏感操作强制 MFA。
- [ ] 每个对象访问有服务器所有权/授权检查。
- [ ] 状态转换服务器重验，不信任客户端标记。
- [ ] 辅助/合作/招聘渠道身份验证与主服务一致。
- [ ] 持续检测、对照泄露凭据。
- [ ] WAF 使用行为检测，不仅固定签名。
- [ ] 不直接应用来源不明 IP 清单，可信 IoC 也审查有效期/误封风险。
- [ ] SIEM 同来源多阶段关联。
- [ ] 认证/访问/出站日志充分保留。
- [ ] 维护暴露资产清单、缩减攻击面、迅速修补边界/应用/中间件 n-day。
- [ ] 网络分段限制横向移动与权限提升影响范围。
- [ ] 响应/日志/错误不暴露秘密。
- [ ] 准备经风险评估的自动阻断/隔离，避免全依赖人工批准。

---

## 6. 事件响应摘要

自主代理可将原本长期的探测与外流压缩到更短时间，响应设计必须考虑这种速度。

- **预备自动处置：** 对高置信信号按既定策略隔离来源、失效会话、降低速率，不把所有动作都置于人工等待；评估误封并留回滚。
- **事先定义日志：** 认证、访问（合规允许的正文范围）、出站、DNS，为事后链路重建保留证据。
- **按资产追踪范围：** 重建入口到横向移动/权限提升的完整路径，不仅修补首次入口。

### 6.1 疑似主机与流量研判流程

分为“本服务是否被针对”与“某主机是否运行过 ARTEX”。不以单一命中定论，结合多指标与行为；静态指标缺失但行为异常时继续调查。

**目标侧：本服务是否被针对**

1. 查访问/认证日志的 artex-enrich/1.0，是否多资产短间隔、无重定向单 GET。UA 可改，未命中仍继续。
2. 查同一/少量轮换来源的侦察→枚举→探测→认证/注入链，是否按响应调整、401/403/429 后持续。
3. SIEM 部署适用的 [Sigma 关联](../detections/README.ko.md)并结合通用 Web 模板；达到本地高置信条件时按响应策略隔离来源/失效会话，不能将主机侧 guard 指标误作目标侧流量。

**主机取证：某主机是否运行过 ARTEX**

依据第 2 节及 [CSV](../detections/indicators/artex_indicators.csv)，先用只读 [triage](../detections/triage/)检查端口、出站/guard 日志、状态/代理存储；第五项通用破坏命令不属于独有指纹，需人工对照历史。无 SIEM 时也可执行；所有命中只是线索。

1. **端口：** 本机检查默认 :8787 与 127.0.0.1:8788。
   ```sh
   ss -ltnp | grep -E ':8787|:8788'   # 无 ss 时使用 netstat -ltnp
   ```
   --addr/--proxy 可改，结果为空仍检查全部监听与内部管理 UI。
2. **出站：** selfupdate/ 的 artex-selfupdate 查询 GitHub Release。
3. **审计：** guard/guard.go 的 `【ARTEX 平台管控·非目标防御】`，被阻止工具调用带此 framing。
4. **存储：** PostgreSQL exploration_nodes/assets/companies/activity 与 agent_prompts；--data 默认可执行文件旁数据目录，含 tasks/、transcripts/。server/manager.go 将 traffic/ 用作记录存储，CA 在 traffic/_ca/mitmproxy-ca-cert.pem，索引在 traffic/_index/index.sqlite，正文在 traffic/_blobs/；组合出现较强地支持代理运行。
5. **命令审计：** 对照 rm -rf、DROP DATABASE、FLUSHALL、外流管道等通用狩猎线索，正常管理员也会执行，不单凭此定论。

端口、UA、标记均可修改/清除，缺失不等于安全。结合目标侧行为与主机侧证据才是研判核心。

---

## 7. 韩国官方渠道与报告义务（原文法律背景）

本节保留上游韩国适用背景，不是中国法律指引。韩国组织应通过可信官方渠道取得建议并遵守当地报告义务；其他地区按所在地法律及专业意见处理。

### 7.1 官方指标与安全建议

- **KISA/韩国互联网振兴院 [KrCERT/CC](https://www.boho.or.kr)：** 安全建议、漏洞与响应信息。C-TAS 为成员共享体系，需单独申请，不等同公开门户。
- **[金融安全院 FSI](https://www.fsec.or.kr)：** 金融 ISAC 跨机构威胁信息。
- **[韩国个人信息保护委员会](https://www.pipc.go.kr)：** 数据泄露报告与保护措施指引。

第 5 节可信 IoC 来源包括这些渠道；即使官方指标也须审查有效期与误封风险，不能跳过第 2 节所述评估。

### 7.2 韩国法律报告义务

事先把报告流程纳入第 6 节响应计划。以下仅翻译原文摘要，准确适用对象、条件与期限须核对最新官方规定并征询法律意见。

- **个人信息泄露：** 原文依据韩国《个人信息保护法》第 34 条，在满足报告条件时，知悉后 72 小时内向委员会或 KISA 报告并通知主体；条件包括 1,000 人以上、敏感/唯一识别信息或非法外部访问等。
- **网络入侵事件：** 原文依据韩国信息通信网络相关法律，服务提供者知悉后 24 小时内向科学技术信息通信部及 KISA/KrCERT/CC 报告。
- **金融机构：** 还可能须向金融监督院/FSI 单独报告，核对行业规则。

韩国事件渠道为 [KrCERT/CC](https://www.boho.or.kr)或 118（KISA）；个人信息泄露见[个人信息门户](https://www.privacy.go.kr)。事先记录负责人、联系方式和适用期限。

---

## 参考

- 原始项目：[Autumn-27/ARTEX](https://github.com/Autumn-27/ARTEX)，AGPL-3.0；2026-10 本环境 GitHub API 返回 404，表示当前不可见，不证明永久删除。
- [根 README 安全与使用范围](../README.md)。
- 本指南由原本韩语维护材料改为简体中文；韩国法律背景如上保留。未授权探测在多数辖区违法，先取得书面授权及约定范围。
- 通用加固标准：[OWASP Top 10](https://owasp.org/www-project-top-ten/)、[ASVS](https://owasp.org/www-project-application-security-verification-standard/)、[API Security Top 10](https://api-security.owasp.org/)。

> 欢迎通过仓库 issue 建议检测规则、加固项目与指南补充；本文用于提升防御和检测能力。
