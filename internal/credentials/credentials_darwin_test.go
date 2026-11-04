//go:build darwin

package credentials

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"nvrstatus/internal/model"
)

func TestAvailableReflectsSecurityBinary(t *testing.T) {
	oldPath := securityCLIPath
	t.Cleanup(func() { securityCLIPath = oldPath })

	dir := t.TempDir()
	fake := filepath.Join(dir, "security")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	securityCLIPath = fake
	if !Available() {
		t.Fatalf("Available() = false for existing %s", fake)
	}

	securityCLIPath = filepath.Join(dir, "missing")
	if Available() {
		t.Fatal("Available() = true for a missing security binary")
	}
}

func TestSecurityCLICommandInjection(t *testing.T) {
	oldRunner := runSecurity
	t.Cleanup(func() { runSecurity = oldRunner })

	var calls [][]string
	runSecurity = func(args []string) (string, int, error) {
		copied := append([]string(nil), args...)
		calls = append(calls, copied)
		if len(args) > 0 && args[0] == "find-generic-password" {
			return "keychain-pw\n", 0, nil
		}
		return "", 0, nil
	}

	device := model.Device{IP: "9.9.9.9", Username: "admin"}
	if !SetPassword("p", device, "pw1") {
		t.Fatal("SetPassword() = false, want true")
	}
	if got := GetPassword("p", device); got != "keychain-pw" {
		t.Fatalf("GetPassword() = %q, want keychain-pw", got)
	}
	DeletePassword("p", device)

	if len(calls) != 3 {
		t.Fatalf("got %d security invocations, want 3: %#v", len(calls), calls)
	}
	add := calls[0]
	if add[0] != "add-generic-password" {
		t.Fatalf("first command = %q, want add-generic-password", add[0])
	}
	if !containsSequence(add, []string{"-U", "-s", Service, "-a", "p::9.9.9.9", "-w", "pw1"}) {
		t.Fatalf("add-generic-password args missing expected sequence: %#v", add)
	}
	find := calls[1]
	if find[0] != "find-generic-password" || !containsSequence(find, []string{"-s", Service, "-a", "p::9.9.9.9", "-w"}) {
		t.Fatalf("find-generic-password args = %#v", find)
	}
	del := calls[2]
	if del[0] != "delete-generic-password" || !containsSequence(del, []string{"-s", Service, "-a", "p::9.9.9.9"}) {
		t.Fatalf("delete-generic-password args = %#v", del)
	}
}

func TestEmptyPasswordDeletes(t *testing.T) {
	oldRunner := runSecurity
	t.Cleanup(func() { runSecurity = oldRunner })

	var calls [][]string
	runSecurity = func(args []string) (string, int, error) {
		calls = append(calls, append([]string(nil), args...))
		return "", 0, nil
	}
	if SetPassword("p", model.Device{IP: "1.2.3.4"}, "") {
		t.Fatal("SetPassword(empty) = true, want false")
	}
	if len(calls) != 1 || calls[0][0] != "delete-generic-password" {
		t.Fatalf("empty password should delete, calls = %#v", calls)
	}
}

// TestLiveKeychain touches the real Keychain and is therefore opt-in.
func TestLiveKeychain(t *testing.T) {
	if os.Getenv("LIVE_KEYRING") != "1" {
		t.Skip("set LIVE_KEYRING=1 to run against the real Keychain")
	}
	if !Available() {
		t.Skip("security CLI not available")
	}
	profile := fmt.Sprintf("nvrstatus-test-%d", time.Now().UnixNano())
	device := model.Device{Name: "unit-test", IP: "127.0.0.1"}
	defer DeletePassword(profile, device)

	if !SetPassword(profile, device, "s3cret") {
		t.Fatal("SetPassword failed against live Keychain")
	}
	if got := GetPassword(profile, device); got != "s3cret" {
		t.Fatal("GetPassword did not match the stored value")
	}
	DeletePassword(profile, device)
	if got := GetPassword(profile, device); got != "" {
		t.Fatal("GetPassword after delete returned a non-empty value")
	}
}

func containsSequence(haystack, needle []string) bool {
	if len(needle) > len(haystack) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
