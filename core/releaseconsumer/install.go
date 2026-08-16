package releaseconsumer

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/bundle"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/releasebuilder"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

const (
	installRecordName = "install-record.json"
	currentLinkName   = "current"
	releasesName      = "releases"
)

type InstallRecord struct {
	SchemaVersion             int    `json:"schema_version"`
	TrustDomain               string `json:"trust_domain"`
	ReleaseKey                string `json:"release_key"`
	BundleVersion             string `json:"bundle_version"`
	ReleaseSequence           int    `json:"release_sequence"`
	Channel                   string `json:"channel"`
	ArtifactDigest            string `json:"artifact_digest"`
	ManifestDigest            string `json:"manifest_digest"`
	AttestationIdentityDigest string `json:"attestation_identity_digest"`
	SourceCommit              string `json:"source_commit"`
	SourceRef                 string `json:"source_ref"`
	TargetOS                  string `json:"target_os"`
	TargetArch                string `json:"target_arch"`
	CandidateDigest           string `json:"candidate_digest"`
}

type InstallFile struct {
	Target     string `json:"target"`
	Digest     string `json:"digest"`
	Size       int    `json:"size"`
	SourcePath string `json:"-"`
	content    []byte
}

type InstallCandidate struct {
	InstallRoot string                       `json:"install_root"`
	ReleasePath string                       `json:"release_path"`
	Record      InstallRecord                `json:"record"`
	Files       []InstallFile                `json:"files"`
	Payload     bundle.InstallationCandidate `json:"payload"`
	Schemas     *validation.Set              `json:"-"`
}

type ActiveInstallation struct {
	RootExists    bool           `json:"root_exists"`
	CurrentTarget string         `json:"current_target,omitempty"`
	Record        *InstallRecord `json:"record,omitempty"`
}

func BuildInstallCandidate(
	verified VerifiedRelease,
	request Request,
	installRoot string,
	schemas *validation.Set,
) (InstallCandidate, []domain.Finding) {
	defer verified.Close()
	if schemas == nil || verified.Status != "verified" || verified.Policy.Status != "accepted" {
		return InstallCandidate{}, []domain.Finding{finding(
			"GDS_RELEASE_INSTALL_INPUT_INVALID", "Only a fully accepted release can become an installation candidate.",
		)}
	}
	request = verified.installationRequest(request)
	absoluteRoot, err := canonicalInstallRoot(installRoot)
	if err != nil {
		return InstallCandidate{}, []domain.Finding{finding(
			"GDS_RELEASE_INSTALL_ROOT_INVALID", "Installation root is invalid.",
		)}
	}
	artifactPath := filepath.Join(request.ReleaseDirectory, verified.Directory.ArtifactName)
	if err := boundedRegular(artifactPath, 512<<20); err != nil {
		return InstallCandidate{}, []domain.Finding{finding(
			"GDS_RELEASE_ARTIFACT_UNAVAILABLE", "Verified release artifact is unavailable.",
		)}
	}
	artifact, err := os.ReadFile(artifactPath)
	if err != nil {
		return InstallCandidate{}, []domain.Finding{finding(
			"GDS_RELEASE_ARTIFACT_UNAVAILABLE", "Verified release artifact cannot be read.",
		)}
	}
	payload, payloadFindings := bundle.PrepareInstallation(artifact, verified.Envelope, schemas)
	if len(payloadFindings) != 0 {
		return InstallCandidate{}, payloadFindings
	}
	releaseKey := fmt.Sprintf(
		"%010d--%s--%s", verified.Envelope.ReleaseSequence,
		verified.Envelope.BundleVersion,
		strings.TrimPrefix(verified.Envelope.ArtifactDigest, "sha256:")[:16],
	)
	record := InstallRecord{
		SchemaVersion: domain.SchemaVersion, TrustDomain: verified.Trust.TrustDomain,
		ReleaseKey: releaseKey, BundleVersion: verified.Envelope.BundleVersion,
		ReleaseSequence: verified.Envelope.ReleaseSequence, Channel: verified.Envelope.Channel,
		ArtifactDigest:            verified.Envelope.ArtifactDigest,
		ManifestDigest:            verified.Envelope.ManifestDigest,
		AttestationIdentityDigest: verified.Envelope.ExpectedAttestationIdentityDigest,
		SourceCommit:              verified.Envelope.SourceCommit, SourceRef: verified.Envelope.SourceRef,
		TargetOS: request.TargetOS, TargetArch: request.TargetArch,
	}
	files, err := installSourceFiles(
		request, verified.Directory.Files, verified.EvidenceFiles, verified.TrustPolicyDigest,
	)
	if err != nil {
		return InstallCandidate{}, []domain.Finding{finding(
			"GDS_RELEASE_INSTALL_SOURCE_INVALID", "Release or offline evidence changed after verification.",
		)}
	}
	record.CandidateDigest, err = installCandidateDigest(record, files, payload.Files)
	if err != nil {
		return InstallCandidate{}, []domain.Finding{finding(
			"GDS_RELEASE_INSTALL_DIGEST_FAILED", "Installation candidate digest could not be derived.",
		)}
	}
	if findings := validateInstallRecord(record, schemas); len(findings) != 0 {
		return InstallCandidate{}, findings
	}
	return InstallCandidate{
		InstallRoot: absoluteRoot,
		ReleasePath: filepath.Join(absoluteRoot, releasesName, releaseKey),
		Record:      record, Files: files, Payload: payload, Schemas: schemas,
	}, nil
}

