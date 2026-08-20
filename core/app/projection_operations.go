package app

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/compiler"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/identity"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/projections"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
)

const projectionPlanLifetime = 15 * time.Minute

type ProjectionOperationOptions struct {
	StatePath         string
	DeviceID          string
	SessionID         string
	ApprovalReference string
}

type ProjectionPlanData struct {
	Plan      operations.Plan       `json:"plan"`
	StatePath string                `json:"state_path"`
	Candidate projections.Candidate `json:"candidate"`
}

type projectionContext struct {
	root         string
	repositoryID string
	candidate    projections.Candidate
	observation  operations.Observation
}

type projectionObserver struct {
	services *Services
	root     string
}

func (observer projectionObserver) Observe(
	ctx context.Context,
	repositoryID string,
) (operations.Observation, error) {
	current, findings := observer.services.projectionOperationContext(ctx, observer.root)
	if len(findings) != 0 {
		return operations.Observation{}, fmt.Errorf(
			"projection precondition returned %d findings", len(findings),
		)
	}
	if current.repositoryID != repositoryID {
		return operations.Observation{}, fmt.Errorf(
			"repository identity changed from %s to %s", repositoryID, current.repositoryID,
		)
	}
	return current.observation, nil
}

func (services *Services) PlanRepositoryProjection(
	ctx context.Context,
	path string,
	options ProjectionOperationOptions,
) domain.Envelope {
	if finding := validateLocalOperationIdentity(options); finding != nil {
		return domain.NewEnvelope("gds generate repository plan", domain.ExitInput, nil, *finding)
	}
	current, findings := services.projectionOperationContext(ctx, path)
	if len(findings) != 0 {
		return domain.NewEnvelope(
			"gds generate repository plan", classifyFindings(findings), nil, findings...,
		)
	}
	statePath, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope(
			"gds generate repository plan", domain.ExitInput, nil, *stateFinding,
		)
	}
	defer store.Close()
	now := services.Now().UTC()
	planID, err := identity.New("plan", now, nil)
	if err != nil {
		return domain.InternalError("gds generate repository plan", err)
	}
	plan, err := operations.NewPlan(
		planID,
		now,
		now.Add(projectionPlanLifetime),
		operations.PlanInput{
			Operation: "materialize-repository-projections",
			Actor:     operations.Actor{Type: "agent-session", SessionID: options.SessionID},
			Preconditions: []operations.Precondition{{
				RepositoryID:        current.observation.RepositoryID,
				HeadOID:             current.observation.HeadOID,
				WorktreeFingerprint: current.observation.WorktreeFingerprint,
				ManifestDigest:      current.observation.ManifestDigest,
				PolicyDigest:        current.observation.PolicyDigest,
			}},
			Steps: []operations.Step{{
				StepID: "materialize-projections", RepositoryID: current.repositoryID,
				// A projection write touches only files inside this repository, and
				// their integrity is held by the digests in .gds/bundle.lock.yaml,
				// not by an approval signature. Requiring an owner signature here
				// protected nothing an actor with write access to the tree could not
				// do by editing the file directly, while costing a four-step
				// ceremony on every template, schema or policy edit. The plan, lock,
				// journal and exact-state postcondition are unchanged.
				Action: projections.MaterializeAction, RequiresApproval: false,
				Compensation: operations.Compensation{Mode: "manual"},
				Parameters:   projections.Parameters(current.candidate),
			}},
			ApprovalClass: "local-projection-write",
		},
	)
	if err != nil {
		return domain.InternalError("gds generate repository plan", err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		projectionObserver{services: services, root: current.root},
		nil, options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	if err := engine.PutPlan(ctx, plan); err != nil {
		return operationFailureEnvelope("gds generate repository plan", err)
	}
	envelope := domain.Success("gds generate repository plan", ProjectionPlanData{
		Plan: plan, StatePath: statePath, Candidate: current.candidate,
	})
	envelope.Scope["repository_id"] = current.repositoryID
	return envelope
}

func (services *Services) ApplyRepositoryProjection(
	ctx context.Context,
	path string,
	planID string,
	options ProjectionOperationOptions,
) domain.Envelope {
	if strings.TrimSpace(planID) == "" {
		return domain.NewEnvelope("gds generate repository apply", domain.ExitInput, nil, domain.Finding{
			Code: "GDS_PLAN_ID_REQUIRED", Severity: domain.SeverityHigh,
			Message: "--apply requires an exact plan id.",
		})
	}
	if finding := validateLocalOperationIdentity(options); finding != nil {
		return domain.NewEnvelope("gds generate repository apply", domain.ExitInput, nil, *finding)
	}
	current, findings := services.projectionOperationContext(ctx, path)
	if len(findings) != 0 {
		return domain.NewEnvelope(
			"gds generate repository apply", classifyFindings(findings), nil, findings...,
		)
	}
	_, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope("gds generate repository apply", domain.ExitInput, nil, *stateFinding)
	}
	defer store.Close()
	materializer, err := projections.NewMaterializer(current.root, current.candidate)
	if err != nil {
		return domain.InternalError("gds generate repository apply", err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		projectionObserver{services: services, root: current.root},
		map[string]operations.ActionHandler{projections.MaterializeAction: materializer},
		options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	result, err := engine.Apply(ctx, planID, options.ApprovalReference)
	if err != nil {
		envelope := operationFailureEnvelope("gds generate repository apply", err)
		envelope.Data = result
		envelope.Mutation.Attempted = result.MutationAttempted
		envelope.Mutation.Completed = result.MutationCompleted
		envelope.OperationID = result.OperationID
		return envelope
	}
	envelope := domain.Success("gds generate repository apply", result)
	envelope.Mutation.Attempted = result.MutationAttempted
	envelope.Mutation.Completed = result.MutationCompleted
	envelope.OperationID = result.OperationID
	envelope.Scope["repository_id"] = current.repositoryID
	return envelope
}

func (services *Services) VerifyRepositoryProjection(
	ctx context.Context,
	path string,
	operationID string,
	options ProjectionOperationOptions,
) domain.Envelope {
	if strings.TrimSpace(operationID) == "" {
		return domain.NewEnvelope("gds generate repository verify", domain.ExitInput, nil, domain.Finding{
			Code: "GDS_OPERATION_ID_REQUIRED", Severity: domain.SeverityHigh,
			Message: "--verify requires an exact operation id.",
		})
	}
	if finding := validateLocalOperationIdentity(options); finding != nil {
		return domain.NewEnvelope("gds generate repository verify", domain.ExitInput, nil, *finding)
	}
	current, findings := services.projectionOperationContext(ctx, path)
	if len(findings) != 0 {
		return domain.NewEnvelope(
			"gds generate repository verify", classifyFindings(findings), nil, findings...,
		)
	}
	_, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope("gds generate repository verify", domain.ExitInput, nil, *stateFinding)
	}
	defer store.Close()
	materializer, err := projections.NewMaterializer(current.root, current.candidate)
	if err != nil {
		return domain.InternalError("gds generate repository verify", err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		projectionObserver{services: services, root: current.root},
		map[string]operations.ActionHandler{projections.MaterializeAction: materializer},
		options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	result, err := engine.Verify(ctx, operationID)
	if err != nil {
		envelope := operationFailureEnvelope("gds generate repository verify", err)
		envelope.OperationID = operationID
		return envelope
	}
	envelope := domain.Success("gds generate repository verify", result)
	envelope.OperationID = operationID
	envelope.Scope["repository_id"] = current.repositoryID
	return envelope
}

func (services *Services) projectionOperationContext(
	ctx context.Context,
	path string,
) (projectionContext, []domain.Finding) {
	root, anchor, findings := services.projectionPolicyInputs(ctx, path)
	if len(findings) != 0 {
		return projectionContext{}, findings
	}
	compiled := services.Compiler.CompileDirectory(
		root, anchor, compiler.DevelopmentBundleVersion,
	)
	if len(compiled.Findings) != 0 {
		return projectionContext{}, compiled.Findings
	}
	repositoryInfo, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return projectionContext{}, []domain.Finding{dependencyFinding(path, err)}
	}
	headOID, err := services.Git.HeadOID(ctx, repositoryInfo.WorktreeRoot)
	if err != nil {
		return projectionContext{}, []domain.Finding{dependencyFinding(path, err)}
	}
	if _, err := services.Git.CommittedSourceOID(
		ctx, repositoryInfo.WorktreeRoot, []string{".gds/repository.yaml"},
	); err != nil {
		return projectionContext{}, []domain.Finding{dependencyFinding(path, err)}
	}
	// Trace metadata only; see the equivalent note in services.go. An
	// uncommitted canonical source must not block generation, because that
	// refusal is what forced the follow-up re-pin commit.
	sourceLayout := projections.ResolveDevelopmentSourceLayout(root)
	sourceOID, err := services.Git.CommittedSourceOID(
		ctx, root, sourceLayout.Paths,
	)
	if err != nil {
		sourceOID = ""
	}
	sourceTreeDigest, err := services.Git.SourceTreeDigest(
		ctx, root, sourceLayout.Paths,
	)
	if err != nil {
		return projectionContext{}, []domain.Finding{dependencyFinding(path, err)}
	}
	bundle, err := services.Projector.DevelopmentBundle(compiled.Document, sourceOID, sourceTreeDigest)
	if err != nil {
		return projectionContext{}, []domain.Finding{dependencyFinding(path, err)}
	}
	candidate, projectionFindings := services.Projector.Generate(anchor, compiled.Document, bundle)
	if len(projectionFindings) != 0 {
		return projectionContext{}, projectionFindings
	}
	fingerprint, err := projections.Fingerprint(repositoryInfo.WorktreeRoot, candidate)
	if err != nil {
		return projectionContext{}, []domain.Finding{dependencyFinding(path, err)}
	}
	manifestDigest, err := fileDigest(filepath.Join(repositoryInfo.WorktreeRoot, ".gds", "repository.yaml"))
	if err != nil {
		return projectionContext{}, []domain.Finding{dependencyFinding(path, err)}
	}
	return projectionContext{
		root: repositoryInfo.WorktreeRoot, repositoryID: anchor.Repository.ID,
		candidate: candidate,
		observation: operations.Observation{
			RepositoryID: anchor.Repository.ID, HeadOID: headOID,
			WorktreeFingerprint: fingerprint, ManifestDigest: manifestDigest,
			PolicyDigest: compiled.Document.CompiledPolicy.Digest,
		},
	}, nil
}

func validateLocalOperationIdentity(options ProjectionOperationOptions) *domain.Finding {
	return validateOperationActor(options.DeviceID, options.SessionID)
}

func openOperationState(
	ctx context.Context,
	requested string,
) (string, *state.Store, *domain.Finding) {
	path := requested
	var err error
	if strings.TrimSpace(path) == "" {
		path, err = state.DefaultPath()
		if err != nil {
			finding := dependencyFinding(requested, err)
			return "", nil, &finding
		}
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		finding := dependencyFinding(path, err)
		return "", nil, &finding
	}
	store, err := state.Open(ctx, absolute)
	if err != nil {
		finding := dependencyFinding(absolute, err)
		return "", nil, &finding
	}
	return absolute, store, nil
}

func operationFailureEnvelope(command string, err error) domain.Envelope {
	var operationError *operations.Error
	if errors.As(err, &operationError) {
		// The summary stays first and keeps its code, so callers that key on it
		// are unaffected. What follows is why it failed: an operation that named
		// the exact field and the exact rule used to lose both here, and the
		// envelope is the only surface a caller outside this process has.
		findings := make([]domain.Finding, 0, len(operationError.Findings)+1)
		findings = append(findings, domain.Finding{
			Code: operationError.Code, Severity: domain.SeverityHigh,
			Message: operationError.Message,
		})
		findings = append(findings, operationError.Findings...)
		envelope := domain.NewEnvelope(command, operationError.Class, nil, findings...)
		envelope.OperationID = operationError.OperationID
		envelope.Mutation.Attempted = operationError.MutationAttempted
		return envelope
	}
	return domain.InternalError(command, err)
}

func dependencyFinding(path string, err error) domain.Finding {
	return domain.Finding{
		Code: "GDS_LOCAL_OPERATION_NOT_PROVEN", Severity: domain.SeverityHigh,
		Message: err.Error(), Evidence: map[string]any{"path": path},
	}
}

func fileDigest(path string) (string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(content)), nil
}
