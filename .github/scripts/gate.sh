#!/usr/bin/env bash
# gate：流水线唯一入口判定。不做构建，只把「这次构建什么、要不要发布」钉死。
# 任何参数错误都在这里失败，避免把无效参数带进 plan / job 白烧机器时间。
#
# 输入（env）：EVENT REF INPUT_PLATFORM INPUT_TAG INPUT_REF SIGNING_KEY SHA RUN_NUMBER REPO
# 输出（$GITHUB_OUTPUT）：release version tag platform build_ref signing
set -euo pipefail

err()  { echo "::error::$*" >&2; exit 1; }
info() { echo "[gate] $*"; }

ALLOWED_PLATFORMS="all linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64"

# ── 1. 平台参数 ──────────────────────────────────────────────────────
platform="${INPUT_PLATFORM:-}"
[ -z "$platform" ] && platform="all"
case " $ALLOWED_PLATFORMS " in
  *" $platform "*) info "目标平台：$platform" ;;
  *) err "不支持的平台 '$platform'；可选：$ALLOWED_PLATFORMS" ;;
esac

# ── 2. 是否进入 release：只有「显式 tag」才置真 ───────────────────────
release=false
tag=""
if [ "$EVENT" = "push" ] && [ "${REF#refs/tags/v}" != "$REF" ]; then
  release=true
  tag="${REF#refs/tags/}"
  info "触发源：push tag ${tag}"
fi
if [ -n "${INPUT_TAG:-}" ]; then
  release=true
  tag="$INPUT_TAG"
  info "触发源：手动指定 tag ${tag}"
fi
if [ "$EVENT" = "pull_request" ]; then
  # PR 一律不发布：分支名长得像 tag 也不行，避免从 PR 产出 Release
  [ "$release" = "true" ] && info "PR 触发：忽略 tag，不进入 release"
  release=false
  tag=""
fi

# ── 3. 版本号与标签 ─────────────────────────────────────────────────
if [ "$release" = "true" ]; then
  [[ "$tag" =~ ^v[0-9]+\.[0-9]+\.[0-9]+([-.][0-9A-Za-z.-]+)?$ ]] \
    || err "标签格式非法：'${tag}'（要求 vX.Y.Z 或 vX.Y.Z-rc.1）"
  version="${tag#v}"
  # 标签必须已存在于远端：Release 要挂在真实 tag 上，而不是临时造一个
  if [ -n "${REPO:-}" ] && ! git ls-remote --exit-code --tags \
      "https://github.com/${REPO}.git" "refs/tags/${tag}" >/dev/null 2>&1; then
    err "远端不存在标签 ${tag}；请先 git push origin ${tag} 再触发发布"
  fi
  info "发布版本：${version}（tag ${tag}）"
else
  version="0.0.0-cl.${RUN_NUMBER}-${SHA:0:7}"
  info "CL 构建（不发布），版本：${version}"
fi

# ── 4. 签名能力 ─────────────────────────────────────────────────────
if [ -n "${SIGNING_KEY:-}" ]; then
  signing=pem
elif [ "$release" = "true" ]; then
  signing=none
  echo "::warning::未配置 ARTEX_SIGNING_KEY：本次 Release 只提供 SHA256SUMS，不含签名"
else
  signing=none
fi
info "签名模式：${signing}"

build_ref="${INPUT_REF:-}"
[ -z "$build_ref" ] && build_ref="$REF"

{
  echo "release=${release}"
  echo "version=${version}"
  echo "tag=${tag}"
  echo "platform=${platform}"
  echo "build_ref=${build_ref}"
  echo "signing=${signing}"
} >> "$GITHUB_OUTPUT"

{
  echo "## gate 准入"
  echo ""
  echo "| 项 | 值 |"
  echo "| --- | --- |"
  echo "| 触发 | \`${EVENT}\` |"
  echo "| 构建 ref | \`${build_ref}\` |"
  echo "| 平台 | \`${platform}\` |"
  echo "| 版本 | \`${version}\` |"
  echo "| 进入 release | \`${release}\` |"
  echo "| 签名 | \`${signing}\` |"
} >> "$GITHUB_STEP_SUMMARY"
