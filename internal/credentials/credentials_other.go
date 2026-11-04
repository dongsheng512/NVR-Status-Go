//go:build !darwin && !windows

package credentials

// Non-macOS, non-Windows platforms have no built-in credential store. The
// caller falls back to keeping the password in profiles.json (the same policy
// as the Python implementation on Linux).
func platformAvailable() bool { return false }

func platformSetPassword(account, password string) bool { return false }

func platformGetPassword(account string) string { return "" }

func platformDeletePassword(account string) {}
