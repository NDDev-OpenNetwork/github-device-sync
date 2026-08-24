package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/bundle"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/compiler"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/identity"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/projections"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/releaseconsumer"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

const releaseOperationPlanLifetime = 15 * time.Minute

type ReleaseOperationOptions struct {
	ProjectionOperationOptions
	ReleaseDirectory          string
	EvidenceDirectory         string
	TrustPolicyPath           string
	InstallRoot               string
	TargetReleaseKey          string
	RollbackAuthorizationPath string
	ConsumerVersion           string
	TargetOS                  string
	TargetArch                string
}

type ReleaseVerificationData struct {
	StatePath    string                          `json:"state_path"`
	Verification releaseconsumer.VerifiedRelease `json:"verification"`
}

type ReleasePlanData struct {
	Plan      operations.Plan                    `json:"plan"`
	StatePath string                             `json:"state_path"`
	Active    releaseconsumer.ActiveInstallation `json:"active"`
	Candidate releaseconsumer.InstallCandidate   `json:"candidate"`
}

type releaseOperationContext struct {
	repositoryID  string
	root          string
	request       releaseconsumer.Request
	verification  releaseconsumer.VerifiedRelease
	candidate     releaseconsumer.InstallCandidate
	active        releaseconsumer.ActiveInstallation
	authorization *bundle.RollbackAuthorization
	observation   operations.Observation
}

type releaseObserver struct {
	services  *Services
	path      string
	operation string
	options   ReleaseOperationOptions
}

func (observer releaseObserver) Observe(
	ctx context.Context,
	repositoryID string,
) (operations.Observation, error) {
	current, findings := observer.services.releaseOperationContext(
		ctx, observer.path, observer.operation, observer.options,
	)
	if len(findings) != 0 {
		return operations.Observation{}, fmt.Errorf("release precondition returned %d findings", len(findings))
	}
	if current.repositoryID != repositoryID {
		return operations.Observation{}, errors.New("release operation repository identity changed")
	}
	return current.observation, nil
}

func (services *Services) VerifyReleaseEvidence(
	ctx context.Context,
	options ReleaseOperationOptions,
) domain.Envelope {
	command := "gds release verify"
	var pathFinding *domain.Finding
	options, pathFinding = normalizeReleaseOperationPaths(options)
	if pathFinding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *pathFinding)
	}
	if finding := validateReleaseOperationOptions("verify", options); finding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *finding)
	}
	statePath, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *stateFinding)
	}
	defer store.Close()
	trust, err := bundle.LoadTrustFile(options.TrustPolicyPath, services.Schemas)
	if err != nil {
		return domain.NewEnvelope(command, domain.ExitValidation, nil, domain.Finding{
			Code: "GDS_RELEASE_TRUST_POLICY_INVALID", Severity: domain.SeverityHigh,
			Message: "Local consumer trust policy is invalid.",
		})
	}
	acceptance, err := store.BundleAcceptanceState(ctx, trust.TrustDomain)
	if err != nil {
		return envelopeForError(command, statePath, err)
	}
	authorization, authFindings := loadRollbackAuthorization(options.RollbackAuthorizationPath, services.Schemas)
	if len(authFindings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(authFindings), nil, authFindings...)
	}
	if authorization != nil {
		if strings.TrimSpace(options.InstallRoot) == "" {
			return domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{
				Code: "GDS_RELEASE_ROLLBACK_SCOPE_REQUIRED", Severity: domain.SeverityHigh,
				Message: "Rollback verification requires the exact installation root.",
			})
		}
		scope, scopeErr := releaseconsumer.InstallScopeDigest(options.InstallRoot, trust.TrustDomain)
		if scopeErr != nil || authorization.ScopeDigest != scope {
			return domain.NewEnvelope(command, domain.ExitSecurity, nil, domain.Finding{
				Code: "GDS_RELEASE_ROLLBACK_SCOPE_MISMATCH", Severity: domain.SeverityHigh,
				Message: "Rollback authorization does not bind the requested installation scope.",
			})
		}
	}
	verifier, err := services.releaseVerifier(trust.Verification.Verifier)
	if err != nil {
		return domain.NewEnvelope(command, domain.ExitNotProven, nil, domain.Finding{
			Code: "GDS_RELEASE_ATTESTATION_TOOL_UNAVAILABLE", Severity: domain.SeverityHigh,
			Message: "GitHub CLI attestation verifier is unavailable.",
		})
	}
	request := releaseRequest(options)
	verified, findings := verifier.Verify(ctx, request, acceptance, authorization, services.Now().UTC())
	defer verified.Close()
	if len(findings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(findings), ReleaseVerificationData{
			StatePath: statePath, Verification: verified,
		}, findings...)
	}
	return domain.Success(command, ReleaseVerificationData{StatePath: statePath, Verification: verified})
}

