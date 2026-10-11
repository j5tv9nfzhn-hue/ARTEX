#!/usr/bin/env bash
# =============================================================================
# ARTEX 管理员密码重置脚本。
#
# 登录用户名固定为 ARTEX；数据库 settings 表中的 auth.password_hash 保存 bcrypt 哈希。
# 通过 pgcrypto 在数据库内计算 bcrypt，并重新写入该设置。
# 与后端 golang.org/x/crypto/bcrypt 的登录校验兼容。
#
# 两种部署方式：
# local：默认在宿主通过 psql 直连数据库，连接信息优先级如下。
# 命令行参数 > --dsn / ARTEX_PG_DSN > config.json 的 database 字段。
# docker：用 docker compose exec 或 docker exec 在 PostgreSQL 容器执行 psql。
# Compose 默认不向宿主开放 5432，因此需在容器内操作。
#
# 示例：
# ./reset-password.sh                          # 本地读取配置并交互输入新密码
# ./reset-password.sh -p 'NewPass!'            # 本地，直接指定新密码
#   ./reset-password.sh --dsn postgres://u:p@h:5432/artex
#   ./reset-password.sh -H 127.0.0.1 -P 5433 -U autopentest -W pass -d artex
# ./reset-password.sh -m docker                # 读取 .env 的 POSTGRES_*，在 Docker 操作
# ./reset-password.sh -m docker -c pg-container --exec docker
#
# 新密码以环境变量与 psql \getenv 传入，不出现在进程 argv。
# 使用 :'var' 自动转义，避免 SQL 注入；数据库密码由 PGPASSWORD 传入。
# =============================================================================
set -euo pipefail

PASS_KEY="auth.password_hash"
BCRYPT_COST=10

MODE=""            # local | docker，留空自动判断
DSN=""
HOST="" PORT="" USER="" DBPASS="" DBNAME="" SSLMODE=""
CONFIG=""
CONTAINER=""       # Docker 的 PostgreSQL 服务或容器名，默认 postgres
EXEC_KIND=""       # compose | docker，留空自动选择
NEWPASS=""
ASSUME_YES=0

die() { echo "错误：$*" >&2; exit 1; }
info() { echo "· $*" >&2; }

usage() { sed -n '2,25p' "$0" | sed 's/^# \{0,1\}//'; exit 0; }

# 参数解析。
while [[ $# -gt 0 ]]; do
  case "$1" in
    -m|--mode)        MODE="${2:-}"; shift 2 ;;
    --dsn)            DSN="${2:-}"; shift 2 ;;
    -H|--host)        HOST="${2:-}"; shift 2 ;;
    -P|--port)        PORT="${2:-}"; shift 2 ;;
    -U|--user)        USER="${2:-}"; shift 2 ;;
    -W|--db-password) DBPASS="${2:-}"; shift 2 ;;
    -d|--dbname)      DBNAME="${2:-}"; shift 2 ;;
    --sslmode)        SSLMODE="${2:-}"; shift 2 ;;
    --config)         CONFIG="${2:-}"; shift 2 ;;
    -c|--container)   CONTAINER="${2:-}"; shift 2 ;;
    --exec)           EXEC_KIND="${2:-}"; shift 2 ;;
    -p|--new-password) NEWPASS="${2:-}"; shift 2 ;;
    -y|--yes)         ASSUME_YES=1; shift ;;
    -h|--help)        usage ;;
    *) die "未知参数：$1，使用 -h 查看用法" ;;
  esac
done

# 未显式指定连接信息时，在 local 模式读取 config.json 的 database。
# 优先用 python3 可靠解析；没有 Python 时对分离字段使用 grep 后备处理。
read_config_json() {
  local path="$1"
  [[ -f "$path" ]] || return 1
  if command -v python3 >/dev/null 2>&1; then
    python3 - "$path" <<'PY'
import json, sys
try:
    d = json.load(open(sys.argv[1])).get("database", {})
except Exception:
    sys.exit(1)
# 支持完整 DSN 或分离连接字段。
if d.get("dsn"):
    print("DSN\t" + d["dsn"]); sys.exit(0)
for k in ("host","port","user","password","dbname","sslmode"):
    if d.get(k) is not None:
        print(k.upper() + "\t" + str(d[k]))
PY
  else
    # 简单后备逻辑：逐键 grep，值可为字符串或数字。
    local k
    for k in host port user password dbname sslmode; do
      local v
      v=$(grep -oE "\"$k\"[[:space:]]*:[[:space:]]*(\"[^\"]*\"|[0-9]+)" "$path" 2>/dev/null \
            | head -1 | sed -E "s/.*:[[:space:]]*//; s/^\"//; s/\"$//") || true
      [[ -n "$v" ]] && echo -e "${k^^}\t$v"
    done
  fi
}

