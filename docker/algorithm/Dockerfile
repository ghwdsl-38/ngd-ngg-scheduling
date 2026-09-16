# 第一阶段只负责编译无 CGO 依赖的 Go HTTP 主服务。
FROM golang:1.25-alpine AS go-builder
WORKDIR /src/algorithm_server/go
COPY algorithm_server/go/go.mod algorithm_server/go/go.sum ./
COPY algorithm_server/go/vendor ./vendor
COPY algorithm_server/go/algorithm ./algorithm
COPY algorithm_server/go/cmd ./cmd
# 限制构建并发，避免在共享演示服务器上因 Go 编译瞬时并行度过高触发 OOM。
RUN GOMAXPROCS=2 CGO_ENABLED=0 go build -mod=vendor -p=1 -trimpath -ldflags='-s -w' -o /out/algorithm-server ./cmd/algorithm-server

# 第二阶段提供 Python 运行时，并只复制算法 Worker 与 Go 二进制。
FROM python:3.13-alpine
WORKDIR /app
COPY algorithm_server/python /app/python
COPY --from=go-builder /out/algorithm-server /app/algorithm-server
ENV PYTHONPATH=/app/python
ENV PYTHONDONTWRITEBYTECODE=1
# 使用非 root 用户运行；对外入口始终是 Go，Python 由 Go 作为子进程启动。
USER 65532:65532
ENTRYPOINT ["/app/algorithm-server"]
