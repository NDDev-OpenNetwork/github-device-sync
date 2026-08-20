// Package app coordinates read-only GDS use cases over explicit adapters.
package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/bundle"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/compiler"
	contextresolver "github.com/NDDev-OpenNetwork/github-device-sync/core/context"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/discovery"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/estate"
	forkworkflow "github.com/NDDev-OpenNetwork/github-device-sync/core/fork"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/githubmutationruntime"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/githubruntime"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/harness"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/identity"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/inventory"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/manifest"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/memory"
	moduleworkflow "github.com/NDDev-OpenNetwork/github-device-sync/core/module"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/projections"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/releaseconsumer"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/rollout"
	securityscan "github.com/NDDev-OpenNetwork/github-device-sync/core/security"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/skills"
	sourcefacts "github.com/NDDev-OpenNetwork/github-device-sync/core/source"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/workspace"
)

type Services struct {
	Schemas                           *validation.Set
	Git                               *gitprovider.Runner
	GitMutations                      *gitprovider.MutationRunner
	Context                           *contextresolver.Resolver
	Discovery                         *discovery.Local
	Inventory                         *inventory.Compiler
	Compiler                          *compiler.Compiler
	Projector                         *projections.Generator
	Sources                           *sourcefacts.Checker
	GitHubRuntimeBuildOptions         githubruntime.BuildOptions
	GitHubMutationRuntimeBuildOptions githubmutationruntime.BuildOptions
	ReleaseAttestations               releaseconsumer.AttestationVerifier
	Now                               func() time.Time
}

type DiscoveryOptions struct {
	Root            string
	MaxDepth        int
	MaxRepositories int
	Concurrency     int
	IncludeArchived bool
}

type StatusData struct {
	RepositoryID string             `json:"repository_id,omitempty"`
	Status       gitprovider.Status `json:"status"`
}

type ValidationData struct {
	Target          string `json:"target"`
	SchemaCount     int    `json:"schema_count"`
	FixturesChecked bool   `json:"fixtures_checked"`
}

type DoctorData struct {
	Checks  []DoctorCheck           `json:"checks"`
	Context contextresolver.Context `json:"context"`
	Status  *gitprovider.Status     `json:"status,omitempty"`
}

type DoctorCheck struct {
	Name   string `json:"name"`
	Result string `json:"result"`
}

type ProjectionData struct {
	Bundle    projections.Bundle    `json:"bundle"`
	Candidate projections.Candidate `json:"candidate"`
}

type PluginValidationData struct {
	Packages []skills.PackageCandidate `json:"packages"`
}

type StateInspectionData struct {
	Info    state.Info    `json:"info"`
	Summary state.Summary `json:"summary"`
}

type ReleaseCandidateOptions struct {
	BundleVersion     string
	ReleaseSequence   int
	Channel           string
	MinimumCLIVersion string
}

type ReleaseCandidateData struct {
	Candidate               bundle.Candidate `json:"candidate"`
	SourceRef               string           `json:"source_ref"`
	ReadyForExternalRelease bool             `json:"ready_for_external_release"`
}

func NewServices(clock inventory.Clock) (*Services, error) {
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		return nil, err
	}
	git, err := gitprovider.NewRunner()
	if err != nil {
		git = gitprovider.NewRunnerForPath("", 0)
	}
	gitMutations, err := gitprovider.NewMutationRunner()
	if err != nil {
		gitMutations = gitprovider.NewMutationRunnerForPath("", 0)
	}
	manifests := manifest.NewLoader(schemas)
	localDiscovery := discovery.NewLocal(git, manifests)
	projector, err := projections.New(schemas)
	if err != nil {
		return nil, err
	}
	policyCompiler := compiler.New(schemas)
	policyProver := contextresolver.NewCanonicalPolicyProver(git, policyCompiler, projector)
	return &Services{
		Schemas:      schemas,
		Git:          git,
		GitMutations: gitMutations,
		Context:      contextresolver.NewResolver(git, manifests, schemas, policyProver),
		Discovery:    localDiscovery,
		Inventory:    inventory.NewCompiler(localDiscovery, git, clock),
		Compiler:     policyCompiler,
		Projector:    projector,
		Sources:      sourcefacts.NewChecker(),
		Now:          time.Now,
	}, nil
}

func (services *Services) SourceCheck(
	ctx context.Context,
	path string,
	id string,
) domain.Envelope {
	if strings.TrimSpace(id) == "" {
		return domain.NewEnvelope("gds source check", domain.ExitInput, nil, domain.Finding{
			Code: "GDS_SOURCE_ID_REQUIRED", Severity: domain.SeverityHigh,
			Message: "--id must identify one registered source.",
		})
	}
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return envelopeForError("gds source check", path, err)
	}
	register, findings := sourcefacts.Load(info.WorktreeRoot, services.Schemas)
	if len(findings) != 0 {
		return domain.NewEnvelope(
			"gds source check", classifyFindings(findings), nil, findings...,
		)
	}
	var selected *sourcefacts.Entry
	for index := range register.Sources {
		if register.Sources[index].ID == id {
			selected = &register.Sources[index]
			break
		}
	}
	if selected == nil {
		return domain.NewEnvelope("gds source check", domain.ExitInput, nil, domain.Finding{
			Code: "GDS_SOURCE_ID_UNKNOWN", Severity: domain.SeverityHigh,
			Message:  "Requested source id is not registered.",
			Evidence: map[string]any{"id": id},
		})
	}
	result, err := services.Sources.Check(ctx, *selected)
	if err != nil {
		return domain.NewEnvelope("gds source check", domain.ExitProviderTransient, nil, domain.Finding{
			Code: "GDS_SOURCE_CHECK_FAILED", Severity: domain.SeverityHigh,
			Message:  "Official source could not be checked.",
			Evidence: map[string]any{"id": id, "error": err.Error()},
		})
	}
	class := domain.ExitSuccess
	switch result.State {
	case "not-proven":
		class = domain.ExitNotProven
		findings = append(findings, domain.Finding{
			Code: "GDS_SOURCE_SEMANTIC_REVIEW_REQUIRED", Severity: domain.SeverityInfo,
			Message:  "Source content has no approved baseline digest; semantic review is required.",
			Evidence: map[string]any{"id": id, "observed_digest": result.ObservedDigest},
		})
	case "changed-unreviewed":
		class = domain.ExitPolicy
		findings = append(findings, domain.Finding{
			Code: "GDS_SOURCE_CONTENT_CHANGED", Severity: domain.SeverityHigh,
			Message: "Source content differs from the approved digest; governed claims are stale.",
			Evidence: map[string]any{
				"id": id, "expected_digest": result.ExpectedDigest,
				"observed_digest": result.ObservedDigest,
			},
		})
	}
	return domain.NewEnvelope("gds source check", class, result, findings...)
}

