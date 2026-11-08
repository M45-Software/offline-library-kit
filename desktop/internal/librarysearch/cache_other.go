//go:build !linux || (!amd64 && !arm64)

package librarysearch

import (
	"errors"
	"os"
)

func evictFilePages(f *os.File) error {
	return errors.New("file-page eviction is not supported on this platform")
}
func physicalReadBytes() (uint64, error) {
	return 0, errors.New("physical read accounting is not supported on this platform")
}
func availableBytes(path string) (uint64, error) {
	return 0, errors.New("expanded builder volume guard currently requires Linux")
}
func lockWorkFile(f *os.File) error {
	return errors.New("expanded builder locking currently requires Linux")
}