apply_config_fields() {
  local line key val
  while IFS=$'\t' read -r key val; do
    [[ -z "$key" ]] && continue
    case "$key" in
      DSN)      [[ -z "$DSN" ]] && DSN="$val" ;;
      HOST)     [[ -z "$HOST" ]] && HOST="$val" ;;
      PORT)     [[ -z "$PORT" ]] && PORT="$val" ;;
      USER)     [[ -z "$USER" ]] && USER="$val" ;;
      PASSWORD) [[ -z "$DBPASS" ]] && DBPASS="$val" ;;
      DBNAME)   [[ -z "$DBNAME" ]] && DBNAME="$val" ;;
      SSLMODE)  [[ -z "$SSLMODE" ]] && SSLMODE="$val" ;;
    esac
  done
}

# 自动判断运行模式。
if [[ -z "$MODE" ]]; then
  if [[ -n "$DSN$HOST$USER$DBNAME" || -n "${ARTEX_PG_DSN:-}" || -f "${CONFIG:-config.json}" ]]; then
    MODE="local"
  elif command -v docker >/dev/null 2>&1 && [[ -f docker-compose.yml ]]; then
    MODE="docker"
  else
    MODE="local"
  fi
fi
info "部署模式：$MODE"

# 获取新密码。
if [[ -z "$NEWPASS" ]]; then
  read -r -s -p "输入新密码，用户名固定为 ARTEX：" NEWPASS; echo >&2
  [[ -n "$NEWPASS" ]] || die "密码不能为空"
  read -r -s -p "再次输入以确认：" NEWPASS2; echo >&2
  [[ "$NEWPASS" == "$NEWPASS2" ]] || die "两次输入不一致"
fi
[[ -n "$NEWPASS" ]] || die "密码不能为空"

# 以环境变量传给 psql，再由 \getenv 读取，不进入 argv 或 ps 输出。
export ARTEX_RESET_NEWPASS="$NEWPASS"

# 数据库内计算 bcrypt 后 upsert，:'newpw' 自动转义，CREATE EXTENSION 幂等。
# 角色无扩展创建权限时可能失败，见执行失败分支的处理提示。
SQL=$(cat <<SQL
\\set ON_ERROR_STOP on
\\getenv newpw ARTEX_RESET_NEWPASS
CREATE EXTENSION IF NOT EXISTS pgcrypto;
INSERT INTO settings(key, value)
VALUES ('$PASS_KEY', crypt(:'newpw', gen_salt('bf', $BCRYPT_COST)))
ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, updated_at = now();
SQL
)

