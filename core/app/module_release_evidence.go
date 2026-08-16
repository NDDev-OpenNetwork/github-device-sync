package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/estate"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/githubruntime"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/gitops"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/harness"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/harnessevidence"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/trust"
)

// ModuleReleaseEvidence is the signed private QA proof bound into an immutable
// module release plan.
//
// A public provider repository carries no QA of its own: the lanes that decide
// whether a module version is releasable live in the private example-harnesses
// control plane, and their results must not be republished into a public
// repository. The already-signed harness evidence record is therefore the
// authoritative transport, because it already binds the exact module commit
// (ModuleSHA) to a passing, expiring, signed result from a named harness. Reusing
// it keeps one evidence system in the estate instead of two competing ones.
type ModuleReleaseEvidence struct {
	HarnessID         string    `json:"harness_id"`
	HarnessRootSHA    string    `json:"harness_root_sha"`
	ModuleSHA         string    `json:"module_sha"`
	ExecutableVersion string    `json:"executable_version"`
	SuiteVersion      string    `json:"suite_version"`
	SuiteCasesDigest  string    `json:"suite_cases_digest"`
	EvidenceDigest    string    `json:"evidence_digest"`
	GeneratedAt       time.Time `json:"generated_at"`
	ExpiresAt         time.Time `json:"expires_at"`
}

// moduleReleaseHarnessEvidence proves the exact commit being released passed the
// private harness that owns it.
//
// The module bridge decides whether a module needs this proof at all: a module
// with no active mapping (a shared CI or bootstrap module) has no harness and is
// gated by its declared provider checks instead. A module that does have an
// active mapping cannot be released without fresh signed evidence, so a missing
// evidence directory fails closed rather than silently releasing unproven code.
func (services *Services) moduleReleaseHarnessEvidence(
	estateRoot string,
	moduleAnchor domain.RepositoryAnchor,
	commitOID string,
	options ModuleReleaseOptions,
) (*ModuleReleaseEvidence, []domain.Finding) {
	if moduleAnchor.Module == nil {
		return nil, nil
	}
	bridge, _, findings := harness.LoadModuleBridge(estateRoot, services.Schemas)
	if len(findings) != 0 {
		return nil, findings
	}
	moduleID := filepath.Base(moduleAnchor.Provider.Name)
	var harnessID string
	for _, mapping := range bridge.Mappings {
		if mapping.ModuleID == moduleID && mapping.Lifecycle == "active" {
			harnessID = mapping.HarnessID
		}
	}
	if harnessID == "" {
		return nil, nil
	}
	if options.HarnessEvidenceDirectory == "" || options.HarnessEvidenceTrustPolicy == "" {
		return nil, []domain.Finding{moduleReleaseFinding(
			"GDS_MODULE_RELEASE_HARNESS_EVIDENCE_REQUIRED",
			fmt.Sprintf(
				"Module %s is owned by harness %s, so release requires signed harness evidence; "+
					"pass --harness-evidence and --harness-evidence-trust.",
				moduleID, harnessID,
			),
		)}
	}
	record, err := loadModuleReleaseEvidenceRecord(
		options.HarnessEvidenceDirectory, harnessID,
	)
	if err != nil {
		return nil, []domain.Finding{moduleReleaseFinding(
			"GDS_MODULE_RELEASE_HARNESS_EVIDENCE_NOT_PROVEN", err.Error(),
		)}
	}
	policy, err := trust.LoadPolicy(options.HarnessEvidenceTrustPolicy)
	if err != nil {
		return nil, []domain.Finding{moduleReleaseFinding(
			"GDS_MODULE_RELEASE_HARNESS_EVIDENCE_NOT_PROVEN",
			fmt.Sprintf("load harness evidence trust: %v", err),
		)}
	}
	producerCommit, moduleSHAs, err := harnessevidence.AnchoredIdentity(policy)
	if err != nil {
		return nil, []domain.Finding{moduleReleaseFinding(
			"GDS_MODULE_RELEASE_HARNESS_EVIDENCE_NOT_PROVEN", err.Error(),
		)}
	}
	bridgeRaw, err := os.ReadFile(filepath.Join(estateRoot, "harnesses", "module-bridge.yaml"))
	if err != nil {
		return nil, []domain.Finding{moduleReleaseFinding(
			"GDS_MODULE_RELEASE_HARNESS_EVIDENCE_NOT_PROVEN", err.Error(),
		)}
	}
	profileRaw, err := os.ReadFile(filepath.Join(estateRoot, "harnesses", harnessID, "profile.yaml"))
	if err != nil {
		return nil, []domain.Finding{moduleReleaseFinding(
			"GDS_MODULE_RELEASE_HARNESS_EVIDENCE_NOT_PROVEN", err.Error(),
		)}
	}
	// The expectation is pinned to this estate's own bridge and profile, so
	// evidence produced against a different harness contract cannot satisfy it.
	expected := harnessevidence.Expectation{
		HarnessRootSHA: producerCommit,
		ModuleSHAs:     moduleSHAs,
		Now:            services.Now().UTC(),
		ExecutableVersions: map[string]string{
			harnessID: record.Payload.ExecutableVersion,
		},
		ProfileDigests: map[string]string{harnessID: evidenceDigest(profileRaw)},
		BridgeDigests:  map[string]string{harnessID: evidenceDigest(bridgeRaw)},
	}
	verifier := harnessevidence.Verifier{Trust: trust.Verifier{Policy: policy}}
	if err := verifier.Verify(record, expected); err != nil {
		return nil, []domain.Finding{moduleReleaseFinding(
			"GDS_MODULE_RELEASE_HARNESS_EVIDENCE_NOT_PROVEN",
			fmt.Sprintf("verify signed harness evidence for %s: %v", harnessID, err),
		)}
	}
	// The proof must be about this exact commit. Without this the gate would
	// accept a pass recorded for any other revision of the module.
	if record.Payload.HarnessID != harnessID || record.Payload.ModuleSHA != commitOID || moduleSHAs[harnessID] != commitOID {
		return nil, []domain.Finding{moduleReleaseFinding(
			"GDS_MODULE_RELEASE_HARNESS_EVIDENCE_COMMIT_MISMATCH",
			fmt.Sprintf(
				"Signed harness evidence proves module commit %s, not the released commit %s.",
				record.Payload.ModuleSHA, commitOID,
			),
		)}
	}
	return &ModuleReleaseEvidence{
		HarnessID: record.Payload.HarnessID, HarnessRootSHA: record.Payload.HarnessRootSHA,
		ModuleSHA: record.Payload.ModuleSHA, ExecutableVersion: record.Payload.ExecutableVersion,
		SuiteVersion: record.Payload.SuiteVersion, SuiteCasesDigest: record.Payload.SuiteCasesDigest,
		EvidenceDigest: record.EvidenceDigest,
		GeneratedAt:    record.Payload.GeneratedAt.UTC(), ExpiresAt: record.Payload.ExpiresAt.UTC(),
	}, nil
}

