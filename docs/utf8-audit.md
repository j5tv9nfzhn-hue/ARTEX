# UTF-8 全链路审计（批次 7 · 条目 4–5）

批次 7 对「字符串乱码/编码错误」的审计与修复记录。结论：文本写盘、JSON、数据库与 HTTP 链路本身全程 UTF-8；风险集中在 **Windows 控制台代码页** 与 **平台派生出的子进程 stdio 编码** 两处，已修复并提供回退开关。

> **状态（2026-10-16）：** 本文全部修复点已落地并经源码静态核对（`enableUTF8Console`、`childUTF8Env`、PowerShell preamble、`fsutil.WriteFileAtomic` 均在位）；本轮未运行编译与测试（用户要求跳过）。

## 审计结论（按链路）

| 链路 | 现状 | 处理 |
|---|---|---|
| 日志/横幅 → 控制台 | Go 按 UTF-8 字节写 stdout/stderr；中文版 Windows 控制台默认 GBK(936)，直接运行 `artex.exe` 或 `go run` 时中文变乱码 | 启动时进程级切到 UTF-8 代码页 |
| start.bat 启动路径 | 已有 `chcp 65001`，覆盖「经脚本启动」 | 无需改动 |
| 子进程 stdio（自定义工具：Python script、command→PowerShell） | 默认按本地代码页读写；脚本回传 GBK 字节后不是合法 UTF-8，进 JSON/SQLite 时被替换成 U+FFFD | 注入 UTF-8 环境变量 + PowerShell 编码 preamble |
| 平台内 Bash 工具 | 由 norma 依赖实际执行，ARTEX 无法逐条包裹命令 | 依赖控制台 CP 已切 UTF-8，见「未覆盖」 |
| 文本文件写入 | 已统一走 `fsutil.WriteFileAtomic`（原子写，UTF-8 字节） | 无需改动 |
| JSON 序列化 | `encoding/json` 全程 UTF-8 字节，无转码环节 | 无需改动 |
| HTTP 导出 | 响应头已带 `charset=utf-8` | 无需改动 |
| 数据库 | SQLite 文本列按 UTF-8 存储 | 无需改动 |
| 文件名 | Go 与 NTFS 经 UTF-16 往返，不经过代码页 | 无需改动 |

## 修复点

1. **进程控制台代码页**（补「直接运行」路径）：
   - `cmd/artex/console_windows.go`（新增）：`enableUTF8Console()` 调用 `SetConsoleOutputCP(65001)` 与 `SetConsoleCP(65001)`；
   - `cmd/artex/console_other.go`（新增）：非 Windows 空实现（Unix 终端与管道本就是 UTF-8）；
   - `cmd/artex/main.go`：`run()` 开头调用一次。之后派生的 powershell/cmd/python 经 `GetConsoleOutputCP()` 看到 UTF-8，PowerShell 在重定向管道里的默认输出编码随之变为 UTF-8。
2. **子进程环境变量**：`server/utf8env.go`（新增）返回 `PYTHONUTF8=1` 与 `PYTHONIOENCODING=utf-8`；`server/customtool.go` 组装子进程 env 时顺序为 `os.Environ()` → UTF-8 默认值 → 会话 env（会话显式配置同名变量可覆盖）。
3. **PowerShell 编码 preamble**：`server/customtool.go` 在 Windows 且未禁用时，把自定义 command 工具的命令前置为 `$OutputEncoding=[Console]::OutputEncoding=[System.Text.Encoding]::UTF8; …`。

## 开关与回退

| 环境变量 | 取值 | 作用 |
|---|---|---|
| `ARTEX_UTF8_CONSOLE` | `0`/`false`/`off`/`no` | 关闭控制台代码页切换（老式终端或需要固定代码页时） |
| `ARTEX_CHILD_UTF8` | `0`/`false`/`off`/`no` | 关闭子进程 UTF-8 环境注入与 PowerShell preamble（排查编码问题时用） |

默认均为开启。

## 未覆盖与已知残留

- **平台内 Bash 工具**：命令经 norma（`powershell -NoProfile -NonInteractive -Command`）执行，平台无法为每条命令包裹 preamble；只能靠进程控制台 CP + 子进程环境变量改善。如用户环境强制 GBK，可在工具命令中自行设置 `$OutputEncoding`。
- **其余 `os.WriteFile` 调用点**：审计后确认均为二进制/密钥类内容（证书、jwt.key、可执行文件等），不属于文本链路。
