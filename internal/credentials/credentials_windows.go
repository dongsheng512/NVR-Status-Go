//go:build windows

package credentials

import (
	"encoding/binary"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"
)

// Windows Credential Manager constants.
const (
	credTypeGeneric           = 1
	credPersistLocalMachine   = 2
	credentialBlobSizeInBytes = 2 // UTF-16 code unit
)

var (
	advapi32       = syscall.NewLazyDLL("advapi32.dll")
	procCredWrite  = advapi32.NewProc("CredWriteW")
	procCredRead   = advapi32.NewProc("CredReadW")
	procCredDelete = advapi32.NewProc("CredDeleteW")
	procCredFree   = advapi32.NewProc("CredFree")
)

// credentialW mirrors the Windows CREDENTIALW structure. The field order and
// types must not be changed.
type credentialW struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        uint64
	CredentialBlobSize uint32
	CredentialBlob     unsafe.Pointer
	Persist            uint32
	AttributeCount     uint32
	Attributes         unsafe.Pointer
	TargetAlias        *uint16
	UserName           *uint16
}

func platformAvailable() bool { return true }

func platformSetPassword(account, password string) bool {
	target, err := syscall.UTF16PtrFromString(winTarget(account))
	if err != nil {
		return false
	}
	user, err := syscall.UTF16PtrFromString(account)
	if err != nil {
		return false
	}
	blob := syscall.StringToUTF16(password) // includes the trailing NUL
	if len(blob) == 0 {
		return false
	}
	cred := credentialW{
		Type:               credTypeGeneric,
		TargetName:         target,
		CredentialBlobSize: uint32(len(blob) * credentialBlobSizeInBytes),
		CredentialBlob:     unsafe.Pointer(&blob[0]),
		Persist:            credPersistLocalMachine,
		UserName:           user,
	}
	ok, _, _ := procCredWrite.Call(uintptr(unsafe.Pointer(&cred)), 0)
	if ok == 0 {
		return false
	}
	// Clean up the legacy single-slot entry written by old Python releases so
	// a stale password can never be read back for a different device.
	if legacy, e := syscall.UTF16PtrFromString(Service); e == nil {
		_, _, _ = procCredDelete.Call(uintptr(unsafe.Pointer(legacy)), credTypeGeneric, 0)
	}
	return true
}

func platformGetPassword(account string) string {
	if pw := readCredential(winTarget(account), ""); pw != "" {
		return pw
	}
	// Compatibility with the legacy fixed TargetName=Service entry, which
	// stored the account name in UserName.
	return readCredential(Service, account)
}

func platformDeletePassword(account string) {
	if target, err := syscall.UTF16PtrFromString(winTarget(account)); err == nil {
		_, _, _ = procCredDelete.Call(uintptr(unsafe.Pointer(target)), credTypeGeneric, 0)
	}
	// Delete the legacy slot only when it belongs to this account.
	if cred := readCredentialPtr(Service); cred != nil {
		if utf16PtrToString(cred.UserName) == account {
			if legacy, err := syscall.UTF16PtrFromString(Service); err == nil {
				_, _, _ = procCredDelete.Call(uintptr(unsafe.Pointer(legacy)), credTypeGeneric, 0)
			}
		}
		procCredFree.Call(uintptr(unsafe.Pointer(cred)))
	}
}

func readCredential(target, wantUser string) string {
	cred := readCredentialPtr(target)
	if cred == nil {
		return ""
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(cred)))
	if wantUser != "" && utf16PtrToString(cred.UserName) != wantUser {
		return ""
	}
	return blobToString(cred.CredentialBlob, cred.CredentialBlobSize)
}

func readCredentialPtr(target string) *credentialW {
	name, err := syscall.UTF16PtrFromString(target)
	if err != nil {
		return nil
	}
	var cred *credentialW
	ok, _, _ := procCredRead.Call(
		uintptr(unsafe.Pointer(name)),
		credTypeGeneric,
		0,
		uintptr(unsafe.Pointer(&cred)),
	)
	if ok == 0 || cred == nil {
		return nil
	}
	return cred
}

func blobToString(blob unsafe.Pointer, size uint32) string {
	if blob == nil || size == 0 {
		return ""
	}
	raw := unsafe.Slice((*byte)(blob), int(size))
	units := make([]uint16, 0, len(raw)/2)
	for i := 0; i+1 < len(raw); i += 2 {
		units = append(units, binary.LittleEndian.Uint16(raw[i:i+2]))
	}
	return strings.TrimRight(string(utf16.Decode(units)), "\x00")
}

func utf16PtrToString(p *uint16) string {
	if p == nil {
		return ""
	}
	n := 0
	for *(*uint16)(unsafe.Add(unsafe.Pointer(p), n*2)) != 0 {
		n++
	}
	return syscall.UTF16ToString(unsafe.Slice(p, n))
}
