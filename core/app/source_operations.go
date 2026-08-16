package app

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/compiler"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/identity"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/source"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
)

type SourceVerificationOperationOptions struct {
	Operation ProjectionOperationOptions
	Request   source.VerificationRequest
}

type SourceVerificationPlanData struct {
	Plan      operations.Plan              `json:"plan"`
	StatePath string                       `json:"state_path"`
	Candidate source.VerificationCandidate `json:"candidate"`
}

type sourceVerificationContext struct {
	root         string
	repositoryID string
	request      source.VerificationRequest
	candidate    source.VerificationCandidate
	observation  operations.Observation
}

type sourceVerificationObserver struct {
	services *Services
	root     string
	request  source.VerificationRequest
	approved *source.VerificationSpec
}

func (observer sourceVerificationObserver) Observe(
	ctx context.Context,
	repositoryID string,
) (operations.Observation, error) {
	current, findings := observer.services.sourceVerificationContext(
		ctx, observer.root, observer.request, observer.approved, false,
	)
	if len(findings) != 0 {
		return operations.Observation{}, fmt.Errorf(
			"source verification precondition returned %d findings", len(findings),
		)
	}
	if current.repositoryID != repositoryID {
		return operations.Observation{}, fmt.Errorf(
			"repository identity changed from %s to %s", repositoryID, current.repositoryID,
		)
	}
	return current.observation, nil
}

