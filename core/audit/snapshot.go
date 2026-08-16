// Package audit creates private cryptographically signed reconciliation
// snapshots without persisting signing keys or secret material.
package audit

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/identity"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/reconciler"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/secrets"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

const maxSnapshotBytes = int64(serialization.MaxInputBytes)

var snapshotNamePattern = regexp.MustCompile(`^audit_[0-7][0-9A-HJKMNP-TV-Z]{25}\.json$`)

type Payload struct {
	ReconciliationID string            `json:"reconciliation_id"`
	Result           reconciler.Result `json:"result"`
}

type Signature struct {
	Algorithm string `json:"algorithm"`
	PublicKey string `json:"public_key"`
	Value     string `json:"value"`
}

type Snapshot struct {
	SchemaVersion    int       `json:"schema_version"`
	SnapshotID       string    `json:"snapshot_id"`
	EstateID         string    `json:"estate_id"`
	ReconciliationID string    `json:"reconciliation_id"`
	CreatedAt        time.Time `json:"created_at"`
	PayloadDigest    string    `json:"payload_digest"`
	Payload          Payload   `json:"payload"`
	Signature        Signature `json:"signature"`
}

type Recorder struct {
	Store             secrets.Store
	Reference         string
	ExpectedPublicKey string
	Directory         string
	RetentionAge      time.Duration
	Schemas           *validation.Set
}

func (recorder *Recorder) Record(
	ctx context.Context,
	estateID string,
	reconciliationID string,
	result reconciler.Result,
	createdAt time.Time,
) (string, string, error) {
	if err := recorder.Prepare(); err != nil {
		return "", "", err
	}
	snapshotID, err := identity.New("audit", createdAt.UTC(), nil)
	if err != nil {
		return "", "", err
	}
	keyMaterial, err := recorder.Store.Get(ctx, recorder.Reference)
	if err != nil {
		return "", "", fmt.Errorf("audit signing key is unavailable")
	}
	privateKey, err := parsePrivateKey(keyMaterial)
	clear(keyMaterial)
	if err != nil {
		return "", "", fmt.Errorf("audit signing key is invalid")
	}
	publicKey := append(ed25519.PublicKey(nil), privateKey.Public().(ed25519.PublicKey)...)
	payload := Payload{ReconciliationID: reconciliationID, Result: result}
	payloadBytes, err := json.Marshal(payload)
	if err != nil {
		clear(privateKey)
		return "", "", fmt.Errorf("encode audit payload: %w", err)
	}
	payloadDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(payloadBytes))
	snapshot := Snapshot{
		SchemaVersion: domain.SchemaVersion, SnapshotID: snapshotID, EstateID: estateID,
		ReconciliationID: reconciliationID, CreatedAt: createdAt.UTC(),
		PayloadDigest: payloadDigest, Payload: payload,
		Signature: Signature{
			Algorithm: "ed25519",
			PublicKey: base64.RawURLEncoding.EncodeToString(publicKey),
		},
	}
	if snapshot.Signature.PublicKey != recorder.ExpectedPublicKey {
		clear(privateKey)
		return "", "", fmt.Errorf("audit signing key does not match the pinned public key")
	}
	signingBytes, err := signingPayload(snapshot)
	if err != nil {
		clear(privateKey)
		return "", "", err
	}
	snapshot.Signature.Value = base64.RawURLEncoding.EncodeToString(
		ed25519.Sign(privateKey, signingBytes),
	)
	clear(privateKey)
	if err := Verify(snapshot, recorder.ExpectedPublicKey); err != nil {
		return "", "", err
	}
	raw, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return "", "", fmt.Errorf("encode audit snapshot: %w", err)
	}
	raw = append(raw, '\n')
	value, err := serialization.Decode(snapshotID+".json", raw)
	if err != nil {
		return "", "", err
	}
	if findings := recorder.Schemas.Validate("audit-snapshot", value, snapshotID); len(findings) != 0 {
		return "", "", fmt.Errorf("audit snapshot schema failed with %s", findings[0].Code)
	}
	path := filepath.Join(recorder.Directory, snapshotID+".json")
	if err := recorder.prune(createdAt.UTC()); err != nil {
		return "", "", err
	}
	if err := writeExclusive(path, raw); err != nil {
		return "", "", err
	}
	fileDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	return snapshotID, fileDigest, nil
}

func Verify(snapshot Snapshot, expectedPublicKey string) error {
	if snapshot.SchemaVersion != domain.SchemaVersion || snapshot.Signature.Algorithm != "ed25519" ||
		snapshot.ReconciliationID != snapshot.Payload.ReconciliationID {
		return fmt.Errorf("audit snapshot identity contract is invalid")
	}
	payload, err := json.Marshal(snapshot.Payload)
	if err != nil {
		return err
	}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(payload))
	if digest != snapshot.PayloadDigest {
		return fmt.Errorf("audit snapshot payload digest mismatch")
	}
	publicKey, err := base64.RawURLEncoding.DecodeString(snapshot.Signature.PublicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize ||
		snapshot.Signature.PublicKey != expectedPublicKey {
		return fmt.Errorf("audit public key is invalid")
	}
	signature, err := base64.RawURLEncoding.DecodeString(snapshot.Signature.Value)
	signingBytes, signingErr := signingPayload(snapshot)
	if err != nil || len(signature) != ed25519.SignatureSize ||
		signingErr != nil || !ed25519.Verify(ed25519.PublicKey(publicKey), signingBytes, signature) {
		return fmt.Errorf("audit snapshot signature is invalid")
	}
	return nil
}

