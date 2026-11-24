//go:build darwin

package credentials

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
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
// command's combined stdout/stderr, its exit code, and an execution error (nil
// when the command ran but exited non-zero).
var runSecurity = func(args []string, stdin string) (string, int, error) {
	ctx, cancel := context.WithTimeout(context.Background(), securityTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, securityCLIPath, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok {
			return out.String(), exit.ExitCode(), nil
		}
		return "", -1, err
	}
	return out.String(), 0, nil
}

func platformAvailable() bool {
	st, err := os.Stat(securityCLIPath)
	return err == nil && !st.IsDir()
}

func platformSetPassword(account, password string) bool {
	// Pass the secret over stdin via `security -i` with hex `-X` so the
	// plaintext password never appears in process argv (`ps aux`) and arbitrary
	// UTF-8 / quote characters are stored verbatim.
	line := fmt.Sprintf("add-generic-password -U -s %s -a %s -X %s\n",
		quoteSecurityArg(Service),
		quoteSecurityArg(account),
		hex.EncodeToString([]byte(password)),
	)
	_, code, err := runSecurity([]string{"-i"}, line)
	return err == nil && code == 0
}

func platformGetPassword(account string) string {
	// Use `-g` rather than `-w`: `security -w` emits unprefixed hex for any
	// non-ASCII password (e.g. Chinese characters), making it impossible to
	// distinguish from an ASCII hex password. `-g` explicitly prefixes hex
	// values with `password: 0x...` and quotes ASCII values as `password: "..."`.
	out, code, err := runSecurity([]string{
		"find-generic-password",
		"-s", Service,
		"-a", account,
		"-g",
	}, "")
	if err != nil || code != 0 {
		return ""
	}
	return parseSecurityGPassword(out)
}

func platformDeletePassword(account string) {
	// Keychain ACL prompts can block `security`; a failed delete must not
	// break the caller.
	_, _, _ = runSecurity([]string{
		"delete-generic-password",
		"-s", Service,
		"-a", account,
	}, "")
}

func quoteSecurityArg(s string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s)
	return `"` + escaped + `"`
}

// parseSecurityGPassword extracts the secret from `security find-generic-password -g`
// output, handling both `password: "..."` (printable ASCII) and
// `password: 0x<HEX>  "..."` (UTF-8 / non-ASCII / control bytes).
func parseSecurityGPassword(out string) string {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if !strings.HasPrefix(line, "password:") {
			continue
		}
		val := strings.TrimSpace(strings.TrimPrefix(line, "password:"))
		if strings.HasPrefix(val, "0x") || strings.HasPrefix(val, "0X") {
			hexPart := val[2:]
			if sp := strings.IndexAny(hexPart, " \t"); sp >= 0 {
				hexPart = hexPart[:sp]
			}
			if decoded, err := hex.DecodeString(hexPart); err == nil {
				return string(decoded)
			}
		}
		if len(val) >= 2 && val[0] == '"' && val[len(val)-1] == '"' {
			return val[1 : len(val)-1]
		}
		return strings.TrimRight(val, "\x00")
	}
	return ""
}