func (services *Services) PlanReleaseOperation(
	ctx context.Context,
	path string,
	operation string,
	options ReleaseOperationOptions,
) domain.Envelope {
	command := "gds release " + operation + " plan"
	var pathFinding *domain.Finding
	options, pathFinding = normalizeReleaseOperationPaths(options)
	if pathFinding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *pathFinding)
	}
	if finding := validateReleaseOperationOptions(operation, options); finding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *finding)
	}
	current, findings := services.releaseOperationContext(ctx, path, operation, options)
	if len(findings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
	}
	statePath, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *stateFinding)
	}
	defer store.Close()
	now := services.Now().UTC()
	planID, err := identity.New("plan", now, nil)
	if err != nil {
		return domain.InternalError(command, err)
	}
	expectedCurrent := current.active.CurrentTarget
	parameters := releaseconsumer.Parameters(
		current.candidate, operation, current.request, expectedCurrent, current.authorization,
	)
	steps := releaseSteps(current.repositoryID, operation, parameters)
	plan, err := operations.NewPlan(
		planID, now, now.Add(releaseOperationPlanLifetime), operations.PlanInput{
			Operation: "release-" + operation,
			Actor:     operations.Actor{Type: "agent-session", SessionID: options.SessionID},
			Preconditions: []operations.Precondition{{
				RepositoryID:        current.observation.RepositoryID,
				HeadOID:             current.observation.HeadOID,
				WorktreeFingerprint: current.observation.WorktreeFingerprint,
				ManifestDigest:      current.observation.ManifestDigest,
				PolicyDigest:        current.observation.PolicyDigest,
			}},
			Steps: steps, ApprovalClass: "local-release-installation",
		},
	)
	if err != nil {
		return domain.InternalError(command, err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		releaseObserver{services: services, path: path, operation: operation, options: options},
		nil, options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	if err := engine.PutPlan(ctx, plan); err != nil {
		return operationFailureEnvelope(command, err)
	}
	envelope := domain.Success(command, ReleasePlanData{
		Plan: plan, StatePath: statePath, Active: current.active, Candidate: current.candidate,
	})
	envelope.Scope["repository_id"] = current.repositoryID
	envelope.Scope["release_key"] = current.candidate.Record.ReleaseKey
	return envelope
}

func (services *Services) ApplyReleaseOperation(
	ctx context.Context,
	path string,
	operation string,
	planID string,
	options ReleaseOperationOptions,
) domain.Envelope {
	command := "gds release " + operation + " apply"
	if strings.TrimSpace(planID) == "" {
		return domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{
			Code: "GDS_PLAN_ID_REQUIRED", Severity: domain.SeverityHigh,
			Message: "--apply requires an exact plan id.",
		})
	}
	var pathFinding *domain.Finding
	options, pathFinding = normalizeReleaseOperationPaths(options)
	if pathFinding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *pathFinding)
	}
	if finding := validateReleaseOperationOptions(operation, options); finding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *finding)
	}
	current, findings := services.releaseOperationContext(ctx, path, operation, options)
	if len(findings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
	}
	if operation == "rollback" && (current.authorization == nil ||
		options.ApprovalReference != current.authorization.ApprovalRef) {
		return domain.NewEnvelope(command, domain.ExitApproval, nil, domain.Finding{
			Code: "GDS_RELEASE_ROLLBACK_APPROVAL_MISMATCH", Severity: domain.SeverityHigh,
			Message: "Apply approval must exactly match the rollback authorization.",
		})
	}
	_, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *stateFinding)
	}
	defer store.Close()
	handlers := releaseHandlers(operation, current, store, services.Now)
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		releaseObserver{services: services, path: path, operation: operation, options: options},
		handlers, options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	result, err := engine.Apply(ctx, planID, options.ApprovalReference)
	if err != nil {
		envelope := operationFailureEnvelope(command, err)
		envelope.Data = result
		envelope.Mutation.Attempted = result.MutationAttempted
		envelope.Mutation.Completed = result.MutationCompleted
		envelope.OperationID = result.OperationID
		return envelope
	}
	envelope := domain.Success(command, result)
	envelope.Mutation.Attempted = result.MutationAttempted
	envelope.Mutation.Completed = result.MutationCompleted
	envelope.OperationID = result.OperationID
	envelope.Scope["repository_id"] = current.repositoryID
	envelope.Scope["release_key"] = current.candidate.Record.ReleaseKey
	return envelope
}