func signingPayload(snapshot Snapshot) ([]byte, error) {
	return json.Marshal(struct {
		SchemaVersion    int       `json:"schema_version"`
		SnapshotID       string    `json:"snapshot_id"`
		EstateID         string    `json:"estate_id"`
		ReconciliationID string    `json:"reconciliation_id"`
		CreatedAt        time.Time `json:"created_at"`
		PayloadDigest    string    `json:"payload_digest"`
		Payload          Payload   `json:"payload"`
		Algorithm        string    `json:"algorithm"`
		PublicKey        string    `json:"public_key"`
	}{
		SchemaVersion: snapshot.SchemaVersion, SnapshotID: snapshot.SnapshotID,
		EstateID: snapshot.EstateID, ReconciliationID: snapshot.ReconciliationID,
		CreatedAt: snapshot.CreatedAt, PayloadDigest: snapshot.PayloadDigest,
		Payload: snapshot.Payload, Algorithm: snapshot.Signature.Algorithm,
		PublicKey: snapshot.Signature.PublicKey,
	})
}

func parsePrivateKey(value []byte) (ed25519.PrivateKey, error) {
	block, rest := pem.Decode(value)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("audit key PEM is invalid")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok || len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("audit key is not Ed25519")
	}
	return append(ed25519.PrivateKey(nil), key...), nil
}

func (recorder *Recorder) Prepare() error {
	if recorder == nil || recorder.Store == nil || recorder.Reference == "" ||
		recorder.ExpectedPublicKey == "" || recorder.Schemas == nil ||
		!filepath.IsAbs(recorder.Directory) ||
		recorder.RetentionAge < 30*24*time.Hour || recorder.RetentionAge > 3650*24*time.Hour {
		return fmt.Errorf("audit recorder configuration is invalid")
	}
	if err := os.MkdirAll(recorder.Directory, 0o700); err != nil {
		return fmt.Errorf("create audit directory: %w", err)
	}
	info, err := os.Lstat(recorder.Directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("audit directory must be a private real directory")
	}
	return nil
}

func (recorder *Recorder) prune(now time.Time) error {
	entries, err := os.ReadDir(recorder.Directory)
	if err != nil {
		return err
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if snapshotNamePattern.MatchString(entry.Name()) {
			paths = append(paths, filepath.Join(recorder.Directory, entry.Name()))
		}
	}
	sort.Strings(paths)
	remove := make([]string, 0)
	for _, path := range paths {
		snapshot, err := loadSnapshot(path, recorder.ExpectedPublicKey)
		if err != nil {
			return err
		}
		if snapshot.CreatedAt.Before(now.Add(-recorder.RetentionAge)) {
			remove = append(remove, path)
		}
	}
	for _, path := range remove {
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove expired audit snapshot: %w", err)
		}
	}
	return syncDirectory(recorder.Directory)
}

func loadSnapshot(path string, expectedPublicKey string) (Snapshot, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Mode().Perm()&0o077 != 0 || info.Size() < 1 || info.Size() > maxSnapshotBytes {
		return Snapshot{}, fmt.Errorf("audit snapshot file contract is invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		return Snapshot{}, err
	}
	raw, readErr := io.ReadAll(io.LimitReader(file, maxSnapshotBytes+1))
	closeErr := file.Close()
	if readErr != nil || closeErr != nil || int64(len(raw)) > maxSnapshotBytes {
		return Snapshot{}, fmt.Errorf("read audit snapshot")
	}
	var snapshot Snapshot
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&snapshot); err != nil {
		return Snapshot{}, fmt.Errorf("decode audit snapshot: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Snapshot{}, fmt.Errorf("audit snapshot contains trailing JSON")
	}
	if err := Verify(snapshot, expectedPublicKey); err != nil {
		return Snapshot{}, err
	}
	return snapshot, nil
}

func writeExclusive(path string, raw []byte) error {
	if _, err := os.Lstat(path); err == nil || !os.IsNotExist(err) {
		return fmt.Errorf("audit snapshot target already exists")
	}
	temporary := filepath.Join(filepath.Dir(path), ".tmp-"+filepath.Base(path))
	cleanupOrphanTemporaries(filepath.Dir(path))
	file, err := os.OpenFile(temporary, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create audit snapshot temporary file: %w", err)
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporary)
		}
	}()
	if _, err := file.Write(raw); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Link(temporary, path); err != nil {
		return fmt.Errorf("publish audit snapshot without overwrite: %w", err)
	}
	if err := os.Remove(temporary); err != nil {
		_ = os.Remove(path)
		return err
	}
	removeTemporary = false
	if err := syncDirectory(filepath.Dir(path)); err != nil {
		_ = os.Remove(path)
		_ = syncDirectory(filepath.Dir(path))
		return err
	}
	return nil
}

func cleanupOrphanTemporaries(directory string) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasPrefix(entry.Name(), ".tmp-") {
			_ = os.Remove(filepath.Join(directory, entry.Name()))
		}
	}
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil || closeErr != nil {
		return fmt.Errorf("sync audit directory")
	}
	return nil
}
