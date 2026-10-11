#!/usr/bin/env bash
# ARTEX 安装脚本：① 全部 Docker；② 本地编译运行。
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"

info(){ printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok(){   printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn(){ printf '\033[33m[!]\033[0m %s\n' "$*"; }
die(){  printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }
ask(){  local p="$1" d="${2:-}" a; read -rp "$p${d:+ [$d]}: " a; echo "${a:-$d}"; }
rand(){ head -c 18 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 24; }

# 检测 Docker 环境，必要时安装。
ensure_docker(){
  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    ok "已找到 docker 与 docker compose"; return
  fi
  warn "未找到 docker / docker compose"
  case "$(uname -s)" in
    Linux)
      if [ "$(ask '是否自动安装 Docker？(y/n)' y)" = y ]; then
        curl -fsSL https://get.docker.com | sh
        sudo usermod -aG docker "$USER" || true
        ok "Docker 安装完成；用户组变更需重新登录后才能免 sudo 使用"
      else
        die "请手动安装 docker 后重试"
      fi ;;
    Darwin) die "macOS 请安装 Docker Desktop：https://www.docker.com/products/docker-desktop/" ;;
    *)      die "请手动安装 docker 后重试" ;;
  esac
}

# ① 全部 Docker。
install_docker(){
  ensure_docker
  if [ ! -f .env ]; then
    cp .env.example .env 2>/dev/null || true
    local pw key
    pw="$(ask 'Postgres 密码，回车随机生成' "$(rand)")"
    key="$(ask 'ANTHROPIC_API_KEY，可留空，稍后在 UI 配置' '')"
    sed -i.bak "s|^POSTGRES_PASSWORD=.*|POSTGRES_PASSWORD=${pw}|" .env
    sed -i.bak "s|^ANTHROPIC_API_KEY=.*|ANTHROPIC_API_KEY=${key}|" .env
    rm -f .env.bak
    ok "已生成 .env，POSTGRES_PASSWORD 已设置"
  else
    info "使用现有 .env 文件"
  fi
  info "正在获取镜像并启动…"
  docker compose pull || true
  if ! docker compose up -d; then
    warn "无法拉取上游镜像 autumn27/artex，启动失败；可能为镜像不可访问或权限问题，请查看 Docker 的具体错误"
    warn "请重新运行并选择 \"2) 本地运行（Go 编译）\"，或按 README 的源码单二进制构建流程获得中文 UI 与输出"
    die "无法获取镜像，当前 Docker Compose 全 Docker 路径不可用"
  fi
  ok "启动完成 → http://localhost:8787"
  info "首次访问 /setup 时需输入设置令牌，才能创建管理员密码"
  info "查看设置令牌：docker compose logs artex | grep '设置令牌'；默认仅发布到 127.0.0.1，可通过 .env 的 ARTEX_BIND 修改"
  info "查看日志：docker compose logs -f artex"
}

# ② 本地编译运行。
install_local(){
  echo "数据库安装方式："
  echo "  1) 连接现有 PostgreSQL"
  echo "  2) 用 Docker 启动 PostgreSQL（需要 Docker）"
  case "$(ask '请选择' 1)" in
    2)
      ensure_docker
      local pw; pw="$(ask 'Postgres 密码，回车随机生成' "$(rand)")"
      docker run -d --name artex-pg -p 5432:5432 \
        -e POSTGRES_USER=artex -e POSTGRES_PASSWORD="$pw" -e POSTGRES_DB=artex \
        -v artex-pg:/var/lib/postgresql/data postgres:16-alpine
      DB_HOST=127.0.0.1 DB_PORT=5432 DB_USER=artex DB_PASS="$pw" DB_NAME=artex DB_SSL=disable ;;
    *)
      DB_HOST="$(ask '数据库地址' 127.0.0.1)"
      DB_PORT="$(ask '端口' 5432)"
      DB_USER="$(ask '账户' artex)"
      DB_PASS="$(ask '密码' '')"
      DB_NAME="$(ask '数据库名' artex)"
      DB_SSL="$(ask 'sslmode (disable/require)' disable)" ;;
  esac

  # 生成 config.json。
  cat > config.json <<JSON
{
  "database": {
    "host": "${DB_HOST}",
    "port": ${DB_PORT},
    "user": "${DB_USER}",
    "password": "${DB_PASS}",
    "dbname": "${DB_NAME}",
    "sslmode": "${DB_SSL}"
  }
}
JSON
  ok "已生成 config.json"

  # 检查 Go 环境。
  command -v go >/dev/null 2>&1 || die "未找到 Go，请先安装 Go >=1.26：https://go.dev/dl/"
  ok "Go: $(go version)"

  # 内嵌前端需要使用 Node 构建静态产物。
  if command -v npm >/dev/null 2>&1; then
    info "正在构建前端静态产物…"
    ( cd web && npm ci && npm run build:static )
    rm -rf server/webui/dist && mkdir -p server/webui && cp -r web/out server/webui/dist
    info "正在编译内嵌前端的单二进制…"
    CGO_ENABLED=0 go build -tags embedui -trimpath -o artex ./cmd/artex
  else
    warn "未找到 npm，仅编译后端，不内嵌前端；前端需单独通过 npm run dev 启动"
    CGO_ENABLED=0 go build -o artex ./cmd/artex
  fi
  ok "编译完成 → ./artex"

  info "正在启动，Ctrl-C 退出。默认绑定 127.0.0.1:8787；首次访问时将日志中的 [auth] 设置令牌填入 /setup"
  ./artex
}

echo "=============================="
echo "  ARTEX 安装"
echo "  1) 全部 Docker 安装"
echo "  2) 本地运行（Go 编译）"
echo "=============================="
case "$(ask '请选择' 1)" in
  1) install_docker ;;
  2) install_local ;;
  *) die "无效选择" ;;
esac
