package nvgui

import (
	"fmt"
	"testing"

	"github.com/egoist/mygo/ui"
)

// A scan streams lines into the sidebar while it runs, so the view has to keep
// up with the end on its own. Scrolling up has to win, though: otherwise the
// log would yank itself away from under someone reading it.
func TestLogFollowsItsEndUntilTheUserScrollsUp(t *testing.T) {
	app := newTestApp(t)
	app.logExpanded = true
	for i := 0; i < 400; i++ {
		app.logf(fmt.Sprintf("第 %d 行：通道 1 录像正常", i), "")
	}

	tt := ui.NewTester(app.View, 1000, 700)
	tt.Frame()

	if app.logScroll.MaxY <= 0 {
		t.Fatalf("log did not overflow its box (MaxY=%v); this case proves nothing", app.logScroll.MaxY)
	}
	// Following means the view sits at the end; the container writes back the
	// offset it clamped, so compare against MaxY rather than MaxFloat32.
	if app.logScroll.Y < app.logScroll.MaxY {
		t.Fatalf("view did not follow the end: Y=%v MaxY=%v", app.logScroll.Y, app.logScroll.MaxY)
	}

	// Reading back: the next frame must leave the view where the user put it.
	mid := app.logScroll.MaxY / 2
	app.logScroll.Y = mid
	tt.Frame()
	if app.logScroll.Y != mid {
		t.Fatalf("view moved off the read position: Y=%v want %v", app.logScroll.Y, mid)
	}
}
