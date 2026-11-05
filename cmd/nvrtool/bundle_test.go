package main

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestIsSystemDep(t *testing.T) {
	for _, s := range []string{"/usr/lib/libSystem.B.dylib", "/System/Library/Frameworks/AppKit.framework/Versions/C/AppKit", "@executable_path/../lib/x.dylib"} {
		if !isSystemDep(s) {
			t.Errorf("%q should be a system dep", s)
		}
	}
	for _, s := range []string{"/opt/homebrew/lib/libx.dylib", "@loader_path/libx.dylib"} {
		if isSystemDep(s) {
			t.Errorf("%q should not be a system dep", s)
		}
	}
}

func TestCollectDylibsMapsSymlinkNamesToRealFiles(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "libavcodec.63.1.102.dylib")
	link := filepath.Join(dir, "libavcodec.63.dylib")
	for _, p := range []string{real, link} {
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	ffmpeg := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(ffmpeg, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}

	load := func(path string) ([]string, error) {
		if path == ffmpeg || filepath.Base(path) == "ffmpeg" {
			return []string{link, "/usr/lib/libSystem.B.dylib"}, nil
		}
		return nil, nil
	}
	files, refs, err := collectDylibs([]string{ffmpeg}, load)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files["libavcodec.63.1.102.dylib"] != realPath(real) {
		t.Fatalf("files = %v", files)
	}
	if refs[link] != "libavcodec.63.1.102.dylib" {
		t.Fatalf("refs = %v", refs)
	}
}

func TestCollectDylibsRejectsBasenameCollision(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a", "lib.dylib")
	b := filepath.Join(dir, "b", "lib.dylib")
	for _, p := range []string{a, b} {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join(dir, "root")
	if err := os.WriteFile(root, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	load := func(path string) ([]string, error) {
		if path == root {
			return []string{a, b}, nil
		}
		return nil, nil
	}
	if _, _, err := collectDylibs([]string{root}, load); err == nil {
		t.Fatal("expected a collision error")
	}
}

func TestRewriteLoadCommandsUsesRefmap(t *testing.T) {
	var calls [][]string
	cfg := bundleConfig{
		Load: func(string) ([]string, error) {
			return []string{"/opt/lib/libavcodec.63.dylib", "@loader_path/libx.dylib", "/usr/lib/libSystem.B.dylib"}, nil
		},
		RunTool: func(name string, args ...string) error {
			calls = append(calls, append([]string{name}, args...))
			return nil
		},
	}
	refs := map[string]string{"/opt/lib/libavcodec.63.dylib": "libavcodec.63.1.102.dylib"}
	if err := rewriteLoadCommands("dest/ffmpeg", refs, cfg); err != nil {
		t.Fatal(err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %v, want exactly one change", calls)
	}
	want := []string{"install_name_tool", "-change", "/opt/lib/libavcodec.63.dylib", "@loader_path/libavcodec.63.1.102.dylib", "dest/ffmpeg"}
	if strings.Join(calls[0], " ") != strings.Join(want, " ") {
		t.Fatalf("call = %v, want %v", calls[0], want)
	}
}

func TestAbsoluteLeftovers(t *testing.T) {
	got, err := absoluteLeftovers("x", func(string) ([]string, error) {
		return []string{"@loader_path/libx.dylib", "/usr/lib/libSystem.B.dylib", "/opt/homebrew/lib/x.dylib"}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "/opt/homebrew/lib/x.dylib" {
		t.Fatalf("leftovers = %v", got)
	}
}

func TestBundleCompletesWithInjectedTools(t *testing.T) {
	dir := t.TempDir()
	dest := filepath.Join(dir, "bin")
	ffmpeg := filepath.Join(dir, "ffmpeg")
	ffprobe := filepath.Join(dir, "ffprobe")
	depA := filepath.Join(dir, "liba.dylib")
	depB := filepath.Join(dir, "libb.dylib")
	for _, p := range []string{ffmpeg, ffprobe, depA, depB} {
		if err := os.WriteFile(p, []byte("fake"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	load := func(path string) ([]string, error) {
		if strings.HasPrefix(path, dest) {
			// After rewriting, only relative/system references may remain.
			return []string{"@loader_path/liba.dylib", "/usr/lib/libSystem.B.dylib"}, nil
		}
		if path == ffmpeg || path == ffprobe {
			return []string{depA, depB, "/usr/lib/libSystem.B.dylib"}, nil
		}
		return nil, nil
	}
	var tools [][]string
	cfg := bundleConfig{
		Dest:    dest,
		FFmpeg:  ffmpeg,
		FFprobe: ffprobe,
		Check:   false,
		Load:    load,
		RunTool: func(name string, args ...string) error {
			tools = append(tools, append([]string{name}, args...))
			return nil
		},
		RunBinary: func(string, ...string) (string, error) { return "ffmpeg version 9.0\n", nil },
		Out:       io.Discard,
	}
	if err := bundle(cfg); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 4 {
		t.Fatalf("bundled files = %v, want 4", entries)
	}
	var ids, signs int
	for _, call := range tools {
		if call[0] == "install_name_tool" && call[1] == "-id" {
			ids++
		}
		if call[0] == "codesign" {
			signs++
		}
	}
	if ids != 2 || signs != 4 {
		t.Fatalf("ids=%d signs=%d, want 2 / 4", ids, signs)
	}
}

func TestBundleRejectsHalfRewrittenOutput(t *testing.T) {
	dir := t.TempDir()
	ffmpeg := filepath.Join(dir, "ffmpeg")
	ffprobe := filepath.Join(dir, "ffprobe")
	for _, p := range []string{ffmpeg, ffprobe} {
		if err := os.WriteFile(p, []byte("fake"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	cfg := bundleConfig{
		Dest:    filepath.Join(dir, "bin"),
		FFmpeg:  ffmpeg,
		FFprobe: ffprobe,
		Load: func(string) ([]string, error) {
			return []string{"/opt/homebrew/lib/leak.dylib"}, nil
		},
		RunTool:   func(string, ...string) error { return nil },
		RunBinary: func(string, ...string) (string, error) { return "ok\n", nil },
		Out:       io.Discard,
	}
	if err := bundle(cfg); err == nil {
		t.Fatal("bundle must refuse to ship an absolute leftover")
	}
}

// debug/macho replaces `otool -L`. The Go test binary is itself a Mach-O file
// on macOS, so this checks the real parser without needing ffmpeg in CI.
func TestMachoDylibsOnTestBinary(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Mach-O only")
	}
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	libs, err := machoDylibs(exe)
	if err != nil {
		t.Fatal(err)
	}
	if len(libs) == 0 {
		t.Fatal("no dylib load commands found in the test binary")
	}
}
