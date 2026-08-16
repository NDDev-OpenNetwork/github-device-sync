package approval

import (
	"crypto/ed25519"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"os"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/identity"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/trust"
)

// LoadPrivateKey reads one bounded, non-symlinked PKCS#8 Ed25519 PEM key.
func LoadPrivateKey(path string) (ed25519.PrivateKey, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 2 || info.Size() > 16<<10 {
		return nil, errors.New("approval private key is not a bounded regular file")
	}
	if info.Mode().Perm()&0o077 != 0 {
		return nil, errors.New("approval private key must not be accessible by group or others")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, trailing := pem.Decode(raw)
	if block == nil || block.Type != "PRIVATE KEY" || len(trailing) != 0 {
		return nil, errors.New("approval private key must contain exactly one PKCS#8 PEM block")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("approval private key is not valid PKCS#8")
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok || len(key) != ed25519.PrivateKeySize {
		return nil, errors.New("approval private key is not Ed25519")
	}
	return key, nil
}

func LoadRecord(path string) (Record, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 2 || info.Size() > 64<<10 {
		return Record{}, errors.New("signed approval path is not a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return Record{}, err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	var record Record
	if err := decoder.Decode(&record); err != nil {
		return Record{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return Record{}, errors.New("signed approval contains trailing JSON")
	}
	return record, nil
}

func Sign(record Record, keyID string, privateKey ed25519.PrivateKey) (Record, error) {
	if keyID == "" || len(privateKey) != ed25519.PrivateKeySize {
		return Record{}, errors.New("approval signing identity is invalid")
	}
	raw, err := trust.SigningBytes(SignatureDomain, record.Payload())
	if err != nil {
		return Record{}, err
	}
	record.Signature = trust.Signature{Algorithm: trust.Ed25519, KeyID: keyID,
		Value: base64.RawURLEncoding.EncodeToString(ed25519.Sign(privateKey, raw))}
	return record, nil
}

func NewRecord(planID, planDigest, approvalClass, scopeDigest, actorID, actorType, externalReference string, issuedAt time.Time, ttl time.Duration) (Record, error) {
	if ttl <= 0 || ttl > 24*time.Hour || issuedAt.IsZero() {
		return Record{}, errors.New("approval validity window is invalid")
	}
	approvalID, err := identity.New("approval", issuedAt.UTC(), nil)
	if err != nil {
		return Record{}, err
	}
	return Record{SchemaVersion: 1, ApprovalID: approvalID,
		PlanID: planID, PlanDigest: planDigest, ActorID: actorID, ActorType: actorType,
		IssuedAt: issuedAt.UTC(), ExpiresAt: issuedAt.UTC().Add(ttl), ApprovalClass: approvalClass,
		ScopeDigest: scopeDigest, ExternalReference: externalReference}, nil
}