func (services *Services) VerifyReleaseOperation(
	ctx context.Context,
	path string,
	operation string,
	operationID string,
	options ReleaseOperationOptions,
) domain.Envelope {
	command := "gds release " + operation + " verify"
	if strings.TrimSpace(operationID) == "" {
		return domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{
			Code: "GDS_OPERATION_ID_REQUIRED", Severity: domain.SeverityHigh,
			Message: "--verify requires an exact operation id.",
		})
	}
	if finding := validateStoredReleaseOperationOptions(operation, options); finding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *finding)
	}
	_, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope(command, domain.ExitInput, nil, *stateFinding)
	}
	defer store.Close()
	record, err := store.GetOperation(ctx, operationID)
	if err != nil {
		return envelopeForError(command, operationID, err)
	}
	planRecord, err := store.GetPlan(ctx, record.PlanID)
	if err != nil {
		return envelopeForError(command, record.PlanID, err)
	}
	plan, err := operations.DecodePlan(planRecord.Body)
	if err != nil || plan.Operation != "release-"+operation || len(plan.Steps) == 0 ||
		len(plan.Preconditions) != 1 {
		return domain.NewEnvelope(command, domain.ExitConflict, nil, domain.Finding{
			Code: "GDS_RELEASE_VERIFY_OPERATION_MISMATCH", Severity: domain.SeverityHigh,
			Message: "Stored operation does not match the requested release lifecycle.",
		})
	}
	parameters, err := releaseconsumer.DecodeParameters(plan.Steps[0])
	if err != nil {
		return envelopeForError(command, operationID, err)
	}
	var current releaseOperationContext
	if operation == "remove" {
		candidate := releaseconsumer.InstallCandidate{
			InstallRoot: parameters.InstallRoot,
			ReleasePath: filepath.Join(parameters.InstallRoot, "releases", parameters.Record.ReleaseKey),
			Record:      parameters.Record, Files: parameters.Files, Schemas: services.Schemas,
		}
		current = releaseOperationContext{candidate: candidate, active: releaseconsumer.ActiveInstallation{}}
	} else {
		var findings []domain.Finding
		current, findings = services.releaseCandidateFromParameters(
			ctx, parameters, options, store, record.StartedAt,
		)
		if len(findings) != 0 {
			return domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
		}
		if current.candidate.Record.CandidateDigest != parameters.CandidateDigest {
			return domain.NewEnvelope(command, domain.ExitStale, nil, domain.Finding{
				Code: "GDS_RELEASE_VERIFY_CANDIDATE_STALE", Severity: domain.SeverityHigh,
				Message: "Current verified release differs from the stored operation.",
			})
		}
	}
	handlers := releaseHandlers(operation, current, store, services.Now)
	checker := releaseVerifyChecker{observation: operations.Observation{
		RepositoryID:        plan.Preconditions[0].RepositoryID,
		HeadOID:             plan.Preconditions[0].HeadOID,
		WorktreeFingerprint: plan.Preconditions[0].WorktreeFingerprint,
		ManifestDigest:      plan.Preconditions[0].ManifestDigest,
		PolicyDigest:        plan.Preconditions[0].PolicyDigest,
	}}
	engine := operations.NewDefaultEngine(
		store, services.Schemas, checker, handlers, options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	result, err := engine.Verify(ctx, operationID)
	if err != nil {
		envelope := operationFailureEnvelope(command, err)
		envelope.OperationID = operationID
		return envelope
	}
	envelope := domain.Success(command, result)
	envelope.OperationID = operationID
	return envelope
}