func (services *Services) PlanSourceVerification(
	ctx context.Context,
	path string,
	options SourceVerificationOperationOptions,
) domain.Envelope {
	if finding := validateLocalOperationIdentity(options.Operation); finding != nil {
		return domain.NewEnvelope("gds source mark-verified plan", domain.ExitInput, nil, *finding)
	}
	current, findings := services.sourceVerificationContext(
		ctx, path, options.Request, nil, true,
	)
	if len(findings) != 0 {
		return domain.NewEnvelope(
			"gds source mark-verified plan", classifyFindings(findings), nil, findings...,
		)
	}
	statePath, store, stateFinding := openOperationState(ctx, options.Operation.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope(
			"gds source mark-verified plan", domain.ExitInput, nil, *stateFinding,
		)
	}
	defer store.Close()
	now := services.Now().UTC()
	planID, err := identity.New("plan", now, nil)
	if err != nil {
		return domain.InternalError("gds source mark-verified plan", err)
	}
	plan, err := operations.NewPlan(
		planID, now, now.Add(projectionPlanLifetime), operations.PlanInput{
			Operation: "mark-source-verified",
			Actor: operations.Actor{
				Type: "agent-session", SessionID: options.Operation.SessionID,
			},
			Preconditions: []operations.Precondition{{
				RepositoryID:        current.observation.RepositoryID,
				HeadOID:             current.observation.HeadOID,
				WorktreeFingerprint: current.observation.WorktreeFingerprint,
				ManifestDigest:      current.observation.ManifestDigest,
				PolicyDigest:        current.observation.PolicyDigest,
			}},
			Steps: []operations.Step{{
				StepID:           "materialize-source-verification",
				RepositoryID:     current.repositoryID,
				Action:           source.MaterializeVerificationAction,
				RequiresApproval: true,
				Compensation:     operations.Compensation{Mode: "manual"},
				Parameters:       source.VerificationParameters(current.candidate, current.request),
			}},
			ApprovalClass: "source-semantic-review",
		},
	)
	if err != nil {
		return domain.InternalError("gds source mark-verified plan", err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		sourceVerificationObserver{
			services: services, root: current.root, request: current.request,
		}, nil, options.Operation.DeviceID, options.Operation.SessionID,
	)
	engine.Now = services.Now
	if err := engine.PutPlan(ctx, plan); err != nil {
		return operationFailureEnvelope("gds source mark-verified plan", err)
	}
	envelope := domain.Success(
		"gds source mark-verified plan",
		SourceVerificationPlanData{Plan: plan, StatePath: statePath, Candidate: current.candidate},
	)
	envelope.Scope["repository_id"] = current.repositoryID
	return envelope
}

func (services *Services) ApplySourceVerification(
	ctx context.Context,
	path string,
	planID string,
	options ProjectionOperationOptions,
) domain.Envelope {
	if strings.TrimSpace(planID) == "" {
		return sourceOperationIDRequired("gds source mark-verified apply", "--apply", "plan")
	}
	if finding := validateLocalOperationIdentity(options); finding != nil {
		return domain.NewEnvelope("gds source mark-verified apply", domain.ExitInput, nil, *finding)
	}
	_, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope("gds source mark-verified apply", domain.ExitInput, nil, *stateFinding)
	}
	defer store.Close()
	request, spec, err := sourceRequestAndSpecForPlan(ctx, store, planID)
	if err != nil {
		return operationFailureEnvelope("gds source mark-verified apply", err)
	}
	current, findings := services.sourceVerificationContext(ctx, path, request, &spec, false)
	if len(findings) != 0 {
		return domain.NewEnvelope(
			"gds source mark-verified apply", classifyFindings(findings), nil, findings...,
		)
	}
	materializer, err := source.NewVerificationMaterializer(
		current.root, current.candidate, current.request,
	)
	if err != nil {
		return domain.InternalError("gds source mark-verified apply", err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		sourceVerificationObserver{
			services: services, root: current.root, request: request, approved: &spec,
		},
		map[string]operations.ActionHandler{source.MaterializeVerificationAction: materializer},
		options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	result, err := engine.Apply(ctx, planID, options.ApprovalReference)
	if err != nil {
		envelope := operationFailureEnvelope("gds source mark-verified apply", err)
		envelope.Data = result
		envelope.Mutation.Attempted = result.MutationAttempted
		envelope.Mutation.Completed = result.MutationCompleted
		envelope.OperationID = result.OperationID
		return envelope
	}
	envelope := domain.Success("gds source mark-verified apply", result)
	envelope.Mutation.Attempted = result.MutationAttempted
	envelope.Mutation.Completed = result.MutationCompleted
	envelope.OperationID = result.OperationID
	envelope.Scope["repository_id"] = current.repositoryID
	return envelope
}

func (services *Services) VerifySourceVerification(
	ctx context.Context,
	path string,
	operationID string,
	options ProjectionOperationOptions,
) domain.Envelope {
	if strings.TrimSpace(operationID) == "" {
		return sourceOperationIDRequired("gds source mark-verified verify", "--verify", "operation")
	}
	if finding := validateLocalOperationIdentity(options); finding != nil {
		return domain.NewEnvelope("gds source mark-verified verify", domain.ExitInput, nil, *finding)
	}
	_, store, stateFinding := openOperationState(ctx, options.StatePath)
	if stateFinding != nil {
		return domain.NewEnvelope("gds source mark-verified verify", domain.ExitInput, nil, *stateFinding)
	}
	defer store.Close()
	operation, err := store.GetOperation(ctx, operationID)
	if err != nil {
		return operationFailureEnvelope("gds source mark-verified verify", err)
	}
	request, spec, err := sourceRequestAndSpecForPlan(ctx, store, operation.PlanID)
	if err != nil {
		return operationFailureEnvelope("gds source mark-verified verify", err)
	}
	root, anchor, findings := services.policyInputs(ctx, path)
	if len(findings) != 0 {
		return domain.NewEnvelope(
			"gds source mark-verified verify", classifyFindings(findings), nil, findings...,
		)
	}
	if finding := requireControlPlaneRole(anchor); finding != nil {
		return domain.NewEnvelope(
			"gds source mark-verified verify", domain.ExitPolicy, nil, *finding,
		)
	}
	candidate := source.VerificationCandidate{
		Path: source.RegisterPath, SourceID: request.ID,
		ObservedAt: spec.ObservedAt, HTTPStatus: spec.HTTPStatus,
		ContentBytes: spec.ContentBytes, ObservedDigest: spec.ObservedDigest,
		CandidateDigest: spec.CandidateDigest,
	}
	materializer, err := source.NewVerificationMaterializer(root, candidate, request)
	if err != nil {
		return domain.InternalError("gds source mark-verified verify", err)
	}
	engine := operations.NewDefaultEngine(
		store, services.Schemas,
		sourceVerificationObserver{services: services, root: root, request: request},
		map[string]operations.ActionHandler{source.MaterializeVerificationAction: materializer},
		options.DeviceID, options.SessionID,
	)
	engine.Now = services.Now
	result, err := engine.Verify(ctx, operationID)
	if err != nil {
		envelope := operationFailureEnvelope("gds source mark-verified verify", err)
		envelope.OperationID = operationID
		return envelope
	}
	envelope := domain.Success("gds source mark-verified verify", result)
	envelope.OperationID = operationID
	envelope.Scope["repository_id"] = anchor.Repository.ID
	return envelope
}

func (services *Services) sourceVerificationContext(
	ctx context.Context,
	path string,
	request source.VerificationRequest,
	approved *source.VerificationSpec,
	requireReproducible bool,
) (sourceVerificationContext, []domain.Finding) {
	root, anchor, findings := services.policyInputs(ctx, path)
	if len(findings) != 0 {
		return sourceVerificationContext{}, findings
	}
	if finding := requireControlPlaneRole(anchor); finding != nil {
		return sourceVerificationContext{}, []domain.Finding{*finding}
	}
	compiled := services.Compiler.CompileDirectory(root, anchor, compiler.DevelopmentBundleVersion)
	if len(compiled.Findings) != 0 {
		return sourceVerificationContext{}, compiled.Findings
	}
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return sourceVerificationContext{}, []domain.Finding{dependencyFinding(path, err)}
	}
	headOID, err := services.Git.HeadOID(ctx, info.WorktreeRoot)
	if err != nil {
		return sourceVerificationContext{}, []domain.Finding{dependencyFinding(path, err)}
	}
	register, registerFindings := source.Load(root, services.Schemas)
	if len(registerFindings) != 0 {
		return sourceVerificationContext{}, registerFindings
	}
	var entry *source.Entry
	for index := range register.Sources {
		if register.Sources[index].ID == request.ID {
			entry = &register.Sources[index]
			break
		}
	}
	if entry == nil {
		return sourceVerificationContext{}, []domain.Finding{{
			Code: "GDS_SOURCE_ID_UNKNOWN", Severity: domain.SeverityHigh,
			Message:  "Requested source id is not registered.",
			Evidence: map[string]any{"id": request.ID},
		}}
	}
	var check source.CheckResult
	if requireReproducible {
		check, err = services.Sources.CheckReproducible(ctx, *entry)
	} else {
		check, err = services.Sources.Check(ctx, *entry)
	}
	if err != nil {
		var reproducibilityError *source.NonReproducibleError
		if errors.As(err, &reproducibilityError) {
			return sourceVerificationContext{}, []domain.Finding{{
				Code: "GDS_SOURCE_CONTENT_NONDETERMINISTIC", Severity: domain.SeverityHigh,
				Message: "Official source does not provide a reproducible verification representation.",
				Evidence: map[string]any{
					"id": request.ID, "first_digest": reproducibilityError.FirstDigest,
					"next_digest": reproducibilityError.NextDigest,
				},
			}}
		}
		return sourceVerificationContext{}, []domain.Finding{{
			Code: "GDS_SOURCE_CHECK_FAILED", Severity: domain.SeverityHigh,
			Message:  "Official source could not be checked before verification.",
			Evidence: map[string]any{"id": request.ID, "error": err.Error()},
		}}
	}
	if approved != nil {
		if check.ObservedDigest != approved.ObservedDigest {
			return sourceVerificationContext{}, []domain.Finding{{
				Code: "GDS_SOURCE_CONTENT_CHANGED", Severity: domain.SeverityHigh,
				Message: "Source content changed after the verification plan was created.",
				Evidence: map[string]any{
					"id": request.ID, "planned_digest": approved.ObservedDigest,
					"observed_digest": check.ObservedDigest,
				},
			}}
		}
		check.ObservedAt = approved.ObservedAt
		check.HTTPStatus = approved.HTTPStatus
		check.Bytes = approved.ContentBytes
	}
	candidate, candidateFindings := source.BuildVerificationCandidate(
		register, request, check, services.Schemas,
	)
	if len(candidateFindings) != 0 {
		return sourceVerificationContext{}, candidateFindings
	}
	if approved != nil && candidate.CandidateDigest != approved.CandidateDigest {
		return sourceVerificationContext{}, []domain.Finding{{
			Code: "GDS_SOURCE_CANDIDATE_CHANGED", Severity: domain.SeverityHigh,
			Message: "Source register candidate differs from the approved plan.",
			Evidence: map[string]any{
				"id": request.ID, "planned_digest": approved.CandidateDigest,
				"candidate_digest": candidate.CandidateDigest,
			},
		}}
	}
	fingerprint, err := source.VerificationFingerprint(root, candidate, request)
	if err != nil {
		return sourceVerificationContext{}, []domain.Finding{dependencyFinding(path, err)}
	}
	manifestDigest, err := fileDigest(filepath.Join(root, ".gds", "repository.yaml"))
	if err != nil {
		return sourceVerificationContext{}, []domain.Finding{dependencyFinding(path, err)}
	}
	return sourceVerificationContext{
		root: root, repositoryID: anchor.Repository.ID, request: request, candidate: candidate,
		observation: operations.Observation{
			RepositoryID: anchor.Repository.ID, HeadOID: headOID,
			WorktreeFingerprint: fingerprint, ManifestDigest: manifestDigest,
			PolicyDigest: compiled.Document.CompiledPolicy.Digest,
		},
	}, nil
}

func requireControlPlaneRole(anchor domain.RepositoryAnchor) *domain.Finding {
	for _, role := range anchor.Repository.Roles {
		if role == "control-plane" {
			return nil
		}
	}
	return &domain.Finding{
		Code: "GDS_CONTROL_PLANE_ROLE_REQUIRED", Severity: domain.SeverityHigh,
		Message:  "Source verification mutations are restricted to the GDS control-plane repository.",
		Evidence: map[string]any{"repository_id": anchor.Repository.ID},
	}
}

func sourceRequestAndSpecForPlan(
	ctx context.Context,
	store *state.Store,
	planID string,
) (source.VerificationRequest, source.VerificationSpec, error) {
	record, err := store.GetPlan(ctx, planID)
	if err != nil {
		return source.VerificationRequest{}, source.VerificationSpec{}, err
	}
	plan, err := operations.DecodePlan(record.Body)
	if err != nil {
		return source.VerificationRequest{}, source.VerificationSpec{}, err
	}
	if plan.Operation != "mark-source-verified" || len(plan.Steps) != 1 {
		return source.VerificationRequest{}, source.VerificationSpec{}, fmt.Errorf(
			"plan %s is not a source verification plan", planID,
		)
	}
	spec, err := source.SpecFromParameters(plan.Steps[0].Parameters)
	if err != nil {
		return source.VerificationRequest{}, source.VerificationSpec{}, err
	}
	request, err := source.RequestFromParameters(plan.Steps[0].Parameters)
	return request, spec, err
}

func sourceOperationIDRequired(command, flag, kind string) domain.Envelope {
	return domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{
		Code: "GDS_" + strings.ToUpper(kind) + "_ID_REQUIRED", Severity: domain.SeverityHigh,
		Message: flag + " requires an exact " + kind + " id.",
	})
}
