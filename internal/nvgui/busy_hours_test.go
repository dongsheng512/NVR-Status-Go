package nvgui

import "testing"

// The GUI stores the preferred hours in numberFieldState, not in a.opt. Make
// sure they actually reach ScanOptions, otherwise the setting is cosmetic even
// after the probe window logic is fixed.
func TestOptionsCarryPreferredHours(t *testing.T) {
	app := newTestApp(t)
	app.busyStart.set(9)
	app.busyEnd.set(18)
	app.busyDaysAgo.set(2)

	o := app.options()
	if o.BusyStart != 9 || o.BusyEnd != 18 || o.BusyDaysAgo != 2 {
		t.Fatalf("preferred hours = %d-%d, daysAgo=%d; want 9-18 / 2",
			o.BusyStart, o.BusyEnd, o.BusyDaysAgo)
	}
}
