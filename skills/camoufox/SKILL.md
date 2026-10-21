---
name: camoufox
description: 浏览器自动化与前端动态交互（Camoufox / Firefox 内核）。用于 SPA 动态渲染、登录态维持、DOM/Cookie/Storage 提取、DOM XSS 与前端状态验证。静态 HTTP 请求渲染不出页面、需要真实浏览器执行 JS 或需要规避无头 Chromium 指纹时调用。
allowed-tools: Bash(camoufox-cli:*) Bash(python:*) Bash(python3:*)
---

# 浏览器自动化（Camoufox）

## 为什么是这个

无头 Chromium 是当前最容易被拦的自动化形态：一项覆盖 1 万个站点的测量显示，
Chromium 无头软封禁率 **15.2%**，而 Firefox 无头只有 **6.8%**（与有头几乎相同）。
主因是 Chromium 会在 `sec-ch-ua` 里暴露 `HeadlessChrome`，而 Firefox 根本不实现
Client Hints，这条检测面直接不存在。

Camoufox 是在 **C++ 引擎层**改指纹的 Firefox 分支（不是 JS 补丁），通过 Juggler
协议驱动。所以本项目的浏览器自动化**只用 Camoufox**。

> **硬规则**：不要调用 `playwright-cli`、`npx @playwright/mcp`、puppeteer、系统
> Chrome/Chromium/Edge，也不要传 `--browser=chrome` 之类的参数。任何回退到
> Chromium 系浏览器的做法都会重新引入上面那套指纹，属于**违规操作**。

## 一次性安装

```bash
python -m pip install -U "camoufox[geoip]"
python -m camoufox fetch          # 下载打了补丁的 Firefox（约 100MB）
python scripts/camoufox_cli.py doctor   # 自检：python / camoufox / playwright / 浏览器版本
```

`doctor` 输出的 `browser build` 是 `未知` 时，说明 `fetch` 没成功，先解决它再谈自动化。

## 快速开始

命令入口是本技能目录下的 `scripts/camoufox_cli.py`（下面统一写 `camoufox-cli`，二者等价；
若你已把入口软链进 PATH，可以直接用短命令）。在**技能目录内**先 `cd` 进去：

```bash
cd <技能目录>/camoufox
alias camoufox-cli='python scripts/camoufox_cli.py'    # Windows: doskey camoufox-cli=python scripts\camoufox_cli.py $*

camoufox-cli open https://target.example/     # 启动并导航
camoufox-cli snapshot                          # 给可交互元素编号 e1/e2…
camoufox-cli fill e3 "admin"
camoufox-cli fill e5 "password" --submit
camoufox-cli snapshot
camoufox-cli eval "document.cookie"
camoufox-cli screenshot --full --filename=out/home.png
camoufox-cli close
```

没有 alias 时，把每条命令里的 `camoufox-cli` 换成 `python scripts/camoufox_cli.py` 即可。

会话由后台守护进程维护：**一次 `open`，之后每命令都是同一页面**，登录态、标签页、
滚动位置都保持。`close` 结束会话；45 分钟无操作也会自动退出。

## 命令速查

| 类别 | 命令 |
|---|---|
| 会话 | `open [url] [--headed] [--os=windows\|macos\|linux] [--geoip] [--proxy=http://host:port] [--profile=DIR] [--block-images] [--no-humanize]` |
| 导航 | `goto URL` / `url` / `title` / `wait [e3] [--ms=1000]` |
| 读取 | `snapshot [--limit=N]` / `text [e3] [--whole]` / `eval "js" [e3]` |
| 交互 | `click e3` / `dblclick` / `fill e5 "文本" [--submit]` / `type "文本"` / `press Enter` / `hover e3` / `select e9 value` / `check e12` / `uncheck e12` |
| 截图 | `screenshot [e3] [--full] [--filename=path]` |
| 标签页 | `tab-list` / `tab-new [url]` / `tab-select 0` / `tab-close` |
| Cookie | `cookie-list [--domain=x]` / `cookie-set NAME VALUE [--url=...]` / `cookie-clear` |
| Storage | `localstorage-list` / `localstorage-set K V` / `localstorage-clear` |
| 登录态 | `state-save [file]` / `state-load file` |
| 其他 | `status` / `doctor` / `close` |

**元素引用**：`snapshot` 会给页面上可见的 `a/button/input/select/textarea` 等打上
`e1…eN`，后续 `click e3`、`fill e5` 直接用它。页面重新渲染后引用会失效，**重新
`snapshot` 一次**再点。也可以直接传 CSS 选择器：`click "#login-btn"`。

## 指纹默认值（除非有理由，否则别改）

```bash
camoufox-cli open https://target/ --os=windows --geoip --proxy=http://127.0.0.1:8080
```

- `--os=windows`：指纹里的操作系统要和你声称的 UA 一致；不传则三选一随机。
- `--humanize`（默认开）：鼠标走贝塞尔曲线，不是瞬移。
- `--geoip`：有代理时**必开**，让时区/语言/WebRTC 跟随出口 IP。代理在韩国却报
  `Asia/Shanghai` 是最典型的自杀式指纹。
- `--profile=DIR`：**默认不落盘**（会话结束即丢）。只有需要跨会话保留登录态时才用，
  而且要定期清理这个目录，否则会像 Chromium 那样堆到几百 MB。
- 固定 `--window` 尺寸会削弱指纹一致性，除调试外不要设置。

## 常见坑

| 现象 | 原因与处理 |
|---|---|
| `未安装 camoufox` | 没执行安装命令；按提示 `pip install -U "camoufox[geoip]"` 后再 `camoufox fetch` |
| `浏览器会话未启动` | 还没 `open`，或者守护进程因空闲退出了 |
| 点了没反应 | 元素索引过期；重新 `snapshot` 后再点，或改用 CSS 选择器 |
| `geoip` 报错 | 没装 `[geoip]` extra，或没配代理 |
| 想导出 PDF | **不支持**。Firefox 引擎没有 Playwright 的 `page.pdf()`；用 `screenshot --full` 代替 |
| 需要 CDP 那套能力 | Camoufox 没有 CDP。抓包用 `eval` 挂钩 `fetch`/`XMLHttpRequest`，或用 `api-recon` 技能的 `runtime_harvest.py` |
| 自签名证书站点 | 浏览器会拦；优先走 `http://`，或先在系统/Firefox 信任该 CA，不要关证书校验来"绕过" |

## 验证指纹是否真的干净

```bash
camoufox-cli open https://abrowser.camoufox.com --os=windows
camoufox-cli open https://bot.sannysoft.com
```

`abrowser.camoufox.com` 是 Camoufox 自带的指纹自检页，重点看 WebGL、
navigator、时区、字体是否与声明的 `--os` 一致。

## 与 API 侦察的配合

SPA 的接口面通常在运行时才暴露。`api-recon` 技能的 `runtime_harvest.py` 就是基于
本引擎做的：注入伪造登录态 → 改写鉴权失败码 → 遍历路由 → 记录 API 调用与 WebSocket。
需要抓接口时**先用那个脚本**，不要自己写一套 Chromium 脚本。
