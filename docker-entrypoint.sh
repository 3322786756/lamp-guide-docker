#!/bin/sh
# 容器入口：先把数据目录改成运行用户能写，再降权启动网关。
#
# 为什么需要这一步：数据目录通常由宿主机或 NAS 面板创建，属主是 root 或 nobody，
# 而镜像里的运行用户是 uid 10001，于是网关一启动就报
#   open store: open /data/config.json.tmp: permission denied
# 以 root 进来、只把 $ZEN_GATE_HOME 改一次属主、随即 su-exec 降权，
# 服务进程本身仍然不是 root。
#
# 运行用户可以用 ZEN_GATE_UID / ZEN_GATE_GID 指定，也认 NAS 上常见的 PUID / PGID。
# 不想让容器以 root 起步，就先在宿主机 chown 好目录，再用 --user 指定同一个 uid。
set -eu

: "${ZEN_GATE_HOME:=/data}"
ZEN_GATE_HOME="${ZEN_GATE_HOME%/}"
[ -n "$ZEN_GATE_HOME" ] || ZEN_GATE_HOME=/

uid="${ZEN_GATE_UID:-${PUID:-10001}}"
gid="${ZEN_GATE_GID:-${PGID:-10001}}"

case "$uid" in
    ''|*[!0-9]*)
        echo "docker-entrypoint: 运行用户 id 不是数字（ZEN_GATE_UID/PUID=$uid）" >&2
        exit 64
        ;;
esac
case "$gid" in
    ''|*[!0-9]*)
        echo "docker-entrypoint: 运行组 id 不是数字（ZEN_GATE_GID/PGID=$gid）" >&2
        exit 64
        ;;
esac

if [ "$(id -u)" = "0" ]; then
    mkdir -p "$ZEN_GATE_HOME"
    current="$(stat -c '%u:%g' "$ZEN_GATE_HOME" 2>/dev/null || echo unknown)"
    if [ "$current" != "$uid:$gid" ]; then
        echo "docker-entrypoint: $ZEN_GATE_HOME 当前属主 $current，改成 $uid:$gid"
        if ! chown -R "$uid:$gid" "$ZEN_GATE_HOME"; then
            echo "docker-entrypoint: 改属主失败；网关可能写不了 $ZEN_GATE_HOME" >&2
        fi
    fi
    exec su-exec "$uid:$gid" /usr/local/bin/lamp-guide-server "$@"
fi

# 已经是非 root（例如 docker run --user）：不动属主，只把权限问题提前说清楚，
# 免得用户对着一行 Go 报错猜。
if ( : >"$ZEN_GATE_HOME/.write-test" ) 2>/dev/null; then
    rm -f "$ZEN_GATE_HOME/.write-test"
else
    echo "docker-entrypoint: 当前用户 $(id -u):$(id -g) 写不了 $ZEN_GATE_HOME" >&2
    echo "docker-entrypoint: 在宿主机执行 chown -R $(id -u):$(id -g) <数据目录> 后重启容器" >&2
fi

exec /usr/local/bin/lamp-guide-server "$@"
