#!/system/bin/sh
# post-fs-data.sh — 上游域名静态解析（Magisk systemless hosts），开机早期刷新。
#
# 为什么需要：wb2api 为 CGO_ENABLED=0 的纯 Go 二进制，DNS 解析器只读
# /etc/resolv.conf 与 /etc/hosts，不走 Android netd 的动态 DNS。设备无前者
# （Karing 等 VPN 接管 DNS 时尤其如此），域名解析全部落到 [::1]:53 被拒 ——
# 模型列表空、积分/签到/活动全部失败（日志特征：
# "lookup ... on [::1]:53: connection refused"）。
#
# 修法：把网关全部上游域名写入 Magisk systemless hosts
# （$MODDIR/system/etc/hosts，与 Magisk 内置 hosts 模块同一路径）。
# Go 解析顺序 hosts 优先于 DNS，命中即返回。
#
# IP 从哪来：运行时用系统 resolver 现场解析（与浏览器/curl 同源，含 VPN
# DNS），不写死。post-fs-data 阶段网络可能未就绪 —— 解析不到的域名沿用
# 持久目录里上一轮的记录；连底都没有时留空，由 service.sh 晚期补刷
# （runner.sh 启动前会再调用一次本逻辑）。
#
# 为什么不写 /etc/resolv.conf：那需要动系统路径，且 nameserver 走直连会
# 绕过 VPN 分流。hosts 条目只是"域名→IP"映射，出站连接仍走系统路由，
# Karing 分流规则不受影响。

MODDIR=${0%/*}
HOSTS_SRC="$MODDIR/system/etc/hosts"
BASE=/data/adb/workbuddy2api
PERSIST="$BASE/hosts.last"
DOMAINS="copilot.tencent.com www.codebuddy.cn www.workbuddy.cn www.workbuddy.ai"

mkdir -p "$MODDIR/system/etc"

# 从持久底档读上一轮记录：域名=IP 每行一条。
prev_ip() {
  [ -r "$PERSIST" ] || return 1
  awk -F= -v d="$1" '$1==d {print $2; exit}' "$PERSIST"
}

# 解析一个域名：nslookup + 依次尝试 netd/VPN/公共 DNS。
resolve() {
  d="$1"
  for ns in 10.20.0.2 223.5.5.5 114.114.114.114; do
    ip=$(nslookup "$d" "$ns" 2>/dev/null | awk '/^Address/ && $NF !~ /:/ {print $NF}' | head -1)
    [ -n "$ip" ] && { echo "$ip"; return 0; }
  done
  return 1
}

# 基础条目（与 Android 默认一致）。
{
  echo "127.0.0.1 localhost"
  echo "::1       ip6-localhost"
} > "$HOSTS_SRC"

: > "$PERSIST.new" 2>/dev/null || true
for d in $DOMAINS; do
  ip=$(resolve "$d") || ip=$(prev_ip "$d") || ip=""
  if [ -n "$ip" ]; then
    echo "$ip $d" >> "$HOSTS_SRC"
    echo "$d=$ip" >> "$PERSIST.new"
  fi
done
[ -s "$PERSIST.new" ] && mv -f "$PERSIST.new" "$PERSIST" 2>/dev/null
rm -f "$PERSIST.new" 2>/dev/null
chmod 644 "$HOSTS_SRC"