func installSourceFiles(
	request Request,
	releaseFiles []releasebuilder.OutputFile,
	evidenceFiles []releasebuilder.OutputFile,
	trustPolicyDigest string,
) ([]InstallFile, error) {
	files := make([]InstallFile, 0, len(releaseFiles)+4)
	for _, file := range releaseFiles {
		files = append(files, InstallFile{
			Target: filepath.ToSlash(filepath.Join("release", file.Path)),
			Digest: file.Digest, Size: file.Size,
			SourcePath: filepath.Join(request.ReleaseDirectory, file.Path),
		})
	}
	if len(evidenceFiles) != 3 {
		return nil, errors.New("verified offline evidence set is incomplete")
	}
	for index, name := range []string{ProvenanceBundleName, SBOMBundleName, TrustedRootName} {
		expected := evidenceFiles[index]
		if expected.Path != name || expected.Size < 1 || expected.Digest == "" {
			return nil, errors.New("verified offline evidence identity is invalid")
		}
		path := filepath.Join(request.EvidenceDirectory, name)
		if err := boundedRegular(path, int64(expected.Size)); err != nil {
			return nil, err
		}
		digest, err := fileDigest(path)
		if err != nil {
			return nil, err
		}
		if digest != expected.Digest {
			return nil, errors.New("offline evidence digest changed")
		}
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if int(info.Size()) != expected.Size {
			return nil, errors.New("offline evidence size changed")
		}
		files = append(files, InstallFile{
			Target: filepath.ToSlash(filepath.Join("evidence", name)),
			Digest: expected.Digest, Size: expected.Size, SourcePath: path,
		})
	}
	if err := boundedRegular(request.TrustPolicyPath, 1<<20); err != nil {
		return nil, err
	}
	trustDigest, err := fileDigest(request.TrustPolicyPath)
	if err != nil {
		return nil, err
	}
	if trustDigest != trustPolicyDigest {
		return nil, errors.New("local trust policy digest changed")
	}
	trustInfo, err := os.Lstat(request.TrustPolicyPath)
	if err != nil {
		return nil, err
	}
	files = append(files, InstallFile{
		Target: "consumer-trust.yaml", Digest: trustDigest,
		Size: int(trustInfo.Size()), SourcePath: request.TrustPolicyPath,
	})
	for index := range files {
		if err := boundedRegular(files[index].SourcePath, int64(files[index].Size)); err != nil {
			return nil, err
		}
		raw, err := os.ReadFile(files[index].SourcePath)
		if err != nil || len(raw) != files[index].Size || bytesDigest(raw) != files[index].Digest {
			return nil, errors.New("installation source digest changed")
		}
		files[index].content = raw
	}
	sort.Slice(files, func(left, right int) bool { return files[left].Target < files[right].Target })
	return files, nil
}

