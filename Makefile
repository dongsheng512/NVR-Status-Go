.PHONY: build test vet cli gui agent fmt clean

# MyGo 要求 Go 1.27+；本机默认模块缓存在沙箱内不可写时，
# 可在命令前覆盖：make build GOMODCACHE=/tmp/gomod
GOMODCACHE ?=
GOENV := $(if $(GOMODCACHE),GOMODCACHE=$(GOMODCACHE),)

build: cli gui agent

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