func (services *Services) SourceStatus(
	ctx context.Context,
	path string,
	asOf string,
) domain.Envelope {
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return envelopeForError("gds source status", path, err)
	}
	evaluationTime := services.Now().UTC()
	if strings.TrimSpace(asOf) != "" {
		evaluationTime, err = time.Parse(time.DateOnly, asOf)
		if err != nil {
			return domain.NewEnvelope("gds source status", domain.ExitInput, nil, domain.Finding{
				Code: "GDS_SOURCE_AS_OF_INVALID", Severity: domain.SeverityHigh,
				Message:  "--as-of must use YYYY-MM-DD.",
				Evidence: map[string]any{"value": asOf},
			})
		}
	}
	register, findings := sourcefacts.Load(info.WorktreeRoot, services.Schemas)
	if len(findings) != 0 {
		return domain.NewEnvelope(
			"gds source status", classifyFindings(findings), nil, findings...,
		)
	}
	report := sourcefacts.Evaluate(register, evaluationTime)
	class := domain.ExitSuccess
	if report.Summary.Blocked != 0 {
		class = domain.ExitPolicy
		findings = append(findings, domain.Finding{
			Code: "GDS_SOURCE_REVIEW_BLOCKED", Severity: domain.SeverityHigh,
			Message:  "One or more source claims changed without semantic review.",
			Evidence: map[string]any{"count": report.Summary.Blocked},
		})
	} else if report.Summary.Overdue != 0 {
		class = domain.ExitStale
		findings = append(findings, domain.Finding{
			Code: "GDS_SOURCE_REVIEW_OVERDUE", Severity: domain.SeverityHigh,
			Message:  "One or more source claims exceeded their review date.",
			Evidence: map[string]any{"count": report.Summary.Overdue},
		})
	} else if report.Summary.NotProven != 0 {
		class = domain.ExitNotProven
		findings = append(findings, domain.Finding{
			Code: "GDS_SOURCE_CONTENT_DIGEST_NOT_PROVEN", Severity: domain.SeverityInfo,
			Message:  "One or more source claims have no pinned content digest or runtime evidence.",
			Evidence: map[string]any{"count": report.Summary.NotProven},
		})
	}
	return domain.NewEnvelope("gds source status", class, report, findings...)
}

func (services *Services) CompilePolicy(ctx context.Context, path string) domain.Envelope {
	root, anchor, findings := services.policyInputs(ctx, path)
	if len(findings) != 0 {
		return domain.NewEnvelope(
			"gds compile policy", classifyFindings(findings), nil, findings...,
		)
	}
	compiled := services.Compiler.CompileDirectory(
		root, anchor, compiler.DevelopmentBundleVersion,
	)
	class := classifyFindings(compiled.Findings)
	envelope := domain.NewEnvelope(
		"gds compile policy", class, compiled.Document, compiled.Findings...,
	)
	envelope.Scope["repository_id"] = anchor.Repository.ID
	return envelope
}

func (services *Services) GenerateRepository(
	ctx context.Context,
	path string,
	check bool,
) domain.Envelope {
	root, anchor, findings := services.projectionPolicyInputs(ctx, path)
	if len(findings) != 0 {
		return domain.NewEnvelope(
			"gds generate repository", classifyFindings(findings), nil, findings...,
		)
	}
	compiled := services.Compiler.CompileDirectory(
		root, anchor, compiler.DevelopmentBundleVersion,
	)
	if len(compiled.Findings) != 0 {
		return domain.NewEnvelope(
			"gds generate repository", classifyFindings(compiled.Findings), nil,
			compiled.Findings...,
		)
	}
	repositoryInfo, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return envelopeForError("gds generate repository", path, err)
	}
	if _, err := services.Git.CommittedSourceOID(
		ctx, repositoryInfo.WorktreeRoot, []string{".gds/repository.yaml"},
	); err != nil {
		return envelopeForError("gds generate repository", path, err)
	}
	// The source commit is trace metadata now that the bundle identity is the
	// source content, so an uncommitted canonical source no longer blocks
	// generation. That refusal was the whole reason a source edit and its
	// regenerated projection could not be one commit.
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
		return envelopeForError("gds generate repository", path, err)
	}
	bundle, err := services.Projector.DevelopmentBundle(compiled.Document, sourceOID, sourceTreeDigest)
	if err != nil {
		return domain.InternalError("gds generate repository", err)
	}
	candidate, projectionFindings := services.Projector.Generate(
		anchor, compiled.Document, bundle,
	)
	if check && len(projectionFindings) == 0 {
		projectionFindings = projections.Verify(repositoryInfo.WorktreeRoot, candidate)
	}
	class := classifyFindings(projectionFindings)
	envelope := domain.NewEnvelope(
		"gds generate repository", class,
		ProjectionData{Bundle: bundle, Candidate: candidate}, projectionFindings...,
	)
	envelope.Scope["repository_id"] = anchor.Repository.ID
	return envelope
}

func (services *Services) ValidateProjections(ctx context.Context, path string) domain.Envelope {
	envelope := services.GenerateRepository(ctx, path, true)
	envelope.Command = "gds validate projections"
	return envelope
}

func (services *Services) policyInputs(
	ctx context.Context,
	path string,
) (string, domain.RepositoryAnchor, []domain.Finding) {
	outcome := services.Context.Resolve(ctx, path)
	if outcome.Context.Workspace.GitWorktreeRoot == "" {
		return "", domain.RepositoryAnchor{}, outcome.Findings
	}
	anchor, anchorFindings := manifest.NewLoader(services.Schemas).LoadRepository(
		outcome.Context.Workspace.GitWorktreeRoot,
	)
	if len(anchorFindings) != 0 {
		return "", domain.RepositoryAnchor{}, anchorFindings
	}
	root := outcome.Context.Estate.Root
	if root == "" {
		return "", anchor, []domain.Finding{{
			Code:     "GDS_POLICY_ESTATE_NOT_PROVEN",
			Severity: domain.SeverityHigh,
			Message:  "A trusted estate root is required to resolve canonical policy sources.",
			Evidence: map[string]any{"repository_id": anchor.Repository.ID},
		}}
	}
	return root, anchor, nil
}

// projectionPolicyInputs permits a public module to render only its own local
// projections from policy sources shipped in that same public tree. It does
// not make the module an estate authority: every provider, workspace and
// cross-repository operation continues to use policyInputs and therefore
// requires a verified external control-plane. The compiler independently
// rejects any private-distribution policy selected for a public target.
func (services *Services) projectionPolicyInputs(
	ctx context.Context,
	path string,
) (string, domain.RepositoryAnchor, []domain.Finding) {
	root, anchor, findings := services.policyInputs(ctx, path)
	if root != "" || len(findings) == 0 {
		return root, anchor, findings
	}
	if len(findings) != 1 || findings[0].Code != "GDS_POLICY_ESTATE_NOT_PROVEN" ||
		anchor.Classification.VisibilityContract != "public" ||
		!hasRole(anchor.Repository.Roles, "module") {
		return root, anchor, findings
	}
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return "", anchor, []domain.Finding{dependencyFinding(path, err)}
	}
	return info.WorktreeRoot, anchor, nil
}

func (services *Services) ResolveContext(ctx context.Context, path string) domain.Envelope {
	outcome := services.Context.Resolve(ctx, path)
	envelope := domain.NewEnvelope("gds context", outcome.Class, outcome.Context, outcome.Findings...)
	if outcome.Context.Repository.ID != "" {
		envelope.Scope["repository_id"] = outcome.Context.Repository.ID
	}
	return envelope
}