func installCandidateDigest(
	record InstallRecord,
	files []InstallFile,
	payload []bundle.FileRecord,
) (string, error) {
	record.CandidateDigest = ""
	type fileIdentity struct {
		Target string `json:"target"`
		Digest string `json:"digest"`
		Size   int    `json:"size"`
	}
	identities := make([]fileIdentity, len(files))
	for index, file := range files {
		identities[index] = fileIdentity{Target: file.Target, Digest: file.Digest, Size: file.Size}
	}
	raw, err := json.Marshal(struct {
		Record  InstallRecord       `json:"record"`
		Files   []fileIdentity      `json:"files"`
		Payload []bundle.FileRecord `json:"payload"`
	}{Record: record, Files: identities, Payload: payload})
	if err != nil {
		return "", err
	}
	return bytesDigest(raw), nil
}

func (candidate InstallCandidate) WriteReleaseNew() (returnErr error) {
	if findings := validateInstallRecord(candidate.Record, candidate.Schemas); len(findings) != 0 {
		return errors.New("installation record is invalid")
	}
	if err := ensureInstallRoot(candidate.InstallRoot); err != nil {
		return err
	}
	if info, err := os.Lstat(candidate.ReleasePath); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || candidate.VerifyRelease() != nil {
			return errors.New("release installation path already exists with different content")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return errors.New("release installation path cannot be inspected")
	}
	releasesRoot := filepath.Join(candidate.InstallRoot, releasesName)
	temporary, err := os.MkdirTemp(releasesRoot, ".release.tmp-")
	if err != nil {
		return err
	}
	defer func() {
		if returnErr != nil {
			_ = os.RemoveAll(temporary)
		}
	}()
	if err := candidate.Payload.WriteNew(filepath.Join(temporary, "payload")); err != nil {
		return err
	}
	root, err := os.OpenRoot(temporary)
	if err != nil {
		return err
	}
	for _, file := range candidate.Files {
		local := filepath.FromSlash(file.Target)
		if err := root.MkdirAll(filepath.Dir(local), 0o755); err != nil {
			_ = root.Close()
			return err
		}
		output, err := root.OpenFile(local, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			_ = root.Close()
			return err
		}
		if _, err := output.Write(file.content); err != nil {
			_ = output.Close()
			_ = root.Close()
			return err
		}
		if err := output.Chmod(0o644); err != nil {
			_ = output.Close()
			_ = root.Close()
			return err
		}
		if err := output.Sync(); err != nil {
			_ = output.Close()
			_ = root.Close()
			return err
		}
		if err := output.Close(); err != nil {
			_ = root.Close()
			return err
		}
	}
	recordRaw, err := json.MarshalIndent(candidate.Record, "", "  ")
	if err != nil {
		_ = root.Close()
		return err
	}
	recordRaw = append(recordRaw, '\n')
	recordFile, err := root.OpenFile(installRecordName, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = root.Close()
		return err
	}
	if _, err := recordFile.Write(recordRaw); err != nil {
		_ = recordFile.Close()
		_ = root.Close()
		return err
	}
	if err := recordFile.Chmod(0o644); err != nil {
		_ = recordFile.Close()
		_ = root.Close()
		return err
	}
	if err := recordFile.Sync(); err != nil {
		_ = recordFile.Close()
		_ = root.Close()
		return err
	}
	if err := recordFile.Close(); err != nil {
		_ = root.Close()
		return err
	}
	if err := root.Close(); err != nil {
		return err
	}
	if err := candidate.verifyReleaseAt(temporary); err != nil {
		return err
	}
	if err := syncDirectory(temporary); err != nil {
		return err
	}
	if err := os.Rename(temporary, candidate.ReleasePath); err != nil {
		return err
	}
	return syncDirectory(releasesRoot)
}

