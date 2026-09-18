#!/system/bin/sh
# Long-lived native runner. Persistent credentials/config deliberately live outside
# the module directory so an in-place Magisk module update cannot overwrite them.
MODDIR=$1
BASE=/data/adb/workbuddy2api
BIN="$MODDIR/bin/wb2api"
LOG="$BASE/log/wb2api.log"
PIDFILE="$BASE/wb2api.pid"
SUPERVISOR_PID="$BASE/runner.pid"

umask 077
mkdir -p "$BASE/auths" "$BASE/data" "$BASE/log"
chmod 700 "$BASE" "$BASE/auths" "$BASE/data" "$BASE/log"

# Late-start is normally after boot. Waiting briefly prevents a first-start race
# with /data and Android network services, without holding a wake lock afterwards.
i=0
while [ "$(getprop sys.boot_completed)" != "1" ] && [ "$i" -lt 90 ]; do
  sleep 2
  i=$((i + 1))
done

# Do not run two copies if service.sh is accidentally invoked more than once.
if [ -r "$SUPERVISOR_PID" ]; then
  old=$(cat "$SUPERVISOR_PID" 2>/dev/null)
  # kill -0 只证明"这个 PID 活着"，不证明"它是我们的 runner"。PID 被系统
  # 复用时（上次进程被强杀、pidfile 残留）会误判"已有实例"而永远不自启。
  # 补一道 cmdline 校验：/proc/<pid>/cmdline 里必须真含 runner.sh。
  if [ -n "$old" ] && kill -0 "$old" 2>/dev/null \
      && tr '\0' ' ' < "/proc/$old/cmdline" 2>/dev/null | grep -q "runner.sh"; then
    exit 0
  fi
  # 校验不过 = 陈旧 pidfile（进程已死或 PID 被复用），清掉继续启动。
  rm -f "$SUPERVISOR_PID"
fi
printf '%s\n' "$$" > "$SUPERVISOR_PID"
trap 'rm -f "$PIDFILE" "$SUPERVISOR_PID"; exit 0' INT TERM EXIT

# 网络已就绪（boot_completed 之后）：启动服务前刷新一次上游域名的静态
# 解析，覆盖 post-fs-data 阶段网络未就绪导致的空解析。纯 Go 二进制只认
# /etc/hosts（systemless overlay 即映射到该路径），见 post-fs-data.sh。
sh "$MODDIR/post-fs-data.sh" >/dev/null 2>&1 || true

# This is deliberately a normal idle TCP server: no foreground notification,
# wakelock, alarm loop, or polling watchdog. The Go scheduler performs the
# existing check-in, activity, travel, keepalive, blackcat, and balance jobs.
export WB2A_ANDROID_MODULE=1
export WB2A_AUTH_DIR="$BASE/auths"
export WB2A_STATE_FILE="$BASE/data/state.json"
export TZ=Asia/Shanghai

backoff=5
while [ -x "$BIN" ]; do
  # Tiny rotation only at process boundaries; request logs are otherwise stdout/stderr.
  if [ -f "$LOG" ]; then
    size=$(wc -c < "$LOG" 2>/dev/null)
    if [ "${size:-0}" -gt 2097152 ]; then
      mv -f "$LOG" "$LOG.1" 2>/dev/null
    fi
  fi
  printf '%s runner: starting wb2api\n' "$(date '+%Y-%m-%d %H:%M:%S')" >> "$LOG"
  "$BIN" -config "$BASE/config.json" >> "$LOG" 2>&1 &
  child=$!
  printf '%s\n' "$child" > "$PIDFILE"
  wait "$child"
  code=$?
  rm -f "$PIDFILE"
  printf '%s runner: wb2api exited code=%s; retry in %ss\n' "$(date '+%Y-%m-%d %H:%M:%S')" "$code" "$backoff" >> "$LOG"
  sleep "$backoff"
  [ "$backoff" -lt 60 ] && backoff=$((backoff * 2))
done
