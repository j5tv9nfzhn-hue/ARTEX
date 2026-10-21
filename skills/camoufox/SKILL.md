---
name: camoufox
description: 浏览器自动化与前端动态交互（Camoufox / Firefox 内核）。用于 SPA 动态渲染、登录态维持、DOM/Cookie/Storage 提取、DOM XSS 与前端状态验证。静态 HTTP 请求渲染不出页面、需要真实浏览器执行 JS，或需要规避无头 Chromium 指纹时调用。
allowed-tools: Bash(camoufox-cli:*) Bash(python:*) Bash(python3:*)
---

# 浏览器自动化（Camoufox）

## 两档引擎（默认轻，按需强）

| 档 | 引擎 | 额外磁盘 | 什么时候用 |
|---|---|---|---|
| **L1 默认** | 机器上已装的 Chrome / Edge（CDP） | **0** | 绝大多数任务。真二进制 = 真 TLS/JA4、真编解码，没有「补丁版对不上版本戳」的矛盾 |
| **L3 高级** | Camoufox（C++ 层改指纹的 Firefox，Juggler） | 约 300MB（`camoufox fetch`） | 目标明确在跑指纹检测（Cloudflare/DataDome 高级档），或 L1 被拦时 |

`open` 不带 `--engine` 时是 `auto`：先找系统 Chrome，再找 Edge，都没有才回落到 Camoufox。
也可以显式 `--engine=chrome|edge|camoufox`。

> **硬规则**
> 1. 不要调用 `playwright-cli`、`npx @playwright/mcp`、puppeteer —— 本技能只用 `camoufox-cli`。
> 2. 不要安装任何"浏览器 MCP 服务器"——本项目走 `Skill(camoufox)` + `camoufox-cli`，
>    多一层 MCP 只会多一个要维护、要拉进程的东西。
> 3. 系统引擎只驱动**真实已安装**的 Chrome/Edge；不要下载 Chromium 包（那是 400MB 的重复安装）。
> 4. 同一个任务不要中途换引擎——换引擎等于换身份，登录态与指纹都会断。
> 5. L1 默认 `--headless=new`；被拦先加 `--headed` 再试，别直接上 L3。

## 安装

L1 不需要装任何东西（用机器上的 Chrome 或 Edge）。确认能探测到：

```bash
python scripts/camoufox_cli.py doctor     # 会打印 system browser: chrome → .../chrome.exe
```

L3（可选，约 300MB；只有需要更强指纹时才装）：

```bash
python -m pip install -U "camoufox[geoip]"
python -m camoufox fetch
python -m camoufox version
```

想在频道上跟进（可选）：

```bash
python -m camoufox set official/stable && python -m camoufox fetch
python -m camoufox list
```

Linux 另需：`libgtk-3-0 libx11-xcb1 libasound2`；要走 `--virtual` 再加 `xvfb`。

## 怎么调用（AI 必读）

命令入口是本技能目录下的 `scripts/camoufox_cli.py`：

```bash
cd <技能目录>/camoufox
python scripts/camoufox_cli.py open https://target.example/
```

`scripts/` 里另有 `camoufox-cli`（sh）与 `camoufox-cli.cmd`（Windows）两个启动器；
把 `scripts` 目录加进 PATH 后可以直接写 `camoufox-cli`。**下文示例用短名，
没配 PATH 就自行替换成 `python scripts/camoufox_cli.py`。**

调用节奏：**一次 `open`，之后每条命令都是同一页面**（登录态、标签页、滚动位置都保持）。
不要每条命令都 `open`，那样等于每次都换一个全新的浏览器身份。

找不到脚本位置时（cwd 不确定），先定位一次再调：

```bash
python -c "import glob;print(glob.glob('**/camoufox_cli.py', recursive=True)[:3])"
```

拿到绝对路径后就用绝对路径调用，别在路径上猜。

## 快速开始

```bash
cd <技能目录>/camoufox

python scripts/camoufox_cli.py open https://target.example/ --os=windows
python scripts/camoufox_cli.py snapshot                  # 给可交互元素编号 e1/e2…
python scripts/camoufox_cli.py fill e3 "admin"
python scripts/camoufox_cli.py fill e5 "password" --submit
python scripts/camoufox_cli.py snapshot                  # 重新渲染后必须重新 snapshot
python scripts/camoufox_cli.py eval "document.cookie"
python scripts/camoufox_cli.py screenshot --full --filename=out/home.png
python scripts/camoufox_cli.py close
```

## 命令速查

| 类别 | 命令 |
|---|---|
| 会话 | `open [url] [选项…]` / `close` / `status` / `doctor` |
| 导航 | `goto URL` / `url` / `title` / `wait [e3] [--ms=1000]` |
| 读取 | `snapshot [--limit=N]` / `text [e3] [--whole]` / `eval "js" [e3]` |
| 交互 | `click e3` / `dblclick` / `fill e5 "文本" [--submit]` / `type "文本"` / `press Enter` / `hover e3` / `select e9 value` / `check e12` / `uncheck e12` |
| 截图 | `screenshot [e3] [--full] [--filename=path]` |
| 标签页 | `tab-list` / `tab-new [url]` / `tab-select 0` / `tab-close` |
| Cookie | `cookie-list [--domain=x]` / `cookie-set NAME VALUE [--url=...]` / `cookie-clear` |
| Storage | `localstorage-list` / `localstorage-set K V` / `localstorage-clear` |
| 登录态 | `state-save [file]` / `state-load file` |

