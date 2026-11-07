# WakeHub v0.3.2 — 多阶段构建（前端 + Go + 最小运行时镜像）
#
# 多架构构建（amd64 + arm64，用 buildx）：
#   docker buildx build --platform linux/amd64,linux/arm64 -t wakehub:0.3.2 .
# GOARCH 取构建目标的 TARGETARCH（buildx 自动传入）；不用 buildx 时
# 回退为构建机原生架构。golang/node/alpine 基础镜像均为多架构。
#
# 单容器模式（v0.3.0 起，v0.3.1 修权限）：默认 ENTRYPOINT 是
# docker-entrypoint.sh，同一容器内同时跑 Server 与本地 Agent
# （--network host + --cap-add NET_RAW），一条 docker run 即部署，
# 见 README“快速启动”。入口以 root 启动：先修 /app/data 属主
# （bind mount 的宿主机目录属主不对时自动改成 wakehub，不用手动
# chown），再用 su-exec 降权到 wakehub 跑 Server 与 Agent。
# docker-compose 的两个服务会显式覆盖 entrypoint，各自只跑一个进程，
# 并用 user: "10001:10001" 跑成 wakehub（不经过入口的 root 阶段）。
#
# 注意：本机沙箱跑不起容器（runc setns 被禁），镜像未在本机实测构建，
# 首次构建请在用户 VM / 真机上验证（见 docs/deployment.md）。
#
# 构建源（v0.2.9 问题 17）：默认用国内镜像源加速，可被构建参数覆盖，
# 不要写死在 RUN 里：
#   docker build --build-arg NPM_REGISTRY=https://registry.npmjs.org \
#                --build-arg GOPROXY=https://proxy.golang.org,direct .
# 或在 .env 里写 NPM_REGISTRY / GOPROXY（compose build.args 会取用）。

# Stage 1: Build Vue 3 Web UI
ARG NPM_REGISTRY=https://registry.npmmirror.com
FROM node:20-alpine AS web-builder
ARG NPM_REGISTRY
WORKDIR /build/web
# 先只拷清单文件做 npm ci（可复现；不要用 npm install），再拷源码。
COPY web/package.json web/package-lock.json ./
RUN npm ci --registry=${NPM_REGISTRY}
COPY web/ ./
RUN npm run build

# Stage 2: Build Go Server & Agent Binaries
ARG GOPROXY=https://goproxy.cn,direct
FROM golang:1.22-alpine AS go-builder
ARG GOPROXY
ENV GOPROXY=${GOPROXY}
ARG TARGETARCH
WORKDIR /build
# 先只拷 go.mod/go.sum 下载依赖（缓存友好），再拷源码。
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH:-$(go env GOARCH)} \
    go build -trimpath -ldflags="-s -w" -o /out/wakehub-server ./cmd/server && \
    CGO_ENABLED=0 GOOS=linux GOARCH=${TARGETARCH:-$(go env GOARCH)} \
    go build -trimpath -ldflags="-s -w" -o /out/wakehub-agent ./cmd/agent

# Stage 3: Final Minimal Runtime Image
FROM alpine:3.20
# libcap 提供 setcap：给 /bin/ping 文件能力 cap_net_raw+ep。
# 容器以非 root（USER wakehub）运行，cap_add 只把 CAP_NET_RAW 放进
# bounding set，非 root 进程 exec 普通二进制时拿不到它（permitted 归零），
# ping 建 raw socket 照样 EPERM；文件能力才能让能力在 exec 时落到
# ping 进程上。cap_add 仍不可少（文件能力须与 bounding set 求交）。
# su-exec：单容器入口以 root 启动后，用它把 Server 与 Agent 降权到
# wakehub 身份运行（v0.3.1；无中间包装进程，PID 与信号行为与直接
# 启动一致，环境变量原样透传）。
RUN apk add --no-cache ca-certificates tzdata iputils curl libcap su-exec && \
    setcap cap_net_raw+ep /bin/ping && \
    adduser -D -u 10001 wakehub
WORKDIR /app
COPY --from=go-builder /out/wakehub-server /app/wakehub-server
COPY --from=go-builder /out/wakehub-agent /app/wakehub-agent
COPY --from=web-builder /build/web/dist /app/web/dist
COPY migrations/ /app/migrations/
COPY docker-entrypoint.sh /app/docker-entrypoint.sh
RUN mkdir -p /app/data && chown -R wakehub:wakehub /app && \
    chmod +x /app/docker-entrypoint.sh
VOLUME ["/app/data"]
# 注意：这里故意不写 USER wakehub（v0.3.1）。单容器入口须以 root 启动，
# 先修 /app/data 属主（bind mount 的宿主机目录属主不对时自动改成
# wakehub，不用手动 chown），再用 su-exec 降权跑 Server 与 Agent；
# 长驻进程仍以 wakehub(10001) 运行（审计第 19 项），不是以 root 跑服务。
# compose 的 server/agent 服务用 user: "10001:10001" 显式跑成 wakehub。
# Agent 侧 ping 靠 /bin/ping 的文件能力 cap_net_raw+ep（上文 setcap），
# compose 的 cap_add 仍须保留。
EXPOSE 8080
# 健康检查调公开的 /api/v1/health（契约 §12.2），不要调 GET /。
# 端口跟随 WAKEHUB_LISTEN_ADDR（默认 :8080；单容器改端口时不误报）。
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD sh -c 'PORT=${WAKEHUB_LISTEN_ADDR##*:}; curl -fsS http://127.0.0.1:${PORT:-8080}/api/v1/health || exit 1'
# 默认入口：单容器模式（v0.3.0 起，v0.3.1 修权限），同时跑 Server 与本地 Agent。
# compose 的 server/agent 服务会显式覆盖 entrypoint，只跑单个进程。
ENTRYPOINT ["/app/docker-entrypoint.sh"]
