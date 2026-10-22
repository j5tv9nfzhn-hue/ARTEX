#!/bin/sh
# ARTEX-REA 启动脚本（Unix / Docker ENTRYPOINT，现在是可选兜底）
#
# 不装这个脚本也能用：artex-rea 自己会做单实例仲裁，更新装好后也会自己拉起
# 新进程重启。只有想让它崩溃后也自动重开时才需要这个脚本。
#
# 用法：
# ./start.sh                    前台运行，Ctrl-C 停止
# nohup ./start.sh >artex-rea.log 2>&1 &   后台运行
# ./start.sh -addr :9000        命令行原样传给 artex-rea
#
# 退出码约定：
# 0     正常停止，脚本退出（不再循环）
# 75    请求重启（例如网页上点了"立即更新"），脚本立刻重启进程
# 其它  异常退出：按 1/2/4 秒递增等待后重试，最多 60 秒
#
# 下载、SHA256 校验、版本切换全部在 Go 的 selfupdate 包里实现，这里只负责
# "进程死了要不要再起一个"。
set -u

cd "$(dirname "$0")" || exit 1

# 告诉 artex-rea「外面有个循环会在我以 75 退出后把我重新拉起来」。
# 只有这个变量为真时，页面才允许自动更新：换装后进程要退出，没人拉起就变成
# 「更新完打不开」。直接跑二进制（没有本脚本）时它为假，界面会禁用自动更新。
ARTEX_SUPERVISED=1
export ARTEX_SUPERVISED

BIN=./artex-rea
if [ ! -x "$BIN" ] && [ -x ./artex ]; then
	BIN=./artex
fi
[ -x "$BIN" ] || { echo "[artex-rea] 找不到可执行文件：$BIN" >&2; exit 1; }

RESTART_CODE=75
MAX_DELAY=60

child=0
stopping=0

# 将停止信号转发给 artex-rea。
#
# Docker 必须转发，因为 docker stop 只向 PID 1，即本脚本发送 SIGTERM。
# 子进程不会自动收到信号，不转发会阻止优雅退出。
# 十秒后 SIGKILL 强制终止，会中断正在执行的任务。
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

	# 信号中断 wait 时会返回大于 128 的值，但子进程可能仍在优雅退出。
	# 再 wait 一次，取得真正的退出码。
	wait "$child"
	code=$?
	if [ "$code" -gt 128 ]; then
		wait "$child"
		code=$?
	fi
	child=0

	if [ "$stopping" -eq 1 ]; then
		echo "[artex-rea] 已停止"
		exit 0
	fi

	case "$code" in
		0)
			echo "[artex-rea] 正常退出"
			exit 0
			;;
		"$RESTART_CODE")
			# 更新或回滚已准备，重新运行时完成版本替换，参见 selfupdate.Bootstrap。
			echo "[artex-rea] 收到重启请求，正在应用新版本…"
			delay=1
			;;
		*)
			echo "[artex-rea] 异常退出 (code=$code)，${delay} 秒后重启" >&2
			sleep "$delay"
			delay=$((delay * 2))
			[ "$delay" -gt "$MAX_DELAY" ] && delay=$MAX_DELAY
			;;
	esac
done