// repositoryReleaseObserver binds a read client to one repository so the verify
// handler sees the same narrow contract the mutator offers, without the write
// half. The scope carries only identity; no mutation operation is claimed.
type repositoryReleaseObserver struct {
	client *githubprovider.Client
	scope  githubprovider.RepositoryMutationScope
}

func (observer repositoryReleaseObserver) GetReleaseByTag(
	ctx context.Context, tagName string,
) (githubprovider.Release, error) {
	return observer.client.GetReleaseByTag(ctx, observer.scope.Owner, observer.scope.Name, tagName)
}

func (observer repositoryReleaseObserver) ListReleaseAssets(
	ctx context.Context, releaseID int64,
) ([]githubprovider.ReleaseAsset, error) {
	return observer.client.ListReleaseAssets(ctx, observer.scope.Owner, observer.scope.Name, releaseID)
}

func (observer repositoryReleaseObserver) Scope() githubprovider.RepositoryMutationScope {
	return observer.scope
}

// moduleReleaseObserverBinding builds the read-only provider binding a verify
// needs. It fails closed: without it the command cannot prove the live release,
// and reporting success from recorded evidence alone would be a false negative
// for exactly the drift verification exists to catch.
func (services *Services) moduleReleaseObserverBinding(
	ctx context.Context,
	assessment ModuleReleaseAssessment,
	options ModuleReleaseOptions,
) (gitops.GitHubReleaseObserver, *domain.Envelope) {
	const command = "gds module release verify"
	estateRoot, _, findings := services.policyInputs(ctx, assessment.ModuleRoot)
	if len(findings) != 0 {
		envelope := domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
		return nil, &envelope
	}
	desired, estateFindings := estate.Load(estateRoot, services.Schemas)
	if len(estateFindings) != 0 {
		envelope := domain.NewEnvelope(command, classifyFindings(estateFindings), nil, estateFindings...)
		return nil, &envelope
	}
	config, err := githubruntime.Load(options.RuntimeConfig, desired, services.Schemas)
	if err != nil {
		envelope := githubRuntimeError(command, err)
		return nil, &envelope
	}
	readers, err := githubruntime.BuildReaders(config, desired, services.GitHubRuntimeBuildOptions)
	if err != nil {
		envelope := githubRuntimeError(command, err)
		return nil, &envelope
	}
	client, found := readers[assessment.Installation]
	if !found {
		envelope := githubRuntimeError(command, errors.New("the module installation has no configured GitHub reader"))
		return nil, &envelope
	}
	return repositoryReleaseObserver{client: client, scope: githubprovider.RepositoryMutationScope{
		RepositoryID: assessment.ProviderRepositoryID,
		Owner:        assessment.Owner, Name: assessment.Name,
	}}, nil
}

