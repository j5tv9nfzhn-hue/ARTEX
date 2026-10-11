@echo off
rem 将控制台切换为 UTF-8，避免中文在其他代码页下乱码。
chcp 65001 >nul 2>&1
rem ARTEX 启动监护脚本，Windows 版本。
rem
rem 用法：
rem   start.bat                  前台运行，Ctrl-C 停止
rem   start.bat -addr :9000      其他参数原样传给 artex
rem
rem 启动 artex.exe，进程退出后按退出码决定是否重启。
rem
rem   0      正常停止，结束循环
rem   75     请求重启，立即再运行，用于页面的一键更新或回滚
rem   其他   异常退出，退避重启，1、2、4 秒递增，最多 60 秒
rem
rem 下载、SHA256 校验和版本替换由 artex 启动时自行执行，不放入本脚本。
rem 对应 selfupdate 包；保持脚本简单，详情见 start.sh。

setlocal enabledelayedexpansion
cd /d "%~dp0"

set "BIN=artex.exe"
if not exist "%BIN%" (
	echo [artex] 找不到可执行文件：%BIN% 1>&2
	exit /b 1
)

set "RESTART_CODE=75"
set "MAX_DELAY=60"
set /a delay=1

:loop
"%BIN%" %*
set "code=!ERRORLEVEL!"

if "!code!"=="0" (
	echo [artex] 正常退出
	exit /b 0
)

if "!code!"=="%RESTART_CODE%" (
	rem 已准备更新或回滚，重新运行时由 artex 完成版本替换。
	echo [artex] 收到重启请求，正在应用新版本…
	set /a delay=1
	goto loop
)

echo [artex] 异常退出 ^(code=!code!^)，!delay! 秒后重启 1>&2
rem timeout 在重定向控制台可能失败，改用 ping；延迟 N 秒需要 N+1 次请求。
set /a pings=!delay!+1
ping -n !pings! 127.0.0.1 >nul 2>&1
set /a delay=!delay!*2
if !delay! gtr %MAX_DELAY% set /a delay=%MAX_DELAY%
goto loop
