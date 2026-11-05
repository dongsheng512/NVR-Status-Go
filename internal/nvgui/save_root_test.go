package nvgui

import (
	"errors"
	"strings"
	"testing"

	"github.com/egoist/mygo"
)

// The native picker cannot open in a headless test, so stub it and check the
// options it is given: this must be a directory picker (not a file picker),
// start from the current path, and let the user create a folder.
func TestPickSaveRootUsesFolderDialog(t *testing.T) {
	app := newTestApp(t)
	app.saveRoot = "/tmp/previous"

	old := openFolderDialog
	defer func() { openFolderDialog = old }()
	var opts mygo.OpenDialogOptions
	openFolderDialog = func(o mygo.OpenDialogOptions) ([]string, error) {
		opts = o
		return []string{"/tmp/chosen"}, nil
	}

	app.pickSaveRoot("/tmp/previous")
	if !opts.Directory || !opts.CreateDirectories {
		t.Fatalf("not a folder picker: %+v", opts)
	}
	if opts.DefaultPath != "/tmp/previous" {
		t.Fatalf("DefaultPath = %q", opts.DefaultPath)
	}
	if app.saveRoot != "/tmp/chosen" || app.options().AVSaveRoot != "/tmp/chosen" {
		t.Fatalf("chosen path not applied: %q / %q", app.saveRoot, app.options().AVSaveRoot)
	}
}

func TestPickSaveRootCancelAndErrorLeavePathAlone(t *testing.T) {
	app := newTestApp(t)
	app.saveRoot = "/tmp/keep"
	old := openFolderDialog
	defer func() { openFolderDialog = old }()

	openFolderDialog = func(mygo.OpenDialogOptions) ([]string, error) { return nil, nil }
	app.pickSaveRoot("/tmp/keep")
	openFolderDialog = func(mygo.OpenDialogOptions) ([]string, error) { return nil, errors.New("dialog failed") }
	app.pickSaveRoot("/tmp/keep")
	if app.saveRoot != "/tmp/keep" {
		t.Fatalf("cancel/error changed the path to %q", app.saveRoot)
	}
}

// The picker itself is a native dialog, but the state change it makes must be
// the state change it makes must be verified: the path has to reach both the
// form and ScanOptions, and a cancel (empty path) must leave it alone.
func TestApplySaveRoot(t *testing.T) {
	app := newTestApp(t)

	app.applySaveRoot("")
	if app.saveRoot != "" {
		t.Fatalf("cancel changed saveRoot to %q", app.saveRoot)
	}

	app.applySaveRoot("/tmp/av_samples")
	if app.saveRoot != "/tmp/av_samples" {
		t.Fatalf("saveRoot = %q", app.saveRoot)
	}
	if got := app.options().AVSaveRoot; got != "/tmp/av_samples" {
		t.Fatalf("AVSaveRoot = %q, want the chosen folder", got)
	}
	found := false
	for _, l := range app.logs {
		if strings.Contains(l.Text, "已设置保存目录") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no feedback log line; logs = %v", app.logs)
	}

	app.applySaveRoot("")
	if app.saveRoot != "/tmp/av_samples" {
		t.Fatalf("second cancel wiped the choice: %q", app.saveRoot)
	}
}
