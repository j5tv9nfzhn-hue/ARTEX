@echo off
rem 将控制台切换为 UTF-8，避免中文在其他代码页下乱码。
chcp 65001 >nul 2>&1
rem ARTEX-REA 启动监护脚本，Windows 版本。
rem
rem 用法：
rem   start.bat                  前台运行，Ctrl-C 停止
rem   start.bat -addr :9000      其他参数原样传给 artex-rea
rem
rem 启动 artex-rea.exe，进程退出后按退出码决定是否重启。
rem
rem   0      正常停止，结束循环
rem   75     请求重启，立即再运行，用于页面的一键更新或回滚
rem   其他   异常退出，退避重启，1、2、4 秒递增，最多 60 秒
rem
rem 下载、SHA256 校验和版本替换由 artex-rea 启动时自行执行，不放入本脚本。
rem 对应 selfupdate 包；保持脚本简单，详情见 start.sh。

setlocal enabledelayedexpansion
cd /d "%~dp0"

set "BIN=artex-rea.exe"
if not exist "%BIN%" (
	if exist "artex.exe" (
		set "BIN=artex.exe"
	) else (
		echo [artex-rea] 找不到可执行文件：%BIN% 1>&2
		exit /b 1
	)
)

rem 告诉 artex-rea「外面有个循环会在我以 75 退出后把我重新拉起来」。
rem 只有它为真时页面才允许自动更新：换装后进程要退出，没人拉起就变成
rem 「更新完打不开」。直接双击 exe（没有本脚本）时它为假，界面会禁用自动更新。
set "ARTEX_SUPERVISED=1"

set "RESTART_CODE=75"
set "MAX_DELAY=60"
set /a delay=1

:loop
"%BIN%" %*
set "code=!ERRORLEVEL!"

if "!code!"=="0" (
	echo [artex-rea] 正常退出
	exit /b 0
)

if "!code!"=="%RESTART_CODE%" (
	rem 已准备更新或回滚，重新运行时由 artex-rea 完成版本替换。
	echo [artex-rea] 收到重启请求，正在应用新版本…
	set /a delay=1
	goto loop
)

echo [artex-rea] 异常退出 ^(code=!code!^)，!delay! 秒后重启 1>&2
rem timeout 在重定向控制台可能失败，改用 ping；延迟 N 秒需要 N+1 次请求。
set /a pings=!delay!+1
ping -n !pings! 127.0.0.1 >nul 2>&1
set /a delay=!delay!*2
if !delay! gtr %MAX_DELAY% set /a delay=%MAX_DELAY%
goto loop
