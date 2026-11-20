# 只使用稳定基础指令，不下载外部 Dockerfile frontend，避免额外的 Docker Hub 前置依赖。
# 多阶段构建：React 静态资源 + Go 二进制 → 最小运行时镜像。
# 基础镜像经 ARG 可换镜像源；默认值使用官方 docker.io 引用。

ARG NODE_IMAGE=node:24-alpine
ARG GO_IMAGE=golang:1.27-alpine
ARG RUNTIME_IMAGE=alpine:3.21

# ---- 前端构建 ----
FROM ${NODE_IMAGE} AS frontend
WORKDIR /src
RUN corepack enable
COPY frontend/package.json frontend/pnpm-lock.yaml frontend/pnpm-workspace.yaml ./
RUN pnpm install --frozen-lockfile
COPY frontend/ ./
RUN pnpm build

# ---- 后端构建 ----
FROM ${GO_IMAGE} AS backend
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /src
COPY backend-go/go.mod backend-go/go.sum ./
RUN go mod download
COPY backend-go/ ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/server ./cmd/server

# ---- 运行时 ----
FROM ${RUNTIME_IMAGE}
RUN apk add --no-cache ca-certificates wget && adduser -D -u 10001 app
WORKDIR /app
COPY --from=backend /out/server ./server
COPY --from=frontend /src/dist ./frontend
RUN mkdir -p /data/transfer_space && chown -R app:app /data
# 以下为镜像级默认；charts/deploy-platform/templates/deployment.yaml
# 显式渲染同一组值——改其一需同步另一处（两处真相有意保留：镜像默认
# 服务裸 docker 运行，chart 值服务 k8s 部署）。
ENV ENV=production \
    MOCK_RUNTIME=false \
    HTTP_ADDR=:8000 \
    FRONTEND_DIR=/app/frontend \
    TRANSFER_DIR=/data/transfer_space
USER app
EXPOSE 8000
HEALTHCHECK --interval=15s --timeout=3s --start-period=10s \
    CMD wget -qO- http://127.0.0.1:8000/health/live >/dev/null || exit 1
ENTRYPOINT ["/app/server"]
