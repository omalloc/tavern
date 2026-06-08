# multi-stage Dockerfile for a Go app
FROM m.daocloud.io/docker.io/library/golang:1.26 AS builder

WORKDIR /src

# cache modules
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/root/.cache/go-build,id=go-build \
    --mount=type=cache,target=/go/pkg/mod,id=go-mod \
    go env -w GO111MODULE=on && \
    go env -w GOPROXY=https://goproxy.cn,direct && \
    go mod download

COPY . .

ENV CGO_ENABLED=0 GOOS=linux GOARCH=amd64
RUN go build -trimpath -ldflags="-s -w" -o /app/app .

FROM m.daocloud.io/docker.io/library/alpine:3.23

RUN sed -i 's/dl-cdn.alpinelinux.org/mirrors.aliyun.com/g' /etc/apk/repositories \
    && apk add --no-cache curl \
    && mkdir -p /usr/local/tavern/logs 

COPY --from=builder /app/app /usr/local/tavern/tavern

WORKDIR /usr/local/tavern

EXPOSE 8080

ENV TZ=Asia/Shanghai

HEALTHCHECK --interval=30s --timeout=30s --start-period=5s --retries=3 \
    CMD curl -sS http://localhost:8080/healthz || exit 1

ENTRYPOINT ["/usr/local/tavern/tavern", "-c", "/usr/local/tavern/config.yaml"]