func (services *Services) Status(ctx context.Context, path string) domain.Envelope {
	outcome := services.Context.Resolve(ctx, path)
	status, err := services.Git.InspectStatus(ctx, path)
	if err != nil {
		findings := append(outcome.Findings, domain.Finding{
			Code:     "GDS_GIT_STATUS_NOT_PROVEN",
			Severity: domain.SeverityHigh,
			Message:  fmt.Sprintf("Cannot inspect Git status: %v", err),
			Evidence: map[string]any{"path": path},
		})
		return domain.NewEnvelope("gds status", domain.ExitNotProven, map[string]any{
			"repository_id": outcome.Context.Repository.ID,
		}, findings...)
	}
	data := StatusData{RepositoryID: outcome.Context.Repository.ID, Status: status}
	envelope := domain.NewEnvelope("gds status", outcome.Class, data, outcome.Findings...)
	if data.RepositoryID != "" {
		envelope.Scope["repository_id"] = data.RepositoryID
	}
	return envelope
}

func (services *Services) GitTopology(ctx context.Context, path string) domain.Envelope {
	topology, err := services.Git.InspectTopology(ctx, path)
	if err != nil {
		return envelopeForError("gds git topology", path, err)
	}
	return domain.Success("gds git topology", topology)
}

func (services *Services) NewIdentity(kind string) domain.Envelope {
	allowed := map[string]struct{}{
		"repo": {}, "estate": {}, "device": {}, "owner": {}, "portfolio": {},
		"installation": {}, "plan": {}, "operation": {}, "rollout": {},
		"exception": {}, "task": {}, "lock": {},
	}
	if _, found := allowed[kind]; !found {
		return domain.NewEnvelope("gds identity new", domain.ExitInput, nil, domain.Finding{
			Code: "GDS_IDENTITY_KIND_INVALID", Severity: domain.SeverityHigh,
			Message:  "Identity kind is not part of the GDS v1 typed namespace.",
			Evidence: map[string]any{"kind": kind},
		})
	}
	value, err := identity.New(kind, time.Now(), nil)
	if err != nil {
		return domain.InternalError("gds identity new", err)
	}
	return domain.Success("gds identity new", map[string]any{
		"kind": kind, "id": value,
	})
}

func (services *Services) GitHubDoctor() domain.Envelope {
	return domain.NewEnvelope(
		"gds github doctor", domain.ExitNotProven,
		map[string]any{
			"api_version":            githubprovider.APIVersion,
			"base_url":               githubprovider.DefaultBaseURL,
			"authentication":         "github-app-installation-token",
			"permission_enforcement": "exact-before-request",
			"repository_selection":   "all",
			"governance_reads": []string{
				"repository-metadata", "actions-policy", "workflow-token-policy", "rulesets",
			},
			"runtime_schema":                "github-runtime-v1",
			"mutation_runtime_schema":       "github-mutation-runtime-v1",
			"mutation_repository_selection": "selected",
			"mutation_identity_separation":  "exact-before-write",
			"governance_operations": []string{
				"plan", "apply", "verify",
			},
			"secret_adapters": []string{
				"environment", "file", "gh-cli", "linux-secret-service", "macos-keychain",
			},
			"live_credentials_loaded":    false,
			"external_request_attempted": false,
			"read_concurrency_default":   4,
			"response_limit_bytes":       githubprovider.DefaultBodyLimit,
		},
		domain.Finding{
			Code: "GDS_GITHUB_RUNTIME_NOT_PROVEN", Severity: domain.SeverityHigh,
			Message: "Secure runtime adapters and the read-only provider passed isolated contract tests, but no device runtime or live installation was used by this diagnostic.",
		},
	)
}

func (services *Services) BuildReleaseCandidate(
	ctx context.Context,
	path string,
	options ReleaseCandidateOptions,
) domain.Envelope {
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return envelopeForError("gds release candidate", path, err)
	}
	status, err := services.Git.InspectStatus(ctx, info.WorktreeRoot)
	if err != nil {
		return envelopeForError("gds release candidate", path, err)
	}
	if status.Head.Mode != "branch" || status.Branch.Name == "" {
		return domain.NewEnvelope("gds release candidate", domain.ExitPolicy, nil, domain.Finding{
			Code: "GDS_BUNDLE_SOURCE_REF_NOT_PROVEN", Severity: domain.SeverityHigh,
			Message: "A bundle candidate requires an attached branch so its source ref is unambiguous.",
		})
	}
	if status.Changes.Staged != 0 || status.Changes.Unstaged != 0 ||
		status.Changes.Untracked != 0 || status.Changes.Conflicted != 0 ||
		status.Submodules.Modified != 0 || status.Submodules.Conflicted != 0 ||
		status.Submodules.Uninitialized != 0 {
		return domain.NewEnvelope("gds release candidate", domain.ExitPolicy, nil, domain.Finding{
			Code: "GDS_BUNDLE_SOURCE_DIRTY", Severity: domain.SeverityHigh,
			Message: "Bundle source must be fully tracked and clean so source_commit covers every released byte.",
		})
	}
	trust, err := bundle.LoadTrust(info.WorktreeRoot, services.Schemas)
	if err != nil {
		return envelopeForError("gds release candidate", info.WorktreeRoot, err)
	}
	if len(trust.Source.AllowedWorkflows) != 1 {
		return domain.NewEnvelope("gds release candidate", domain.ExitPolicy, nil, domain.Finding{
			Code: "GDS_BUNDLE_WORKFLOW_AMBIGUOUS", Severity: domain.SeverityHigh,
			Message: "The current candidate builder requires one exact trusted release workflow.",
		})
	}
	tracked, err := services.Git.TrackedPaths(ctx, info.WorktreeRoot)
	if err != nil {
		return envelopeForError("gds release candidate", info.WorktreeRoot, err)
	}
	trackedSources := make([]string, 0, len(tracked))
	for _, item := range tracked {
		trackedSources = append(trackedSources, item.Path)
	}
	sourceRef := "refs/heads/" + status.Branch.Name
	candidate, findings := bundle.Build(info.WorktreeRoot, bundle.BuildOptions{
		BundleVersion: options.BundleVersion, ReleaseSequence: options.ReleaseSequence,
		Channel: options.Channel, SourceCommit: status.Head.OID,
		MinimumCLIVersion: options.MinimumCLIVersion,
		Workflow:          trust.Source.AllowedWorkflows[0], SourceRef: sourceRef,
		// The candidate command has no evidence input surface and performs no
		// publication. Its canary is therefore explicitly provisional; the hosted
		// stable/frozen builder still requires the signed active-five manifest.
		HarnessEvidenceProvisional: options.Channel == "canary",
		TrackedSources:             trackedSources,
	}, trust, services.Schemas)
	if len(findings) != 0 {
		return domain.NewEnvelope(
			"gds release candidate", classifyFindings(findings), nil, findings...,
		)
	}
	return domain.NewEnvelope(
		"gds release candidate", domain.ExitNotProven,
		ReleaseCandidateData{Candidate: candidate, SourceRef: sourceRef, ReadyForExternalRelease: false},
		domain.Finding{
			Code: "GDS_BUNDLE_EXTERNAL_PROVENANCE_NOT_CREATED", Severity: domain.SeverityInfo,
			Message: "The candidate is reproducible in memory, but no artifact, attestation, SBOM, tag, release, or external write was created.",
		},
	)
}

