package audit

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/estate"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/reconciler"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

type auditKeyStore []byte

func (store auditKeyStore) Get(context.Context, string) ([]byte, error) {
	return append([]byte(nil), store...), nil
}

func TestRecorderWritesVerifiablePinnedSnapshotAndRejectsMetadataTamper(t *testing.T) {
	recorder, publicKey := auditRecorder(t)
	createdAt := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	reconciliationID := "reconciliation_01KX7BV07RHD6KRA4Z4J0KCHGS"
	snapshotID, digest, err := recorder.Record(
		context.Background(), "estate_01KX7PNHB7DFRJ36HK7G12E6PF",
		reconciliationID, auditResult(), createdAt,
	)
	if err != nil || snapshotID == "" || digest == "" {
		t.Fatalf("snapshot=%q digest=%q err=%v", snapshotID, digest, err)
	}
	path := filepath.Join(recorder.Directory, snapshotID+".json")
	info, err := os.Lstat(path)
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("info=%+v err=%v", info, err)
	}
	snapshot, err := loadSnapshot(path, publicKey)
	if err != nil {
		t.Fatal(err)
	}
	snapshot.EstateID = "estate_01KX7PNHB7DFRJ36HK7G12E6PG"
	if err := Verify(snapshot, publicKey); err == nil {
		t.Fatal("tampered signed metadata was accepted")
	}
	if err := Verify(snapshot, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"); err == nil {
		t.Fatal("untrusted public key was accepted")
	}
}

func TestRecorderPrunesOnlyExpiredVerifiedSnapshots(t *testing.T) {
	recorder, _ := auditRecorder(t)
	current := time.Date(2026, 7, 11, 0, 0, 0, 0, time.UTC)
	oldID, _, err := recorder.Record(
		context.Background(), "estate_01KX7PNHB7DFRJ36HK7G12E6PF",
		"reconciliation_01KX7BV07RHD6KRA4Z4J0KCHGS", auditResult(),
		current.Add(-40*24*time.Hour),
	)
	if err != nil {
		t.Fatal(err)
	}
	currentID, _, err := recorder.Record(
		context.Background(), "estate_01KX7PNHB7DFRJ36HK7G12E6PF",
		"reconciliation_01KX7BV07RHD6KRA4Z4J0KCHGT", auditResult(), current,
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(recorder.Directory, oldID+".json")); !os.IsNotExist(err) {
		t.Fatalf("expired snapshot error=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(recorder.Directory, currentID+".json")); err != nil {
		t.Fatalf("current snapshot error=%v", err)
	}
}

func auditRecorder(t *testing.T) (*Recorder, string) {
	t.Helper()
	publicKey, privateKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	expected := base64.RawURLEncoding.EncodeToString(publicKey)
	return &Recorder{
		Store: auditKeyStore(keyPEM), Reference: "secret:gds/controller/audit-signing-key",
		ExpectedPublicKey: expected, Directory: filepath.Join(t.TempDir(), "audit"),
		RetentionAge: 30 * 24 * time.Hour, Schemas: schemas,
	}, expected
}

func auditResult() reconciler.Result {
	return reconciler.Result{Inventory: estate.CompiledInventory{
		EstateID: "estate_01KX7PNHB7DFRJ36HK7G12E6PF",
		Repositories: []estate.Assignment{{
			ProviderID: 1, Owner: "example-user", Name: "example",
			IdentityState: "unassigned", ManagementMode: "observe-only",
		}},
	}}
}
