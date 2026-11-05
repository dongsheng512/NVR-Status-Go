package main

import (
	"debug/macho"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// bundleConfig holds everything bundle() needs. Load and RunTool/RunBinary are
// injectable so the dependency-closure logic can be unit-tested without ffmpeg,
// install_name_tool or codesign.
type bundleConfig struct {
	Dest      string
	FFmpeg    string
	FFprobe   string
	Check     bool
	Load      func(string) ([]string, error)
	RunTool   func(name string, args ...string) error
	RunBinary func(path string, args ...string) (string, error)
	Missing   func(names []string) error // nil => warn and skip
	Out       io.Writer
}

const defaultExtraSearch = "/opt/homebrew/bin:/usr/local/bin:/opt/local/bin"

func runBundle(argv []string) error {
	fs := flag.NewFlagSet("bundle-ffmpeg", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dest := fs.String("dest", filepath.Join(repoRoot(), "resources", "darwin", "bin"), "output directory")
	check := fs.Bool("check", false, "report without writing")
	require := fs.Bool("require-ffmpeg", false, "fail instead of skipping when ffmpeg is missing")
	if err := fs.Parse(argv); err != nil {
		return err
	}
	if runtime.GOOS != "darwin" {
		return errors.New("bundle-ffmpeg only makes sense on macOS")
	}

	extra := os.Getenv("NVR_FFMPEG_BIN")
	if extra == "" {
		extra = defaultExtraSearch
	}
	ffmpeg := resolveTool("ffmpeg", extra)
	ffprobe := resolveTool("ffprobe", extra)
	var missing []string
	if ffmpeg == "" {
		missing = append(missing, "ffmpeg")
	}
	if ffprobe == "" {
		missing = append(missing, "ffprobe")
	}

	cfg := bundleConfig{
		Dest:      *dest,
		FFmpeg:    ffmpeg,
		FFprobe:   ffprobe,
		Check:     *check,
		Load:      machoDylibs,
		RunTool:   runTool,
		RunBinary: runBinary,
		Out:       os.Stdout,
	}
	if len(missing) > 0 {
		found := fmt.Errorf("未找到 %s", strings.Join(missing, "/"))
		cfg.Missing = func([]string) error {
			if *require {
				return fmt.Errorf("%w；--require-ffmpeg 要求必须捆绑，构建中止。装好 ffmpeg 后重跑 make ffmpeg", found)
			}
			fmt.Fprintf(os.Stderr, "warning: %v，跳过 ffmpeg 捆绑；本次 .app 不带 ffmpeg，"+
				"深度抽检会降级。装好 ffmpeg 后重跑 make ffmpeg\n", found)
			return errSkipBundle
		}
	}
	err := bundle(cfg)
	if errors.Is(err, errSkipBundle) {
		return nil
	}
	return err
}

var errSkipBundle = errors.New("skip ffmpeg bundling")

func repoRoot() string {
	// The hook and Makefile both run from the repository root, but `go run` can
	// be invoked from anywhere; walk up to go.mod so --dest stays predictable.
	dir, err := os.Getwd()
	if err != nil {
		return "."
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "."
		}
		dir = parent
	}
}

func resolveTool(name, extraSearch string) string {
	if p, err := exec.LookPath(name); err == nil {
		return realPath(p)
	}
	for _, dir := range strings.Split(extraSearch, string(os.PathListSeparator)) {
		if dir == "" {
			continue
		}
		p := filepath.Join(dir, name)
		if st, err := os.Stat(p); err == nil && !st.IsDir() && st.Mode()&0o111 != 0 {
			return realPath(p)
		}
	}
	return ""
}

func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

// machoDylibs replaces `otool -L`: debug/macho reads the same load commands
// directly, with no external process. Verified against otool on the Homebrew
// ffmpeg binary (36 imports, identical order and names).
func machoDylibs(path string) ([]string, error) {
	f, err := macho.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return f.ImportedLibraries()
}

func runTool(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func runBinary(path string, args ...string) (string, error) {
	out, err := exec.Command(path, args...).CombinedOutput()
	return string(out), err
}

func isSystemDep(dep string) bool {
	return strings.HasPrefix(dep, "/usr/lib/") ||
		strings.HasPrefix(dep, "/System/") ||
		strings.HasPrefix(dep, "@executable_path/")
}

func nonSystem(deps []string) []string {
	out := make([]string, 0, len(deps))
	for _, d := range deps {
		if !isSystemDep(d) {
			out = append(out, d)
		}
	}
	return out
}

// collectDylibs walks the dependency graph. files maps the bundled filename to
// the source path; refs maps each recorded load-command name to that filename.
// otool reports unversioned symlink names (libavcodec.63.dylib) while the real
// file is libavcodec.63.1.102.dylib, so rewriting must go through refs rather
// than through basenames.
func collectDylibs(roots []string, load func(string) ([]string, error)) (map[string]string, map[string]string, error) {
	files := map[string]string{}
	refs := map[string]string{}
	seen := map[string]bool{}
	var queue []string
	for _, root := range roots {
		deps, err := load(root)
		if err != nil {
			return nil, nil, fmt.Errorf("load %s: %w", root, err)
		}
		queue = append(queue, nonSystem(deps)...)
	}
	for len(queue) > 0 {
		dep := queue[0]
		queue = queue[1:]
		if seen[dep] {
			continue
		}
		seen[dep] = true
		st, err := os.Stat(dep)
		if err != nil || st.IsDir() {
			return nil, nil, fmt.Errorf("dependency not found on disk: %s", dep)
		}
		real := realPath(dep)
		name := filepath.Base(real)
		refs[dep] = name
		if prev, ok := files[name]; ok && prev != real {
			return nil, nil, fmt.Errorf("two different libraries share the name %s:\n  %s\n  %s", name, prev, real)
		}
		files[name] = real
		deps, err := load(real)
		if err != nil {
			return nil, nil, fmt.Errorf("load %s: %w", real, err)
		}
		queue = append(queue, nonSystem(deps)...)
	}
	return files, refs, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, st.Mode().Perm())
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, st.Mode().Perm())
}