type releaseVerifyChecker struct {
	observation operations.Observation
}

func (checker releaseVerifyChecker) Observe(context.Context, string) (operations.Observation, error) {
	return checker.observation, nil
}

func (services *Services) releaseCandidateFromParameters(
	ctx context.Context,
	parameters releaseconsumer.OperationParameters,
	options ReleaseOperationOptions,
	store *state.Store,
	verificationTime time.Time,
) (releaseOperationContext, []domain.Finding) {
	installedRoot := filepath.Join(
		parameters.InstallRoot, "releases", parameters.Record.ReleaseKey,
	)
	request := releaseconsumer.Request{
		ReleaseDirectory:  filepath.Join(installedRoot, "release"),
		EvidenceDirectory: filepath.Join(installedRoot, "evidence"),
		TrustPolicyPath:   filepath.Join(installedRoot, "consumer-trust.yaml"),
		ConsumerVersion:   parameters.ConsumerVersion,
		TargetOS:          parameters.Record.TargetOS,
		TargetArch:        parameters.Record.TargetArch,
	}
	trust, err := bundle.LoadTrustFile(request.TrustPolicyPath, services.Schemas)
	if err != nil {
		return releaseOperationContext{}, []domain.Finding{dependencyFinding(request.TrustPolicyPath, err)}
	}
	acceptance, err := store.BundleAcceptanceState(ctx, trust.TrustDomain)
	if err != nil {
		return releaseOperationContext{}, []domain.Finding{dependencyFinding(options.StatePath, err)}
	}
	verifier, err := services.releaseVerifier(trust.Verification.Verifier)
	if err != nil {
		return releaseOperationContext{}, []domain.Finding{{
			Code: "GDS_RELEASE_ATTESTATION_TOOL_UNAVAILABLE", Severity: domain.SeverityHigh,
			Message: "GitHub CLI attestation verifier is unavailable.",
		}}
	}
	verified, findings := verifier.Verify(
		ctx, request, acceptance, parameters.RollbackAuthorization, verificationTime.UTC(),
	)
	defer verified.Close()
	if len(findings) != 0 {
		return releaseOperationContext{}, findings
	}
	candidate, findings := releaseconsumer.BuildInstallCandidate(
		verified, request, parameters.InstallRoot, services.Schemas,
	)
	if len(findings) != 0 {
		return releaseOperationContext{}, findings
	}
	if candidate.Record != parameters.Record || candidate.Record.CandidateDigest != parameters.CandidateDigest {
		return releaseOperationContext{}, []domain.Finding{{
			Code: "GDS_RELEASE_STORED_CANDIDATE_MISMATCH", Severity: domain.SeverityHigh,
			Message: "Stored release plan differs from current verified evidence.",
		}}
	}
	active, err := releaseconsumer.InspectActive(parameters.InstallRoot, services.Schemas)
	if err != nil {
		return releaseOperationContext{}, []domain.Finding{dependencyFinding(parameters.InstallRoot, err)}
	}
	return releaseOperationContext{
		request: request, verification: verified, candidate: candidate, active: active,
		authorization: parameters.RollbackAuthorization,
	}, nil
}

