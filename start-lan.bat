@echo off
chcp 65001 >nul 2>&1
rem ARTEX-HYDRA 局域网模式入口（Windows）
rem
rem 双击它就等于 start.bat -lan：监听 :8787，同一个 Wi-Fi 下的手机能直接连。
rem 带 -lan 启动走「接管」分支：旧实例还在也能接过来，不用先手动关窗口。
rem
rem 一般用不到这个脚本：程序里的「系统 → 局域网连接」页有按钮，点一下就会把
rem listen 写进 config.json 并自己重启，之后 start.bat 也一直是局域网模式。
rem
rem 与 start.bat 一样不做重启循环（重启在 exe 内部），也不会有 Ctrl-C 的终止询问，
rem 最后一行 goto 未定义标签的写法见 start.bat 顶部的说明。

cd /d "%~dp0"

set "BIN=%~dp0artex-rea.exe"
if not exist "%BIN%" set "BIN=%~dp0artex.exe"
if not exist "%BIN%" (
	echo [artex-rea] 找不到 artex-rea.exe 或 artex.exe（目录：%~dp0） 1>&2
	pause
	exit /b 1
)

goto #_undefined_# 2>NUL || title %COMSPEC% & "%BIN%" -lan %* || if not defined ARTEX_NO_PAUSE pause