func (candidate InstallCandidate) VerifyRelease() error {
	return candidate.verifyReleaseAt(candidate.ReleasePath)
}

func (candidate InstallCandidate) verifyReleaseAt(path string) error {
	info, err := os.Lstat(path)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("installed release root is invalid")
	}
	record, err := LoadInstallRecord(filepath.Join(path, installRecordName), candidate.Schemas)
	if err != nil || record != candidate.Record {
		return errors.New("installed release record differs from candidate")
	}
	if err := candidate.Payload.Verify(filepath.Join(path, "payload")); err != nil {
		return err
	}
	expected := map[string]InstallFile{}
	for _, file := range candidate.Files {
		expected[file.Target] = file
	}
	seen := map[string]struct{}{}
	err = filepath.WalkDir(path, func(current string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if current == path {
			return nil
		}
		relative, err := filepath.Rel(path, current)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.Type()&os.ModeSymlink != 0 {
			return errors.New("installed release contains a symlink")
		}
		if entry.IsDir() {
			if relative == "payload" {
				return filepath.SkipDir
			}
			if relative != "release" && relative != "evidence" {
				return errors.New("installed release contains undeclared directory")
			}
			return nil
		}
		if relative == installRecordName {
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o644 {
				return errors.New("installed release record metadata differs")
			}
			seen[relative] = struct{}{}
			return nil
		}
		file, found := expected[relative]
		if !found {
			return errors.New("installed release contains undeclared file")
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o644 ||
			info.Size() != int64(file.Size) {
			return errors.New("installed release file metadata differs")
		}
		raw, err := os.ReadFile(current)
		if err != nil || bytesDigest(raw) != file.Digest {
			return errors.New("installed release file digest differs")
		}
		seen[relative] = struct{}{}
		return nil
	})
	if err != nil {
		return err
	}
	if len(seen) != len(expected)+1 {
		return errors.New("installed release file set is incomplete")
	}
	return nil
}

func ensureInstallRoot(root string) error {
	absolute, err := filepath.Abs(root)
	if err != nil || absolute == string(filepath.Separator) {
		return errors.New("installation root is invalid")
	}
	info, err := os.Lstat(absolute)
	if os.IsNotExist(err) {
		parent := filepath.Dir(absolute)
		parentInfo, parentErr := os.Lstat(parent)
		if parentErr != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
			return errors.New("installation root parent is invalid")
		}
		if err := os.Mkdir(absolute, 0o755); err != nil {
			return err
		}
		info, err = os.Lstat(absolute)
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("installation root is not a real directory")
	}
	releases := filepath.Join(absolute, releasesName)
	releasesInfo, releasesErr := os.Lstat(releases)
	if os.IsNotExist(releasesErr) {
		if err := os.Mkdir(releases, 0o755); err != nil {
			return err
		}
		releasesInfo, releasesErr = os.Lstat(releases)
	}
	if releasesErr != nil || !releasesInfo.IsDir() || releasesInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("installation releases path is invalid")
	}
	return nil
}

