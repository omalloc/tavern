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


LDFLAGS=-ldflags "-w -s -extldflags=-static"

default:
	@make clean
	@make build
	@make toolchain

.PHONY: install
install:
	go mod tidy

.PHONY: build
build:
	@env CGO_ENABLED=0 go build ${LDFLAGS} -o bin/tavern .

.PHONY: generate
generate:
	@go generate ./...

.PHONY: toolchain
toolchain:
	@env CGO_ENABLED=0 go build ${LDFLAGS} -o bin/tq cmd/tq/main.go
	@env CGO_ENABLED=0 go build ${LDFLAGS} -o bin/ttop cmd/top/main.go

.PHONY: run
run:
	@env CGO_ENABLED=0 go run ${LDFLAGS} . -c config.yaml

.PHONY: clean
clean:
	@rm -rf bin/*

.PHONY: check
check:
	@go vet ./...
	@staticcheck ./...

.PHONY: init
init:
	@go env -w GOPROXY=https://goproxy.cn,direct
	@go install honnef.co/go/tools/cmd/staticcheck@latest

# --- Changelog & Release ---

GIT_CLIFF ?= git-cliff

.PHONY: tools/git-cliff
tools/git-cliff:
	@command -v $(GIT_CLIFF) >/dev/null 2>&1 || { \
		echo "Installing git-cliff..."; \
		mkdir -p $(HOME)/.local/bin; \
		curl -sL https://github.com/orhun/git-cliff/releases/download/v2.10.0/git-cliff-2.10.0-x86_64-unknown-linux-musl.tar.gz | tar xz -C /tmp; \
		cp /tmp/git-cliff-2.10.0/git-cliff $(HOME)/.local/bin/git-cliff; \
		rm -rf /tmp/git-cliff-2.10.0; \
		echo "git-cliff installed to $(HOME)/.local/bin/git-cliff"; \
	}

.PHONY: changelog
changelog: tools/git-cliff
	@$(GIT_CLIFF) --unreleased

.PHONY: changelog-update
changelog-update: tools/git-cliff
	@$(GIT_CLIFF) -o CHANGELOG.md
	@echo "CHANGELOG.md updated."

# Determine next version from unreleased commits, update changelog, commit, and tag.
# Usage: make release-patch | release-minor | release-major
_release: tools/git-cliff
	@echo "Computing next version..."
	@NEXT=$$($(GIT_CLIFF) --bumped-version 2>/dev/null) || NEXT=""; \
	if [ -z "$$NEXT" ] || [ "$$NEXT" = "v0.0.0" ]; then \
		echo "No version-changing commits found. Use chore(release) commit or conventional commits (feat/fix)."; \
		exit 1; \
	fi; \
	echo "Next version: $$NEXT"; \
	$(GIT_CLIFF) -o CHANGELOG.md; \
	git add CHANGELOG.md; \
	git commit -m "chore(release): $$(echo $$NEXT | sed 's/^v//')" && \
	git tag "$$NEXT"; \
	echo "Release $$NEXT created. Run 'git push --follow-tags origin main' to publish."

.PHONY: release-patch
release-patch: _release

.PHONY: release-minor
release-minor: _release

.PHONY: release-major
release-major: _release
