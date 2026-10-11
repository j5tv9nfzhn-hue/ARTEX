#!/usr/bin/env bash
# ARTEX 更新脚本：① 拉取 Docker 镜像并重建；② 本地重新编译。
# install.sh 负责首次安装，update.sh 负责升级已有部署。
# 无需手动迁移；artex 每次启动都幂等执行 schema.sql。
# 包含 ADD COLUMN / CREATE INDEX IF NOT EXISTS，重启即迁移，不修改 pgdata、data 或 skills 数据。
set -euo pipefail
cd "$(cd "$(dirname "$0")" && pwd)"

info(){ printf '\033[36m[*]\033[0m %s\n' "$*"; }
ok(){   printf '\033[32m[+]\033[0m %s\n' "$*"; }
warn(){ printf '\033[33m[!]\033[0m %s\n' "$*"; }
die(){  printf '\033[31m[x]\033[0m %s\n' "$*" >&2; exit 1; }
ask(){  local p="$1" d="${2:-}" a; read -rp "$p${d:+ [$d]}: " a; echo "${a:-$d}"; }

# 可选：更新仓库代码，使 Compose、脚本与本地编译源码保持一致。
sync_repo(){
  [ -d .git ] && command -v git >/dev/null 2>&1 || { warn "当前不是 Git 工作副本，跳过 git pull"; return; }
  [ "$(ask '是否获取最新代码（git pull --ff-only）？(y/n)' y)" = y ] || return
  if ! git pull --ff-only; then
    warn "无法快进 git pull，可能有本地修改或分支分叉。请手动处理后重试，本次继续使用现有代码。"
  fi
}

# ① Docker 更新。
update_docker(){
  command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1 \
    || die "未找到 docker / docker compose，请先通过 ./install.sh 安装部署"
  [ -f .env ] || die "未找到 .env，请先通过 ./install.sh 完成首次部署"

  # 可选指定镜像 tag；留空使用 .env 的 ARTEX_TAG，默认 latest。
  local tag; tag="$(ask '目标镜像 tag，回车使用 .env / latest' '')"
  if [ -n "$tag" ]; then
    if grep -q '^ARTEX_TAG=' .env; then
      sed -i.bak "s|^ARTEX_TAG=.*|ARTEX_TAG=${tag}|" .env && rm -f .env.bak
    else
      printf '\nARTEX_TAG=%s\n' "$tag" >> .env
    fi
    ok "ARTEX_TAG 已设置为 ${tag}"
  fi

  # 仅更新 artex，PostgreSQL 固定为 16-alpine，不随应用升级。
  # 无需额外下载数据库镜像，也避免数据库主版本改变的兼容性风险。
  # artex 的 depends_on 会在 PostgreSQL 未运行时启动它；已运行则保持不变。
  info "正在获取新镜像，仅更新 artex…"
  if ! docker compose pull artex; then
    warn "无法获取上游镜像 autumn27/artex，请检查镜像可访问性、权限及 Docker 的具体错误"
    die "当前 Docker 镜像更新不可用。请选择 \"2) 本地更新（Go 重新编译）\" 获得本分支的中文版本。"
  fi
  info "正在重建并启动，artex 重启时自动迁移 schema…"
  docker compose up -d artex
  ok "更新完成 → http://localhost:8787"
  info "查看日志：docker compose logs -f artex"
  info "可选清理旧镜像：docker image prune -f"
}

# ② 本地编译更新。
update_local(){
  command -v go >/dev/null 2>&1 || die "未找到 Go >=1.26：https://go.dev/dl/"
  [ -f config.json ] || warn "未找到 config.json，首次部署请使用 ./install.sh"
  ok "Go: $(go version)"

  if command -v npm >/dev/null 2>&1; then
    info "正在重新构建前端静态产物…"
    ( cd web && npm ci && npm run build:static )
    rm -rf server/webui/dist && cp -r web/out server/webui/dist
    info "正在重新编译内嵌前端的单二进制…"
    CGO_ENABLED=0 go build -tags embedui -trimpath -o artex ./cmd/artex
  else
    warn "未找到 npm，仅编译不内嵌前端的后端；前端需另用 npm run dev 启动"
    CGO_ENABLED=0 go build -o artex ./cmd/artex
  fi
  ok "编译完成 → ./artex"
  warn "请重启现有 artex 进程应用改动，重启时会自动迁移 schema"
}

echo "=============================="
echo "  ARTEX 更新"
echo "  1) Docker 更新（获取新镜像并重建）"
echo "  2) 本地更新（Go 重新编译）"
echo "=============================="
case "$(ask '请选择' 1)" in
  1) sync_repo; update_docker ;;
  2) sync_repo; update_local ;;
  *) die "无效选择" ;;
esac
