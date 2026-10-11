#!/usr/bin/env bash
set -euo pipefail

# 1. 安装 Next.js 依赖并静态构建前端。
echo "[*] Building Frontend..."
cd web
npm ci
npm run build:static
cd ..

# 2. 将前端产物复制到 Go embed 的内嵌目录。
echo "[*] Copying static assets..."
mkdir -p server/webui/dist
cp -r web/out/* server/webui/dist/

# 3. 编译单二进制，必须使用 embedui tag。
echo "[*] Compiling Go Backend..."
CGO_ENABLED=0 go build -tags embedui -o artex ./cmd/artex

echo "[+] Build Completed successfully!"
