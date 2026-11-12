#!/usr/bin/env bash
# dispatch：从本地一键触发 CI（CL）构建；可只构建单个平台。
#
# 用法：
#   .github/scripts/dispatch.sh                       # 全平台 CL 构建（不发布）
#   .github/scripts/dispatch.sh linux/amd64           # 只构建 linux/amd64
#   .github/scripts/dispatch.sh windows/arm64         # 只构建 windows/arm64
#   .github/scripts/dispatch.sh all v1.2.3            # 带 tag → 进入 release 流程
#   .github/scripts/dispatch.sh all "" feature/x      # 指定远端分支/提交
#   .github/scripts/dispatch.sh --list                # 列出可选平台
#
# 依赖：gh（已登录）；仓库取当前目录的 origin。
set -euo pipefail

ALLOWED="all linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64"

usage() {
  sed -n '2,15p' "$0" | sed 's/^# \{0,1\}//'
}

if [ "${1:-}" = "--list" ] || [ "${1:-}" = "-l" ]; then
  echo "可选平台："
  for p in $ALLOWED; do echo "  - $p"; done
  exit 0
fi
if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
  usage
  exit 0
fi

PLATFORM="${1:-all}"
TAG="${2:-}"
REF="${3:-}"

case " $ALLOWED " in
  *" $PLATFORM "*) ;;
  *)
    echo "不支持的平台：$PLATFORM" >&2
    echo "可选：$ALLOWED" >&2
    exit 1
    ;;
esac

command -v gh >/dev/null 2>&1 || { echo "未安装 gh（GitHub CLI）" >&2; exit 1; }

args=(-f "platform=$PLATFORM")
[ -n "$TAG" ] && args+=(-f "tag=$TAG")
[ -n "$REF" ] && args+=(-f "ref=$REF")

echo "[dispatch] 平台=$PLATFORM  标签=${TAG:-<无，不发布>}  ref=${REF:-<当前>}"
gh workflow run ci.yml "${args[@]}"

sleep 3
echo "[dispatch] 已触发，最近一次运行："
gh run list --workflow=ci.yml --limit 1
echo
echo "跟踪日志： gh run watch \$(gh run list --workflow=ci.yml --limit 1 --json databaseId -q '.[0].databaseId')"
echo "下载产物： gh run download <run-id> -n artex-CL -D ACTION"
