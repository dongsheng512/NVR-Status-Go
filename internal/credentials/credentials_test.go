package credentials

import (
	"runtime"
	"testing"

	"nvrstatus/internal/model"
)

func TestAccountNaming(t *testing.T) {
	cases := []struct {
		name    string
		profile string
		device  model.Device
		want    string
	}{
		{
			name:    "ip wins",
			profile: "默认",
			device:  model.Device{IP: "1.2.3.4", Name: "NVR-A", Username: "admin"},
			want:    "默认::1.2.3.4",
		},
		{
			name:    "falls back to device name",
			profile: "p",
			device:  model.Device{Name: "NVR-A"},
			want:    "p::NVR-A",
		},
		{
			name:    "ip is trimmed",
			profile: "p",
			device:  model.Device{IP: "  10.0.0.1  ", Name: "x"},
			want:    "p::10.0.0.1",
		},
		{
			name:    "username is not part of the key",
			profile: "p",
			device:  model.Device{IP: "10.0.0.1", Username: "admin"},
			want:    "p::10.0.0.1",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := account(tc.profile, tc.device); got != tc.want {
				t.Fatalf("account() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestWinTargetIsPerDevice(t *testing.T) {
	a := account("p", model.Device{IP: "1.1.1.1"})
	b := account("p", model.Device{IP: "2.2.2.2"})
	if winTarget(a) == winTarget(b) {
		t.Fatalf("Windows targets must be unique per device: %q", winTarget(a))
	}
	const prefix = Service + "/"
	if got := winTarget(a); len(got) <= len(prefix) || got[:len(prefix)] != prefix {
		t.Fatalf("winTarget(a) = %q, want prefix %q", got, prefix)
	}
}

func TestAvailableMatchesPlatform(t *testing.T) {
	switch runtime.GOOS {
	case "windows":
		if !Available() {
			t.Fatal("Available() must be true on Windows")
		}
	case "darwin":
		// Covered in detail by the darwin-only tests; here we simply require
		// that it reports a boolean without panicking.
		_ = Available()
	default:
		if Available() {
			t.Fatal("Available() must be false on non-macOS/non-Windows platforms")
		}
	}
}