func (services *Services) releaseOperationContext(
	ctx context.Context,
	path string,
	operation string,
	options ReleaseOperationOptions,
) (releaseOperationContext, []domain.Finding) {
	root, anchor, findings := services.policyInputs(ctx, path)
	if len(findings) != 0 {
		return releaseOperationContext{}, findings
	}
	if !hasRole(anchor.Repository.Roles, "control-plane") {
		return releaseOperationContext{}, []domain.Finding{{
			Code: "GDS_RELEASE_CONTROL_PLANE_REQUIRED", Severity: domain.SeverityHigh,
			Message: "Release installation lifecycle is available only in the control-plane repository.",
		}}
	}
	compiled := services.Compiler.CompileDirectory(root, anchor, compiler.DevelopmentBundleVersion)
	if len(compiled.Findings) != 0 {
		return releaseOperationContext{}, compiled.Findings
	}
	statePath, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return releaseOperationContext{}, []domain.Finding{*stateFinding}
	}
	defer store.Close()
	active, err := releaseconsumer.InspectActive(options.InstallRoot, services.Schemas)
	if err != nil {
		return releaseOperationContext{}, []domain.Finding{dependencyFinding(options.InstallRoot, err)}
	}
	authorization, authFindings := loadRollbackAuthorization(options.RollbackAuthorizationPath, services.Schemas)
	if len(authFindings) != 0 {
		return releaseOperationContext{}, authFindings
	}
	request := releaseRequest(options)
	resolvedTargetKey := ""
	if operation == "rollback" || operation == "remove" {
		resolvedTargetKey = options.TargetReleaseKey
		if operation == "remove" && active.Record != nil {
			resolvedTargetKey = active.Record.ReleaseKey
		}
		if !safeReleaseKey(resolvedTargetKey) {
			return releaseOperationContext{}, []domain.Finding{{
				Code: "GDS_RELEASE_TARGET_KEY_INVALID", Severity: domain.SeverityHigh,
				Message: "Target release key is invalid or missing.",
			}}
		}
		targetRoot := filepath.Join(options.InstallRoot, "releases", resolvedTargetKey)
		request.ReleaseDirectory = filepath.Join(targetRoot, "release")
		request.EvidenceDirectory = filepath.Join(targetRoot, "evidence")
		request.TrustPolicyPath = filepath.Join(targetRoot, "consumer-trust.yaml")
	}
	trust, err := bundle.LoadTrustFile(request.TrustPolicyPath, services.Schemas)
	if err != nil {
		return releaseOperationContext{}, []domain.Finding{dependencyFinding(request.TrustPolicyPath, err)}
	}
	acceptance, err := store.BundleAcceptanceState(ctx, trust.TrustDomain)
	if err != nil {
		return releaseOperationContext{}, []domain.Finding{dependencyFinding(statePath, err)}
	}
	if operation == "remove" {
		installedRecord, recordErr := releaseconsumer.LoadInstallRecord(
			filepath.Join(options.InstallRoot, "releases", resolvedTargetKey, "install-record.json"),
			services.Schemas,
		)
		if recordErr != nil || acceptance.AcceptedDigests[installedRecord.ReleaseSequence] !=
			installedRecord.ArtifactDigest {
			return releaseOperationContext{}, []domain.Finding{{
				Code: "GDS_RELEASE_REMOVE_ACCEPTANCE_NOT_PROVEN", Severity: domain.SeverityHigh,
				Message: "Removal target is not bound by the local acceptance ledger.",
			}}
		}
		// Removing an already accepted active release is not an activation. Verify
		// that exact accepted release without weakening the stored anti-rollback
		// floor for future install, upgrade, or rollback operations.
		acceptance.HighestSequence = installedRecord.ReleaseSequence
	}
	verifier, err := services.releaseVerifier(trust.Verification.Verifier)
	if err != nil {
		return releaseOperationContext{}, []domain.Finding{{
			Code: "GDS_RELEASE_ATTESTATION_TOOL_UNAVAILABLE", Severity: domain.SeverityHigh,
			Message: "GitHub CLI attestation verifier is unavailable.",
		}}
	}
	verified, verifyFindings := verifier.Verify(
		ctx, request, acceptance, authorization, services.Now().UTC(),
	)
	defer verified.Close()
	if len(verifyFindings) != 0 {
		return releaseOperationContext{}, verifyFindings
	}
	candidate, candidateFindings := releaseconsumer.BuildInstallCandidate(
		verified, request, options.InstallRoot, services.Schemas,
	)
	if len(candidateFindings) != 0 {
		return releaseOperationContext{}, candidateFindings
	}
	active, lifecycleFindings := releaseconsumer.ValidateLifecycle(
		operation, candidate, authorization, services.Now().UTC(),
	)
	if len(lifecycleFindings) != 0 {
		return releaseOperationContext{}, lifecycleFindings
	}
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return releaseOperationContext{}, []domain.Finding{dependencyFinding(path, err)}
	}
	if _, err := services.Git.CommittedSourceOID(ctx, releaseImplementationRoot(root), []string{
		"requirements/bundle-trust.yaml", ".github/workflows/release-bundle.yml",
		"scripts/validate_release.sh", "core/app/release_operations.go", "core/app/release_scope.go", "core/bundle",
		"core/cli", "core/cmd/gds", "core/operations", "core/providers/git/source.go",
		"core/releasebuilder", "core/releaseconsumer", "core/semver", "core/state", "core/validation",
		"schemas/v1/bundle-manifest.schema.json", "schemas/v1/bundle-trust.schema.json",
		"schemas/v1/release-envelope.schema.json", "schemas/v1/release-installation.schema.json",
		"schemas/v1/rollback-authorization.schema.json", "schemas/v1/plan.schema.json",
	}); err != nil {
		return releaseOperationContext{}, []domain.Finding{dependencyFinding(root, err)}
	}
	head, err := services.Git.HeadOID(ctx, info.WorktreeRoot)
	if err != nil {
		return releaseOperationContext{}, []domain.Finding{dependencyFinding(path, err)}
	}
	manifestDigest, err := fileDigest(filepath.Join(info.WorktreeRoot, ".gds", "repository.yaml"))
	if err != nil {
		return releaseOperationContext{}, []domain.Finding{dependencyFinding(path, err)}
	}
	fingerprint, err := canonicaljson.Digest(map[string]any{
		"candidate_digest": candidate.Record.CandidateDigest,
		"active":           active,
		"operation":        operation,
	})
	if err != nil {
		return releaseOperationContext{}, []domain.Finding{dependencyFinding(path, err)}
	}
	return releaseOperationContext{
		repositoryID: anchor.Repository.ID, root: root, request: request,
		verification: verified, candidate: candidate, active: active,
		authorization: authorization,
		observation: operations.Observation{
			RepositoryID: anchor.Repository.ID, HeadOID: head,
			WorktreeFingerprint: fingerprint, ManifestDigest: manifestDigest,
			PolicyDigest: compiled.Document.CompiledPolicy.Digest,
		},
	}, nil
}

