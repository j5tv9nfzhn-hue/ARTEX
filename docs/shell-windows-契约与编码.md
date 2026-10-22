
## 九、命令在 Windows 上为什么"时好时坏"：执行器契约与编码（2026-10-22）

现象：同一类命令，模型有时写对、有时写错；报错内容五花八门（`无法将…识别为 cmdlet`、
`'<' operator is reserved`、`The term 'X' is not recognized`、输出全是乱码），
模型据此判断"命令失败"，换个写法再试，越试越偏。

根因两条，都不是模型能力问题：

1. **模型不知道自己在跟谁说话**。这条工具叫 `Bash`，SDK 描述写的是 "runs through the
   shell"；Windows 上真正执行它的是 **Windows PowerShell 5.1**
   （`powershell -NoProfile -NonInteractive -Command`，见 norma `tool.shellCmd`）。
   此前 `windowsShellGuidance` 只补进了**工具目录库**（`server/assembly.go` 的
   `platformShellNote`，`ON CONFLICT DO NOTHING` 首写不改），worker 走的是
   `WrapCrossPlatformBash` 直接包 SDK 工具，**从来没拿到这段说明**——它只看到系统提示
   末尾一句"命令按 powershell 语法书写"。
2. **编码不是 UTF-8**。中文 Windows 上 PowerShell 与原生程序的管道默认按本地代码页
   （GBK/OEM）走：Go 侧读回的不是合法 UTF-8，落库/送模型时被替换成 `U+FFFD`。
   模型看到乱码不会认为"编码错了"，只会认为命令失败。

修法（都在 `agent/`，未改 HARNESS）：

| 位置 | 改动 |
| --- | --- |
| `agent/shell_platform.go`（新） | 执行器契约（`shellContractLine`/`shellContractBlock`，按 GOOS 生成）；`ensureUTF8Output`（UTF-16LE BOM → GB18030 → U+FFFD 兜底）；`mergeChildUTF8Env`（补 `PYTHONUTF8`/`PYTHONIOENCODING`，不覆盖已有键）；`windowsDialectHint`（写法类报错追加正确写法） |
| `agent/shell_builtins.go` | `crossPlatformBashTool` 覆盖 `Description()`/`Prompt()`，让**每条命令都挨着**执行器契约与语法对照；`Call` 统一清洗文本块并追加写法提示；Windows 分支改为 `wrapWindowsCommand`（UTF-8 前导 → 命令 → 真退出码） |
| `server/env_context.go` | 环境简报里的执行器说明从"按 X 语法书写"改成三条高频差异 + 编码口径 |
| `server/shellcap.go` | 目录版 `windowsShellGuidance` 补编码口径 |

Windows 上每条命令实际变成：

```powershell
[Console]::OutputEncoding=[Text.UTF8Encoding]::new($false); [Console]::InputEncoding=[Text.UTF8Encoding]::new($false); $OutputEncoding=[Text.UTF8Encoding]::new($false); $ProgressPreference='SilentlyContinue'; chcp 65001 > $null;
<模型写的命令>
if ($null -eq $LASTEXITCODE) { exit 0 } else { exit $LASTEXITCODE }
```

开关：`ARTEX_WINDOWS_UTF8=0` 关掉前导与子进程 UTF-8 环境（排查用）；
`ARTEX_WINDOWS_EXIT_WRAP=0` 关掉退出码收尾。输出兜底解码始终开启（只在字符串本来
就不是合法 UTF-8 时动手）。

回归测试：`agent/shell_platform_test.go`（GBK/UTF-16LE 还原、乱码兜底、环境注入口径、
写法提示识别、命令组装顺序、契约定点）。

未覆盖：`shell_open`/`shell_send` 的交互式 PTY 会话与 MCP 侧 shell（不在本条工具链上），
以及 `->` 这类 PowerShell 5.1 之外（pwsh 7）才有的语法差异。
