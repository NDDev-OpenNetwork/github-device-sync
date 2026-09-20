package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/approval"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/identity"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/sessionevidence"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/trust"
)

// SessionEvidenceRecordOptions describes one repo-scoped session evidence
// capture. The artifact is private: it names working-tree paths and a session
// identity, so it is written under the device state root, never into the
// repository.
type SessionEvidenceRecordOptions struct {
	Path           string
	DeviceID       string
	SessionID      string
	HarnessID      string
	HarnessVersion string
	ActorID        string
	KeyID          string
	PrivateKeyPath string
	Output         string
	EvidenceRoot   string
	GDSVersion     string
}

// SessionEvidenceRecordData reports one recorded artifact.
type SessionEvidenceRecordData struct {
	EvidenceID       string `json:"evidence_id"`
	RepositoryID     string `json:"repository_id"`
	Path             string `json:"path"`
	EvidenceDigest   string `json:"evidence_digest"`
	PreviousDigest   string `json:"previous_evidence_digest,omitempty"`
	SubmoduleCount   int    `json:"submodule_count"`
	ChangedPathCount int    `json:"changed_path_count"`
}

var harnessIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// RecordSessionEvidence captures the current repository boundary — head,
// branch, upstream position, change counts and paths, and every Git module
// inside it — into a signed artifact under the device evidence root. It
// records observed state only; it does not claim the named session produced
// that state, and it reads nothing outside the repository boundary.
func (services *Services) RecordSessionEvidence(
	ctx context.Context, options SessionEvidenceRecordOptions,
) domain.Envelope {
	command := "gds evidence record"
	fail := func(code, message string, evidence map[string]any) domain.Envelope {
		return domain.NewEnvelope(command, domain.ExitInput, map[string]any{
			"target": options.Path,
		}, domain.Finding{Code: code, Severity: domain.SeverityHigh, Message: message, Evidence: evidence})
	}
	if options.DeviceID == "" || options.SessionID == "" || options.ActorID == "" ||
		options.KeyID == "" || options.PrivateKeyPath == "" {
		return fail("GDS_INPUT_INVALID",
			"--device-id, --session-id, --actor-id, --key-id and --private-key are required.", nil)
	}
	if !harnessIDPattern.MatchString(options.HarnessID) {
		return fail("GDS_INPUT_INVALID",
			"--harness must be a lowercase harness identifier such as codex, claude-code, cursor or grok.",
			map[string]any{"harness": options.HarnessID})
	}
	info, err := services.Git.RepositoryInfo(ctx, options.Path)
	if err != nil {
		return envelopeForError(command, options.Path, err)
	}
	anchor, anchorFindings := services.Manifests.LoadRepository(info.WorktreeRoot)
	if len(anchorFindings) != 0 {
		return domain.NewEnvelope(command, domain.ExitInput, map[string]any{
			"target": info.WorktreeRoot,
		}, anchorFindings...)
	}
	status, err := services.Git.InspectStatus(ctx, info.WorktreeRoot)
	if err != nil {
		return envelopeForError(command, info.WorktreeRoot, err)
	}
	if status.Head.OID == "" {
		return fail("GDS_SESSION_EVIDENCE_BASELINE_UNAVAILABLE",
			"The repository has no commit to bind as evidence baseline; record after the first commit.",
			map[string]any{"head_mode": status.Head.Mode})
	}
	topology, err := services.Git.InspectTopology(ctx, info.WorktreeRoot)
	if err != nil {
		return envelopeForError(command, info.WorktreeRoot, err)
	}
	changedPaths, err := services.sessionChangedPaths(ctx, info.WorktreeRoot)
	if err != nil {
		return envelopeForError(command, info.WorktreeRoot, err)
	}
	statusDigest, err := canonicaljson.Digest(status)
	if err != nil {
		return envelopeForError(command, info.WorktreeRoot, err)
	}
	submodules := make([]sessionevidence.SubmoduleEvidence, 0, len(topology.Submodules))
	for _, module := range topology.Submodules {
		submodules = append(submodules, sessionevidence.SubmoduleEvidence{
			Path:          module.Path,
			GitlinkOID:    module.GitlinkOID,
			CurrentOID:    module.CurrentOID,
			WorktreeState: module.WorktreeState,
		})
	}
	sort.Slice(submodules, func(i, j int) bool { return submodules[i].Path < submodules[j].Path })
	evidenceRoot, err := sessionEvidenceRoot(options.EvidenceRoot)
	if err != nil {
		return envelopeForError(command, options.Path, err)
	}
	previousDigest, err := latestSessionEvidenceDigest(evidenceRoot, anchor.Repository.ID)
	if err != nil {
		return envelopeForError(command, info.WorktreeRoot, err)
	}
	now := services.Now().UTC()
	evidenceID, err := identity.New("sev", now, nil)
	if err != nil {
		return envelopeForError(command, info.WorktreeRoot, err)
	}
	repositoryName := anchor.Provider.Name
	if repositoryName == "" {
		repositoryName = anchor.Repository.DisplayName
	}
	payload := sessionevidence.Payload{
		SchemaVersion:  sessionevidence.SchemaVersion,
		EvidenceID:     evidenceID,
		RepositoryID:   anchor.Repository.ID,
		RepositoryName: repositoryName,
		DeviceID:       options.DeviceID,
		SessionID:      options.SessionID,
		HarnessID:      options.HarnessID,
		HarnessVersion: options.HarnessVersion,
		ActorID:        options.ActorID,
		ObservedAt:     now,
		GDSVersion:     options.GDSVersion,
		Baseline: sessionevidence.Baseline{
			HeadOID:        status.Head.OID,
			HeadMode:       status.Head.Mode,
			Branch:         status.Branch.Name,
			Upstream:       status.Branch.Upstream,
			UpstreamState:  status.Branch.UpstreamState,
			Ahead:          status.Branch.Ahead,
			Behind:         status.Branch.Behind,
			Diverged:       status.Branch.Diverged,
			Staged:         status.Changes.Staged,
			Unstaged:       status.Changes.Unstaged,
			Untracked:      status.Changes.Untracked,
			Conflicted:     status.Changes.Conflicted,
			Classification: status.Classification,
			ChangedPaths:   changedPaths,
			StatusDigest:   statusDigest,
			Submodules:     submodules,
		},
		PreviousDigest: previousDigest,
	}
	privateKey, err := approval.LoadPrivateKey(options.PrivateKeyPath)
	if err != nil {
		return envelopeForError(command, options.PrivateKeyPath, err)
	}
	artifact, err := sessionevidence.Sign(payload, options.KeyID, privateKey)
	if err != nil {
		return envelopeForError(command, info.WorktreeRoot, err)
	}
	outputPath := options.Output
	if outputPath == "" {
		outputPath = filepath.Join(evidenceRoot, safePathSegment(repositoryName), evidenceID+".json")
	}
	if err := writeSessionEvidenceArtifact(outputPath, artifact); err != nil {
		return envelopeForError(command, outputPath, err)
	}
	return domain.Success(command, SessionEvidenceRecordData{
		EvidenceID:       evidenceID,
		RepositoryID:     anchor.Repository.ID,
		Path:             outputPath,
		EvidenceDigest:   artifact.EvidenceDigest,
		PreviousDigest:   previousDigest,
		SubmoduleCount:   len(submodules),
		ChangedPathCount: len(changedPaths),
	})
}

