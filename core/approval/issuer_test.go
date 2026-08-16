package approval

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadPrivateKeyAndSignExactRecord(t *testing.T) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "approval-key.pem")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadPrivateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	record, err := NewRecord("plan_01K20J6M6E6M2YAHG8W0W8N4AN", "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"repository-write", "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"owner:test", "owner", "issue:123", time.Date(2026, 8, 9, 12, 0, 0, 0, time.UTC), 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	signed, err := Sign(record, "key-1", loaded)
	if err != nil || signed.Signature.Value == "" || signed.Signature.KeyID != "key-1" {
		t.Fatalf("signed=%#v err=%v", signed, err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadPrivateKey(path); err == nil {
		t.Fatal("group-readable private key was accepted")
	}
}
