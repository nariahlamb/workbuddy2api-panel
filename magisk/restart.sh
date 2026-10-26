#!/system/bin/sh
# 快速重启：面板「重启进程」按钮与命令行共用。
#
# 为什么不是简单 kill 掉 wb2api：
#   runner.sh 是常驻 supervisor，wb2api 只是它的子进程。只杀子进程会走进
#   runner 的退避重试（5s→60s 指数回退），而且期间面板是断的、按钮也点不了。
#   这里的口径是「整条链路重来」：先停 supervisor（runner 的 trap 会清两个
#   pidfile），再兜底清 wb2api，确认都退干净后重新拉起 runner.sh。
#
# 调用方式（面板进程内）：
#   nohup sh restart.sh >/dev/null 2>&1 &
# 脚本自身脱离父进程，父进程随后退出也不会带走它。

MODDIR=${0%/*}
BASE=/data/adb/workbuddy2api
RUNNER_PIDFILE="$BASE/runner.pid"
CHILD_PIDFILE="$BASE/wb2api.pid"
LOG="$BASE/log/wb2api.log"

mkdir -p "$BASE/log" 2>/dev/null
{
  printf '%s restart: 收到重启请求（mod=%s）\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$MODDIR"
} >>"$LOG" 2>/dev/null

# 读 pidfile 的辅助：只输出纯数字，其它一律空（防脏值被当成 pid 误杀）。
read_pid() {
  [ -r "$1" ] || return 0
  v=$(cat "$1" 2>/dev/null | tr -dc '0-9')
  case "$v" in ''|'0') return 0 ;; esac
  printf '%s' "$v"
}

# alive_pid <pid> <cmdline 关键字>：进程活着且确实是目标进程（防 PID 复用误杀）。
alive_pid() {
  [ -n "$1" ] && kill -0 "$1" 2>/dev/null || return 1
  [ -r "/proc/$1/cmdline" ] || return 1
  tr '\0' ' ' <"/proc/$1/cmdline" 2>/dev/null | grep -q "$2"
}

SUP=$(read_pid "$RUNNER_PIDFILE")
CHILD=$(read_pid "$CHILD_PIDFILE")

# 1) 先停 supervisor：它的 trap 会删掉两个 pidfile，避免重启后残留脏 pid。
if alive_pid "$SUP" runner.sh; then
  kill -TERM "$SUP" 2>/dev/null
fi
# 2) 再停 wb2api 本体（supervisor 的 trap 不杀子进程）。
if alive_pid "$CHILD" wb2api; then
  kill -TERM "$CHILD" 2>/dev/null
fi

# 3) 宽限 6 秒等它自己退（Go 进程会跑完 graceful shutdown）。
i=0
while [ "$i" -lt 12 ]; do
  n=0
  alive_pid "$SUP" runner.sh && n=$((n + 1))
  alive_pid "$CHILD" wb2api && n=$((n + 1))
  [ "$n" -eq 0 ] && break
  sleep 0.5
  i=$((i + 1))
done

# 4) 还没退干净就强杀（面板卡死不响应 TERM 的兜底）。
alive_pid "$SUP" runner.sh && kill -KILL "$SUP" 2>/dev/null
alive_pid "$CHILD" wb2api && kill -KILL "$CHILD" 2>/dev/null

# 5) 清残留 pidfile（进程已死但文件还在的情况）。
rm -f "$RUNNER_PIDFILE" "$CHILD_PIDFILE" 2>/dev/null

# 6) 兜底扫尾：pidfile 缺失/被覆盖时，按 cmdline 找漏网的同模块 wb2api。
#    只匹配「本模块 config 路径」的那一个，绝不动其它进程。
for d in /proc/[0-9]*; do
  p=${d#/proc/}
  [ -r "$d/cmdline" ] || continue
  cmd=$(tr '\0' ' ' <"$d/cmdline" 2>/dev/null)
  case "$cmd" in
    *"$BASE/config.json"*) kill -KILL "$p" 2>/dev/null ;;
  esac
done

# 7) 重新拉起 supervisor（完全脱离调用者，父进程退出不受影响）。
nohup sh "$MODDIR/runner.sh" "$MODDIR" >/dev/null 2>&1 &

{
  printf '%s restart: 已重拉 runner（sup=%s child=%s）\n' "$(date '+%Y-%m-%d %H:%M:%S')" "${SUP:-none}" "${CHILD:-none}"
} >>"$LOG" 2>/dev/null

exit 0
