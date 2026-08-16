//go:build !darwin && !linux

package gitauthority

import "os"

func trustedExecutableOwner(info os.FileInfo) bool {
	return info != nil
}

func trustedDiscoveryOwner(info os.FileInfo) bool {
	return info != nil
}
