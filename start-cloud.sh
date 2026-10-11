#!/usr/bin/env bash
set -euo pipefail

# Render 随机分配 PORT 环境变量，应用需使用该端口启动。
# 保持默认执行方式与 Skill、数据目录。
: "${PORT:?必须设置 PORT 环境变量}"
echo "[*] Starting ARTEX Server on port :$PORT"
exec ./artex -addr ":$PORT"
