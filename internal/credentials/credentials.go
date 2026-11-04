// Package credentials stores device passwords in the operating system's
// credential store, mirroring services/credentials.py from the Python
// implementation.
//
// macOS uses the `security` CLI (Keychain), Windows uses the Credential
// Manager through advapi32.dll, and every other platform reports the store as
// unavailable so callers can fall back to plaintext storage.
//
// Passwords are never logged by this package.
package credentials

import (
	"strings"

	"nvrstatus/internal/model"
)

// Service is the keychain service name used by the Python implementation.
const Service = "NVRStatus"

// account builds the keychain account key for one device.
//
// It intentionally matches the Python v2 implementation:
//
//	<profile>::<ip or device name>
//
// Keeping this format stable is required to find credentials written by the
// previous Python releases. Note that the device username is deliberately not
// part of the key: the account identifies a device, not a login.
func account(profile string, d model.Device) string {
	ip := strings.TrimSpace(d.IP)
	name := strings.TrimSpace(d.Name)
	host := ip
	if host == "" {
		host = name
	}
	return profile + "::" + host
}

// winTarget returns the Windows Credential Manager TargetName for an account.
// A per-device target is required: the old Python implementation used a single
// fixed target, which made multiple devices overwrite each other.
func winTarget(account string) string {
	return Service + "/" + account
}

// Available reports whether the current platform has a usable credential
// store.
func Available() bool {
	return platformAvailable()
}

// SetPassword stores password for the device. An empty password deletes the
// stored entry and reports false (matching the Python semantics).
func SetPassword(profile string, d model.Device, password string) bool {
	if password == "" {
		DeletePassword(profile, d)
		return false
	}
	return platformSetPassword(account(profile, d), password)
}

// GetPassword returns the stored password, or "" when it does not exist or
// the platform store is unavailable.
func GetPassword(profile string, d model.Device) string {
	return platformGetPassword(account(profile, d))
}

// DeletePassword removes the stored password. It is best effort: missing
// entries and platform errors are ignored.
func DeletePassword(profile string, d model.Device) {
	platformDeletePassword(account(profile, d))
}

// RekeyPassword moves a stored password from one profile name to another.
// It is used when a profile is renamed or cloned.
func RekeyPassword(oldProfile, newProfile string, d model.Device) {
	if oldProfile == newProfile {
		return
	}
	pw := GetPassword(oldProfile, d)
	if pw == "" {
		return
	}
	if SetPassword(newProfile, d, pw) {
		DeletePassword(oldProfile, d)
	}
}
