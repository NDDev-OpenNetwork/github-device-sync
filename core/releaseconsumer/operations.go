package releaseconsumer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/bundle"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
)

const (
	MaterializeReleaseAction = "materialize-release"
	ActivateReleaseAction    = "activate-release"
	RollbackReleaseAction    = "rollback-release"
	RemoveReleaseAction      = "remove-release"
)

type OperationParameters struct {
	Operation             string                        `json:"operation"`
	InstallRoot           string                        `json:"install_root"`
	ReleaseDirectory      string                        `json:"release_directory"`
	EvidenceDirectory     string                        `json:"evidence_directory"`
	TrustPolicyPath       string                        `json:"trust_policy_path"`
	ConsumerVersion       string                        `json:"consumer_version"`
	ExpectedCurrentTarget string                        `json:"expected_current_target"`
	Record                InstallRecord                 `json:"record"`
	CandidateDigest       string                        `json:"candidate_digest"`
	Files                 []InstallFile                 `json:"files"`
	RollbackAuthorization *bundle.RollbackAuthorization `json:"rollback_authorization,omitempty"`
}

func Parameters(
	candidate InstallCandidate,
	operation string,
	request Request,
	expectedCurrent string,
	authorization *bundle.RollbackAuthorization,
) map[string]any {
	return map[string]any{"release_installation": OperationParameters{
		Operation: operation, InstallRoot: candidate.InstallRoot,
		ReleaseDirectory: request.ReleaseDirectory, EvidenceDirectory: request.EvidenceDirectory,
		TrustPolicyPath: request.TrustPolicyPath, ConsumerVersion: request.ConsumerVersion,
		ExpectedCurrentTarget: expectedCurrent,
		Record:                candidate.Record, CandidateDigest: candidate.Record.CandidateDigest,
		Files: append([]InstallFile(nil), candidate.Files...), RollbackAuthorization: authorization,
	}}
}

func DecodeParameters(step operations.Step) (OperationParameters, error) {
	value, found := step.Parameters["release_installation"]
	if !found || len(step.Parameters) != 1 {
		return OperationParameters{}, errors.New("release installation step parameters are missing")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return OperationParameters{}, err
	}
	var parameters OperationParameters
	if err := serialization.DecodeInto("release-installation-parameters.json", raw, &parameters); err != nil {
		return OperationParameters{}, err
	}
	return parameters, nil
}

