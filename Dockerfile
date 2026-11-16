# 只有网关进容器，桌面外壳（Win32 + WebView2）留在 Windows 上。
#
# 这个镜像跑的是 cmd/lamp-guide-server：它把 internal/zen/core 抬成一个独立进程，
# 不碰 internal/shell、internal/native、internal/host 这三层 Windows 专用代码，
# 所以基础镜像里不需要任何 GUI 依赖，CGO 也可以关掉。
#
#   docker build -t lamp-guide:latest .
#   docker run -d --name lamp-guide -p 8787:8787 -v lamp-guide-data:/data lamp-guide:latest
#
# 两个基础镜像的标签都做成可覆盖的构建参数：镜像仓库里的具体版本会随时间变化，
# 你那边如果默认标签已经不适用，用 --build-arg 指过去就行，不必改文件。
#   docker build --build-arg GO_IMAGE=golang:1.26 --build-arg RUNTIME_IMAGE=alpine:3.21 .
#
# 多架构：默认构建 linux/amd64（普通 docker build 即可）。要 amd64 + arm64 + armv7 一起出，
# 用 buildx：
#   docker buildx build --platform linux/amd64,linux/arm64,linux/arm/v7 -t lamp-guide:latest --push .
# 构建阶段始终跑在构建机架构上（$BUILDPLATFORM），靠 Go 的交叉编译产出目标架构二进制，
# 不需要 QEMU 去模拟 arm 跑编译器。

# ---------- 构建 ----------
ARG GO_IMAGE=golang:1.26-alpine
ARG RUNTIME_IMAGE=alpine:3.22

# BUILDPLATFORM 由 buildx 注入；直接 docker build 时回退成 amd64。
ARG BUILDPLATFORM

FROM --platform=${BUILDPLATFORM:-linux/amd64} ${GO_IMAGE} AS build

# 拉依赖。国内网络把 GOPROXY 换成 https://goproxy.cn,direct 即可：
#   docker build --build-arg GOPROXY=https://goproxy.cn,direct -t lamp-guide:latest .
ARG GOPROXY=https://proxy.golang.org,direct
# TARGETOS/TARGETARCH/TARGETVARIANT 由 buildx 注入；直接 docker build 时回退成 linux/amd64。
ARG TARGETOS=linux
ARG TARGETARCH=amd64
ARG TARGETVARIANT

ENV CGO_ENABLED=0 \
    GOOS=${TARGETOS} \
    GOARCH=${TARGETARCH} \
    GOPROXY=${GOPROXY} \
    GOTOOLCHAIN=local

WORKDIR /src

# 先只拷依赖清单，让 go mod download 这一层在源码变动时仍然命中缓存。
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# 静态链接 + 去掉符号表：产物直接跑在 alpine 上，不留 libc 依赖。
# arm 32 位（arm/v7）要把 TARGETVARIANT 的 v7 传给 GOARM，否则默认按 ARMv6 出码。
RUN if [ "${TARGETARCH}" = "arm" ] && [ -n "${TARGETVARIANT}" ]; then export GOARM="${TARGETVARIANT#v}"; fi; \
    echo "building for GOOS=${GOOS} GOARCH=${GOARCH} GOARM=${GOARM:-默认}" && \
    go build -trimpath -ldflags="-s -w" -o /out/lamp-guide-server ./cmd/lamp-guide-server

# ---------- 运行 ----------
FROM ${RUNTIME_IMAGE}

# ca-certificates: 网关要跟各渠道走 HTTPS。
# tzdata:          日志和用量统计按本地时区落盘。
# su-exec:         入口脚本降权用（根用户只是过一手，服务进程不许是 root）。
RUN apk add --no-cache ca-certificates tzdata su-exec \
    && adduser -D -H -u 10001 -s /sbin/nologin lamp \
    && mkdir -p /data \
    && chown 10001:10001 /data

COPY --from=build /out/lamp-guide-server /usr/local/bin/lamp-guide-server
COPY docker-entrypoint.sh /usr/local/bin/docker-entrypoint.sh
RUN chmod 0755 /usr/local/bin/docker-entrypoint.sh

# 入口脚本以 root 起步，只为了把数据目录改成运行用户可写——卷往往由宿主机或 NAS 面板
# 建成 root/nobody 属主，直接以 uid 10001 启动会 permission denied——随后立刻 su-exec
# 降权，网关进程本身跑在 uid 10001（可用 ZEN_GATE_UID/ZEN_GATE_GID 或 PUID/PGID 改）。
# 不接受以 root 起步的玩法：先在宿主机把目录 chown 到 10001:10001，再 docker run
# --user 10001:10001，入口脚本会走非 root 分支，只做可写性检查。
USER root

# /data 是网关的全部状态：config.json（渠道登录、供应商、主密钥、用量统计）。
# 不挂卷等于每次重建都从空白配置开始。
ENV ZEN_GATE_HOME=/data \
    TZ=Asia/Shanghai \
    ZEN_GATE_ALLOW_LAN=1

VOLUME ["/data"]
EXPOSE 8787

# /health 不需要鉴权，且网关此时已经绑好端口、加载完配置。
# 探针必须绕开 HTTP_PROXY：宿主机给容器配了上游代理时，busybox 的 wget 会
# 把回环请求也丢给代理，报出来的「不健康」是假警报。
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 \
    CMD http_proxy= https_proxy= no_proxy='*' \
        wget -q -O /dev/null "http://127.0.0.1:${ZEN_GATE_PORT:-8787}/health" || exit 1

ENTRYPOINT ["/usr/local/bin/docker-entrypoint.sh"]
