.PHONY: build test vet cli gui agent app run ffmpeg fmt clean

# MyGo 要求 Go 1.27+；本机默认模块缓存不可写时（表现为停在
# `go: downloading ...` 很久或被 SIGTERM），可覆盖：
# make build GOMODCACHE=/tmp/gomod
GOMODCACHE ?=
GOENV := $(if $(GOMODCACHE),GOMODCACHE=$(GOMODCACHE),)

APP := build/darwin-arm64/NVR Status.app

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

# 日常验证：构建带 ffmpeg 的 .app 并以新进程启动。
# -n 必须留：open 对同一个 bundle 默认复用已运行的实例，改了代码重编后会看起来"没变"。
run: app
	open -n "$(APP)"

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
