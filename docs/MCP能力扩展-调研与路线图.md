# MCP 能力扩展：调研与路线图（渗透 / 逆向双域）

> 状态：调研稿，未实现。日期 2026-10-20。
> 结论先行：**知识层（SKILLS）已经够厚，执行层（MCP）太薄**。14 个技能覆盖了"怎么想"，
> 4 个 MCP 服务器只覆盖了"怎么点"——资产测绘、静态反编译、封包改包、浏览器。
> 缺口集中在两处：渗透侧的**主动验证与利用**，逆向侧的**动态调试与样本分诊**。

---

## 一、现状实测（不是看目录，是读代码 + 查库）

### 1.1 已有的 MCP 服务器（线上库 `mcp_servers`，共 4 个）

| 名称 | 传输 | 工具数（缓存） | 启用 | 命令 / URL | 定义位置 |
| --- | --- | --- | --- | --- | --- |
| ScopeSentry | http | 17 | 否 | （URL 待填） | `db/mcp_builtins.go:18` |
| browser | stdio | 22 | **是** | `npx -y @playwright/mcp` | `db/mcp_builtins.go:38` |
| rea | stdio | 12 | 否 | 命令为空，需操作员填写 | `db/mcp_builtins.go:66` |
| wpe64 | stdio | 14 | 否 | 命令为空，需操作员填写 | `db/mcp_builtins.go:85` |

合计 **65 个可预览工具**。注意：这些只进 `mcp_tools_cache`，**不进 `tools` 表**
（线上库 `tools` 里 `mcp__%` 行数为 0）——MCP 工具是运行时装配的，所以它天然不在
`/api/tools/drift` 的观测范围内，**远端服务器换版本了我们看不出来**。

### 1.2 基础设施其实不弱（这决定了"加内容"很便宜）

| 能力 | 实现 | 位置 |
| --- | --- | --- |
| 三种传输 | `stdio` / Streamable `http` / 旧版 `sse` | `server/mcpdiscover.go:40` |
| 密钥不外泄 | `env` 里的 `enc:v1:` 密文在唯一出口解密；解不开直接失败 | `db/mcp_env.go`、`MCPEnvPlain` |
| TLS 自签 | `insecure` 开关（需要时可用，但应有可见提示） | `connectMCP` |
| 工具面控制 | `deferred` + `buildSmartSearchExtraTools` / `buildSmartExecuteExtraTool`：**不全量塞进 prompt**，先检索再执行 | `server/assembly.go:98-250` |
| 技能门控 | `skillByMCPServer`：某些 MCP 服务器要对应技能加载后才放开 | `server/assembly.go:240` |
| 逆向引擎抽象 | `REProviderConfig` + `reEnginePresets()`（ghidra / ida / rea）+ 10 个静态操作契约 + `RECapabilityReport`（缺工具、schema 不符都能报） | `server/software_re_presets.go` |

也就是说：**管道已经铺到"多引擎、可校验、可门控"的程度，缺的是往目录里放东西。**

### 1.3 知识层（SKILLS）对照：14 个，明显偏渗透与逆向方法论

```
api-recon                18.9K   接口测绘        scopesentry-mcp      16.6K  平台契约
web-vuln-tactics          8.9K   Web 漏洞战术    rea-mcp               4.5K  静态分析契约
exploit-kb-patterns       6.4K   利用知识库      wpe64-mcp             3.8K  封包契约
ai-gateway-logic         11.2K   AI 网关逻辑     mitmproxy-traffic     2.8K  流量取证
wasm-cloud-saas-tactics   9.5K   WASM/SaaS       playwright-cli       12.8K  浏览器自动化
expert-triage-pivot       5.0K   研判与转向      reverse-engineering   8.0K  逆向总纲
human-verification        8.3K   人工核验        ai-coding-sandbox-tactics 9.3K 沙箱编码
```

技能说得清"要做什么"，但模型**没有手**：想跑一次 nuclei、想看一次 x64dbg 栈、
想摸一次 Frida hook，都得靠 shell 手搓命令。这就是"MCP 只有几个简单逆向手写模块"
的真实体感来源。

---

## 二、外部生态（2026-10 实测检索）

### 2.1 规范层：MCP 已经换代