// VerifySessionEvidence independently verifies one artifact: strict decode,
// schema, canonical digest, Ed25519 signature under the supplied trust policy
// and structural invariants. It does not need the recording session, harness
// or repository to exist.
func (services *Services) VerifySessionEvidence(
	ctx context.Context, filePath string, trustPolicyPath string,
) domain.Envelope {
	command := "gds evidence verify"
	if filePath == "" || trustPolicyPath == "" {
		return domain.NewEnvelope(command, domain.ExitInput, map[string]any{},
			domain.Finding{Code: "GDS_INPUT_INVALID", Severity: domain.SeverityHigh,
				Message: "--file and --trust-policy are required."})
	}
	value, err := serialization.DecodeFile(filePath)
	if err != nil {
		return envelopeForError(command, filePath, err)
	}
	if findings := services.Schemas.Validate(sessionevidence.SchemaName, value, filePath); len(findings) != 0 {
		return domain.NewEnvelope(command, domain.ExitNotProven, map[string]any{"file": filePath}, findings...)
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return envelopeForError(command, filePath, err)
	}
	var artifact sessionevidence.Artifact
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&artifact); err != nil {
		return envelopeForError(command, filePath, fmt.Errorf("decode session evidence: %w", err))
	}
	policy, err := trust.LoadPolicy(trustPolicyPath)
	if err != nil {
		return envelopeForError(command, trustPolicyPath, err)
	}
	verifier, err := sessionevidence.NewVerifier(policy)
	if err != nil {
		return envelopeForError(command, trustPolicyPath, err)
	}
	assessment, err := verifier.Verify(ctx, artifact)
	if err != nil {
		return domain.NewEnvelope(command, domain.ExitNotProven, map[string]any{
			"file":        filePath,
			"evidence_id": artifact.Payload.EvidenceID,
		}, domain.Finding{
			Code:     "GDS_SESSION_EVIDENCE_INVALID",
			Severity: domain.SeverityHigh,
			Message:  err.Error(),
			Evidence: map[string]any{"file": filePath},
		})
	}
	return domain.Success(command, assessment)
}