func rewriteLoadCommands(path string, refs map[string]string, cfg bundleConfig) error {
	deps, err := cfg.Load(path)
	if err != nil {
		return fmt.Errorf("load %s: %w", path, err)
	}
	for _, dep := range deps {
		name, ok := refs[dep]
		if !ok || strings.HasPrefix(dep, "@loader_path/") {
			continue
		}
		if err := cfg.RunTool("install_name_tool", "-change", dep, "@loader_path/"+name, path); err != nil {
			return err
		}
	}
	return nil
}

func signFile(path string, cfg bundleConfig) error {
	return cfg.RunTool("codesign", "--force", "--sign", "-", "--timestamp=none", path)
}

func absoluteLeftovers(path string, load func(string) ([]string, error)) ([]string, error) {
	deps, err := load(path)
	if err != nil {
		return nil, err
	}
	var left []string
	for _, d := range deps {
		if !isSystemDep(d) && !strings.HasPrefix(d, "@loader_path/") {
			left = append(left, d)
		}
	}
	return left, nil
}

func bundle(cfg bundleConfig) error {
	if cfg.Missing != nil {
		var names []string
		if cfg.FFmpeg == "" {
			names = append(names, "ffmpeg")
		}
		if cfg.FFprobe == "" {
			names = append(names, "ffprobe")
		}
		if len(names) > 0 {
			return cfg.Missing(names)
		}
	}
	if cfg.Out == nil {
		cfg.Out = io.Discard
	}
	roots := []string{cfg.FFmpeg, cfg.FFprobe}
	files, refs, err := collectDylibs(roots, cfg.Load)
	if err != nil {
		return err
	}
	total := int64(0)
	for _, p := range append(append([]string{}, roots...), mapValues(files)...) {
		if st, err := os.Stat(p); err == nil {
			total += st.Size()
		}
	}
	fmt.Fprintf(cfg.Out, "ffmpeg  : %s\nffprobe : %s\ndylibs  : %d\npayload : %.1f MB\n",
		cfg.FFmpeg, cfg.FFprobe, len(files), float64(total)/1e6)

	if cfg.Check {
		names := make([]string, 0, len(files))
		for name := range files {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			fmt.Fprintf(cfg.Out, "  %-32s %s\n", name, files[name])
		}
		return nil
	}

	if err := os.RemoveAll(cfg.Dest); err != nil {
		return err
	}
	if err := os.MkdirAll(cfg.Dest, 0o755); err != nil {
		return err
	}
	for name, src := range files {
		if err := copyFile(src, filepath.Join(cfg.Dest, name)); err != nil {
			return err
		}
	}
	for name, src := range map[string]string{"ffmpeg": cfg.FFmpeg, "ffprobe": cfg.FFprobe} {
		if err := copyFile(src, filepath.Join(cfg.Dest, name)); err != nil {
			return err
		}
	}

	for name := range files {
		target := filepath.Join(cfg.Dest, name)
		if err := cfg.RunTool("install_name_tool", "-id", "@loader_path/"+name, target); err != nil {
			return err
		}
		if err := rewriteLoadCommands(target, refs, cfg); err != nil {
			return err
		}
		if err := signFile(target, cfg); err != nil {
			return err
		}
	}
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		target := filepath.Join(cfg.Dest, name)
		if err := os.Chmod(target, 0o755); err != nil {
			return err
		}
		if err := rewriteLoadCommands(target, refs, cfg); err != nil {
			return err
		}
		if err := signFile(target, cfg); err != nil {
			return err
		}
	}

	// A half-rewritten bundle only breaks on a machine without Homebrew, which
	// is exactly where nobody tests. Verify here instead.
	entries, err := os.ReadDir(cfg.Dest)
	if err != nil {
		return err
	}
	for _, e := range entries {
		left, err := absoluteLeftovers(filepath.Join(cfg.Dest, e.Name()), cfg.Load)
		if err != nil {
			return fmt.Errorf("verify %s: %w", e.Name(), err)
		}
		if len(left) > 0 {
			return fmt.Errorf("%s still references %v", e.Name(), left)
		}
	}
	for _, name := range []string{"ffmpeg", "ffprobe"} {
		out, err := cfg.RunBinary(filepath.Join(cfg.Dest, name), "-version")
		if err != nil {
			return fmt.Errorf("%s does not run from the bundle: %w: %s", name, err, out)
		}
		first := strings.SplitN(out, "\n", 2)[0]
		fmt.Fprintf(cfg.Out, "ok  %s: %s\n", name, first)
	}
	var size int64
	for _, e := range entries {
		if st, err := e.Info(); err == nil {
			size += st.Size()
		}
	}
	fmt.Fprintf(cfg.Out, "\nbundled %d files, %.1f MB -> %s\n", len(entries), float64(size)/1e6, cfg.Dest)
	return nil
}

func mapValues(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for _, v := range m {
		out = append(out, v)
	}
	return out
}