func (services *Services) PlanRollout(_ context.Context, requestPath string) domain.Envelope {
	if strings.TrimSpace(requestPath) == "" {
		return domain.NewEnvelope("gds rollout plan", domain.ExitInput, nil, domain.Finding{
			Code: "GDS_ROLLOUT_REQUEST_REQUIRED", Severity: domain.SeverityHigh,
			Message: "--file must identify a side-effect-free rollout request.",
		})
	}
	absolute, err := filepath.Abs(requestPath)
	if err != nil {
		return envelopeForError("gds rollout plan", requestPath, err)
	}
	if findings := services.Schemas.ValidateFile("rollout-request", absolute); len(findings) != 0 {
		return domain.NewEnvelope(
			"gds rollout plan", classifyFindings(findings), nil, findings...,
		)
	}
	raw, err := os.ReadFile(absolute)
	if err != nil {
		return envelopeForError("gds rollout plan", absolute, err)
	}
	var request rollout.Request
	if err := serialization.DecodeInto(absolute, raw, &request); err != nil {
		return envelopeForError("gds rollout plan", absolute, err)
	}
	plan, findings := rollout.BuildRequest(request, services.Schemas)
	return domain.NewEnvelope(
		"gds rollout plan", classifyFindings(findings), plan, findings...,
	)
}

func (services *Services) InspectModule(ctx context.Context, path string) domain.Envelope {
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return envelopeForError("gds module inspect", path, err)
	}
	anchor, findings := manifest.NewLoader(services.Schemas).LoadRepository(info.WorktreeRoot)
	if len(findings) != 0 {
		return domain.NewEnvelope(
			"gds module inspect", classifyFindings(findings), nil, findings...,
		)
	}
	topology, err := services.Git.InspectTopology(ctx, info.WorktreeRoot)
	if err != nil {
		return envelopeForError("gds module inspect", path, err)
	}
	report, moduleFindings := moduleworkflow.Inspect(anchor, topology)
	envelope := domain.NewEnvelope(
		"gds module inspect", classifyFindings(moduleFindings), report, moduleFindings...,
	)
	envelope.Scope["repository_id"] = anchor.Repository.ID
	return envelope
}

func (services *Services) ValidateGitlinks(ctx context.Context, path string) domain.Envelope {
	envelope := services.InspectModule(ctx, path)
	envelope.Command = "gds validate gitlinks"
	return envelope
}

func (services *Services) InspectFork(ctx context.Context, path string) domain.Envelope {
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return envelopeForError("gds fork inspect", path, err)
	}
	anchor, findings := manifest.NewLoader(services.Schemas).LoadRepository(info.WorktreeRoot)
	if len(findings) != 0 {
		return domain.NewEnvelope(
			"gds fork inspect", classifyFindings(findings), nil, findings...,
		)
	}
	report, forkFindings := (forkworkflow.Inspector{Git: services.Git}).Inspect(
		ctx, info.WorktreeRoot, anchor,
	)
	envelope := domain.NewEnvelope(
		"gds fork inspect", classifyFindings(forkFindings), report, forkFindings...,
	)
	envelope.Scope["repository_id"] = anchor.Repository.ID
	return envelope
}

func (services *Services) Discover(
	ctx context.Context,
	options DiscoveryOptions,
) domain.Envelope {
	if finding := validateDiscoveryOptions(options); finding != nil {
		return domain.NewEnvelope("gds discover", domain.ExitInput, nil, *finding)
	}
	result, err := services.Discovery.Discover(ctx, options.Root, discovery.Options{
		MaxDepth: options.MaxDepth, MaxRepositories: options.MaxRepositories,
		Concurrency: options.Concurrency,
	})
	if err != nil {
		return envelopeForError("gds discover", options.Root, err)
	}
	class := classifyFindings(result.Findings)
	return domain.NewEnvelope("gds discover", class, map[string]any{
		"root": result.Root, "boundaries": result.Boundaries,
		"count": len(result.Boundaries), "provider_observation": "not-implemented",
	}, result.Findings...)
}

func (services *Services) CompileInventory(
	ctx context.Context,
	options DiscoveryOptions,
) domain.Envelope {
	if finding := validateDiscoveryOptions(options); finding != nil {
		return domain.NewEnvelope("gds inventory", domain.ExitInput, nil, *finding)
	}
	result, err := services.Inventory.Compile(ctx, options.Root, discovery.Options{
		MaxDepth: options.MaxDepth, MaxRepositories: options.MaxRepositories,
		Concurrency: options.Concurrency, IncludeArchived: options.IncludeArchived,
	})
	if err != nil {
		return envelopeForError("gds inventory", options.Root, err)
	}
	class := classifyFindings(result.Findings)
	return domain.NewEnvelope("gds inventory", class, result, result.Findings...)
}

func (services *Services) CompileRelationshipIndex(
	ctx context.Context,
	options DiscoveryOptions,
) domain.Envelope {
	if finding := validateDiscoveryOptions(options); finding != nil {
		return domain.NewEnvelope("gds inventory relationships", domain.ExitInput, nil, *finding)
	}
	discovered, err := services.Discovery.Discover(ctx, options.Root, discovery.Options{
		MaxDepth: options.MaxDepth, MaxRepositories: options.MaxRepositories,
		Concurrency: options.Concurrency,
	})
	if err != nil {
		return envelopeForError("gds inventory relationships", options.Root, err)
	}
	indexed := make([]estate.IndexedRepository, 0, len(discovered.Boundaries))
	findings := append([]domain.Finding(nil), discovered.Findings...)
	loader := manifest.NewLoader(services.Schemas)
	for _, boundary := range discovered.Boundaries {
		if boundary.AnchorState != "valid" {
			continue
		}
		anchorValue, anchorFindings := loader.LoadRepository(boundary.Path)
		findings = append(findings, anchorFindings...)
		if len(anchorFindings) == 0 {
			indexed = append(indexed, estate.IndexedRepository{Path: boundary.Path, Anchor: anchorValue})
		}
	}
	index, indexFindings := estate.BuildIdentityIndex(indexed, false)
	findings = append(findings, indexFindings...)
	return domain.NewEnvelope("gds inventory relationships", classifyFindings(findings), map[string]any{
		"root": discovered.Root, "discovered": len(discovered.Boundaries),
		"anchored": len(indexed), "index": index,
	}, findings...)
}

func (services *Services) ValidateSchemas(
	ctx context.Context,
	path string,
	fixtures bool,
) domain.Envelope {
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return envelopeForError("gds validate schemas", path, err)
	}
	root := info.WorktreeRoot
	fixturePath := ""
	if fixtures {
		candidate := filepath.Join(root, "tests", "fixtures", "schemas", "v1", "cases.json")
		if _, err := os.Stat(candidate); err == nil {
			fixturePath = candidate
		} else if !errors.Is(err, os.ErrNotExist) {
			return envelopeForError("gds validate schemas", candidate, err)
		}
	}
	findings := services.Schemas.ValidateCanonical(root, fixturePath)
	class := classifyFindings(findings)
	return domain.NewEnvelope("gds validate schemas", class, ValidationData{
		Target: root, SchemaCount: len(services.Schemas.Names()),
		FixturesChecked: fixturePath != "",
	}, findings...)
}