func InspectActive(root string, schemas *validation.Set) (ActiveInstallation, error) {
	absolute, err := canonicalInstallRoot(root)
	if err != nil {
		return ActiveInstallation{}, err
	}
	info, err := os.Lstat(absolute)
	if os.IsNotExist(err) {
		return ActiveInstallation{}, nil
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ActiveInstallation{}, errors.New("installation root is invalid")
	}
	state := ActiveInstallation{RootExists: true}
	releases := filepath.Join(absolute, releasesName)
	releasesInfo, releasesErr := os.Lstat(releases)
	if releasesErr != nil || !releasesInfo.IsDir() || releasesInfo.Mode()&os.ModeSymlink != 0 {
		return ActiveInstallation{}, errors.New("installation releases root is invalid")
	}
	link := filepath.Join(absolute, currentLinkName)
	linkInfo, err := os.Lstat(link)
	if os.IsNotExist(err) {
		return state, nil
	}
	if err != nil || linkInfo.Mode()&os.ModeSymlink == 0 {
		return ActiveInstallation{}, errors.New("installation current pointer is not a symlink")
	}
	target, err := os.Readlink(link)
	if err != nil || !validCurrentTarget(target) {
		return ActiveInstallation{}, errors.New("installation current pointer is unsafe")
	}
	releasePath := filepath.Join(absolute, filepath.FromSlash(target))
	releaseInfo, releaseErr := os.Lstat(releasePath)
	if releaseErr != nil || !releaseInfo.IsDir() || releaseInfo.Mode()&os.ModeSymlink != 0 {
		return ActiveInstallation{}, errors.New("installation current release root is invalid")
	}
	record, err := LoadInstallRecord(filepath.Join(releasePath, installRecordName), schemas)
	if err != nil || target != filepath.ToSlash(filepath.Join(releasesName, record.ReleaseKey)) {
		return ActiveInstallation{}, errors.New("installation current record is invalid")
	}
	state.CurrentTarget = target
	state.Record = &record
	return state, nil
}

func Activate(candidate InstallCandidate, expectedCurrent string) error {
	if err := candidate.VerifyRelease(); err != nil {
		return fmt.Errorf("%w: candidate verification: %w", ErrActivationIntegrity, err)
	}
	return withInstallScopeLock(context.Background(), candidate, func() error {
		return activateWhileLocked(candidate, expectedCurrent)
	})
}

func activateWhileLocked(candidate InstallCandidate, expectedCurrent string) error {
	return activateWhileLockedWithPostRenameHook(candidate, expectedCurrent, nil)
}

func activateWhileLockedWithPostRenameHook(
	candidate InstallCandidate,
	expectedCurrent string,
	postRenameHook func(),
) error {
	if err := candidate.VerifyRelease(); err != nil {
		return fmt.Errorf("%w: candidate verification: %w", ErrActivationIntegrity, err)
	}
	before, err := InspectActive(candidate.InstallRoot, candidate.Schemas)
	if err != nil {
		return fmt.Errorf("%w: inspect current installation: %w", ErrActivationIntegrity, err)
	}
	target := filepath.ToSlash(filepath.Join(releasesName, candidate.Record.ReleaseKey))
	if before.CurrentTarget == target {
		if before.Record == nil || before.Record.CandidateDigest != candidate.Record.CandidateDigest ||
			before.Record.TrustDomain != candidate.Record.TrustDomain {
			return fmt.Errorf("%w: active release differs from candidate", ErrActivationIntegrity)
		}
		return nil
	}
	if before.CurrentTarget != expectedCurrent {
		return fmt.Errorf("%w: current pointer changed before activation", ErrActivationConflict)
	}
	if before.Record != nil && before.Record.TrustDomain != candidate.Record.TrustDomain {
		return fmt.Errorf("%w: active trust domain differs from candidate", ErrActivationConflict)
	}
	temporary, err := newTemporaryLink(candidate.InstallRoot, target)
	if err != nil {
		return fmt.Errorf("%w: create temporary current pointer: %w", ErrActivationIntegrity, err)
	}
	defer os.Remove(temporary)
	before, err = InspectActive(candidate.InstallRoot, candidate.Schemas)
	if err != nil || before.CurrentTarget != expectedCurrent {
		return fmt.Errorf("%w: current pointer changed during activation", ErrActivationConflict)
	}
	if before.Record != nil && before.Record.TrustDomain != candidate.Record.TrustDomain {
		return fmt.Errorf("%w: active trust domain changed during activation", ErrActivationConflict)
	}
	if err := candidate.VerifyRelease(); err != nil {
		return fmt.Errorf("%w: candidate changed during activation", ErrActivationIntegrity)
	}
	if err := os.Rename(temporary, filepath.Join(candidate.InstallRoot, currentLinkName)); err != nil {
		return fmt.Errorf("%w: replace current pointer: %w", ErrActivationIntegrity, err)
	}
	if err := syncDirectory(candidate.InstallRoot); err != nil {
		activationErr := fmt.Errorf("%w: sync activated current pointer: %w", ErrActivationIntegrity, err)
		if recoveryErr := restoreActiveWhileLocked(candidate, before, target); recoveryErr != nil {
			return errors.Join(
				activationErr,
				fmt.Errorf("%w: restore previous current pointer: %w", ErrActivationRecovery, recoveryErr),
			)
		}
		return activationErr
	}
	if postRenameHook != nil {
		postRenameHook()
	}
	if err := candidate.VerifyRelease(); err != nil {
		activationErr := fmt.Errorf("%w: activated candidate changed before completion: %w", ErrActivationIntegrity, err)
		if recoveryErr := restoreActiveWhileLocked(candidate, before, target); recoveryErr != nil {
			return errors.Join(
				activationErr,
				fmt.Errorf("%w: restore previous current pointer: %w", ErrActivationRecovery, recoveryErr),
			)
		}
		return activationErr
	}
	return nil
}

