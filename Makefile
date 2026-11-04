.PHONY: build test vet cli gui agent fmt clean

# MyGo 要求 Go 1.27+；本机默认模块缓存在沙箱内不可写时，
# 可在命令前覆盖：make build GOMODCACHE=/tmp/gomod
GOMODCACHE ?=
GOENV := $(if $(GOMODCACHE),GOMODCACHE=$(GOMODCACHE),)

# 发布相关的默认构建：只出 GUI 与 Agent，不含命令行巡检。
build: gui agent

# 命令行巡检：仅用于真机排障与 M8 的 Python/Go 逐字段对照，不随发布分发。
cli:
	$(GOENV) CGO_ENABLED=0 go build -o build/nvr ./cmd/nvr

gui:
	$(GOENV) CGO_ENABLED=0 go build -o build/nvr-gui ./cmd/nvr-gui

agent:
	$(GOENV) CGO_ENABLED=0 go build -o build/nvr-agent ./cmd/nvr-agent

test:
	$(GOENV) CGO_ENABLED=0 go test ./...

vet:
	$(GOENV) CGO_ENABLED=0 go vet ./...

fmt:
	go fmt ./...

clean:
	rm -rf build
