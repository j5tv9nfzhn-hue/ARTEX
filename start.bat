@echo off
chcp 65001 >nul 2>&1
rem ARTEX-HYDRA 启动脚本（Windows）
rem
rem 用法：
rem   start.bat                 前台运行，Ctrl-C 直接退出
rem   start.bat -addr :9000     命令行参数原样传给 artex-rea
rem
rem 这个脚本只做三件事：切到脚本目录、找到可执行文件、把它交出去。
rem
rem 一、为什么不在这里写重启循环
rem   重启已经在 exe 里：入口程序会等核心退出，只有 updates\restart.request 存在时
rem   才重新拉起（5 分钟内最多 5 次），用户按 Ctrl-C 出去的进程不会被复活。
rem   外层再套一层循环只会带来两个后果：
rem     - 端口被占用时每一轮都撞同一个端口，无限重启刷屏、窗口反复闪；
rem     - Ctrl-C 之后还要用户回答 Terminate batch job (Y/N)?，按错就重来一遍。
rem
rem 二、为什么最后一行要 goto 一个不存在的标签
rem   cmd 里 goto 到未定义标签是致命错误，会立刻结束批处理上下文；但同一行里
rem   已经解析好的后续命令仍会执行——exe 于是在「命令行上下文」里前台运行。
rem   没有批处理作业可终止，Ctrl-C 就不会再问 Y/N，信号直接落到 exe 自己身上，
rem   由它按正常流程收尾（关库、释放单实例锁）。npm 的 cmd-shim 用的是同一个写法。
rem
rem 三、退出码
rem   0   正常停止（或已有一个实例在跑，本次启动让路）
rem   2   端口被占用：程序会打印占用端口与处置办法，脚本不再重试
rem   75  已把重启交给新进程/上层（脚本不干预）
rem   其它 异常：留住窗口让用户看错误
rem
rem 设置 ARTEX_NO_PAUSE=1 可以让它在无人值守场景里不停顿。

cd /d "%~dp0"

set "BIN=%~dp0artex-rea.exe"
if not exist "%BIN%" set "BIN=%~dp0artex.exe"
if not exist "%BIN%" (
	echo [artex-rea] 找不到 artex-rea.exe 或 artex.exe（目录：%~dp0） 1>&2
	pause
	exit /b 1
)

goto #_undefined_# 2>NUL || title %COMSPEC% & "%BIN%" %* || if not defined ARTEX_NO_PAUSE pause
