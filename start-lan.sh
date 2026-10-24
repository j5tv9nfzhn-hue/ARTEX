#!/bin/sh
# ARTEX-HYDRA 局域网模式入口（Linux / macOS）
#
# 等价于 ./start.sh -lan：监听 :8787，手机在同一个 Wi-Fi 下能连上。
# 已经有实例在跑也没关系：带 -lan 启动会接管旧实例。
#
# 一般用不到这个脚本：程序里的「系统 → 局域网连接」页有按钮，
# 点一下就会把 listen 写进 config.json 并自己重启，之后 ./start.sh 也一直是局域网模式。
exec sh "$(dirname "$0")/start.sh" -lan
