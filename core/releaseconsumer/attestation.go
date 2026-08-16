package releaseconsumer

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/bundle"
)

const (
	provenancePredicate = "https://slsa.dev/provenance/v1"
	spdxPredicate       = "https://spdx.dev/Document/v2.3"
	maximumGHOutput     = 16 << 20
	maximumEvidenceFile = 32 << 20
)

type GHAttestationVerifier struct {
	Binary           string
	Timeout          time.Duration
	Identity         bundle.TrustVerifier
	ExecutableDigest string
	identityVerified bool
	executable       []byte
	runner           commandRunner
}

type commandRunner interface {
	Run(context.Context, string, []string, []string) ([]byte, error)
}

type execCommandRunner struct{}

func NewGHAttestationVerifier(identity bundle.TrustVerifier) (*GHAttestationVerifier, error) {
	resolved, err := exec.LookPath("gh")
	if err != nil {
		return nil, errors.New("GitHub CLI with attestation support is unavailable")
	}
	resolved, err = filepath.EvalSymlinks(resolved)
	if err != nil {
		return nil, errors.New("GitHub CLI path cannot be resolved")
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return nil, errors.New("GitHub CLI path cannot be made absolute")
	}
	executable, err := readStableRegularFile(resolved, maximumVerifierExecutable, true)
	if err != nil {
		return nil, errors.New("GitHub CLI digest cannot be verified")
	}
	digest := bytesDigest(executable)
	expectedDigest := ""
	for _, executable := range identity.Executables {
		if executable.OS == runtime.GOOS && executable.Arch == runtime.GOARCH {
			if expectedDigest != "" {
				return nil, errors.New("GitHub CLI trust identity is ambiguous for this platform")
			}
			expectedDigest = executable.Digest
		}
	}
	if identity.Name != "github-cli" || identity.Version == "" || expectedDigest == "" || digest != expectedDigest {
		return nil, errors.New("GitHub CLI does not match the approved verifier identity")
	}
	verificationRoot, err := os.MkdirTemp("", "gds-gh-identity-")
	if err != nil {
		return nil, errors.New("GitHub CLI identity snapshot cannot be created")
	}
	defer os.RemoveAll(verificationRoot)
	snapshot, err := writeVerifierExecutable(verificationRoot, executable)
	if err != nil {
		return nil, errors.New("GitHub CLI identity snapshot cannot be created")
	}
	versionContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	versionOutput, err := (execCommandRunner{}).Run(
		versionContext, snapshot, []string{"version"}, isolatedGHEnvironment(verificationRoot),
	)
	if err != nil || !strings.HasPrefix(string(versionOutput), "gh version "+identity.Version+" ") {
		return nil, errors.New("GitHub CLI version does not match the approved verifier identity")
	}
	return &GHAttestationVerifier{
		Binary: resolved, Timeout: 2 * time.Minute, Identity: identity,
		ExecutableDigest: digest, identityVerified: true, executable: executable, runner: execCommandRunner{},
	}, nil
}

func (verifier *GHAttestationVerifier) Verify(
	ctx context.Context,
	request AttestationRequest,
) (bundle.AttestationEvidence, error) {
	if verifier == nil || verifier.runner == nil || verifier.Binary == "" || !verifier.identityVerified ||
		verifier.Identity.Name != "github-cli" || verifier.Identity.Version == "" ||
		verifier.ExecutableDigest == "" || bytesDigest(verifier.executable) != verifier.ExecutableDigest {
		return bundle.AttestationEvidence{}, errors.New("GitHub attestation verifier is unavailable")
	}
	timeout := verifier.Timeout
	if timeout <= 0 || timeout > 10*time.Minute {
		return bundle.AttestationEvidence{}, errors.New("GitHub attestation verification timeout is invalid")
	}
	paths, err := verifyAttestationInputs(request)
	if err != nil {
		return bundle.AttestationEvidence{}, err
	}
	config, err := os.MkdirTemp("", "gds-gh-attestation-")
	if err != nil {
		return bundle.AttestationEvidence{}, err
	}
	defer os.RemoveAll(config)
	executable, err := writeVerifierExecutable(config, verifier.executable)
	if err != nil {
		return bundle.AttestationEvidence{}, err
	}
	environment := isolatedGHEnvironment(config)
	verificationContext, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	for _, target := range paths.provenanceTargets {
		if err := verifier.verifyTarget(
			verificationContext, executable, target, paths.provenanceBundle, paths.trustedRoot,
			request, provenancePredicate, environment,
		); err != nil {
			return bundle.AttestationEvidence{}, err
		}
	}
	if err := verifier.verifyTarget(
		verificationContext, executable, paths.artifact, paths.sbomBundle, paths.trustedRoot,
		request, spdxPredicate, environment,
	); err != nil {
		return bundle.AttestationEvidence{}, err
	}
	return bundle.AttestationEvidence{
		Verified: true, ArtifactDigest: request.ArtifactDigest,
		SourceOwner: request.SourceOwner, SourceRepository: request.SourceRepository,
		Workflow: request.Workflow, SourceRef: request.SourceRef,
		SourceCommit: request.SourceCommit, SBOMVerified: true, OfflineMaterial: true,
		VerifierName: verifier.Identity.Name, VerifierVersion: verifier.Identity.Version,
		VerifierOS: runtime.GOOS, VerifierArch: runtime.GOARCH,
		VerifierPath: verifier.Binary, VerifierDigest: verifier.ExecutableDigest,
	}, nil
}

