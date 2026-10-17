# 轻量云主机控制台镜像
#
# 三个阶段全部使用本地已存在的 ubuntu:24.04 / alpine:3.22，
# Go 与 Node 都以官方二进制包的形式随构建上下文提供。
# 原因：宿主的两个 registry-mirrors（dockerproxy.net、docker.1ms.run）均已失效，
# 且 mihomo TUN 的 DNS fake-ip 会让 dockerd 拉取外部镜像返回 404 / TLS 握手超时。
# 这里不引入任何外部镜像，构建才 reproducible。

FROM ubuntu:24.04 AS go-builder

ARG VERSION=0.1.1

COPY build/go.tar.gz /tmp/go.tar.gz
RUN tar -C /usr/local -xzf /tmp/go.tar.gz \
    && rm /tmp/go.tar.gz \
    && /usr/local/go/bin/go version

ENV PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin

COPY go.mod ./
COPY cmd ./cmd
COPY internal ./internal

# 零第三方依赖，无需 go mod download，构建完全离线
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.Version=${VERSION}" \
    -o /out/neo ./cmd/neo

# ---------------------------------------------------------------- 前端

FROM ubuntu:24.04 AS web-builder

COPY build/node.tar /tmp/node.tar
RUN tar -C /usr/local --strip-components=1 -xf /tmp/node.tar && rm /tmp/node.tar

# 依赖先行是为了让 Docker 能缓存这一层；源码变动时不必重复装依赖
COPY web/package.json web/package-lock.json ./web/
RUN cd web && npm install --no-audit --no-fund --registry=https://registry.npmmirror.com

COPY web ./web
# 标识源文件在仓库根的 logo/（与 web 平级，便于跨服务共享）；构建时需一并带进来，
# 这样 ui.tsx 里 ../../../logo/logo.svg 的相对路径在容器内也能解析。
COPY logo /logo
RUN cd web && npm run build

# ---------------------------------------------------------------- 运行

FROM alpine:3.22

# 静态二进制、无 shell 依赖；所有工具通过 exec 形式的 CMD 调用
COPY --from=go-builder /out/neo /usr/local/bin/neo
COPY --from=web-builder /web/dist /usr/share/neo/web

# 会话寿命不在这里给默认值：不设即长期有效，需要收紧时用 NEO_SESSION_TTL 指定。
# 写在镜像里会让「长期」这个默认取不到 —— 容器一旦继承它，部署者改不掉。
ENV TZ=Asia/Shanghai \
    NEO_SOCKET=/var/lib/incus/unix.socket \
    NEO_ADDR=:5902 \
    NEO_DATA_DIR=/data \
    NEO_WEB_DIR=/usr/share/neo/web \
    NEO_INVITE_TTL=15m \
    NEO_INTERVAL=10s

RUN mkdir -p /data

EXPOSE 5902

# healthcheck 不依赖 curl/wget，直接让二进制自检：进程活着但数据链路断了也算不健康
HEALTHCHECK --interval=30s --timeout=10s --retries=3 --start-period=20s \
    CMD ["/usr/local/bin/neo", "probe"]

ENTRYPOINT ["/usr/local/bin/neo"]
CMD ["serve"]
