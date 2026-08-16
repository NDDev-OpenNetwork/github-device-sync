package state

import (
	"fmt"
	"os"
	"path/filepath"
)

func DefaultPath() (string, error) {
	root := os.Getenv("XDG_STATE_HOME")
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory for GDS state: %w", err)
		}
		root = filepath.Join(home, ".local", "state")
	} else if !filepath.IsAbs(root) {
		return "", fmt.Errorf("XDG_STATE_HOME must be an absolute path")
	}
	return filepath.Join(root, "github-device-sync", "state.db"), nil
}