type attestationPaths struct {
	artifact          string
	provenanceTargets []string
	provenanceBundle  string
	sbomBundle        string
	trustedRoot       string
}

func verifyAttestationInputs(request AttestationRequest) (attestationPaths, error) {
	if request.ArtifactName == "" || request.ArtifactDigest == "" || request.SourceCommit == "" ||
		request.SourceRef == "" || request.SourceOwner == "" || request.SourceRepository == "" ||
		request.Workflow == "" {
		return attestationPaths{}, errors.New("attestation identity input is incomplete")
	}
	releaseRoot, err := realDirectory(request.ReleaseDirectory)
	if err != nil {
		return attestationPaths{}, err
	}
	evidenceRoot, err := realDirectory(request.EvidenceDirectory)
	if err != nil {
		return attestationPaths{}, err
	}
	artifact := filepath.Join(releaseRoot, request.ArtifactName)
	targets := []string{
		artifact,
		filepath.Join(releaseRoot, "release-envelope.json"),
		filepath.Join(releaseRoot, "manifest.json"),
		filepath.Join(releaseRoot, "sbom.spdx.json"),
		filepath.Join(releaseRoot, "bundle-trust.yaml"),
	}
	result := attestationPaths{
		artifact: artifact, provenanceTargets: targets,
		provenanceBundle: filepath.Join(evidenceRoot, ProvenanceBundleName),
		sbomBundle:       filepath.Join(evidenceRoot, SBOMBundleName),
		trustedRoot:      filepath.Join(evidenceRoot, TrustedRootName),
	}
	for _, path := range append(append([]string(nil), targets...), result.provenanceBundle, result.sbomBundle, result.trustedRoot) {
		maximum := int64(512 << 20)
		if path == result.provenanceBundle || path == result.sbomBundle || path == result.trustedRoot {
			maximum = maximumEvidenceFile
		}
		if err := boundedRegular(path, maximum); err != nil {
			return attestationPaths{}, err
		}
	}
	if digest, err := fileDigest(artifact); err != nil || digest != request.ArtifactDigest {
		return attestationPaths{}, errors.New("attestation artifact digest does not match release metadata")
	}
	return result, nil
}

func (verifier *GHAttestationVerifier) verifyTarget(
	ctx context.Context,
	executable string,
	target string,
	attestationBundle string,
	trustedRoot string,
	request AttestationRequest,
	predicate string,
	environment []string,
) error {
	// What this proves is the signer identity, not the hosting provider: the
	// attestation is bound to this exact repository, this exact reusable-workflow
	// path, this exact source commit and this exact ref, and the bundle is checked
	// against the estate's own trusted root rather than the public one.
	//
	// The runner environment is deliberately not constrained. This estate builds
	// its releases on its own runners, so requiring GitHub-hosted infrastructure
	// rejected the estate's own valid releases while adding nothing to the
	// identity binding above -- an owner-controlled fleet is exactly as
	// authoritative here as an owner-controlled repository secret.
	workflow := "github.com/" + request.SourceOwner + "/" + request.SourceRepository + "/" + request.Workflow
	arguments := []string{
		"attestation", "verify", target,
		"--repo", request.SourceOwner + "/" + request.SourceRepository,
		"--signer-workflow", workflow,
		"--source-digest", request.SourceCommit,
		"--source-ref", request.SourceRef,
		"--predicate-type", predicate,
		"--bundle", attestationBundle,
		"--custom-trusted-root", trustedRoot,
		"--format", "json",
	}
	output, err := verifier.runner.Run(ctx, executable, arguments, environment)
	if err != nil {
		return fmt.Errorf("offline attestation verification failed for %s", filepath.Base(target))
	}
	expectedDigest, err := fileDigest(target)
	if err != nil {
		return err
	}
	if err := validateGHVerification(output, predicate, strings.TrimPrefix(expectedDigest, "sha256:")); err != nil {
		return fmt.Errorf("offline attestation result is invalid for %s: %w", filepath.Base(target), err)
	}
	return nil
}