func RemoveActive(candidate InstallCandidate, expectedCurrent string) error {
	return withInstallScopeLock(context.Background(), candidate, func() error {
		return removeActiveWhileLocked(candidate, expectedCurrent)
	})
}

func removeActiveWhileLocked(candidate InstallCandidate, expectedCurrent string) error {
	state, err := InspectActive(candidate.InstallRoot, candidate.Schemas)
	if err != nil || state.CurrentTarget != expectedCurrent || state.Record == nil ||
		state.Record.CandidateDigest != candidate.Record.CandidateDigest {
		return errors.New("active installation changed before removal")
	}
	if err := candidate.VerifyRelease(); err != nil {
		return err
	}
	state, err = InspectActive(candidate.InstallRoot, candidate.Schemas)
	if err != nil || state.CurrentTarget != expectedCurrent || state.Record == nil ||
		state.Record.CandidateDigest != candidate.Record.CandidateDigest {
		return errors.New("active installation changed during removal")
	}
	if err := os.Remove(filepath.Join(candidate.InstallRoot, currentLinkName)); err != nil {
		return err
	}
	if err := syncDirectory(candidate.InstallRoot); err != nil {
		return err
	}
	info, err := os.Lstat(candidate.ReleasePath)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("release removal target is invalid")
	}
	releasesRoot, err := os.OpenRoot(filepath.Join(candidate.InstallRoot, releasesName))
	if err != nil {
		return err
	}
	if err := releasesRoot.RemoveAll(candidate.Record.ReleaseKey); err != nil {
		_ = releasesRoot.Close()
		return err
	}
	if err := releasesRoot.Close(); err != nil {
		return err
	}
	return syncDirectory(filepath.Join(candidate.InstallRoot, releasesName))
}

func restoreActiveWhileLocked(
	candidate InstallCandidate,
	previous ActiveInstallation,
	activatedTarget string,
) error {
	current, err := InspectActive(candidate.InstallRoot, candidate.Schemas)
	if err != nil || current.CurrentTarget != activatedTarget || current.Record == nil ||
		current.Record.CandidateDigest != candidate.Record.CandidateDigest {
		return errors.New("activated current pointer changed before recovery")
	}
	currentPath := filepath.Join(candidate.InstallRoot, currentLinkName)
	if previous.CurrentTarget == "" {
		if err := os.Remove(currentPath); err != nil {
			return err
		}
		return syncDirectory(candidate.InstallRoot)
	}
	if !validCurrentTarget(previous.CurrentTarget) || previous.Record == nil {
		return errors.New("previous current pointer is unsafe")
	}
	previousPath := filepath.Join(candidate.InstallRoot, filepath.FromSlash(previous.CurrentTarget))
	previousInfo, err := os.Lstat(previousPath)
	if err != nil || !previousInfo.IsDir() || previousInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("previous current release root is invalid")
	}
	previousRecord, err := LoadInstallRecord(filepath.Join(previousPath, installRecordName), candidate.Schemas)
	if err != nil || previous.CurrentTarget != filepath.ToSlash(filepath.Join(releasesName, previousRecord.ReleaseKey)) ||
		previousRecord.CandidateDigest != previous.Record.CandidateDigest ||
		previousRecord.TrustDomain != candidate.Record.TrustDomain {
		return errors.New("previous current release record is invalid")
	}
	temporary, err := newTemporaryLink(candidate.InstallRoot, previous.CurrentTarget)
	if err != nil {
		return err
	}
	defer os.Remove(temporary)
	if err := os.Rename(temporary, currentPath); err != nil {
		return err
	}
	return syncDirectory(candidate.InstallRoot)
}

