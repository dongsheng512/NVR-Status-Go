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

	type call struct {
		args  []string
		stdin string
	}
	var calls []call
	runSecurity = func(args []string, stdin string) (string, int, error) {
		calls = append(calls, call{args: append([]string(nil), args...), stdin: stdin})
		if len(args) > 0 && args[0] == "find-generic-password" {
			return "keychain: \"login\"\npassword: \"keychain-pw\"\n", 0, nil
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
	if len(add.args) != 1 || add.args[0] != "-i" {
		t.Fatalf("first command args = %#v, want [-i]", add.args)
	}
	wantStdin := fmt.Sprintf("add-generic-password -U -s %q -a \"p::9.9.9.9\" -X 707731\n", Service)
	if add.stdin != wantStdin {
		t.Fatalf("add-generic-password stdin = %q, want %q", add.stdin, wantStdin)
	}
	find := calls[1]
	if find.args[0] != "find-generic-password" || !containsSequence(find.args, []string{"-s", Service, "-a", "p::9.9.9.9", "-g"}) {
		t.Fatalf("find-generic-password args = %#v", find.args)
	}
	del := calls[2]
	if del.args[0] != "delete-generic-password" || !containsSequence(del.args, []string{"-s", Service, "-a", "p::9.9.9.9"}) {
		t.Fatalf("delete-generic-password args = %#v", del.args)
	}
}

func TestParseSecurityGPasswordUTF8Hex(t *testing.T) {
	// Non-ASCII password "密码!" encoded as UTF-8 hex by `security -g`.
	out := "attributes:\npassword: 0xE5AF86E7A08121  \"\\345\\257\\206\\347\\240\\201!\"\n"
	if got := parseSecurityGPassword(out); got != "密码!" {
		t.Fatalf("parseSecurityGPassword = %q, want 密码!", got)
	}
	if got := parseSecurityGPassword("password: \"deadbeef\"\n"); got != "deadbeef" {
		t.Fatalf("ASCII hex-like password = %q, want deadbeef", got)
	}
}

func TestEmptyPasswordDeletes(t *testing.T) {
	oldRunner := runSecurity
	t.Cleanup(func() { runSecurity = oldRunner })

	var calls [][]string
	runSecurity = func(args []string, stdin string) (string, int, error) {
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