func writeVerifierExecutable(directory string, content []byte) (string, error) {
	if len(content) == 0 || int64(len(content)) > maximumVerifierExecutable {
		return "", errors.New("GitHub CLI identity snapshot is invalid")
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return "", err
	}
	defer root.Close()
	file, err := root.OpenFile("gh", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return "", err
	}
	written, writeErr := file.Write(content)
	syncErr := file.Sync()
	closeErr := file.Close()
	if writeErr != nil {
		return "", writeErr
	}
	if syncErr != nil {
		return "", syncErr
	}
	if closeErr != nil {
		return "", closeErr
	}
	if written != len(content) {
		return "", io.ErrShortWrite
	}
	path := filepath.Join(directory, "gh")
	raw, err := readStableRegularFile(path, maximumVerifierExecutable, true)
	if err != nil || !bytes.Equal(raw, content) {
		return "", errors.New("GitHub CLI identity snapshot changed while it was created")
	}
	return path, nil
}

type ghVerification struct {
	VerificationResult struct {
		Statement struct {
			PredicateType string `json:"predicateType"`
			Subject       []struct {
				Digest map[string]string `json:"digest"`
			} `json:"subject"`
		} `json:"statement"`
	} `json:"verificationResult"`
}

func validateGHVerification(raw []byte, predicate, expectedDigest string) error {
	var results []ghVerification
	// GitHub CLI may add fields to the documented result. Decode without
	// DisallowUnknownFields while still validating the exact fields we consume.
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&results); err != nil || len(results) == 0 || len(results) > 30 {
		return errors.New("GitHub CLI returned no bounded JSON verification result")
	}
	matched := false
	for _, result := range results {
		if result.VerificationResult.Statement.PredicateType != predicate {
			continue
		}
		for _, subject := range result.VerificationResult.Statement.Subject {
			if subject.Digest["sha256"] == expectedDigest {
				matched = true
			}
		}
	}
	if !matched {
		return errors.New("verified statement does not bind the expected SHA-256 subject")
	}
	return nil
}

func (execCommandRunner) Run(
	ctx context.Context,
	executable string,
	arguments []string,
	environment []string,
) ([]byte, error) {
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Env = environment
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &boundedWriter{writer: &stdout, remaining: maximumGHOutput}
	command.Stderr = &boundedWriter{writer: &stderr, remaining: maximumGHOutput}
	if err := command.Run(); err != nil {
		return nil, err
	}
	return stdout.Bytes(), nil
}

type boundedWriter struct {
	writer    *bytes.Buffer
	remaining int
}

func (writer *boundedWriter) Write(value []byte) (int, error) {
	if len(value) > writer.remaining {
		return 0, errors.New("GitHub CLI output exceeds bound")
	}
	written, err := writer.writer.Write(value)
	writer.remaining -= written
	return written, err
}

func isolatedGHEnvironment(config string) []string {
	result := []string{
		"GH_CONFIG_DIR=" + config,
		"HOME=" + config,
		"GH_PROMPT_DISABLED=1",
		"NO_COLOR=1",
		"PAGER=cat",
	}
	for _, key := range []string{"PATH", "SSL_CERT_DIR", "SSL_CERT_FILE", "TMPDIR"} {
		if value := os.Getenv(key); value != "" {
			result = append(result, key+"="+value)
		}
	}
	return result
}

func realDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("attestation input is not a real directory: %s", path)
	}
	return absolute, nil
}

func boundedRegular(path string, maximum int64) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() < 1 || info.Size() > maximum {
		return fmt.Errorf("attestation input is not a bounded regular file: %s", path)
	}
	return nil
}

func fileDigest(path string) (string, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() < 1 || info.Size() > 512<<20 {
		return "", errors.New("attestation subject is not a bounded regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		return "", err
	}
	return "sha256:" + hex.EncodeToString(digest.Sum(nil)), nil
}
