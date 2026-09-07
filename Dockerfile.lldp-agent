FROM golang:1.25-alpine AS builder
WORKDIR /src/topology_agent
ARG GOPROXY=https://goproxy.cn,direct
COPY topology_agent/go.mod topology_agent/go.sum ./
RUN go mod download
COPY topology_agent/*.go ./
COPY topology_agent/pkg ./pkg
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath -ldflags='-s -w' -o /out/topology-agent .

FROM alpine:3.22
COPY --from=builder /out/topology-agent /topology-agent
USER 65532:65532
ENTRYPOINT ["/topology-agent"]