func ValidateLifecycle(
	operation string,
	candidate InstallCandidate,
	authorization *bundle.RollbackAuthorization,
	now time.Time,
) (ActiveInstallation, []domain.Finding) {
	active, err := InspectActive(candidate.InstallRoot, candidate.Schemas)
	if err != nil {
		return ActiveInstallation{}, []domain.Finding{finding(
			"GDS_RELEASE_ACTIVE_STATE_INVALID", "Current installation state is invalid.",
		)}
	}
	releaseInfo, releaseErr := os.Lstat(candidate.ReleasePath)
	releaseExists := releaseErr == nil
	if releaseErr != nil && !os.IsNotExist(releaseErr) {
		return active, []domain.Finding{finding(
			"GDS_RELEASE_INSTALL_PATH_INVALID", "Target release path cannot be classified.",
		)}
	}
	if releaseExists && (!releaseInfo.IsDir() || releaseInfo.Mode()&os.ModeSymlink != 0) {
		return active, []domain.Finding{finding(
			"GDS_RELEASE_INSTALL_PATH_INVALID", "Target release path is not a real directory.",
		)}
	}
	target := filepath.ToSlash(filepath.Join(releasesName, candidate.Record.ReleaseKey))
	sameActive := active.Record != nil && active.CurrentTarget == target &&
		active.Record.CandidateDigest == candidate.Record.CandidateDigest
	releaseMatches := releaseExists && candidate.VerifyRelease() == nil
	if active.Record != nil && active.Record.TrustDomain != candidate.Record.TrustDomain {
		return active, []domain.Finding{finding(
			"GDS_RELEASE_TRUST_DOMAIN_MISMATCH", "Active and candidate releases use different trust domains.",
		)}
	}
	switch operation {
	case "install":
		if (active.CurrentTarget != "" && !sameActive) ||
			(releaseExists && !releaseMatches) || authorization != nil {
			return active, []domain.Finding{finding(
				"GDS_RELEASE_INSTALL_PRECONDITION_FAILED", "Initial install requires no competing active or target release.",
			)}
		}
	case "upgrade":
		if !sameActive && (active.Record == nil || authorization != nil ||
			candidate.Record.ReleaseSequence <= active.Record.ReleaseSequence ||
			(releaseExists && !releaseMatches)) {
			return active, []domain.Finding{finding(
				"GDS_RELEASE_UPGRADE_PRECONDITION_FAILED", "Upgrade requires a higher sequence and no conflicting target release.",
			)}
		} else if sameActive && (!releaseMatches || authorization != nil) {
			return active, []domain.Finding{finding(
				"GDS_RELEASE_UPGRADE_PRECONDITION_FAILED", "Upgrade reconciliation requires the exact active release.",
			)}
		}
	case "rollback":
		if active.Record == nil || !releaseExists ||
			candidate.Record.ReleaseSequence >= active.Record.ReleaseSequence ||
			!validRollbackAuthorization(candidate, authorization, now) {
			return active, []domain.Finding{finding(
				"GDS_RELEASE_ROLLBACK_PRECONDITION_FAILED", "Rollback requires an installed lower sequence and exact unexpired authorization.",
			)}
		}
		if err := candidate.VerifyRelease(); err != nil {
			return active, []domain.Finding{finding(
				"GDS_RELEASE_ROLLBACK_TARGET_INVALID", "Rollback target failed installed-release verification.",
			)}
		}
	case "remove":
		target := filepath.ToSlash(filepath.Join(releasesName, candidate.Record.ReleaseKey))
		if active.Record == nil || active.CurrentTarget != target || !releaseExists || authorization != nil {
			return active, []domain.Finding{finding(
				"GDS_RELEASE_REMOVE_PRECONDITION_FAILED", "Remove requires the exact active release and no rollback authorization.",
			)}
		}
		if err := candidate.VerifyRelease(); err != nil {
			return active, []domain.Finding{finding(
				"GDS_RELEASE_REMOVE_TARGET_INVALID", "Removal target failed installed-release verification.",
			)}
		}
	default:
		return active, []domain.Finding{finding(
			"GDS_RELEASE_OPERATION_INVALID", "Release operation must be install, upgrade, rollback, or remove.",
		)}
	}
	return active, nil
}

func InstallScopeDigest(root, trustDomain string) (string, error) {
	_, digest, err := ResolveInstallScope(root, trustDomain)
	return digest, err
}

func ResolveInstallScope(root, trustDomain string) (string, string, error) {
	absolute, err := canonicalInstallRoot(root)
	if err != nil || strings.TrimSpace(trustDomain) == "" {
		return "", "", errors.New("release installation scope is invalid")
	}
	digest, err := canonicaljson.Digest(map[string]any{
		"install_root": absolute,
		"trust_domain": trustDomain,
	})
	if err != nil {
		return "", "", err
	}
	return absolute, digest, nil
}

func validRollbackAuthorization(
	candidate InstallCandidate,
	authorization *bundle.RollbackAuthorization,
	now time.Time,
) bool {
	if authorization == nil || authorization.TargetSequence != candidate.Record.ReleaseSequence ||
		authorization.TargetDigest != candidate.Record.ArtifactDigest ||
		!authorization.ExpiresAt.After(now) || strings.TrimSpace(authorization.RolloutID) == "" ||
		strings.TrimSpace(authorization.Reason) == "" || strings.TrimSpace(authorization.ApprovalRef) == "" {
		return false
	}
	scope, err := InstallScopeDigest(candidate.InstallRoot, candidate.Record.TrustDomain)
	return err == nil && authorization.ScopeDigest == scope
}

type MaterializeHandler struct {
	Candidate InstallCandidate
}

func (handler MaterializeHandler) Apply(
	_ context.Context,
	step operations.Step,
) (operations.ApplyEvidence, error) {
	if err := matchOperationStep(step, handler.Candidate, MaterializeReleaseAction); err != nil {
		return operations.ApplyEvidence{}, err
	}
	before := map[string]any{
		"release_exists": handler.Candidate.VerifyRelease() == nil,
		"release_key":    handler.Candidate.Record.ReleaseKey,
	}
	if err := handler.Candidate.WriteReleaseNew(); err != nil {
		return operations.ApplyEvidence{Before: before}, err
	}
	return operations.ApplyEvidence{
		Before: before,
		After:  map[string]any{"release_exists": true, "release_key": handler.Candidate.Record.ReleaseKey},
	}, nil
}

