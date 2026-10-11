# syntax=docker/dockerfile:1
#
# 仅用于运行的镜像：安装常用工具，放入已交叉编译的 Linux 单二进制。
# CI binaries job 使用纯 Go，不需要 QEMU 编译。
# 各架构二进制位于构建上下文 dist/<TARGETARCH>/artex。
# arm64 仅模拟 apt 层，Next、Go 编译不再模拟，提高构建速度。
#
# 本地手动构建镜像前，先准备二进制：
#   cd web && npm run build:static && cd ..
#   mkdir -p server/webui && cp -r web/out server/webui/dist
#   CGO_ENABLED=0 GOARCH=amd64 go build -tags embedui -o dist/amd64/artex ./cmd/artex
#   docker build -t artex:local .
FROM python:3.12-slim-bookworm
ARG TARGETARCH
# 常用工具：ripgrep、curl、vim 与侦察工具，可按实际需要增减。
# 通过 NodeSource 安装 Node 20；bookworm 默认 Node 18，不满足 Playwright >=20。
RUN apt-get update && apt-get install -y --no-install-recommends \
      ca-certificates ripgrep curl wget vim git jq unzip \
      dnsutils iputils-ping netcat-openbsd inetutils-telnet whois nmap \
    && curl -fsSL https://deb.nodesource.com/setup_20.x | bash - \
    && apt-get install -y --no-install-recommends nodejs \
    && rm -rf /var/lib/apt/lists/*
# 全局预装 Playwright MCP、CLI，避免运行时 npx 下载依赖。
# @playwright/mcp 可直接以 npx @playwright/mcp 启动，无需 -y 或 @latest。
# @playwright/cli 提供 playwright-cli，安装后通过 --help 验证可执行。
# 再安装 playwright，并用 --with-deps 预装 Chromium 及系统依赖。
# 容器首次启动即可使用浏览器，不再临时联网下载。
RUN npm install -g @playwright/mcp@latest @playwright/cli@latest playwright@latest \
    && playwright-cli --help \
    && playwright install --with-deps chromium \
    && rm -rf /var/lib/apt/lists/*
WORKDIR /app
# 放入对应架构的预编译二进制：dist/amd64/artex 或 dist/arm64/artex。
COPY dist/${TARGETARCH}/artex /app/artex
# 启动监护脚本按退出码决定是否重启，完成页面一键更新的版本切换。
# 同时将 SIGTERM 转发到 artex；docker stop 只向 PID 1 发送信号。
# 不转发会导致无法优雅退出，十秒后被 SIGKILL 强制中止。
COPY start.sh /app/start.sh
RUN chmod +x /app/artex /app/start.sh
COPY skills/ /app/skills/
# data 持久化目录，包含 SQLite 与密钥等运行数据。
VOLUME ["/app/data"]
EXPOSE 8787 8788
ENTRYPOINT ["/app/start.sh"]
CMD ["-addr", ":8787", "-proxy", ":8788"]