func releaseImplementationRoot(controlPlaneRoot string) string {
	return projections.ResolveDevelopmentSourceLayout(controlPlaneRoot).EngineRoot
}

func (services *Services) releaseVerifier(identity bundle.TrustVerifier) (releaseconsumer.Verifier, error) {
	if services.ReleaseAttestations != nil {
		return releaseconsumer.Verifier{Schemas: services.Schemas, Attestations: services.ReleaseAttestations}, nil
	}
	attestations, err := releaseconsumer.NewGHAttestationVerifier(identity)
	if err != nil {
		return releaseconsumer.Verifier{}, err
	}
	return releaseconsumer.Verifier{Schemas: services.Schemas, Attestations: attestations}, nil
}

func releaseRequest(options ReleaseOperationOptions) releaseconsumer.Request {
	return releaseconsumer.Request{
		ReleaseDirectory: options.ReleaseDirectory, EvidenceDirectory: options.EvidenceDirectory,
		TrustPolicyPath: options.TrustPolicyPath, ConsumerVersion: options.ConsumerVersion,
		TargetOS: options.TargetOS, TargetArch: options.TargetArch,
	}
}

func normalizeReleaseOperationPaths(options ReleaseOperationOptions) (ReleaseOperationOptions, *domain.Finding) {
	paths := []*string{
		&options.ReleaseDirectory,
		&options.EvidenceDirectory,
		&options.TrustPolicyPath,
		&options.InstallRoot,
		&options.RollbackAuthorizationPath,
	}
	for _, path := range paths {
		if strings.TrimSpace(*path) == "" {
			continue
		}
		absolute, err := filepath.Abs(*path)
		if err != nil {
			return options, &domain.Finding{
				Code: "GDS_RELEASE_PATH_INVALID", Severity: domain.SeverityHigh,
				Message:  "Release lifecycle paths must resolve to exact absolute paths.",
				Evidence: map[string]any{"path": *path},
			}
		}
		*path = filepath.Clean(absolute)
	}
	return options, nil
}

