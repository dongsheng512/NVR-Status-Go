package nvgui

import "testing"

// A profile with AVSave=true but DeepAVCheck=false used to make the settings
// dialog show deep checking off while scan.Build still ran a deep scan,
// because AVSave implies deep. Loading the form must normalize the state.
func TestSyncFormFromOptionsNormalizesSaveImpliesDeep(t *testing.T) {
	app := newTestApp(t)
	app.opt.AVSave = true
	app.opt.DeepAVCheck = false
	app.syncFormFromOptions()
	if !app.opt.DeepAVCheck {
		t.Fatalf("DeepAVCheck = false after sync; AVSave should imply deep")
	}
	if !app.options().DeepAVCheck {
		t.Fatalf("options() still reports deep off")
	}
}
