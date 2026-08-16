package releasebuilder

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/bundle"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

const maxTrustedRootBytes = 32 << 20

type TrustedRootVerification struct {
	SchemaVersion int    `json:"schema_version"`
	Status        string `json:"status"`
	Digest        string `json:"digest"`
	TrustDomain   string `json:"trust_domain"`
}

// VerifyTrustedRoot binds offline Sigstore trust material to the independent
// local consumer trust policy before the root is used to verify attestations.
func VerifyTrustedRoot(
	trustedRootPath string,
	trustPolicyPath string,
	schemas *validation.Set,
) (TrustedRootVerification, error) {
	trust, err := bundle.LoadTrustFile(trustPolicyPath, schemas)
	if err != nil {
		return TrustedRootVerification{}, err
	}
	return VerifyTrustedRootDigest(
		trustedRootPath, trust.Verification.TrustedRootDigest, trust.TrustDomain,
	)
}

func VerifyTrustedRootDigest(
	trustedRootPath string,
	expectedDigest string,
	trustDomain string,
) (TrustedRootVerification, error) {
	encodedDigest := strings.TrimPrefix(expectedDigest, "sha256:")
	if len(encodedDigest) != 64 || !strings.HasPrefix(expectedDigest, "sha256:") ||
		strings.TrimSpace(trustDomain) == "" {
		return TrustedRootVerification{}, fmt.Errorf("trusted root expectation is invalid")
	}
	if _, err := hex.DecodeString(encodedDigest); err != nil {
		return TrustedRootVerification{}, fmt.Errorf("trusted root expectation is invalid")
	}
	info, err := os.Lstat(trustedRootPath)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() < 1 || info.Size() > maxTrustedRootBytes {
		return TrustedRootVerification{}, fmt.Errorf("trusted root is not a bounded regular file")
	}
	raw, err := os.ReadFile(trustedRootPath)
	if err != nil {
		return TrustedRootVerification{}, err
	}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
	if digest != expectedDigest {
		return TrustedRootVerification{}, fmt.Errorf("trusted root digest does not match local consumer policy")
	}
	return TrustedRootVerification{
		SchemaVersion: domain.SchemaVersion, Status: "verified",
		Digest: digest, TrustDomain: trustDomain,
	}, nil
}