func releaseSteps(repositoryID string, operation string, parameters map[string]any) []operations.Step {
	step := func(id, action string) operations.Step {
		return operations.Step{
			StepID: id, RepositoryID: repositoryID, Action: action, RequiresApproval: true,
			Compensation: operations.Compensation{Mode: "manual"}, Parameters: parameters,
		}
	}
	switch operation {
	case "install", "upgrade":
		return []operations.Step{
			step("materialize-release", releaseconsumer.MaterializeReleaseAction),
			step("activate-release", releaseconsumer.ActivateReleaseAction),
		}
	case "rollback":
		return []operations.Step{step("rollback-release", releaseconsumer.RollbackReleaseAction)}
	case "remove":
		return []operations.Step{step("remove-release", releaseconsumer.RemoveReleaseAction)}
	default:
		return nil
	}
}

func releaseHandlers(
	operation string,
	current releaseOperationContext,
	store *state.Store,
	now func() time.Time,
) map[string]operations.ActionHandler {
	switch operation {
	case "install", "upgrade":
		return map[string]operations.ActionHandler{
			releaseconsumer.MaterializeReleaseAction: releaseconsumer.MaterializeHandler{Candidate: current.candidate},
			releaseconsumer.ActivateReleaseAction: releaseconsumer.ActivationHandler{
				Candidate: current.candidate, ExpectedCurrent: current.active.CurrentTarget,
				Operation: operation, Store: store, Now: now,
			},
		}
	case "rollback":
		return map[string]operations.ActionHandler{
			releaseconsumer.RollbackReleaseAction: releaseconsumer.ActivationHandler{
				Candidate: current.candidate, ExpectedCurrent: current.active.CurrentTarget,
				Operation: operation, Store: store, Authorization: current.authorization, Now: now,
			},
		}
	case "remove":
		return map[string]operations.ActionHandler{
			releaseconsumer.RemoveReleaseAction: releaseconsumer.RemoveHandler{
				Candidate: current.candidate, ExpectedCurrent: current.active.CurrentTarget,
			},
		}
	default:
		return nil
	}
}

