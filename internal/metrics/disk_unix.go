//go:build unix

package metrics

import "syscall"

// readDisk uses statfs to compute usage for the filesystem containing path.
func readDisk(path string) (usedPct, usedGiB, totalGiB float64) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, 0
	}
	bs := uint64(st.Bsize)
	total := st.Blocks * bs
	free := st.Bavail * bs
	used := total - free
	const giB = 1 << 30
	totalGiB = float64(total) / giB
	usedGiB = float64(used) / giB
	if total > 0 {
		usedPct = float64(used) / float64(total) * 100
	}
	return usedPct, usedGiB, totalGiB
}
