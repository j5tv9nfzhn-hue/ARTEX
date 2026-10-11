#!/usr/bin/env bash
# 开发模式：后端 127.0.0.1:8787、流量代理 :8788 与前端 next dev :5173。
# 前端 /api 反向代理到后端，Ctrl-C 同时退出两者。
#
# 内嵌前端的单二进制模式请参见 README，不使用此开发脚本。
set -euo pipefail
cd "$(dirname "$0")"

# 退出时结束本进程组全部子进程，包括前、后端。
cleanup() { kill 0 2>/dev/null || true; }
trap cleanup EXIT INT TERM

# 后端采用无 embedui 的 go run；并发 Worker 数量在系统设置中配置。
go run ./cmd/artex -addr 127.0.0.1:8787 -proxy 127.0.0.1:8788 &

# 前端热更新，/api 代理到 :8787。
( cd web && npm run dev ) &

echo "[dev] 后端 :8787 / 代理 :8788 / 前端 http://localhost:5173（Ctrl-C 退出）"
wait
