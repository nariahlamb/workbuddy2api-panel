#!/system/bin/sh
# Credentials and account state are intentionally retained at
# /data/adb/workbuddy2api after uninstall. They contain OAuth tokens and should
# never be silently erased. Remove that directory manually if desired.
BASE=/data/adb/workbuddy2api
if [ -r "$BASE/wb2api.pid" ]; then
  pid=$(cat "$BASE/wb2api.pid" 2>/dev/null)
  [ -n "$pid" ] && kill "$pid" 2>/dev/null
fi
if [ -r "$BASE/runner.pid" ]; then
  pid=$(cat "$BASE/runner.pid" 2>/dev/null)
  [ -n "$pid" ] && kill "$pid" 2>/dev/null
fi
