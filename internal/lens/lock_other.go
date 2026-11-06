//go:build !darwin && !linux && !windows

package lens

import (
	"fmt"
	"os"
)

func lockCacheFile(f *os.File) error {
	return fmt.Errorf("cache locks are unsupported on this platform")
}
func legacyProcessAlive(pid int) (bool, error) {
	return false, fmt.Errorf("legacy process check unsupported")
}
