.PHONY: build test vet cli gui agent app ffmpeg fmt clean

# MyGo 要求 Go 1.27+；本机默认模块缓存在沙箱内不可写时，
# 可在命令前覆盖：make build GOMODCACHE=/tmp/gomod
GOMODCACHE ?=
GOENV := $(if $(GOMODCACHE),GOMODCACHE=$(GOMODCACHE),)

# 发布构建：只出 GUI 与 Agent。cli 故意不在这里，cmd/nvr/release_test.go 会校验。
build: gui agent

# 命令行巡检：与 GUI 同步维护（cmd/nvr/parity_test.go 守着两侧不漏同步），但不进发布包。
cli:
	$(GOENV) CGO_ENABLED=0 go build -o build/nvr ./cmd/nvr

gui:
	$(GOENV) CGO_ENABLED=0 go build -o build/nvr-gui ./cmd/nvr-gui

agent:
	$(GOENV) CGO_ENABLED=0 go build -o build/nvr-agent ./cmd/nvr-agent

# 打成 .app / .dmg。先捆 ffmpeg：MyGo 会把 resources/darwin/bin 拷到
# Contents/Resources/bin，正好是 util.ToolBinDirs 的第一个查找位置。
# 不带这一步，从 Finder/`open` 启动的应用继承 launchd 的 PATH（不含
# /opt/homebrew/bin），深度抽检会静默降级。
app: ffmpeg
	$(GOENV) go tool mygo build

# 重新生成 resources/darwin/bin（依赖闭包 + @loader_path 改写 + ad-hoc 签名）。
# 产物被 .gitignore 忽略；换了 ffmpeg 版本后重跑一次。
ffmpeg:
	python3 scripts/bundle-ffmpeg.py

test:
	$(GOENV) CGO_ENABLED=0 go test ./...

vet:
	$(GOENV) CGO_ENABLED=0 go vet ./...

fmt:
	go fmt ./...

clean:
	rm -rf build