# 执行。
if [[ "$MODE" == "local" ]]; then
  # 连接优先级：命令行 > --dsn / ARTEX_PG_DSN > config.json。
  if [[ -z "$DSN" && -z "$HOST$USER$DBNAME" ]]; then
    [[ -n "${ARTEX_PG_DSN:-}" ]] && DSN="$ARTEX_PG_DSN"
  fi
  if [[ -z "$DSN" && -z "$HOST$USER$DBNAME" ]]; then
    cfg="${CONFIG:-config.json}"
    if [[ -f "$cfg" ]]; then
      info "正在从 $cfg 读取数据库配置"
      apply_config_fields < <(read_config_json "$cfg")
    fi
  fi

  command -v psql >/dev/null 2>&1 || die "未找到 psql，请安装 postgresql-client 或使用 -m docker"

  declare -a PSQL_ARGS=()
  if [[ -n "$DSN" ]]; then
    PSQL_ARGS=("$DSN")
    target="$DSN"
  else
    [[ -n "$USER"   ]] || die "缺少数据库用户 -U，且 config.json / DSN 不可用"
    [[ -n "$DBNAME" ]] || die "缺少数据库名 -d，且 config.json / DSN 不可用"
    HOST="${HOST:-127.0.0.1}"; PORT="${PORT:-5432}"; SSLMODE="${SSLMODE:-disable}"
    PSQL_ARGS=(-h "$HOST" -p "$PORT" -U "$USER" -d "$DBNAME")
    [[ -n "$SSLMODE" ]] && export PGSSLMODE="$SSLMODE"
    [[ -n "$DBPASS" ]] && export PGPASSWORD="$DBPASS"
    target="$USER@$HOST:$PORT/$DBNAME"
  fi

  info "目标数据库：$target"
  if [[ "$ASSUME_YES" -ne 1 ]]; then
    read -r -p "是否在此数据库重置 ARTEX 密码？[y/N]" ans
    [[ "$ans" == "y" || "$ans" == "Y" ]] || die "已取消"
  fi

  if ! printf '%s\n' "$SQL" | psql "${PSQL_ARGS[@]}" -v ON_ERROR_STOP=1 -q >/dev/null; then
    die "写入失败。如提示 pgcrypto 缺失或权限不足，请使用有扩展创建权限的角色，或先手动执行 CREATE EXTENSION pgcrypto。"
  fi

else
  # ---- docker ----
  command -v docker >/dev/null 2>&1 || die "未找到 docker"
  CONTAINER="${CONTAINER:-postgres}"

  # 优先 docker compose exec 按服务名操作，其次 docker exec 按容器名操作。
  if [[ -z "$EXEC_KIND" ]]; then
    if docker compose version >/dev/null 2>&1 && [[ -f docker-compose.yml ]]; then
      EXEC_KIND="compose"
    else
      EXEC_KIND="docker"
    fi
  fi

  # 容器内 psql 凭据优先使用命令行，然后是 .env 的 POSTGRES_*，最后为 Compose 默认值 artex。
  if [[ -f .env ]]; then
    # shellcheck disable=SC1091
    set -a; . ./.env; set +a
  fi
  DUSER="${USER:-${POSTGRES_USER:-artex}}"
  DNAME="${DBNAME:-${POSTGRES_DB:-artex}}"
  [[ -n "$DBPASS" ]] && export PGPASSWORD="$DBPASS"
  [[ -z "${PGPASSWORD:-}" && -n "${POSTGRES_PASSWORD:-}" ]] && export PGPASSWORD="$POSTGRES_PASSWORD"

  info "目标：容器 $CONTAINER 内 psql -U $DUSER -d $DNAME，exec=$EXEC_KIND"
  if [[ "$ASSUME_YES" -ne 1 ]]; then
    read -r -p "是否在此容器数据库重置 ARTEX 密码？[y/N]" ans
    [[ "$ans" == "y" || "$ans" == "Y" ]] || die "已取消"
  fi

  # docker -e 仅传变量名，值继承环境，密码不出现在命令行 argv。
  declare -a EXEC_CMD
  if [[ "$EXEC_KIND" == "compose" ]]; then
    EXEC_CMD=(docker compose exec -T -e ARTEX_RESET_NEWPASS -e PGPASSWORD "$CONTAINER"
              psql -U "$DUSER" -d "$DNAME" -v ON_ERROR_STOP=1 -q)
  else
    EXEC_CMD=(docker exec -i -e ARTEX_RESET_NEWPASS -e PGPASSWORD "$CONTAINER"
              psql -U "$DUSER" -d "$DNAME" -v ON_ERROR_STOP=1 -q)
  fi

  if ! printf '%s\n' "$SQL" | "${EXEC_CMD[@]}" >/dev/null; then
    die "写入失败，请检查容器名 -c、.env 的 POSTGRES_* 与角色的 pgcrypto 权限。"
  fi
fi

unset ARTEX_RESET_NEWPASS
echo "✓ ARTEX 管理员密码已重置，请以 ARTEX 和新密码登录，无需重启服务。"
echo "  旧会话 token 将在数秒内全部失效。若用于账户被抢占或泄露后的恢复，请同时检查自定义工具、MCP、LLM 和通知配置。"
