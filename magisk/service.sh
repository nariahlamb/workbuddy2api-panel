#!/system/bin/sh
# Magisk late_start service entrypoint. Keep this short: runner owns the server.
MODDIR=${0%/*}
[ -x "$MODDIR/runner.sh" ] || exit 0
sh "$MODDIR/runner.sh" "$MODDIR" >/dev/null 2>&1 &
