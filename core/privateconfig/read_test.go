package privateconfig

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestReadRequiresStablePrivateRegularFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runtime.yaml")
	if err := os.WriteFile(path, []byte("schema_version: 1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	absolute, raw, err := Read(path)
	if err != nil || absolute != path || string(raw) != "schema_version: 1\n" {
		t.Fatalf("absolute=%q raw=%q err=%v", absolute, raw, err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err = Read(path)
	var configError *Error
	if !errors.As(err, &configError) || configError.Kind != ErrorSecurity {
		t.Fatalf("public config error=%v", err)
	}
}
