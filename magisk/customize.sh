# Magisk install script. This file is sourced by Magisk; do not add a shebang.
SKIPUNZIP=0
BASE=/data/adb/workbuddy2api

ui_print "- Checking device architecture"
case "$ARCH" in
  arm64*|aarch64) ;;
  *) abort "This module only ships an Android arm64 binary. Detected: ${ARCH:-unknown}" ;;
esac

ui_print "- Creating persistent private data directory"
umask 077
mkdir -p "$BASE/auths" "$BASE/data" "$BASE/log"
chmod 700 "$BASE" "$BASE/auths" "$BASE/data" "$BASE/log"

ui_print "- Installing native service"
set_perm_recursive "$MODPATH" 0 0 0755 0644
set_perm "$MODPATH/service.sh" 0 0 0755
set_perm "$MODPATH/runner.sh" 0 0 0755
set_perm "$MODPATH/bin/wb2api" 0 0 0755
[ -f "$MODPATH/bin/login" ] && set_perm "$MODPATH/bin/login" 0 0 0755
[ -f "$MODPATH/bin/signin_bin" ] && set_perm "$MODPATH/bin/signin_bin" 0 0 0755
[ -f "$MODPATH/bin/credit" ] && set_perm "$MODPATH/bin/credit" 0 0 0755

ui_print "- Data: $BASE"
ui_print "- First boot creates a random API key in $BASE/config.json"
ui_print "- Panel is local-only: http://127.0.0.1:7863/panel/"
