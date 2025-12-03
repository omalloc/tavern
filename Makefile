# vim:noet
ifeq ($(shell uname),Linux)
	OS=linux
else
	OS=darwin
endif

ifeq ($(shell uname -m),aarch64)
    ARCH=arm64
else ifeq ($(shell uname -m),arm64)
    ARCH=arm64
else
    ARCH=amd64
endif

GITHASH=$(shell git rev-parse HEAD)
VERSION=$(shell git describe --tags --always)
LDFLAGS=-ldflags "-w -s -extldflags=-static -X main.Version=${VERSION} -X main.GitHash=${GITHASH} -X main.Built=$(shell date +%s)"

default:
	make clean
	make build

.PHONY: install
install:
	go mod tidy

.PHONY: build
build:
	@env CGO_ENABLED=0 go build ${LDFLAGS} -o bin/tavern main.go

.PHONY: run
run:
	@env CGO_ENABLED=0 go run ${LDFLAGS} main.go -c config.yaml

.PHONY: clean
clean:
	@rm -rf bin/*

.PHONY: check
check:
	@go vet ./...
	@staticcheck ./...

.PHONY: tools
tools:
	@go env -w GOPROXY=https://goproxy.cn,direct
	@go install honnef.co/go/tools/cmd/staticcheck@latest
