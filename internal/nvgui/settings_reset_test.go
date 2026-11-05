package nvgui

import (
	"reflect"
	"testing"

	"nvrstatus/internal/model"
)

// The reset button has to reset both halves of the form: numberFieldState holds
// the numbers, a.opt holds the flags. Resetting only one leaves values that look
// default but scan with the old ones.
func TestResetScanOptionsRestoresBothFormAndFlags(t *testing.T) {
	app := newTestApp(t)
	app.opt = model.ScanOptions{
		Lookback: 240, LookbackUnit: "hour", Workers: 3, DeviceWorkers: 4,
		NoSearch: true, DeepAVCheck: true, AVSave: true, AVSaveRoot: "/tmp/samples",
		AVSeconds: 11, AVWorkers: 3, AVLimit: 7, SilenceDB: -42,
		BusyStart: 3, BusyEnd: 7, BusyDaysAgo: 5,
	}
	app.syncFormFromOptions()

	app.resetScanOptions()

	if got, want := app.options(), model.DefaultScanOptions(); !reflect.DeepEqual(got, want) {
		t.Fatalf("options after reset differ:\n got  %+v\n want %+v", got, want)
	}
	if app.lookbackUnit != "分钟" {
		t.Fatalf("lookback unit left at %q, want 分钟", app.lookbackUnit)
	}
	if app.workers.Value != 8 || app.avSeconds.Value != 6 || app.silenceDB.Value != -80 {
		t.Fatalf("form numbers were not reset: workers=%v avSeconds=%v silenceDB=%v",
			app.workers.Value, app.avSeconds.Value, app.silenceDB.Value)
	}
	if app.saveRoot != "" {
		t.Fatalf("save root left at %q, want empty", app.saveRoot)
	}
}
