//go:build windows

package lens

import (
	"os"
	"syscall"
	"unsafe"
)

var lockFileEx = syscall.NewLazyDLL("kernel32.dll").NewProc("LockFileEx")

func lockCacheFile(f *os.File) error {
	var overlapped syscall.Overlapped
	ok, _, err := lockFileEx.Call(f.Fd(), 3, 0, 1, 0, uintptr(unsafe.Pointer(&overlapped)))
	if ok == 0 {
		return err
	}
	return nil
}
func legacyProcessAlive(pid int) (bool, error) {
	h, err := syscall.OpenProcess(0x1000, false, uint32(pid))
	if err == syscall.Errno(87) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	defer syscall.CloseHandle(h)
	var code uint32
	if err = syscall.GetExitCodeProcess(h, &code); err != nil {
		return false, err
	}
	return code == 259, nil
}
