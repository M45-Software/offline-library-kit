//go:build linux && (amd64 || arm64)

package librarysearch

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"syscall"
)

func evictFilePages(f *os.File) error {
	_, _, e := syscall.Syscall6(syscall.SYS_FADVISE64, f.Fd(), 0, 0, 4, 0, 0)
	if e != 0 {
		return e
	}
	return nil
}
func physicalReadBytes() (uint64, error) {
	b, e := os.ReadFile("/proc/self/io")
	if e != nil {
		return 0, e
	}
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, "read_bytes:") {
			return strconv.ParseUint(strings.TrimSpace(strings.TrimPrefix(line, "read_bytes:")), 10, 64)
		}
	}
	return 0, errors.New("read_bytes unavailable")
}
func availableBytes(path string) (uint64, error) {
	var st syscall.Statfs_t
	if e := syscall.Statfs(path, &st); e != nil {
		return 0, e
	}
	return st.Bavail * uint64(st.Bsize), nil
}
func lockWorkFile(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
}