func (handler MaterializeHandler) Verify(
	_ context.Context,
	step operations.Step,
	_ json.RawMessage,
) error {
	if err := matchOperationStep(step, handler.Candidate, MaterializeReleaseAction); err != nil {
		return err
	}
	return handler.Candidate.VerifyRelease()
}

type AcceptanceStore interface {
	BundleAcceptanceState(context.Context, string) (bundle.AcceptanceState, error)
	PutAcceptedBundle(context.Context, state.AcceptedBundle, *bundle.RollbackAuthorization, time.Time) error
}

type ActivationHandler struct {
	Candidate       InstallCandidate
	ExpectedCurrent string
	Operation       string
	Store           AcceptanceStore
	Authorization   *bundle.RollbackAuthorization
	Now             func() time.Time
}

func (handler ActivationHandler) Apply(
	ctx context.Context,
	step operations.Step,
) (operations.ApplyEvidence, error) {
	action := ActivateReleaseAction
	if handler.Operation == "rollback" {
		action = RollbackReleaseAction
	}
	if err := matchOperationStep(step, handler.Candidate, action); err != nil {
		return operations.ApplyEvidence{}, err
	}
	if handler.Store == nil || handler.Now == nil {
		return operations.ApplyEvidence{}, errors.New("release acceptance dependencies are unavailable")
	}
	var before ActiveInstallation
	var after ActiveInstallation
	err := withInstallScopeLock(ctx, handler.Candidate, func() error {
		var inspectErr error
		before, inspectErr = InspectActive(handler.Candidate.InstallRoot, handler.Candidate.Schemas)
		if inspectErr != nil {
			return fmt.Errorf("%w: inspect activation precondition: %w", ErrActivationIntegrity, inspectErr)
		}
		target := filepath.ToSlash(filepath.Join(releasesName, handler.Candidate.Record.ReleaseKey))
		newlyActivated := before.CurrentTarget != target
		if err := activateWhileLocked(handler.Candidate, handler.ExpectedCurrent); err != nil {
			return err
		}
		if err := handler.recordAcceptance(ctx); err != nil {
			acceptanceErr := fmt.Errorf("%w: %w", ErrActivationAcceptance, err)
			if newlyActivated {
				if recoveryErr := restoreActiveWhileLocked(
					handler.Candidate, before, target,
				); recoveryErr != nil {
					return errors.Join(
						acceptanceErr,
						fmt.Errorf("%w: %w", ErrActivationRecovery, recoveryErr),
					)
				}
			}
			return acceptanceErr
		}
		after, inspectErr = InspectActive(handler.Candidate.InstallRoot, handler.Candidate.Schemas)
		if inspectErr != nil {
			return fmt.Errorf("%w: inspect activation result: %w", ErrActivationIntegrity, inspectErr)
		}
		return nil
	})
	if err != nil {
		return operations.ApplyEvidence{Before: before}, err
	}
	return operations.ApplyEvidence{Before: before, After: after}, nil
}

func (handler ActivationHandler) Verify(
	ctx context.Context,
	step operations.Step,
	_ json.RawMessage,
) error {
	action := ActivateReleaseAction
	if handler.Operation == "rollback" {
		action = RollbackReleaseAction
	}
	if err := matchOperationStep(step, handler.Candidate, action); err != nil {
		return err
	}
	if err := handler.Candidate.VerifyRelease(); err != nil {
		return err
	}
	active, err := InspectActive(handler.Candidate.InstallRoot, handler.Candidate.Schemas)
	target := filepath.ToSlash(filepath.Join(releasesName, handler.Candidate.Record.ReleaseKey))
	if err != nil || active.Record == nil || active.CurrentTarget != target ||
		active.Record.CandidateDigest != handler.Candidate.Record.CandidateDigest {
		return errors.New("release activation result differs from candidate")
	}
	acceptance, err := handler.Store.BundleAcceptanceState(ctx, handler.Candidate.Record.TrustDomain)
	if err != nil || acceptance.AcceptedDigests[handler.Candidate.Record.ReleaseSequence] !=
		handler.Candidate.Record.ArtifactDigest {
		return errors.New("release acceptance ledger does not bind the active release")
	}
	return nil
}