func LoadInstallRecord(path string, schemas *validation.Set) (InstallRecord, error) {
	if schemas == nil {
		return InstallRecord{}, errors.New("installation schema set is unavailable")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() < 1 || info.Size() > 1<<20 {
		return InstallRecord{}, errors.New("installation record is not a bounded regular file")
	}
	value, err := serialization.DecodeFile(path)
	if err != nil {
		return InstallRecord{}, err
	}
	if findings := schemas.Validate("release-installation", value, path); len(findings) != 0 {
		return InstallRecord{}, errors.New("installation record failed schema validation")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return InstallRecord{}, err
	}
	var record InstallRecord
	if err := serialization.DecodeInto(path, raw, &record); err != nil {
		return InstallRecord{}, err
	}
	return record, nil
}

func validateInstallRecord(record InstallRecord, schemas *validation.Set) []domain.Finding {
	raw, err := json.Marshal(record)
	if err != nil {
		return []domain.Finding{finding("GDS_RELEASE_INSTALL_RECORD_INVALID", "Installation record cannot be encoded.")}
	}
	value, err := serialization.Decode("release-installation.json", raw)
	if err != nil {
		return []domain.Finding{finding("GDS_RELEASE_INSTALL_RECORD_INVALID", "Installation record cannot be decoded.")}
	}
	return schemas.Validate("release-installation", value, "in-memory-release-installation")
}

func validCurrentTarget(target string) bool {
	if target == "" || filepath.IsAbs(target) || strings.Contains(target, "\\") {
		return false
	}
	parts := strings.Split(target, "/")
	return len(parts) == 2 && parts[0] == releasesName && parts[1] != "" &&
		filepath.ToSlash(filepath.Clean(filepath.FromSlash(target))) == target
}

func canonicalInstallRoot(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil || absolute == string(filepath.Separator) || len(absolute) > 4096 {
		return "", errors.New("installation root is invalid")
	}
	info, err := os.Lstat(absolute)
	switch {
	case err == nil:
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return "", errors.New("installation root is not a real directory")
		}
		resolved, resolveErr := filepath.EvalSymlinks(absolute)
		if resolveErr != nil || resolved == string(filepath.Separator) {
			return "", errors.New("installation root cannot be resolved")
		}
		return filepath.Clean(resolved), nil
	case !os.IsNotExist(err):
		return "", errors.New("installation root cannot be inspected")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return "", errors.New("installation root parent cannot be resolved")
	}
	parentInfo, err := os.Lstat(parent)
	if err != nil || !parentInfo.IsDir() || parentInfo.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("installation root parent is not a real directory")
	}
	base := filepath.Base(absolute)
	if base == "" || base == "." || base == ".." {
		return "", errors.New("installation root basename is invalid")
	}
	return filepath.Join(parent, base), nil
}

func newTemporaryLink(root, target string) (string, error) {
	for attempt := 0; attempt < 8; attempt++ {
		random := make([]byte, 8)
		if _, err := rand.Read(random); err != nil {
			return "", err
		}
		path := filepath.Join(root, ".current.tmp-"+hex.EncodeToString(random))
		if err := os.Symlink(target, path); err == nil {
			return path, nil
		} else if !os.IsExist(err) {
			return "", err
		}
	}
	return "", errors.New("cannot allocate temporary current pointer")
}

func bytesDigest(raw []byte) string {
	digest := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func syncDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
