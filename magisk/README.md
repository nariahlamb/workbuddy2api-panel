# WorkBuddy2API Magisk module

This directory is assembled by GitHub Actions into an installable **Android arm64 Magisk ZIP**. It contains the native Go gateway and its built-in Web panel; it does not use an APK, foreground-service notification, WakeLock, or a polling keep-alive loop.

## Runtime layout

- module binaries: `/data/adb/modules/workbuddy2api/bin/`
- persistent private data: `/data/adb/workbuddy2api/`
  - `config.json` (mode 0600; random API key generated on first start)
  - `auths/` (OAuth credentials)
  - `data/` (pool state, usage and model cache)
  - `log/wb2api.log` and a single rotated `.1` file

Persistent data is deliberately outside the module directory, so an update cannot replace credentials or state. Uninstall also retains this directory rather than silently deleting OAuth credentials; delete it manually only when you intend to revoke local data.

## First run

1. Flash the release ZIP in Magisk and reboot.
2. Read the generated API key with root:

   ```sh
   su -c 'grep "api_key" /data/adb/workbuddy2api/config.json'
   ```

3. On the phone open `http://127.0.0.1:7863/panel/`, enter that key, then use **添加账号** for browser OAuth.

The module defaults to `127.0.0.1:7863`, so it is reachable only from this phone. To deliberately expose it to a LAN, edit `listen` in `/data/adb/workbuddy2api/config.json` to `0.0.0.0:7863`, keep the generated nonempty `api_key`, and reboot. There is no TLS in the gateway; do not expose it directly to the Internet.

## Power and scheduling

The native binary is an idle HTTP listener. It takes no WakeLock and has no APK foreground-service overhead. It retains the upstream scheduler, including check-in, activity reporting, cat travel, token keepalive, blackcat tasks, streak bonus / school tasks, and periodic balance refresh. The runner uses `TZ=Asia/Shanghai`, matching the upstream task calendar. Android deep sleep can defer any userspace timer; after the CPU resumes the process continues normally, but exact wall-clock task execution is not guaranteed by Android.

## Diagnostics

```sh
su -c 'tail -n 100 /data/adb/workbuddy2api/log/wb2api.log'
su -c 'cat /data/adb/workbuddy2api/wb2api.pid'
su -c 'kill $(cat /data/adb/workbuddy2api/wb2api.pid)' # runner restarts it
```

Use only accounts you are authorized to use and comply with the upstream service terms.
