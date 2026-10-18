#!/usr/bin/env bash
# =============================================================================
# ARTEX-REA 管理员密码重置脚本。
#
# 登录用户名固定为 ARTEX-REA；新密码以 bcrypt 哈希写入 SQLite 库的 settings 表。
# 本脚本是 artex-rea 可执行程序 `-reset-password` 的薄封装：定位程序（或回退
# `go run ./cmd/artex`），交互输入新密码，经环境变量 ARTEX_RESET_NEWPASS
# 传给程序——密码不出现在进程 argv，也不写入任何文件。
#
# 数据库定位（与程序启动一致）：
#   ARTEX_SQLITE_PATH > config.json 的 database.path > config.json 旁的 artex.db。
# 可用 --db 指定库文件（等价于导出 ARTEX_SQLITE_PATH），--config 指定配置文件。
#
# 示例：
# ./reset-password.sh                              # 交互输入新密码
# ./reset-password.sh -p 'NewPass!'                # 直接指定（会留在 shell 历史，慎用）
# ./reset-password.sh --bin /opt/artex/artex-rea   # 指定 artex-rea 可执行文件
# ./reset-password.sh --db /data/artex.db          # 指定 SQLite 数据库文件
# ./reset-password.sh -y                           # 跳过确认
# ./reset-password.sh --go                         # 强制 go run（源码目录内）
#
# 依赖：bash 与 read -s（Windows Git Bash / Linux 均可）；回退 go run 需要本机
# 有 Go 工具链，且当前目录是仓库源码目录（存在 go.mod）。
# =============================================================================
set -euo pipefail

BIN=""
DBPATH=""
CONFIG=""
NEWPASS=""
ASSUME_YES=0
FORCE_GO=0

die() { echo "错误：$*" >&2; exit 1; }
info() { echo "· $*" >&2; }

usage() { sed -n '2,24p' "$0" | sed 's/^# \{0,1\}//'; exit 0; }

# 参数解析。
while [[ $# -gt 0 ]]; do
  case "$1" in
    --bin)             BIN="${2:-}"; shift 2 ;;
    --db)              DBPATH="${2:-}"; shift 2 ;;
    --config)          CONFIG="${2:-}"; shift 2 ;;
    -p|--new-password) NEWPASS="${2:-}"; shift 2 ;;
    -y|--yes)          ASSUME_YES=1; shift ;;
    --go)              FORCE_GO=1; shift ;;
    -h|--help)         usage ;;
    *) die "未知参数：$1，使用 -h 查看用法" ;;
  esac
done

# 定位 artex-rea 可执行文件：--bin > 当前目录与脚本目录的 artex-rea/artex-rea.exe > go run 回退。
SCRIPT_DIR=$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" >/dev/null 2>&1 && pwd)

try_bin() {
  local c
  for c in "$@"; do
    [[ -n "$c" && -f "$c" ]] || continue
    if [[ ! -x "$c" ]]; then
      chmod +x "$c" 2>/dev/null || continue
      [[ -x "$c" ]] || continue
    fi
    echo "$c"
    return 0
  done
  return 1
}

CMD=()
if [[ "$FORCE_GO" -eq 1 ]]; then
  command -v go >/dev/null 2>&1 || die "未找到 go 工具链，无法使用 --go"
  CMD=(go run ./cmd/artex)
elif [[ -n "$BIN" ]]; then
  if [[ -f "$BIN" ]]; then
    CMD=("$BIN")
  elif resolved=$(command -v "$BIN" 2>/dev/null); then
    CMD=("$resolved")
  else
    die "--bin 指定的可执行文件不存在：$BIN"
  fi
elif FOUND=$(try_bin "./artex-rea" "./artex-rea.exe" "$SCRIPT_DIR/artex-rea" "$SCRIPT_DIR/artex-rea.exe" "./artex" "./artex.exe" "$SCRIPT_DIR/artex" "$SCRIPT_DIR/artex.exe"); then
  CMD=("$FOUND")
elif [[ -f go.mod ]] && command -v go >/dev/null 2>&1; then
  info "未找到 artex-rea 二进制，回退 go run ./cmd/artex"
  CMD=(go run ./cmd/artex)
else
  die "未找到 artex-rea/artex-rea.exe（当前目录与脚本目录均无），且无法回退 go run；请先用 build.sh 构建，或用 --bin 指定可执行文件。"
fi
info "使用：${CMD[*]} -reset-password"

# 获取新密码（不进 argv；经 ARTEX_RESET_NEWPASS 传给程序）。
if [[ -z "$NEWPASS" ]]; then
  [[ -t 0 ]] || die "未指定 -p 且标准输入不是终端，无法交互输入新密码"
  read -r -s -p "输入新密码，用户名固定为 ARTEX-REA：" NEWPASS; echo >&2
  [[ -n "$NEWPASS" ]] || die "密码不能为空"
  read -r -s -p "再次输入以确认：" NEWPASS2; echo >&2
  [[ "$NEWPASS" == "$NEWPASS2" ]] || die "两次输入不一致"
fi
[[ -n "$NEWPASS" ]] || die "密码不能为空"

# 确认与执行（结果提示由程序本身打印，退出码原样透传）。
if [[ "$ASSUME_YES" -ne 1 ]]; then
  read -r -p "确认重置 ARTEX-REA 管理员密码？[y/N]" ans
  [[ "$ans" == "y" || "$ans" == "Y" ]] || die "已取消"
fi

export ARTEX_RESET_NEWPASS="$NEWPASS"
if [[ -n "$DBPATH" ]]; then export ARTEX_SQLITE_PATH="$DBPATH"; fi
if [[ -n "$CONFIG" ]]; then export ARTEX_CONFIG="$CONFIG"; fi

"${CMD[@]}" -reset-password