**元素引用**：`snapshot` 给可见的 `a/button/input/select/textarea` 打 `data-cfx-ref="eN"`，
后续 `click e3`、`fill e5` 直接用它；也接受 CSS 选择器（`click "#login-btn"`）。
页面重新渲染后引用会失效，**重新 snapshot 一次**。

## open 参数（按官方 0.5.x 语义）

| 参数 | 语义 | 什么时候用 |
|---|---|---|
| `--os=windows\|macos\|linux` | 指纹里的操作系统。不传则三选一随机 | **每个目标固定一个**，别让它随机；本机是 Windows，一般 `--os=windows` |
| `--geoip` | 用出口 IP 反查经纬度/国家/locale/WebRTC IP | **有代理时必开**。CLI 会额外把解析出的时区显式传给浏览器 context（启动级 `geoip` 只配 locale/坐标/WebRTC，**不会**应用时区，见 camoufox#773） |
| `--proxy=http://user:pass@host:port` | 出口代理，支持 http/https/socks5 | 抓包/换地域。凭据会被拆成 `username`/`password`——Playwright 的 Firefox 不解析 `server` 里内联的凭据，直接塞会 407 |
| `--locale=zh-CN` | BCP-47，可写多个（`zh-CN,en-US`） | 想固定语言时；开了 `--geoip` 一般不用 |
| `--preset` | 用打包的真实设备指纹预设（0.5.x 的 `fingerprint_preset`） | 目标有强一致性校验时；旧版本会自动降级忽略 |
| `--disable-coop` | 关掉 COOP，跨域 iframe（如 Turnstile 复选框）才点得到 | 遇到 Cloudflare 人机验证时 |
| `--trust-os-certs` | 打开 `security.enterprise_roots.enabled` | 浏览器要走**抓包代理（MITM）**时，让它信任系统证书库里的 CA |
| `--virtual` | Linux 用 Xvfb 跑有头模式（`headless="virtual"`） | Linux 服务器上最稳；Windows/macOS 不支持 |
| `--profile=DIR` | 持久 profile（默认**不落盘**，会话结束即丢） | 需要跨会话保留登录态时才用，且要定期清理 |
| `--block-images` | 屏蔽图片，省代理流量 | 只抓接口/结构时 |
| `--no-humanize` | 关掉拟人鼠标轨迹（默认开） | 极少用 |
| `--headed` | 显示窗口 | 手工过验证码/调试 |
| `--window=1280x800` | 固定窗口尺寸 | **仅调试**：固定尺寸本身就是指纹 |

## 指纹一致性清单

1. **OS 与 UA 要对得上**：`--os=windows` 生成的就是 Windows 指纹，别再手工改 UA。
2. **时区要跟出口 IP 一致**：用 `--geoip` + `--proxy`，CLI 会把时区落到 context 上。
   代理在韩国却报 `Asia/Shanghai` 是最典型的自杀式指纹。
3. **别固定窗口尺寸**，也**别手工传 `config`** 覆盖指纹字段（会触发一致性告警）。
4. **每次任务用同一个身份**：一次 `open` 跑完，不要中途 close 重开。
5. 自检：

```bash
python scripts/camoufox_cli.py open https://abrowser.camoufox.com --os=windows
python scripts/camoufox_cli.py open https://bot.sannysoft.com
```

重点看 WebGL、`navigator`、时区、字体是否与声明的 `--os` 一致。

## 常见坑

| 现象 | 原因与处理 |
|---|---|
| `未安装 camoufox` | 先 `pip install -U "camoufox[geoip]"`，再 `python -m camoufox fetch` |
| `浏览器会话未启动` | 还没 `open`，或守护进程空闲 45 分钟自动退出了 |
| 代理连上但全 407/401 | 凭据没拆出来（旧版 CLI 的行为）；新版已按 `user:pass@` 解析，确认密码里的特殊字符做了 URL 编码 |
| 时区还是本机时区 | `--geoip` 没开、`[geoip]` extra 没装，或用了 `--profile`（持久 context 的时区在创建时已固定，只能靠启动前的 `TZ`） |
| 点了没反应 | 元素索引过期；重新 `snapshot`，或改用 CSS 选择器 |
| 想导出 PDF | **不支持**。Firefox 引擎没有 Playwright 的 `page.pdf()`，用 `screenshot --full` |
| 想要 `go_back()` | 没开页面缓存（Camoufox 默认关，省内存）；本 CLI 不暴露，直接 `goto` 目标地址 |
| 需要 CDP 抓包/性能追踪 | 没有 CDP。抓接口用 `api-recon` 的 `runtime_harvest.py`（route 拦截 + `add_init_script`） |
| Linux 上残留 Xvfb 进程 | `headless="virtual"` 的已知缺陷（camoufox#907），启动失败时可能留下；`pgrep -a Xvfb` 清掉，或改用 `headless=True` |
| 自签名证书站点 | 别关证书校验；把 CA 装进系统信任库后加 `--trust-os-certs`，或先走 `http://` |

## 关于"能不能过 Cloudflare"

Camoufox 不解决 CAPTCHA：它能让你**看起来像真人 Firefox**，不会替你解题。
遇到 Turnstile 可以试 `--disable-coop` + `--headed` 手工点一次，再 `state-save` 复用登录态。
另外官方在 2026 年公开说明过维护曾中断、部分一致性校验被新发现，能力会随版本波动——
**不要承诺"零检测"**，以实测为准。

## 与 API 侦察的配合

SPA 的接口面通常在运行时才暴露。`api-recon` 的 `runtime_harvest.py` 就是基于本引擎：
注入伪造登录态 → 改写鉴权失败码 → 遍历路由 → 记录 API 调用与 WebSocket 帧。
需要抓接口时**先用那个脚本**，不要自己写一套新脚本。