func (services *Services) ValidateRepository(ctx context.Context, path string) domain.Envelope {
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return envelopeForError("gds validate repository", path, err)
	}
	anchorPath := filepath.Join(info.WorktreeRoot, ".gds", "repository.yaml")
	findings := services.Schemas.ValidateFile("repository", anchorPath)
	// The anchor's claimed gate and the tracked ruleset that asks the provider
	// for it are two declarations of one thing. Only compared once the anchor
	// itself is schema-valid: a document that failed its schema has no field to
	// read, and reporting drift against nothing would be noise on top of a real
	// failure.
	if len(findings) == 0 {
		anchor, anchorFindings := manifest.NewLoader(services.Schemas).LoadRepository(info.WorktreeRoot)
		if len(anchorFindings) == 0 {
			findings = append(findings, validation.RequiredContextFindings(
				info.WorktreeRoot, anchor.Verification.RequiredContexts,
			)...)
		}
	}
	class := classifyFindings(findings)
	envelope := domain.NewEnvelope("gds validate repository", class, map[string]any{
		"target": info.WorktreeRoot, "anchor": anchorPath,
	}, findings...)
	return envelope
}

func (services *Services) ValidatePolicies(ctx context.Context, path string) domain.Envelope {
	envelope := services.CompilePolicy(ctx, path)
	envelope.Command = "gds validate policies"
	return envelope
}

func (services *Services) ValidateContext(ctx context.Context, path string) domain.Envelope {
	envelope := services.ResolveContext(ctx, path)
	envelope.Command = "gds validate context"
	return envelope
}

func (services *Services) ValidateGitState(ctx context.Context, path string) domain.Envelope {
	envelope := services.Status(ctx, path)
	envelope.Command = "gds validate git-state"
	if data, ok := envelope.Data.(StatusData); ok && data.Status.Changes.Conflicted != 0 {
		envelope = domain.NewEnvelope(
			"gds validate git-state", domain.ExitConflict, data, domain.Finding{
				Code: "GDS_GIT_STATE_CONFLICTED", Severity: domain.SeverityHigh,
				Message:  "Repository has unresolved Git conflicts.",
				Evidence: map[string]any{"repository_id": data.RepositoryID},
			},
		)
	}
	return envelope
}

func (services *Services) ValidateSecurity(
	ctx context.Context,
	path string,
	mode string,
) domain.Envelope {
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return envelopeForError("gds validate "+mode, path, err)
	}
	tracked, err := services.Git.TrackedPaths(ctx, info.WorktreeRoot)
	if err != nil {
		return envelopeForError("gds validate "+mode, path, err)
	}
	report, findings := securityscan.Scan(info.WorktreeRoot, tracked)
	if mode == "absolute-paths" {
		filtered := []domain.Finding{}
		for _, finding := range findings {
			if finding.Code == "GDS_PORTABLE_ABSOLUTE_PATH" ||
				finding.Code == "GDS_SECURITY_PATH_INVALID" {
				filtered = append(filtered, finding)
			}
		}
		findings = filtered
	}
	return domain.NewEnvelope(
		"gds validate "+mode, classifyFindings(findings), report, findings...,
	)
}

func (services *Services) ValidateVisibility(ctx context.Context, path string) domain.Envelope {
	envelope := services.GenerateRepository(ctx, path, false)
	envelope.Command = "gds validate visibility"
	return envelope
}

func (services *Services) ValidateSourceFreshness(ctx context.Context, path string) domain.Envelope {
	envelope := services.SourceStatus(ctx, path, "")
	envelope.Command = "gds validate source-freshness"
	return envelope
}

func (services *Services) ValidateReproducibility(ctx context.Context, path string) domain.Envelope {
	first := services.GenerateRepository(ctx, path, false)
	if first.ExitClass != domain.ExitSuccess {
		first.Command = "gds validate reproducibility"
		return first
	}
	second := services.GenerateRepository(ctx, path, false)
	if second.ExitClass != domain.ExitSuccess {
		second.Command = "gds validate reproducibility"
		return second
	}
	firstBytes, firstErr := json.Marshal(first.Data)
	secondBytes, secondErr := json.Marshal(second.Data)
	if firstErr != nil || secondErr != nil || string(firstBytes) != string(secondBytes) {
		return domain.NewEnvelope(
			"gds validate reproducibility", domain.ExitValidation, nil, domain.Finding{
				Code: "GDS_REPRODUCIBILITY_MISMATCH", Severity: domain.SeverityCritical,
				Message: "Identical canonical inputs did not produce byte-identical candidates.",
			},
		)
	}
	return domain.Success("gds validate reproducibility", map[string]any{
		"candidate_digest": fmt.Sprintf("sha256:%x", sha256.Sum256(firstBytes)),
		"runs":             2,
	})
}

func (services *Services) ValidateEstate(ctx context.Context, path string) domain.Envelope {
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return envelopeForError("gds validate estate", path, err)
	}
	summary, findings := services.Schemas.ValidateEstateTree(info.WorktreeRoot)
	return domain.NewEnvelope(
		"gds validate estate", classifyFindings(findings), summary, findings...,
	)
}

func (services *Services) ValidatePlan(_ context.Context, path string) domain.Envelope {
	if strings.TrimSpace(path) == "" {
		return domain.NewEnvelope("gds validate plan", domain.ExitInput, nil, domain.Finding{
			Code: "GDS_PLAN_PATH_REQUIRED", Severity: domain.SeverityHigh,
			Message: "--file must identify a plan JSON or YAML document.",
		})
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return envelopeForError("gds validate plan", path, err)
	}
	findings := services.Schemas.ValidateFile("plan", absolute)
	return domain.NewEnvelope(
		"gds validate plan", classifyFindings(findings),
		map[string]any{"path": absolute}, findings...,
	)
}

func (services *Services) InspectState(ctx context.Context, path string) domain.Envelope {
	if strings.TrimSpace(path) == "" {
		defaultPath, err := state.DefaultPath()
		if err != nil {
			return envelopeForError("gds state inspect", path, err)
		}
		path = defaultPath
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return envelopeForError("gds state inspect", path, err)
	}
	store, err := state.OpenReadOnly(ctx, absolute)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "no such file") {
			return domain.NewEnvelope(
				"gds state inspect", domain.ExitNotProven,
				map[string]any{"path": absolute, "created": false}, domain.Finding{
					Code: "GDS_STATE_NOT_INITIALIZED", Severity: domain.SeverityInfo,
					Message:  "The local GDS state database does not exist; inspection did not create it.",
					Evidence: map[string]any{"path": absolute},
				},
			)
		}
		return envelopeForError("gds state inspect", absolute, err)
	}
	defer store.Close()
	info, err := store.Info(ctx)
	if err != nil {
		return envelopeForError("gds state inspect", absolute, err)
	}
	summary, err := store.Summary(ctx)
	if err != nil {
		return envelopeForError("gds state inspect", absolute, err)
	}
	return domain.Success(
		"gds state inspect", StateInspectionData{Info: info, Summary: summary},
	)
}

