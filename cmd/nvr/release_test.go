package main

// 发布边界的机械保障。
//
// cmd/nvr 与 GUI 同步维护，但**不进发布包**：.app/.dmg 里只有 nvr-gui。
// 这条边界靠两件事守住：Makefile 的 build 目标不依赖 cli，且除了 cli 目标之外
// 没有任何步骤引用 build/nvr（否则就是被拷进 bundle 的前兆）。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type makeTarget struct {
	deps []string
	body []string
}

var makeTargetLine = regexp.MustCompile(`^([A-Za-z0-9_.-]+):([^=]|$)`)

// cliBinaryRef 匹配裸的 build/nvr，但不会命中 build/nvr-gui / build/nvr-agent。
var cliBinaryRef = regexp.MustCompile(`build/nvr(?:\s|$|"|')`)

func parseMakefile(t *testing.T, path string) map[string]*makeTarget {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 Makefile: %v", err)
	}
	targets := map[string]*makeTarget{}
	var cur *makeTarget
	for _, line := range strings.Split(string(data), "\n") {
		if m := makeTargetLine.FindStringSubmatch(line); m != nil {
			if strings.HasPrefix(strings.TrimSpace(line), "#") {
				continue
			}
			cur = &makeTarget{deps: strings.Fields(line[strings.Index(line, ":")+1:])}
			targets[m[1]] = cur
			continue
		}
		switch {
		case strings.HasPrefix(line, "\t"):
			if cur != nil {
				cur.body = append(cur.body, line)
			}
		case strings.TrimSpace(line) == "" || strings.HasPrefix(strings.TrimSpace(line), "#"):
			// 空行与注释不结束目标块：make 在目标块之后仍可能紧跟注释，
			// 而这里只关心「某条命令是否属于某个目标」，保持上一目标即可。
		default:
			cur = nil
		}
	}
	return targets
}

func TestReleaseBuildDoesNotIncludeCLI(t *testing.T) {
	targets := parseMakefile(t, filepath.Join("..", "..", "Makefile"))

	build, ok := targets["build"]
	if !ok {
		t.Fatal("Makefile 缺少 build 目标")
	}
	for _, dep := range build.deps {
		if dep == "cli" {
			t.Error("`make build` 依赖了 cli：发布构建会连带产出命令行巡检程序。\n" +
				"CLI 与 GUI 同步维护，但只走单独的 `make cli`，不进发布产物。")
		}
	}
	hasGUI := false
	for _, dep := range build.deps {
		if dep == "gui" {
			hasGUI = true
		}
	}
	if !hasGUI {
		t.Errorf("`make build` 不再构建 gui（依赖: %v），发布产物会缺失", build.deps)
	}
}

func TestOnlyCLITargetBuildsTheCLIBinary(t *testing.T) {
	targets := parseMakefile(t, filepath.Join("..", "..", "Makefile"))

	cli, ok := targets["cli"]
	if !ok {
		t.Fatal("Makefile 缺少 cli 目标：CLI 需与 GUI 同步维护，请保留 `make cli`")
	}
	cliMentionsBinary := false
	for _, line := range cli.body {
		if cliBinaryRef.MatchString(line) {
			cliMentionsBinary = true
		}
	}
	if !cliMentionsBinary {
		t.Errorf("cli 目标没有产出 build/nvr，实际命令: %v", cli.body)
	}

	for name, target := range targets {
		if name == "cli" || name == ".PHONY" {
			continue
		}
		for _, line := range target.body {
			if cliBinaryRef.MatchString(line) {
				t.Errorf("目标 %q 引用了 build/nvr：CLI 二进制一旦被其它目标拷走，\n"+
					"就可能混进发布包。命令行: %s", name, strings.TrimSpace(line))
			}
		}
	}
}
