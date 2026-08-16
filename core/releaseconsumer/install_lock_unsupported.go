//go:build !darwin && !linux

package releaseconsumer

import (
	"context"
	"errors"
	"os"
)

func openInstallScopeLockFile(_ *os.Root, _ string) (*os.File, error) {
	return nil, errors.New("release installation locks require darwin or linux")
}

func acquireInstallScopeLock(_ context.Context, _ *os.File) error {
	return errors.New("release installation locks require darwin or linux")
}
