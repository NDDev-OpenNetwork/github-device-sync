//go:build !darwin && !linux

package state

import "os"

func ownedByCurrentUser(os.FileInfo) bool { return true }
