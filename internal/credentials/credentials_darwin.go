//go:build darwin

package credentials

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"time"
)

// securityCLIPath is a variable so tests can simulate a missing or present
// `security` binary without touching the real one.
var securityCLIPath = "/usr/bin/security"

// securityTimeout bounds every keychain invocation: an unresponsive Keychain
// ACL prompt must never hang the GUI.
var securityTimeout = 15 * time.Second

// runSecurity is the injectable command runner used by tests. It returns the
// command's stdout, its exit code, and an execution error (nil when the
// command ran but exited non-zero).
var runSecurity = func(args []string) (string, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), securityTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, securityCLIPath, args...)
	out, err := cmd.Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return string(out), exit.ExitCode(), nil
		}
		return "", -1, err
	}
	return string(out), 0, nil
}

func platformAvailable() bool {
	st, err := os.Stat(securityCLIPath)
	return err == nil && !st.IsDir()
}

func platformSetPassword(account, password string) bool {
	_, code, err := runSecurity([]string{
		"add-generic-password",
		"-U",
		"-s", Service,
		"-a", account,
		"-w", password,
	})
	return err == nil && code == 0
}

func platformGetPassword(account string) string {
	out, code, err := runSecurity([]string{
		"find-generic-password",
		"-s", Service,
		"-a", account,
		"-w",
	})
	if err != nil || code != 0 {
		return ""
	}
	return strings.TrimRight(out, "\r\n\x00")
}

func platformDeletePassword(account string) {
	// Keychain ACL prompts can block `security`; a failed delete must not
	// break the caller.
	_, _, _ = runSecurity([]string{
		"delete-generic-password",
		"-s", Service,
		"-a", account,
	})
}