func (services *Services) ValidateSkills(ctx context.Context, path string) domain.Envelope {
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return envelopeForError("gds validate skills", path, err)
	}
	outcome := skills.Validate(info.WorktreeRoot, services.Schemas)
	return domain.NewEnvelope(
		"gds validate skills", classifyFindings(outcome.Findings), outcome.Report,
		outcome.Findings...,
	)
}

func (services *Services) PackagePlugin(
	ctx context.Context,
	path string,
	plugin string,
) domain.Envelope {
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return envelopeForError("gds skill package", path, err)
	}
	candidate, findings := skills.BuildPackage(info.WorktreeRoot, plugin, services.Schemas)
	return domain.NewEnvelope(
		"gds skill package", classifyFindings(findings), candidate, findings...,
	)
}

func (services *Services) ValidatePlugins(ctx context.Context, path string) domain.Envelope {
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return envelopeForError("gds validate plugins", path, err)
	}
	catalog := skills.Validate(info.WorktreeRoot, services.Schemas)
	findings := append([]domain.Finding{}, catalog.Findings...)
	data := PluginValidationData{}
	if len(catalog.Findings) == 0 {
		for _, plugin := range catalog.Registry.Plugins {
			candidate, pluginFindings := skills.BuildPackage(
				info.WorktreeRoot, plugin.ID, services.Schemas,
			)
			data.Packages = append(data.Packages, candidate)
			findings = append(findings, pluginFindings...)
		}
	}
	return domain.NewEnvelope(
		"gds validate plugins", classifyFindings(findings), data, findings...,
	)
}

func (services *Services) ValidateHarness(
	ctx context.Context,
	path string,
	harnessID string,
) domain.Envelope {
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return envelopeForError("gds validate harnesses", path, err)
	}
	var report any
	var findings []domain.Finding
	switch harnessID {
	case "all":
		report, findings = harness.ValidateAll(info.WorktreeRoot, services.Schemas)
	case "selected":
		selected, selectionFindings := services.estateSelectedHarnesses(info.WorktreeRoot)
		if len(selectionFindings) != 0 {
			return domain.NewEnvelope(
				"gds validate harnesses", classifyFindings(selectionFindings), nil, selectionFindings...,
			)
		}
		report, findings = harness.ValidateSelected(info.WorktreeRoot, selected, services.Schemas)
	default:
		report, findings = harness.Validate(info.WorktreeRoot, harnessID, services.Schemas)
	}
	return domain.NewEnvelope(
		"gds validate harnesses", classifyFindings(findings), report, findings...,
	)
}

// estateSelectedHarnesses returns the sorted union of harnesses selected across
// every device descriptor in estate/devices. It is the owner-selected set the
// release gate must prove runtime evidence for (RVR-P2-009).
func (services *Services) estateSelectedHarnesses(root string) ([]string, []domain.Finding) {
	pattern := filepath.Join(root, "estate", "devices", "*.yaml")
	paths, err := filepath.Glob(pattern)
	if err != nil {
		return nil, []domain.Finding{{
			Code: "GDS_HARNESS_SELECTION_READ_FAILED", Severity: domain.SeverityHigh,
			Message:  "Cannot enumerate device descriptors for the selected harness set.",
			Evidence: map[string]any{"pattern": pattern, "error": err.Error()},
		}}
	}
	union := map[string]struct{}{}
	var findings []domain.Finding
	for _, path := range paths {
		descriptor, deviceFindings := workspace.LoadDevice(path, services.Schemas)
		if len(deviceFindings) != 0 {
			findings = append(findings, deviceFindings...)
			continue
		}
		for _, id := range descriptor.Harnesses {
			union[id] = struct{}{}
		}
	}
	if len(findings) != 0 {
		return nil, findings
	}
	if len(union) == 0 {
		return nil, []domain.Finding{{
			Code: "GDS_HARNESS_SELECTION_EMPTY", Severity: domain.SeverityHigh,
			Message:  "No device descriptor selects any harness; the release gate cannot derive a required set.",
			Evidence: map[string]any{"pattern": pattern},
		}}
	}
	selected := make([]string, 0, len(union))
	for id := range union {
		selected = append(selected, id)
	}
	sort.Strings(selected)
	return selected, harness.ValidateDeviceSelection(selected)
}

func (services *Services) ValidateHarnessStatic(
	ctx context.Context,
	path string,
	harnessID string,
) domain.Envelope {
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return envelopeForError("gds validate harnesses", path, err)
	}
	var report any
	var findings []domain.Finding
	if harnessID == "all" {
		report, findings = harness.ValidateStaticAll(info.WorktreeRoot, services.Schemas)
	} else {
		report, findings = harness.ValidateStatic(info.WorktreeRoot, harnessID, services.Schemas)
	}
	return domain.NewEnvelope(
		"gds validate harnesses", classifyFindings(findings), report, findings...,
	)
}

// serenaPosture reads what the repository's own anchor declares about Serena
// state in its tree.
//
// An unreadable anchor falls back to the strict posture rather than to the
// permissive one. The opt-out has to be stated to take effect; inferring it from
// a file that failed to parse would let a broken anchor disable a gate silently,
// which is the failure mode this whole change exists to remove.
func (services *Services) serenaPosture(root string) memory.Posture {
	anchor, findings := manifest.NewLoader(services.Schemas).LoadRepository(root)
	if len(findings) != 0 {
		return memory.StrictPosture
	}
	return memory.Posture{
		Enabled:            anchor.Agent.Serena.Enabled,
		ProvenanceRequired: anchor.Agent.Serena.ProvenanceRequired,
	}
}

func (services *Services) ValidateMemories(
	ctx context.Context,
	path string,
) domain.Envelope {
	return services.validateMemories(ctx, path, "gds validate memories")
}

func (services *Services) ValidateMemoryCommand(
	ctx context.Context,
	path string,
) domain.Envelope {
	return services.validateMemories(ctx, path, "gds memory validate")
}

func (services *Services) validateMemories(
	ctx context.Context,
	path string,
	command string,
) domain.Envelope {
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return envelopeForError(command, path, err)
	}
	report, findings := memory.ValidateWithPosture(
		info.WorktreeRoot, services.Schemas, services.serenaPosture(info.WorktreeRoot),
		func(commit string) (time.Time, bool) {
			return services.Git.CommitTime(ctx, info.WorktreeRoot, commit)
		})
	return domain.NewEnvelope(
		command, classifyFindings(findings), report, findings...,
	)
}

