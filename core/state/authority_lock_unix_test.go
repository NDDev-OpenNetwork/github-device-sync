//go:build darwin || linux

package state

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestStoreHoldsCooperativeAuthorityLockForItsLifetime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "state.db")
	store, err := Initialize(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(filepath.Dir(path), stateAuthorityLockName(filepath.Base(path)))
	lock, err := os.OpenFile(lockPath, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil ||
		(!errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN)) {
		t.Fatalf("exclusive pathname mutation lock was not blocked by the live Store: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("authority lock was not released with Store: %v", err)
	}
}