func loadModuleReleaseEvidenceRecord(
	directory string,
	harnessID string,
) (harnessevidence.Record, error) {
	resolved, err := filepath.Abs(directory)
	if err != nil {
		return harnessevidence.Record{}, errors.New("harness evidence directory is invalid")
	}
	path := filepath.Join(resolved, harnessID+".json")
	info, err := os.Lstat(path)
	if err != nil {
		return harnessevidence.Record{}, fmt.Errorf("inspect harness evidence %s: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 2 || info.Size() > 2<<20 {
		return harnessevidence.Record{}, fmt.Errorf("harness evidence %s is not a bounded regular file", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return harnessevidence.Record{}, err
	}
	defer file.Close()
	var record harnessevidence.Record
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&record); err != nil {
		return harnessevidence.Record{}, fmt.Errorf("decode harness evidence %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return harnessevidence.Record{}, fmt.Errorf("harness evidence %s has trailing JSON", path)
	}
	return record, nil
}

func evidenceDigest(raw []byte) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
}

// decodeModuleReleaseEvidence reads the proof back out of an immutable plan. A
// present but malformed record is rejected rather than treated as absent, so a
// damaged plan cannot degrade into an ungated release.
func decodeModuleReleaseEvidence(raw any) (*ModuleReleaseEvidence, bool) {
	if raw == nil {
		return nil, true
	}
	object, ok := raw.(map[string]any)
	if !ok {
		return nil, false
	}
	text := func(key string) (string, bool) {
		value, present := object[key].(string)
		return value, present && value != ""
	}
	stamp := func(key string) (time.Time, bool) {
		value, present := text(key)
		if !present {
			return time.Time{}, false
		}
		parsed, err := time.Parse(time.RFC3339, value)
		return parsed.UTC(), err == nil
	}
	evidence := ModuleReleaseEvidence{}
	var valid = true
	assign := func(target *string, key string) {
		value, present := text(key)
		valid = valid && present
		*target = value
	}
	assign(&evidence.HarnessID, "harness_id")
	assign(&evidence.HarnessRootSHA, "harness_root_sha")
	assign(&evidence.ModuleSHA, "module_sha")
	assign(&evidence.ExecutableVersion, "executable_version")
	assign(&evidence.SuiteVersion, "suite_version")
	assign(&evidence.SuiteCasesDigest, "suite_cases_digest")
	assign(&evidence.EvidenceDigest, "evidence_digest")
	generated, generatedOK := stamp("generated_at")
	expires, expiresOK := stamp("expires_at")
	evidence.GeneratedAt, evidence.ExpiresAt = generated, expires
	if !valid || !generatedOK || !expiresOK {
		return nil, false
	}
	return &evidence, true
}

// equalModuleReleaseEvidence compares the re-observed proof with the approved
// one. Evidence expires, so re-observation can legitimately produce a fresher
// record; what may never change is which harness proved which commit, and the
// digest of the proof itself.
func equalModuleReleaseEvidence(left, right *ModuleReleaseEvidence) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	return *left == *right
}