func (services *Services) ReadMemory(
	ctx context.Context,
	path string,
	name string,
) domain.Envelope {
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return envelopeForError("gds memory read", path, err)
	}
	document, findings := memory.Read(info.WorktreeRoot, name, services.Schemas)
	if document.Metadata.ScopeID != "" {
		sourcePaths, sourceFindings := memory.SourcePaths(
			info.WorktreeRoot, name, services.Schemas,
		)
		findings = append(findings, sourceFindings...)
		if len(sourceFindings) == 0 {
			committedSource, sourceErr := services.Git.CommittedSourceOID(
				ctx, info.WorktreeRoot, sourcePaths,
			)
			switch {
			case sourceErr != nil:
				findings = append(findings, domain.Finding{
					Code: "GDS_MEMORY_COMMITTED_SOURCE_NOT_PROVEN", Severity: domain.SeverityHigh,
					Message:  "Memory provenance cannot be tied to committed source inputs.",
					Evidence: map[string]any{"name": name, "error": sourceErr.Error()},
				})
			case document.Metadata.SourceCommit != committedSource:
				findings = append(findings, domain.Finding{
					Code: "GDS_MEMORY_SOURCE_COMMIT_MISMATCH", Severity: domain.SeverityHigh,
					Message: "Memory source commit differs from the latest committed source input.",
					Evidence: map[string]any{
						"name": name, "expected": committedSource,
						"observed": document.Metadata.SourceCommit,
					},
				})
			}
		}
	}
	return domain.NewEnvelope(
		"gds memory read", classifyFindings(findings), document, findings...,
	)
}

func (services *Services) GenerateMemory(
	ctx context.Context,
	path string,
	name string,
) domain.Envelope {
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return envelopeForError("gds memory generate", path, err)
	}
	sourcePaths, findings := memory.SourcePaths(info.WorktreeRoot, name, services.Schemas)
	if len(findings) != 0 {
		return domain.NewEnvelope(
			"gds memory generate", classifyFindings(findings), nil, findings...,
		)
	}
	sourceCommit, err := services.Git.CommittedSourceOID(ctx, info.WorktreeRoot, sourcePaths)
	if err != nil {
		finding := domain.Finding{
			Code: "GDS_MEMORY_COMMITTED_SOURCE_NOT_PROVEN", Severity: domain.SeverityHigh,
			Message:  "Commit all declared memory sources before building a candidate.",
			Evidence: map[string]any{"name": name, "error": err.Error()},
		}
		return domain.NewEnvelope(
			"gds memory generate", domain.ExitNotProven, nil, finding,
		)
	}
	candidate, findings := memory.Generate(
		info.WorktreeRoot, name, sourceCommit, services.Schemas,
	)
	return domain.NewEnvelope(
		"gds memory generate", classifyFindings(findings), candidate, findings...,
	)
}

// VerifyMemory records that a memory's body was read against its current
// sources and still holds. Same refusals as GenerateMemory, plus the ordering
// rule the validator enforces: a verification cannot be dated before the
// source commit it claims to have read.
func (services *Services) VerifyMemory(
	ctx context.Context,
	path string,
	name string,
) domain.Envelope {
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return envelopeForError("gds memory verify", path, err)
	}
	sourcePaths, findings := memory.SourcePaths(info.WorktreeRoot, name, services.Schemas)
	if len(findings) != 0 {
		return domain.NewEnvelope(
			"gds memory verify", classifyFindings(findings), nil, findings...,
		)
	}
	sourceCommit, err := services.Git.CommittedSourceOID(ctx, info.WorktreeRoot, sourcePaths)
	if err != nil {
		finding := domain.Finding{
			Code: "GDS_MEMORY_COMMITTED_SOURCE_NOT_PROVEN", Severity: domain.SeverityHigh,
			Message:  "Commit all declared memory sources before verifying.",
			Evidence: map[string]any{"name": name, "error": err.Error()},
		}
		return domain.NewEnvelope("gds memory verify", domain.ExitNotProven, nil, finding)
	}
	committedAt, ok := services.Git.CommitTime(ctx, info.WorktreeRoot, sourceCommit)
	if !ok {
		finding := domain.Finding{
			Code: "GDS_MEMORY_COMMITTED_SOURCE_NOT_PROVEN", Severity: domain.SeverityHigh,
			Message:  "Cannot read the source commit time, so a verification cannot be ordered against it.",
			Evidence: map[string]any{"name": name, "source_commit": sourceCommit},
		}
		return domain.NewEnvelope("gds memory verify", domain.ExitNotProven, nil, finding)
	}
	candidate, findings := memory.Verify(
		info.WorktreeRoot, name, sourceCommit, committedAt, services.Now(), services.Schemas,
	)
	return domain.NewEnvelope(
		"gds memory verify", classifyFindings(findings), candidate, findings...,
	)
}

func (services *Services) DetectHarness(
	ctx context.Context,
	path string,
	harnessID string,
) domain.Envelope {
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return envelopeForError("gds harness detect", path, err)
	}
	var report any
	var findings []domain.Finding
	if harnessID == "all" {
		report, findings = harness.DetectAll(ctx, info.WorktreeRoot, services.Schemas)
	} else {
		report, findings = harness.Detect(ctx, info.WorktreeRoot, harnessID, services.Schemas)
	}
	return domain.NewEnvelope(
		"gds harness detect", classifyFindings(findings), report, findings...,
	)
}

func (services *Services) ValidateAll(
	ctx context.Context,
	path string,
	fixtures bool,
) domain.Envelope {
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return envelopeForError("gds validate", path, err)
	}
	repository := services.ValidateRepository(ctx, info.WorktreeRoot)
	outcome := services.Context.Resolve(ctx, info.WorktreeRoot)
	if !hasRole(outcome.Context.Repository.Roles, "control-plane") {
		return domain.NewEnvelope(
			"gds validate",
			repository.ExitClass,
			map[string]any{"repository": repository.Data},
			repository.Findings...,
		)
	}
	schema := services.ValidateSchemas(ctx, info.WorktreeRoot, fixtures)
	estateResult := services.ValidateEstate(ctx, info.WorktreeRoot)
	policyResult := services.ValidatePolicies(ctx, info.WorktreeRoot)
	contextResult := services.ValidateContext(ctx, info.WorktreeRoot)
	projectionResult := services.ValidateProjections(ctx, info.WorktreeRoot)
	securityResult := services.ValidateSecurity(ctx, info.WorktreeRoot, "security")
	reproducibilityResult := services.ValidateReproducibility(ctx, info.WorktreeRoot)
	skillResult := services.ValidateSkills(ctx, info.WorktreeRoot)
	pluginResult := services.ValidatePlugins(ctx, info.WorktreeRoot)
	harnessResult := services.ValidateHarnessStatic(ctx, info.WorktreeRoot, "all")
	memoryResult := services.ValidateMemories(ctx, info.WorktreeRoot)
	findings := append([]domain.Finding{}, repository.Findings...)
	findings = append(findings, schema.Findings...)
	findings = append(findings, estateResult.Findings...)
	findings = append(findings, policyResult.Findings...)
	findings = append(findings, contextResult.Findings...)
	findings = append(findings, projectionResult.Findings...)
	findings = append(findings, securityResult.Findings...)
	findings = append(findings, reproducibilityResult.Findings...)
	findings = append(findings, skillResult.Findings...)
	findings = append(findings, pluginResult.Findings...)
	findings = append(findings, harnessResult.Findings...)
	findings = append(findings, memoryResult.Findings...)
	class := strongestClass(
		repository.ExitClass, schema.ExitClass, estateResult.ExitClass,
		policyResult.ExitClass, contextResult.ExitClass, projectionResult.ExitClass,
		securityResult.ExitClass, reproducibilityResult.ExitClass,
		skillResult.ExitClass, pluginResult.ExitClass,
		harnessResult.ExitClass, classifyFindings(findings),
		memoryResult.ExitClass,
	)
	return domain.NewEnvelope("gds validate", class, map[string]any{
		"repository": repository.Data, "schemas": schema.Data, "estate": estateResult.Data,
		"policies": policyResult.Data, "context": contextResult.Data,
		"projections": projectionResult.Data, "security": securityResult.Data,
		"reproducibility": reproducibilityResult.Data,
		"skills":          skillResult.Data,
		"plugins":         pluginResult.Data,
		"harnesses":       harnessResult.Data,
		"memories":        memoryResult.Data,
	}, findings...)
}

