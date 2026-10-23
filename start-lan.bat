@echo off
chcp 65001 >nul 2>&1
rem ARTEX-REA 局域网模式入口（Windows）
rem
rem 双击它就等于 start.bat -lan：监听 :8787，手机在同一个 Wi-Fi 下能连上。
rem
rem 已经有实例在跑也没关系：带 -lan 启动会接管旧实例，不需要先手动关窗口
rem （这条是 2.3.1 起的约定——旧版本遇到「已在运行」会直接退出，看起来像没反应）。
rem
rem 一般用不到这个脚本：程序里的「系统 → 局域网连接」页有按钮，
rem 点一下就会把 listen 写进 config.json 并自己重启，之后 start.bat 也一直是局域网模式。

call "%~dp0start.bat" -lan
