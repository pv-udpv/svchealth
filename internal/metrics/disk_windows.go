//go:build windows

package metrics

import (
	"syscall"
	"unsafe"
)

// readDisk uses the Win32 GetDiskFreeSpaceExW API to compute usage for the
// volume containing path. It is dependency-free (loads kernel32 lazily).
func readDisk(path string) (usedPct, usedGiB, totalGiB float64) {
	if path == "" {
		path = `C:\`
	}
	p, err := syscall.UTF16PtrFromString(path)
	if err != nil {
		return 0, 0, 0
	}
	var freeAvail, totalBytes, totalFree uint64
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetDiskFreeSpaceExW")
	r, _, _ := proc.Call(
		uintptr(unsafe.Pointer(p)),
		uintptr(unsafe.Pointer(&freeAvail)),
		uintptr(unsafe.Pointer(&totalBytes)),
		uintptr(unsafe.Pointer(&totalFree)),
	)
	if r == 0 || totalBytes == 0 {
		return 0, 0, 0
	}
	used := totalBytes - totalFree
	const giB = 1 << 30
	totalGiB = float64(totalBytes) / giB
	usedGiB = float64(used) / giB
	usedPct = float64(used) / float64(totalBytes) * 100
	return usedPct, usedGiB, totalGiB
}