func (services *Services) Doctor(
	ctx context.Context,
	path string,
	fixtures bool,
) domain.Envelope {
	outcome := services.Context.Resolve(ctx, path)
	checks := []DoctorCheck{{Name: "context", Result: string(outcome.Class)}}
	findings := append([]domain.Finding{}, outcome.Findings...)

	var status *gitprovider.Status
	inspectedStatus, statusErr := services.Git.InspectStatus(ctx, path)
	if statusErr != nil {
		checks = append(checks, DoctorCheck{Name: "git-status", Result: "not-proven"})
		findings = append(findings, domain.Finding{
			Code:     "GDS_DOCTOR_GIT_STATUS_NOT_PROVEN",
			Severity: domain.SeverityHigh,
			Message:  fmt.Sprintf("Git status is not proven: %v", statusErr),
			Evidence: map[string]any{"path": path},
		})
	} else {
		checks = append(checks, DoctorCheck{Name: "git-status", Result: "pass"})
		status = &inspectedStatus
	}

	if outcome.Context.Repository.ID != "" && hasRole(outcome.Context.Repository.Roles, "control-plane") {
		schemaResult := services.ValidateSchemas(
			ctx, outcome.Context.Workspace.GitWorktreeRoot, fixtures,
		)
		checks = append(checks, DoctorCheck{
			Name: "schemas", Result: resultLabel(schemaResult.ExitClass),
		})
		findings = append(findings, schemaResult.Findings...)
		estateResult := services.ValidateEstate(
			ctx, outcome.Context.Workspace.GitWorktreeRoot,
		)
		checks = append(checks, DoctorCheck{
			Name: "estate", Result: resultLabel(estateResult.ExitClass),
		})
		findings = append(findings, estateResult.Findings...)
		skillResult := services.ValidateSkills(
			ctx, outcome.Context.Workspace.GitWorktreeRoot,
		)
		checks = append(checks, DoctorCheck{
			Name: "skills", Result: resultLabel(skillResult.ExitClass),
		})
		findings = append(findings, skillResult.Findings...)
		pluginResult := services.ValidatePlugins(
			ctx, outcome.Context.Workspace.GitWorktreeRoot,
		)
		checks = append(checks, DoctorCheck{
			Name: "plugins", Result: resultLabel(pluginResult.ExitClass),
		})
		findings = append(findings, pluginResult.Findings...)
		harnessResult := services.ValidateHarness(
			ctx, outcome.Context.Workspace.GitWorktreeRoot, "all",
		)
		checks = append(checks, DoctorCheck{
			Name: "harness-registry", Result: resultLabel(harnessResult.ExitClass),
		})
		findings = append(findings, harnessResult.Findings...)
		memoryResult := services.ValidateMemories(
			ctx, outcome.Context.Workspace.GitWorktreeRoot,
		)
		checks = append(checks, DoctorCheck{
			Name: "serena-memories", Result: resultLabel(memoryResult.ExitClass),
		})
		findings = append(findings, memoryResult.Findings...)
	} else if outcome.Context.Workspace.GitWorktreeRoot != "" {
		repositoryResult := services.ValidateRepository(ctx, outcome.Context.Workspace.GitWorktreeRoot)
		checks = append(checks, DoctorCheck{
			Name: "repository-anchor", Result: resultLabel(repositoryResult.ExitClass),
		})
		findings = append(findings, repositoryResult.Findings...)
	}

	class := strongestClass(outcome.Class, classifyFindings(findings))
	envelope := domain.NewEnvelope("gds doctor", class, DoctorData{
		Checks: checks, Context: outcome.Context, Status: status,
	}, deduplicateFindings(findings)...)
	if outcome.Context.Repository.ID != "" {
		envelope.Scope["repository_id"] = outcome.Context.Repository.ID
	}
	return envelope
}

func envelopeForError(command, path string, err error) domain.Envelope {
	class := domain.ExitNotProven
	code := "GDS_DEPENDENCY_NOT_PROVEN"
	if errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "not a directory") {
		class = domain.ExitInput
		code = "GDS_INPUT_INVALID"
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		code = "GDS_OPERATION_CANCELED"
	}
	return domain.NewEnvelope(command, class, map[string]any{"target": path}, domain.Finding{
		Code: code, Severity: domain.SeverityHigh, Message: err.Error(),
		Evidence: map[string]any{"path": path},
	})
}

func classifyFindings(findings []domain.Finding) domain.ExitClass {
	return domain.ClassifyFindings(findings)
}

func strongestClass(classes ...domain.ExitClass) domain.ExitClass {
	priority := map[domain.ExitClass]int{
		domain.ExitSuccess: 0, domain.ExitNotProven: 1, domain.ExitValidation: 2,
		domain.ExitInput: 3, domain.ExitStale: 4, domain.ExitApproval: 4,
		domain.ExitAuthorization: 5, domain.ExitConflict: 5, domain.ExitPolicy: 5,
		domain.ExitPartial: 5, domain.ExitProviderTransient: 5,
		domain.ExitUnsupported: 5, domain.ExitSecurity: 6, domain.ExitInternal: 7,
	}
	strongest := domain.ExitSuccess
	for _, class := range classes {
		if priority[class] > priority[strongest] {
			strongest = class
		}
	}
	return strongest
}

func resultLabel(class domain.ExitClass) string {
	if class == domain.ExitSuccess {
		return "pass"
	}
	return string(class)
}

func hasRole(roles []string, expected string) bool {
	for _, role := range roles {
		if role == expected {
			return true
		}
	}
	return false
}

func deduplicateFindings(findings []domain.Finding) []domain.Finding {
	seen := map[string]struct{}{}
	result := make([]domain.Finding, 0, len(findings))
	for _, finding := range findings {
		key := finding.Code + "\x00" + finding.Message
		if _, duplicate := seen[key]; duplicate {
			continue
		}
		seen[key] = struct{}{}
		result = append(result, finding)
	}
	return result
}

func validateDiscoveryOptions(options DiscoveryOptions) *domain.Finding {
	invalid := ""
	switch {
	case options.MaxDepth < 1:
		invalid = "--max-depth must be at least 1"
	case options.MaxRepositories < 1:
		invalid = "--max-repositories must be at least 1"
	case options.Concurrency < 1 || options.Concurrency > 16:
		invalid = "--concurrency must be between 1 and 16"
	}
	if invalid == "" {
		return nil
	}
	return &domain.Finding{
		Code:     "GDS_DISCOVERY_OPTIONS_INVALID",
		Severity: domain.SeverityHigh,
		Message:  invalid + ".",
	}
}

func DefaultClock() time.Time { return time.Now() }