func (handler ActivationHandler) recordAcceptance(ctx context.Context) error {
	acceptance, err := handler.Store.BundleAcceptanceState(ctx, handler.Candidate.Record.TrustDomain)
	if err != nil {
		return err
	}
	if existing := acceptance.AcceptedDigests[handler.Candidate.Record.ReleaseSequence]; existing != "" {
		if existing != handler.Candidate.Record.ArtifactDigest {
			return state.ErrBundleConflict
		}
		return nil
	}
	now := handler.Now().UTC()
	err = handler.Store.PutAcceptedBundle(ctx, state.AcceptedBundle{
		TrustDomain:               handler.Candidate.Record.TrustDomain,
		ReleaseSequence:           handler.Candidate.Record.ReleaseSequence,
		BundleVersion:             handler.Candidate.Record.BundleVersion,
		ArtifactDigest:            handler.Candidate.Record.ArtifactDigest,
		ManifestDigest:            handler.Candidate.Record.ManifestDigest,
		AttestationIdentityDigest: handler.Candidate.Record.AttestationIdentityDigest,
		AcceptedAt:                now,
	}, handler.Authorization, now)
	if err == nil {
		return nil
	}
	observed, observeErr := handler.Store.BundleAcceptanceState(
		ctx, handler.Candidate.Record.TrustDomain,
	)
	if observeErr == nil && observed.AcceptedDigests[handler.Candidate.Record.ReleaseSequence] ==
		handler.Candidate.Record.ArtifactDigest {
		return nil
	}
	if observeErr != nil {
		return errors.Join(err, observeErr)
	}
	return err
}

type RemoveHandler struct {
	Candidate       InstallCandidate
	ExpectedCurrent string
}

func (handler RemoveHandler) Apply(
	ctx context.Context,
	step operations.Step,
) (operations.ApplyEvidence, error) {
	if err := matchOperationStep(step, handler.Candidate, RemoveReleaseAction); err != nil {
		return operations.ApplyEvidence{}, err
	}
	var before ActiveInstallation
	var after ActiveInstallation
	err := withInstallScopeLock(ctx, handler.Candidate, func() error {
		var inspectErr error
		before, inspectErr = InspectActive(handler.Candidate.InstallRoot, handler.Candidate.Schemas)
		if inspectErr != nil {
			return inspectErr
		}
		if err := removeActiveWhileLocked(handler.Candidate, handler.ExpectedCurrent); err != nil {
			return err
		}
		after, inspectErr = InspectActive(handler.Candidate.InstallRoot, handler.Candidate.Schemas)
		return inspectErr
	})
	if err != nil {
		return operations.ApplyEvidence{Before: before}, err
	}
	return operations.ApplyEvidence{Before: before, After: after}, nil
}

func (handler RemoveHandler) Verify(
	_ context.Context,
	step operations.Step,
	_ json.RawMessage,
) error {
	if err := matchOperationStep(step, handler.Candidate, RemoveReleaseAction); err != nil {
		return err
	}
	active, err := InspectActive(handler.Candidate.InstallRoot, handler.Candidate.Schemas)
	if err != nil || active.CurrentTarget != "" || active.Record != nil {
		return errors.New("release removal left an active installation")
	}
	if _, err := os.Lstat(handler.Candidate.ReleasePath); !os.IsNotExist(err) {
		return errors.New("release removal target still exists")
	}
	return nil
}

func matchOperationStep(step operations.Step, candidate InstallCandidate, action string) error {
	if step.Action != action {
		return errors.New("release handler action does not match step")
	}
	parameters, err := DecodeParameters(step)
	if err != nil {
		return err
	}
	if parameters.InstallRoot != candidate.InstallRoot || parameters.Record != candidate.Record ||
		parameters.CandidateDigest != candidate.Record.CandidateDigest ||
		len(parameters.Files) != len(candidate.Files) {
		return errors.New("release handler candidate does not match plan")
	}
	for index := range parameters.Files {
		if parameters.Files[index].Target != candidate.Files[index].Target ||
			parameters.Files[index].Digest != candidate.Files[index].Digest ||
			parameters.Files[index].Size != candidate.Files[index].Size {
			return fmt.Errorf("release handler file %d does not match plan", index)
		}
	}
	return nil
}