func loadRollbackAuthorization(
	path string,
	schemas *validation.Set,
) (*bundle.RollbackAuthorization, []domain.Finding) {
	if strings.TrimSpace(path) == "" {
		return nil, nil
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, []domain.Finding{dependencyFinding(path, err)}
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() < 1 || info.Size() > 1<<20 {
		return nil, []domain.Finding{dependencyFinding(
			absolute, errors.New("rollback authorization is not a bounded regular file"),
		)}
	}
	value, err := serialization.DecodeFile(absolute)
	if err != nil {
		return nil, []domain.Finding{dependencyFinding(absolute, err)}
	}
	if findings := schemas.Validate("rollback-authorization", value, absolute); len(findings) != 0 {
		return nil, findings
	}
	raw, err := os.ReadFile(absolute)
	if err != nil {
		return nil, []domain.Finding{dependencyFinding(absolute, err)}
	}
	var authorization bundle.RollbackAuthorization
	if err := serialization.DecodeInto(absolute, raw, &authorization); err != nil {
		return nil, []domain.Finding{dependencyFinding(absolute, err)}
	}
	return &authorization, nil
}

func validateReleaseOperationOptions(operation string, options ReleaseOperationOptions) *domain.Finding {
	if operation != "verify" {
		if finding := validateLocalOperationIdentity(options.ProjectionOperationOptions); finding != nil {
			return finding
		}
	}
	if strings.TrimSpace(options.ConsumerVersion) == "" ||
		(options.TargetOS != "darwin" && options.TargetOS != "linux") ||
		(options.TargetArch != "amd64" && options.TargetArch != "arm64") {
		finding := domain.Finding{
			Code: "GDS_RELEASE_RUNTIME_INPUT_INVALID", Severity: domain.SeverityHigh,
			Message: "Consumer version and target platform are required.",
		}
		return &finding
	}
	if operation == "install" || operation == "upgrade" || operation == "verify" {
		if options.ReleaseDirectory == "" || options.EvidenceDirectory == "" || options.TrustPolicyPath == "" {
			finding := domain.Finding{
				Code: "GDS_RELEASE_EVIDENCE_INPUT_REQUIRED", Severity: domain.SeverityHigh,
				Message: "Release, evidence, and local trust paths are required.",
			}
			return &finding
		}
	}
	if operation != "verify" && strings.TrimSpace(options.InstallRoot) == "" {
		finding := domain.Finding{
			Code: "GDS_RELEASE_INSTALL_ROOT_REQUIRED", Severity: domain.SeverityHigh,
			Message: "Release lifecycle requires an exact installation root.",
		}
		return &finding
	}
	if operation == "rollback" && (options.TargetReleaseKey == "" || options.RollbackAuthorizationPath == "") {
		finding := domain.Finding{
			Code: "GDS_RELEASE_ROLLBACK_INPUT_REQUIRED", Severity: domain.SeverityHigh,
			Message: "Rollback requires exact target release key and authorization file.",
		}
		return &finding
	}
	if operation != "rollback" && operation != "verify" && options.RollbackAuthorizationPath != "" {
		finding := domain.Finding{
			Code: "GDS_RELEASE_ROLLBACK_INPUT_UNEXPECTED", Severity: domain.SeverityHigh,
			Message: "Rollback authorization is valid only for rollback verification and apply.",
		}
		return &finding
	}
	return nil
}

func validateStoredReleaseOperationOptions(operation string, options ReleaseOperationOptions) *domain.Finding {
	if finding := validateLocalOperationIdentity(options.ProjectionOperationOptions); finding != nil {
		return finding
	}
	switch operation {
	case "install", "upgrade", "rollback", "remove":
		return nil
	default:
		return &domain.Finding{
			Code: "GDS_RELEASE_OPERATION_INVALID", Severity: domain.SeverityHigh,
			Message: "Release operation must be install, upgrade, rollback, or remove.",
		}
	}
}

func safeReleaseKey(value string) bool {
	return value != "" && filepath.Base(value) == value && !strings.ContainsAny(value, `/\\`) &&
		len(value) <= 180
}
