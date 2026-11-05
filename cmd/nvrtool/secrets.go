package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// errBlocked is a silent non-zero exit: runSecrets already printed the reason.
var errBlocked = errors.New("commit blocked")

var textExt = map[string]bool{
	".go": true, ".md": true, ".json": true, ".txt": true, ".yml": true,
	".yaml": true, ".toml": true, ".cfg": true, ".ini": true, ".py": true,
	".sh": true, ".html": true, ".css": true, ".js": true, ".ts": true,
	".plist": true,
}

var skipParts = map[string]bool{
	".git": true, ".workbuddy": true, "build": true, "dist": true,
	"__pycache__": true, ".venv": true, "node_modules": true,
}

var (
	ipv4Re    = regexp.MustCompile(`^\d{1,3}(?:\.\d{1,3}){3}$`)
	rfc1918Re = regexp.MustCompile(`\b(?:10\.\d{1,3}|172\.(?:1[6-9]|2\d|3[01])|192\.168)\.\d{1,3}\.\d{1,3}\b`)
)

func runSecrets(argv []string) error {
	fset := flag.NewFlagSet("check-secrets", flag.ContinueOnError)
	fset.SetOutput(os.Stderr)
	all := fset.Bool("all", false, "scan the whole working tree instead of the git index")
	if err := fset.Parse(argv); err != nil {
		return err
	}
	var files []string
	if *all {
		files = walkTextFiles(".")
	} else {
		files = stagedTextFiles()
	}
	files = sortedUnique(files)
	values := loadTrueValues(profileCandidates())

	if len(values) == 0 {
		var warned []string
		for _, path := range files {
			lines, err := readLines(path)
			if err != nil {
				continue
			}
			for i, line := range lines {
				if rfc1918Re.MatchString(line) {
					warned = append(warned, fmt.Sprintf("%s:%d", path, i+1))
					break
				}
			}
		}
		fmt.Println("! 未找到本机 NVRStatus 档案，跳过精确比对。")
		if len(warned) > 0 {
			fmt.Printf("! 以下 %d 个文件含 RFC1918 内网地址样式，请人工确认是否为示例值：\n", len(warned))
			for i, item := range warned {
				if i >= 10 {
					break
				}
				fmt.Println("   " + item)
			}
		}
		fmt.Println("  （此模式只警告不阻断；建议在开发机保留 profiles.json 以启用精确阻断）")
		return nil
	}

	var hits []string
	for _, path := range files {
		lines, err := readLines(path)
		if err != nil {
			continue
		}
		for i, line := range lines {
			if containsAny(line, values) {
				// Only the first hit per file, so a device name in a table does
				// not flood the output. The values themselves are never printed.
				hits = append(hits, fmt.Sprintf("%s:%d", path, i+1))
				break
			}
		}
	}
	if len(hits) > 0 {
		fmt.Println("⛔ 提交被阻止：以下文件命中本机真实档案中的设备名 / IP（不打印真值）：")
		for _, item := range hits {
			fmt.Println("   " + item)
		}
		fmt.Println("   请替换为示例值（如 NVR-A / 192.0.2.x）后重新暂存。")
		return errBlocked
	}
	fmt.Printf("✓ 脱敏检查通过：扫描 %d 个文本文件，0 命中\n", len(files))
	return nil
}

func profileCandidates() []string {
	var out []string
	if p := os.Getenv("NVRSTATUS_PROFILE"); p != "" {
		out = append(out, p)
	}
	home, err := os.UserHomeDir()
	if err == nil {
		out = append(out,
			filepath.Join(home, "Library", "Application Support", "NVRStatus", "profiles.json"),
			filepath.Join(home, ".config", "NVRStatus", "profiles.json"),
		)
	}
	if appdata := os.Getenv("APPDATA"); appdata != "" {
		out = append(out, filepath.Join(appdata, "NVRStatus", "profiles.json"))
	}
	return out
}

// loadTrueValues returns the real IPs and names from the first readable local
// profile. Values are only ever compared against, never printed or written.
func loadTrueValues(paths []string) map[string]bool {
	for _, path := range paths {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var node any
		if err := json.Unmarshal(data, &node); err != nil {
			continue
		}
		values := map[string]bool{}
		walkProfile(node, values)
		out := map[string]bool{}
		for v := range values {
			if len([]rune(v)) >= 3 {
				out[v] = true
			}
		}
		return out
	}
	return map[string]bool{}
}

func walkProfile(node any, out map[string]bool) {
	switch v := node.(type) {
	case map[string]any:
		for key, value := range v {
			if s, ok := value.(string); ok {
				k := strings.ToLower(key)
				if (k == "ip" || k == "host" || k == "address") && ipv4Re.MatchString(s) {
					out[s] = true
				} else if (k == "name" || k == "device_name") && len([]rune(s)) >= 3 {
					out[s] = true
				}
			} else {
				walkProfile(value, out)
			}
		}
	case []any:
		for _, item := range v {
			walkProfile(item, out)
		}
	}
}

func isTextFile(path string) bool {
	if !textExt[strings.ToLower(filepath.Ext(path))] {
		return false
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if skipParts[part] {
			return false
		}
	}
	return true
}

func walkTextFiles(root string) []string {
	var out []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if skipParts[d.Name()] {
				return fs.SkipDir
			}
			return nil
		}
		if isTextFile(path) {
			out = append(out, path)
		}
		return nil
	})
	return out
}

func stagedTextFiles() []string {
	out, err := exec.Command("git", "diff", "--cached", "--name-only", "--diff-filter=ACM").Output()
	if err != nil {
		return nil
	}
	var files []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" && isTextFile(line) {
			files = append(files, line)
		}
	}
	return files
}

func readLines(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n"), nil
}

func containsAny(line string, values map[string]bool) bool {
	for v := range values {
		if strings.Contains(line, v) {
			return true
		}
	}
	return false
}

func sortedUnique(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}
