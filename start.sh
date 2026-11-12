#!/bin/sh
# ARTEX 守护启动脚本（Linux / macOS / Docker ENTRYPOINT）
#
# 用法：
#   ./start.sh                       前台运行（Ctrl-C 停止）
#   nohup ./start.sh >artex.log 2>&1 &   后台常驻
#   ./start.sh -addr :9000           额外参数原样透传给 artex
#
# 它只做一件事：把 artex 跑起来，进程退出后按退出码决定要不要再拉起。
#
#   0      用户正常停止        → 退出循环
#   75     程序请求重启        → 立刻重跑（页面点了"一键更新"或"回滚"）
#   其他   崩溃                → 退避后重跑（1→2→4…最多 60 秒）
#
# 刻意不在这里做下载、SHA256 校验或换装：那些逻辑在 sh 和 bat 上要写两套，
# 而它们恰恰是最不能出错的一环——一旦换上跑不起来的二进制，本脚本会忠实地
# 反复拉起它，用户只能上机器手工救。所以校验/换装全部留在 Go 里（selfupdate 包），
# 由 artex 自己在启动时完成，脚本保持傻瓜化。
set -u

# 编码：本脚本的提示是中文。跑在没有 UTF-8 locale 的环境里（Docker 默认 POSIX
# locale、systemd 的 LANG=C、cron）时不至于失败，但终端/日志里会变成乱码字节，
# 排查问题时反而更难读。这里把输出固定成 UTF-8。
#
# 关键是"只在真的装了 UTF-8 locale 时才切"：硬设一个不存在的 locale（比如
# en_US.UTF-8 而系统没生成它）会让每个子进程启动都打印
# "setlocale: cannot change locale" —— 那是把乱码换成了报错，问题更糟。
# 已经处于 UTF-8 时一律不动，避免覆盖用户/容器的显式设置。
locale_is_utf8() {
	case "${1:-}" in
		*[Uu][Tt][Ff]-8* | *[Uu][Tt][Ff]8*) return 0 ;;
		*) return 1 ;;
	esac
}

if ! locale_is_utf8 "${LC_ALL:-${LC_CTYPE:-${LANG:-}}}"; then
	for candidate in C.UTF-8 C.utf8 en_US.UTF-8 en_US.utf8; do
		if locale -a 2>/dev/null | grep -qix "$candidate"; then
			LC_ALL=$candidate
			LC_CTYPE=$candidate
			LANG=$candidate
			export LC_ALL LC_CTYPE LANG
			break
		fi
	done
fi

cd "$(dirname "$0")" || exit 1

BIN=./artex
[ -x "$BIN" ] || { echo "[artex] 找不到可执行文件 $BIN" >&2; exit 1; }

RESTART_CODE=75
MAX_DELAY=60

child=0
stopping=0

# 转发停止信号给 artex 本体。
#
# Docker 下这是必需的：docker stop 只把 SIGTERM 发给 PID 1（也就是本脚本），
# 不会发给子进程。不转发的话 artex 收不到信号、做不了优雅关闭，10 秒后被 SIGKILL
# 硬杀，正在跑的任务直接断在半路。
forward() {
	stopping=1
	if [ "$child" -ne 0 ]; then
		kill -TERM "$child" 2>/dev/null || true
	fi
}
trap forward INT TERM

delay=1
while :; do
	"$BIN" "$@" &
	child=$!

	# 信号会打断 wait 并让它返回 >128。此时子进程其实还在做优雅关闭，
	# 必须再 wait 一次才能拿到它真正的退出码。
	wait "$child"
	code=$?
	if [ "$code" -gt 128 ]; then
		wait "$child"
		code=$?
	fi
	child=0

	if [ "$stopping" -eq 1 ]; then
		echo "[artex] 已停止"
		exit 0
	fi

	case "$code" in
		0)
			echo "[artex] 正常退出"
			exit 0
			;;
		"$RESTART_CODE")
			# 更新/回滚已就绪：重跑后 artex 会在启动时完成换装（见 selfupdate.Bootstrap）。
			echo "[artex] 请求重启（应用新版本）…"
			delay=1
			;;
		*)
			echo "[artex] 异常退出 (code=$code)，${delay}s 后重启" >&2
			sleep "$delay"
			delay=$((delay * 2))
			[ "$delay" -gt "$MAX_DELAY" ] && delay=$MAX_DELAY
			;;
	esac
done