- 现行规范是 **2026-07-28**：无状态核心、Extensions 框架、**Tasks**（长任务）、
  **MCP Apps**（服务端渲染 UI）、OAuth/OIDC 加固，且**远程 MCP 强制 OAuth 2.1**
  ([dev.to 综述](https://dev.to/amareswer/best-mcp-servers-in-2026-one-pick-per-category-1mdg))。
- 生态规模：Glama 收录 2.2万–3.8万 个服务器；社区事实标准是"**3–7 个**服务器，
  再多就是 tool bloat，反而降低 agent 表现"
  ([aifloxium](https://www.aifloxium.online/blog/best-mcp-servers-2026))。

> 对我们的直接含义：**不要追求"装得多"，要追求"每域 3–5 个高覆盖服务器 + 工具面收敛"。**
> 现有 `deferred` / smart-search 机制正好是这个方向的正确实现。

### 2.2 安全现实：MCP 本身就是攻击面（我们必须正视）

- 约 7,000 个公开服务器中 **36.7% 存在 SSRF**、**41% 完全没有认证**、
  **53% 用长期静态密钥**、**只有 8.5% 用 OAuth**
  ([BlueRock/Astrix 统计](https://www.mcpbundles.com/blog/best-mcp-servers))。
- 2026 年 1–2 月 60 天内针对 MCP 服务器提了 **30 个 CVE**，其中 **43% 是
  shell/exec 注入**；MCPTox 基准里工具投毒对 o1-mini 成功率 **72.8%**
  ([Septim Labs](https://septimlabs.com/blog/best-mcp-servers-2026))。
- 反向证据：Google 的 Chrome DevTools MCP 默认开**使用统计**、性能工具会把 trace URL
  发给 CrUX API，除非显式关掉 ([agenticcontrolplane](https://agenticcontrolplane.com/mcp-controls/chrome-devtools))。

> 对 ARTE X 的含义：我们是**甲方**（装别人的 MCP 来干活），所以需要
> 版本钉住、来源分级、调用留痕、默认最小权限。这一条要写进架构，而不是事后补。

### 2.3 渗透域：可用的成熟服务器

| 项目 | 形态 | 关键能力 | 备注 |
| --- | --- | --- | --- |
| **PortSwigger MCP Server**（官方） | Burp 扩展，SSE `127.0.0.1:9876` + stdio 代理 | 发 HTTP/1.1 与 HTTP/2、Proxy 历史（含 WebSocket）正则过滤、Repeater/Intruder、Collaborator OOB、配置导出、编码工具 | 27 个工具（Community 24）；有"自动批准目标"与"配置编辑"开关 ([BApp Store](https://portswigger.net/bappstore/9952290f04ed4f628e624d0aa9dccebc)) |
| **BurpMCP-Ultra** / **N0K0/burp-mcp** / **six2dez/burp-ai-agent** | Burp 扩展 | 150 / 53 / 59 个工具；主动扫描、竞态、JWT、IDOR 批量、WebSocket 全生命周期 | 工具面更大，需自己收敛 |
| **HexStrike AI** | stdio/HTTP | 150+ 安全工具编排 | 12.7k★，覆盖面最广但黑盒 ([awesome-ai-security-tools](https://github.com/scadastrangelove/awesome-ai-security-tools)) |
| **FuzzingLabs/mcp-security-hub** | 38 个容器化服务器 | 侦察/Web/二进制/区块链/云/AD，300+ 工具 | 生产级加固，**容器隔离**这一条最值钱 ([awesome-ai-pentesting](https://github.com/skyvanguard/awesome-ai-pentesting)) |
| **MCP-Kali-Server** | 本机/容器 | nmap/hydra/sqlmap/metasploit/wpscan | 已进 Kali 官方源（`apt install mcp-kali-server`） |
| WireMCP / SuricataMCP | 本机 | tshark / Suricata 流量分析、凭据猎取 | 补我们只有 HTTP 录制的短板 |
| pentest-ai / AutoPentest-AI / mcp-vanguard | 单体大而全 | 205 / 68+ / 89 工具 | 方法论打包，适合当"参考实现"而不是当依赖 |
| PortSwigger **mcp-server-scanner**、mcptrotter、Cisco MCP Scanner | 扫描器 | 扫 MCP 服务器本身的漏洞 | 我们自己装 MCP，就该有一个"装前扫描" |

### 2.4 逆向域：已经从"手写桥"变成"官方供给"

| 项目 | 引擎 | 能力 | 备注 |
| --- | --- | --- | --- |
| **Hex-Rays ida-mcp**（官方） | IDA 9.4+ / idalib | Code Mode（agent 写 IDAPython）、GUI 与无头共享数据库 | MIT，2026-10 发布，**官方已取代社区方案** |
| **mrexodia/ida-pro-mcp** | IDA 8.3+ | 反编译、xref、改名、`py_eval`、可选调试器 | 13k★；`py_eval`/patch 属高危，默认应关 |
| **jtsylve/ida-mcp 2.0** | IDA / Ghidra 无头 | ~190 工具、36 resources、多二进制并行 | 适合批处理与 CI |
| **bethington/ghidra-mcp** | Ghidra 12.1+ | 179–200+ 工具、GUI + 无头、**lazy tool loading**、批操作、Ghidra Server | 免费路线的首选 |
| **ReVa** | Ghidra | 上下文友好的小工具集、PyGhidra 脚本 | 上下文预算紧张时的替代 |
| **radare2-mcp**（官方） | radare2 | 26+ 工具、stdio、只读模式、沙箱 | 无 GUI、可脚本化，适合无头流水线 |
| **x64dbg MCP**（多实现） | x64dbg/x32dbg | 40+ / 79 / ~152 个工具：断点、内存、寄存器、反汇编、反调试绕过、PE dump | 我们在 Windows，这是最顺手的一把 |
| **Frida MCP** | Frida | attach、脚本注入、hook、实时插桩 | 移动/客户端逻辑逆向的必需项 |
| **JADX AI MCP**（2.9k★） | JADX | APK 反编译 + 代码检索 | 安卓侧事实标准 |
| **ILSpy MCP** | .NET | 程序集反编译 | 补 .NET 全空白 |
| **Volatility3 MCP** | 内存镜像 | 进程/网络/注册表取证 | 应急与恶意样本分析 |
| **capa / YARA / FLOSS / binwalk** | 分诊 | 能力识别、规则扫描、字符串还原、固件拆包 | 都属于"几分钟出结论"的高性价比工具 |
| **open-reverselab** / **reversing-mcp** | 集成包 | 100+ 工具打包（Ghidra/Frida/x64dbg/Rizin/YARA） | 想快速铺开时的捷径 |

> 注意 **REA 本身就在这份清单里**（`morluto/rea`，多引擎 + JS/Electron/.NET/Android 目标，
> 带证据溯源）。我们内置的 `rea` 预设与生态是同一条路线，说明方向对，只是**只有一条腿**。

### 2.5 浏览器域：Playwright 之外还差一层"CDP"

| 维度 | Playwright MCP（已装，22 工具） | **Chrome DevTools MCP**（官方，29–45 工具，`--slim` 只有 3 个） |
| --- | --- | --- |
| 定位 | 驱动（点击/填表/导航） | **检查与调试**（DevTools 级） |
| 网络 | `browser_network_requests` | `list_network_requests` 保留**最近 3 次导航**、按资源类型过滤、请求/响应体**落盘**避免爆上下文 |
| 性能 | 无 | `performance_start_trace` / `analyze_insight`（LCP/INP/CLS）、`lighthouse_audit`、堆快照 |
| 控制台 | 有 | 带 source map 的调用栈 |
| 连接 | 启动新实例 | `--autoConnect` 接管已开的 Chrome（144+）、`--isolated`、`--headless` |
| 隐私 | — | 默认开使用统计；`--no-usage-statistics` / `--no-performance-crux` 可关 |

渗透场景里"改一个包、看一次 WS 帧、抓一次 CSP 违规"靠的是 CDP 而不是点击器。
**建议两个都留**：Playwright 负责"走到位置"，Chrome DevTools 负责"看清现场"。

---

## 三、差距矩阵（一眼看出该补哪）

### 3.1 渗透

| 环节 | 现在靠什么 | 缺口 | 补什么 |
| --- | --- | --- | --- |
| 资产测绘 | ScopeSentry MCP（17 工具） | 无 | — |
| 主动验证（漏洞命中） | shell 手搓 / 无 | **nuclei、ffuf、sqlmap 都没有** | 3 个轻量 stdio 预设 |
| 抓包与重放 | 内置 mitmproxy 录制 | 不能"改一个再发" | Burp 官方 MCP（Repeater/Intruder/OOB） |
| 流量取证 | HTTP 记录 | pcap、非 HTTP 协议 | WireMCP（tshark）/ Suricata |
| 浏览器 | Playwright | CDP 级网络/性能 | Chrome DevTools MCP |
| 情报 | 无 | 指纹、暴露面、IOC | Shodan / VirusTotal / Censys |
| 权限提升与横向 | 无 | AD、BloodHound | BloodHound MCP（P3） |

### 3.2 逆向

| 环节 | 现在靠什么 | 缺口 | 补什么 |
| --- | --- | --- | --- |
| 静态反编译 | `rea`（依赖外部 Ghidra/IDA/Hopper） | 无独立引擎预设、无批处理 | bethington/ghidra-mcp 或 Hex-Rays 官方 ida-mcp |
| 动态调试 | **无** | 断点、内存、寄存器官 | x64dbg MCP（Windows 主场） |
| 插桩 hook | **无** | Frida | Frida MCP |
| 移动 | **无** | APK/DEX | JADX AI MCP |
| .NET | **无** | 程序集 | ILSpy MCP |
| 内存取证 | **无** | 镜像分析 | Volatility3 MCP（P3） |
| 样本分诊 | **无** | 能力/规则/字符串 | capa + YARA + FLOSS（一个预设打包） |
| 固件 | 无 | 拆包 | binwalk（P3） |

**一句话**：渗透缺"手"，逆向缺"眼和手"。

---

## 四、架构决策（实现前先定，避免又堆出一地写死模块）

### D1 预设目录化：MCP 服务器变成"可审阅的目录条目"

把 `db/mcp_builtins.go` 从"4 个变量"升级为**声明式预设表**，每条至少包含：

```
key / title / domain(pentest|reverse|browser|general) / transport
command+args+env（带版本钉） / requires（二进制、版本、许可证）
risk（read-only | mutate | exec | network-egress）
expected_tools（工具名 + schema 指纹，用于发现后校验）
probe（一个只读工具，用来证明"真的连上了"）
outputs（证据落点：finding / traffic / artifact）
```

预置一律 `enabled=false`：装不等于启用，启用要过审阅——与现有 `REProviderConfig.ReviewedStatic`
的哲学保持一致。

### D2 工具面预算：12 + N

- 每个 MCP 服务器**直连暴露 ≤12 个核心工具**，其余统一走既有 `deferred` + smart search。
- 单会话 MCP 工具总量上限可配（默认 ~60），超了就在装配期告警，不静默丢。
- 依据：生态共识"3–7 个服务器/工具过多反而降智"，以及 bethington/GhidraMCP 的 lazy loading 实践。

### D3 域门控跟着既有决策走（不新增闸门）

沿用 2026-10-20 已定的域策略：**渗透/逆向跟随既有设置**，通用/开发才用 picker。
预设按 `domain` 打标只影响"界面上先给谁看"，**不改变任何既有执行路径**。

### D4 供应链：来源分级 + 版本钉 + 隔离

| 级别 | 含义 | 策略 |
| --- | --- | --- |
| A 官方 | PortSwigger / Hex-Rays / radareorg / ChromeDevTools / Microsoft | 允许直跑，仍需钉版本 |
| B 知名社区 | GhidraMCP / ida-pro-mcp / x64dbg MCP | 钉 tag 或 commit，首次启用要人工确认 |
| C 容器化/聚合 | mcp-security-hub / HexStrike / Kali | **优先跑在容器**，只暴露必要端口与目录 |

同时：**默认拒绝无认证的远程 http/sse**；`insecure`（跳过 TLS 校验）在 UI 上必须显式标记；
每次工具调用记录 `server / tool / 参数摘要 / 耗时 / 成败`，进审计流（否则报告里引用不了证据）。

### D5 可用性必须可证：probe

每个预设带一个只读探针（`nmap --version`、`x64dbg_status`、`list_functions` 空查询）。
UI 上显示三态：**未配置 / 已连接但缺工具 / 已验证可用**。
这与"接口有路由 ≠ 功能可用"的教训直接对应：没有 probe 通过，不许在技能里写"MCP 已就绪"。

### D6 观测 MCP 目录漂移

MCP 工具不进 `tools` 表，所以 `toolseed` 那套漂移检测管不到它。补一条：发现后把
`expected_tools` 与远端实际工具集比对，差异进 `/api/mcp/health`，UI 上给"远端已变更"提示。

---

## 五、分阶段路线

### P0 基建（先让"加一条"变成 10 分钟的事）

- 预设表结构 + 种子（把现有 4 个 + 首批 12 个写成声明）
- `GET /api/mcp/presets`、`POST /api/mcp/presets/{key}/install`（写 `mcp_servers`，默认 disabled）、
  `POST /api/mcp/presets/{key}/probe`
- 审计：MCP 调用落 `mcp_call_log`（server/tool/args 摘要/耗时/结果）
- 工具面预算 + 装配期告警
- `/system/mcp` 页加"预设"分区（若坚持控制台不动，则新开独立路由）

**验收**：一台干净机器，从"没有 nmap"到"模型用 MCP 跑出 `nmap -sV` 结果并入证据链"，
全程不超过 10 分钟且无需改代码。

### P1 渗透首批（6 条，覆盖"验证—利用"闭环）

nuclei · ffuf · sqlmap · Burp 官方 MCP（Repeater/Intruder/Collaborator）·
Chrome DevTools MCP · WireMCP（tshark）

**验收**：对一个授权靶站，模型能完成"指纹 → 目录发现 → 参数盲注验证 → 用 Burp 复现 →
CDP 抓取前端加密逻辑 → 出带证据的 finding"，每一步的工具调用都可在审计里回放。

### P2 逆向首批（6 条）

x64dbg MCP · Frida MCP · Ghidra MCP（bethington 或 ReVa）· Hex-Rays 官方 ida-mcp ·
JADX AI MCP · ILSpy MCP · （capa/YARA/FLOSS 打包成一条"分诊"预设）

**验收**：拿到一个 Windows 样本，模型能完成"分诊（capa/strings）→ 静态定位关键函数（Ghidra）
→ 动态断点验证（x64dbg）→ hook 取解密参数（Frida）"，并把证据挂到同一个样本 ID 下。

### P3 情报与深水区

Shodan / VirusTotal / Censys · Volatility3 · binwalk · BloodHound（AD）· Metasploit（可选）

### P4 治理

MCP 服务器自身扫描（PortSwigger mcp-server-scanner 思路）· 出网与文件写权限策略 ·
审计看板 · 预设升级/回滚

---

## 六、首批预设清单（建议直接照这个做）

### 渗透

| key | 形态 | 依赖 | 风险 | 核心工具（≤12 直连） |
| --- | --- | --- | --- | --- |
| `nuclei` | stdio | nuclei 二进制 | network-egress | `scan`、`list_templates`、`scan_status` |
| `ffuf` | stdio | ffuf 二进制 | network-egress | `fuzz`、`fuzz_status`、`wordlists` |
| `sqlmap` | stdio | sqlmap | exec | `scan`、`status`、`dump`（默认关） |
| `burp` | sse `127.0.0.1:9876` | Burp + 官方扩展 | mutate | `send_http1/2_request`、`get_proxy_http_history`、`create_repeater_tab`、`generate_collaborator_payload` |
| `chrome-devtools` | stdio `npx chrome-devtools-mcp` | Chrome 144+ | network-egress | `list_network_requests`、`get_network_request`、`evaluate_script`、`list_console_messages`、`performance_*` |
| `tshark`（WireMCP） | stdio | Wireshark/tshark | network-capture | `capture`、`read_pcap`、`follow_stream` |

### 逆向

| key | 形态 | 依赖 | 风险 | 核心工具 |
| --- | --- | --- | --- | --- |
| `x64dbg` | stdio + 插件 HTTP | x64dbg | exec | `launch`、`attach`、`bp_set`、`regs`、`mem_read/write`、`disasm` |
| `frida` | stdio | frida + frida-server | exec | `attach`、`load_script`、`hook`、`mem_*` |
| `ghidra` | stdio（bridge）或 http | Ghidra 12.1 + JDK21 | read-only | `decompile`、`xrefs`、`rename`、`search_functions`、`get_callgraph` |
| `ida` | stdio | IDA 9.4+ / 8.3+ | exec（py_eval 默认关） | `decompile`、`xrefs`、`list_functions`、`get_bytes` |
| `jadx` | stdio | JADX + 插件 | read-only | `decompile_class`、`search_code`、`manifest` |
| `ilspy` | stdio | .NET 8 | read-only | `list_types`、`decompile`、`il` |
| `triage` | stdio（自建或包装） | capa/YARA/FLOSS | read-only | `capa`、`yara_scan`、`floss`、`entropy` |

> `rea` 与 `wpe64` 保留原样不动——它们已经承担"自有引擎"的角色，新增全部走目录，
> 不改它们的语义。

---

## 七、不变式（写在代码前面，防止重蹈覆辙）

1. 渗透既有链路（`agent/mainagent.go`、`planner.go`、`tools*.go`、`promptcatalog.go`、
   `server/orchestration.go`）**零改动**；MCP 扩展只在装配与目录层。
2. 任何新增预设默认 `enabled=false`，启用动作留痕。
3. 不做"静默降级"：连不上、缺工具、schema 不符，全部可见。
4. 不留后门式默认值：`py_eval`、`dump`、`mem_write` 这类高危工具默认不直连，需要显式解锁。
5. 工具预算超限要告警，不静默裁剪。
6. 每期一个 commit，可独立回退。

---

## 八、待定（需要你拍板）

1. **首批顺序**：先渗透 6 条，还是渗透/逆向各 3 条并行？
2. **依赖形态**：允许在本机直跑（npx/pip/二进制），还是优先容器（更安全但要 Docker）？
3. **界面位置**：在 `/system/mcp` 就地加"预设"分区，还是新开独立路由（保持控制台页面不动）？

## 九、落实记录（2026-10-20，第一批已落地）

方向调整（用户决定）：**不包装社区 MCP，改为自己用 Go 写简化核心版**；同时对一切"仿官方"
的服务器一律重命名，避免雷同。理由与本文档原有判断一致：分发成本（单二进制）、
供应链风险（41% 无认证 / 43% 的 CVE 是 shell 注入）、以及输入输出要能进我们的证据链。

### 9.1 新增包 `mcpbuiltin/`（纯 Go，无子进程、无外部运行时）

| 服务器 | 域 | 工具 | 说明 |
| --- | --- | --- | --- |
| `artex-recon` | 渗透 | 4 | `dns_recon`（含子域爆破）、`port_probe`（TCP connect + banner）、`tls_inspect`（证书链/协议/套件）、`tech_hints`（框架/WAF 指纹） |
| `artex-webscan` | 渗透 | 6 | `http_send`（原始请求复现）、`http_fuzz`（FUZZ 模板 + 基线比对 + 正则提取）、`dir_probe`（路径枚举）、`param_probe`（反射/差异）、`jwt_inspect`（解析 + 弱密钥）、`sqli_probe`（报错/布尔/时间三态） |
| `artex-binscan` | 逆向 | 5 | `file_identify`（魔数/哈希/熵）、`pe_info`（节表/导入/熵）、`strings_scan`（ASCII+UTF-16+偏移）、`entropy_scan`（加壳线索）、`ioc_extract`（URL/域名/IP/邮箱/哈希/路径） |

### 9.2 接入方式（零改动复用既有链路）

`mcp_servers` 表上有 `CHECK (transport IN ('stdio','http','sse'))`，改约束需要重建表
（含外键与触发器），对已有用户数据的库不合算。因此约定：

- `transport='stdio'`、`command='artex-builtin'`、`args=["<server>"]`；
- `connectMCP` 在 stdio 分支先识别这个组合，直接返回进程内客户端，**不 exec 任何东西**；
- 工具命名仍是 `mcp__<server>__<tool>`，所以工具缓存、agent 绑定、`deferred` 解锁、
  计量与审计全部照旧，没有任何特殊分支；
- 未注册的名字明确报错，不返回空工具集。

### 9.3 默认值与失败可见

- 三个服务器一律 `enabled=false` 播种，工具清单在未启用时也能在 `/system/mcp` 预览
  （缓存由 `mcpbuiltin.All()` 直接生成，避免"代码改了、预览清单没改"）。
- 修正了一类会静默改变行为的默认值 bug：`clamp(v, lo, hi)` 把"没传"（0）夹成下限，
  导致默认并发=1、默认截断=200 字节。新增 `defInt(v, def, lo, hi)` 统一处理"未传用默认值"。

### 9.4 验证（不是"有接口"，是跑通）

- `go build ./...` / `go vet ./mcpbuiltin ./server ./db` 通过；
- `go test ./mcpbuiltin`：目录枚举、路径枚举命中、模糊测试基线比对、JWT 弱密钥命中、
  字符串/IOC/哈希、PE 自解析（Windows）；
- `go test ./server -run TestBuiltinMCP`：`connectMCP` 真能取到 `mcp__artex-webscan__*`，
  未知名字报错；
- 冷启动一份库副本：`mcp_servers` 新增 3 行（`artex-builtin`），
  `mcp_tools_cache` 预置 4/6/5 条，启动日志无错误。

### 9.5 尚未做（下一批）

按同一套模式继续：调试类（x64dbg/Frida 需要外部引擎，只做"桥"）、移动/.NET
（JADX/ILSpy 走外部 CLI）、流量（pcap 解析）、Web 的 CORS/开放重定向/反序列化探测、
二进制分诊的 YARA-lite 与 capa-lite。UI 侧仍是"可选启用"，没有做预设分区。