// sessionChangedPaths lists every path Git reports as changed inside the
// repository, sorted. Paths are recorded verbatim because the artifact never
// leaves the device state root.
func (services *Services) sessionChangedPaths(ctx context.Context, root string) ([]string, error) {
	result, err := services.Git.Run(ctx, root, "status", "--porcelain=v2", "--branch", "-z")
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, 16)
	// Porcelain v2 -z emits NUL-separated fields; every record's path is its
	// last space-separated token, and a rename/copy record trails a second
	// field carrying the source path.
	entries := bytes.Split(result.Stdout, []byte{0})
	for index := 0; index < len(entries); index++ {
		entry := entries[index]
		if len(entry) < 2 {
			continue
		}
		kind := entry[0]
		if kind == '#' {
			continue
		}
		// Fixed field counts before the unquoted path in porcelain v2:
		// ordinary entries carry 8 fields, unmerged 10, rename/copy 9 and
		// untracked 1.
		fields := map[byte]int{'1': 8, 'u': 10, '2': 9, '?': 1}[kind]
		if fields == 0 {
			continue
		}
		path := string(entry)
		for field := 0; field < fields; field++ {
			cut := strings.IndexByte(path, ' ')
			if cut < 0 {
				path = ""
				break
			}
			path = path[cut+1:]
		}
		if kind == '2' {
			index++
		}
		if path != "" {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func sessionEvidenceRoot(configured string) (string, error) {
	if configured != "" {
		return configured, nil
	}
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("locate device state root: %w", err)
		}
		stateHome = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(stateHome, "github-device-sync", "session-evidence"), nil
}

func writeSessionEvidenceArtifact(path string, artifact sessionevidence.Artifact) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create session evidence directory: %w", err)
	}
	raw, err := json.MarshalIndent(artifact, "", "  ")
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".evidence-*.tmp")
	if err != nil {
		return fmt.Errorf("create session evidence temporary file: %w", err)
	}
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		os.Remove(temporary.Name())
		return fmt.Errorf("secure session evidence temporary file: %w", err)
	}
	if _, err := temporary.Write(append(raw, '\n')); err != nil {
		temporary.Close()
		os.Remove(temporary.Name())
		return fmt.Errorf("write session evidence artifact: %w", err)
	}
	if err := temporary.Close(); err != nil {
		os.Remove(temporary.Name())
		return fmt.Errorf("close session evidence artifact: %w", err)
	}
	if err := os.Rename(temporary.Name(), path); err != nil {
		os.Remove(temporary.Name())
		return fmt.Errorf("publish session evidence artifact: %w", err)
	}
	return nil
}

// latestSessionEvidenceDigest links each artifact to the newest previously
// recorded artifact for the same repository on this device, forming a local
// hash chain. A missing or unreadable prior artifact is not an error; a
// malformed one is ignored only when it cannot be decoded at all.
func latestSessionEvidenceDigest(root string, repositoryID string) (string, error) {
	entries, err := filepath.Glob(filepath.Join(root, "*", "*.json"))
	if err != nil {
		return "", err
	}
	var newest sessionevidence.Artifact
	found := false
	for _, path := range entries {
		raw, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var artifact sessionevidence.Artifact
		if err := json.Unmarshal(raw, &artifact); err != nil {
			continue
		}
		if artifact.Payload.RepositoryID != repositoryID {
			continue
		}
		if !found || artifact.Payload.ObservedAt.After(newest.Payload.ObservedAt) {
			newest, found = artifact, true
		}
	}
	if !found {
		return "", nil
	}
	return newest.EvidenceDigest, nil
}

var unsafePathSegment = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

func safePathSegment(name string) string {
	segment := unsafePathSegment.ReplaceAllString(strings.TrimSpace(name), "-")
	if segment == "" || segment == "." || segment == ".." {
		return "repository"
	}
	return segment
}
