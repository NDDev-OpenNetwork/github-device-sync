// Package contextresolver deterministically resolves the current GDS scope
// before skill or workflow routing.
package contextresolver

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/anchor"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/capabilities"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/estateregistry"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/freshness"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/manifest"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

type Resolver struct {
	git       *gitprovider.Runner
	manifests *manifest.Loader
	schemas   *validation.Set
	prover    *CanonicalPolicyProver
	getenv    func(string) string
	userHome  func() (string, error)
	now       func() time.Time
}

const maxAppliedContextFileBytes = 4 << 20

type Outcome struct {
	Context  Context
	Findings []domain.Finding
	Class    domain.ExitClass
}

type Context struct {
	Workspace    WorkspaceContext     `json:"workspace"`
	Repository   RepositoryContext    `json:"repository"`
	Mode         ModeContext          `json:"mode"`
	Estate       EstateContext        `json:"estate"`
	Policy       PolicyContext        `json:"policy"`
	Agent        AgentContext         `json:"agent"`
	Boundaries   []BoundaryContext    `json:"boundaries"`
	Capabilities Capabilities         `json:"capabilities"`
	Freshness    freshness.Assessment `json:"freshness"`
}

type WorkspaceContext struct {
	Path            string `json:"path"`
	GitWorktreeRoot string `json:"git_worktree_root,omitempty"`
	CommonGitDir    string `json:"common_git_dir,omitempty"`
}

type RepositoryContext struct {
	ID                 string   `json:"id,omitempty"`
	Roles              []string `json:"roles"`
	Lifecycle          string   `json:"lifecycle,omitempty"`
	VisibilityContract string   `json:"visibility_contract,omitempty"`
	ContextProfile     string   `json:"context_profile,omitempty"`
}

type ModeContext struct {
	Kind           string `json:"kind"`
	SuperprojectID string `json:"superproject_id,omitempty"`
}

type EstateContext struct {
	Registered bool   `json:"registered"`
	Root       string `json:"root,omitempty"`
}

type PolicyContext struct {
	BundleLockPresent bool   `json:"bundle_lock_present"`
	Provenance        string `json:"provenance"`
	BundleVersion     string `json:"bundle_version,omitempty"`
	BundleDigest      string `json:"bundle_digest,omitempty"`
	InputDigest       string `json:"input_digest,omitempty"`
	OutputDigest      string `json:"output_digest,omitempty"`
	Digest            string `json:"digest,omitempty"`
}

type bundleLockDocument struct {
	SchemaVersion int `json:"schema_version"`
	Bundle        struct {
		Version                   string `json:"version"`
		ReleaseSequence           int    `json:"release_sequence"`
		Channel                   string `json:"channel"`
		SourceCommit              string `json:"source_commit"`
		SourceTreeDigest          string `json:"source_tree_digest,omitempty"`
		Digest                    string `json:"digest"`
		AttestationIdentityDigest string `json:"attestation_identity_digest,omitempty"`
	} `json:"bundle"`
	Projection struct {
		InputDigest  string `json:"input_digest"`
		OutputDigest string `json:"output_digest"`
		Files        []struct {
			Path   string `json:"path"`
			Digest string `json:"digest"`
		} `json:"files"`
	} `json:"projection"`
}

type AgentContext struct {
	Harness       string   `json:"harness"`
	SkillProfiles []string `json:"skill_profiles"`
}

type BoundaryContext struct {
	RepositoryID     string `json:"repository_id,omitempty"`
	Path             string `json:"path"`
	MutationBoundary bool   `json:"mutation_boundary"`
}

type Capabilities = capabilities.Set
type Capability = capabilities.State

func NewResolver(
	git *gitprovider.Runner,
	manifests *manifest.Loader,
	schemas *validation.Set,
	prover *CanonicalPolicyProver,
) *Resolver {
	return &Resolver{
		git: git, manifests: manifests, schemas: schemas, prover: prover,
		getenv: os.Getenv, userHome: os.UserHomeDir, now: time.Now,
	}
}

func (resolver *Resolver) Resolve(ctx context.Context, path string) Outcome {
	resolvedPath, err := resolveDirectory(path)
	if err != nil {
		return Outcome{
			Context: Context{
				Workspace: WorkspaceContext{Path: path},
				Policy:    PolicyContext{Provenance: "not-proven"},
			},
			Findings: []domain.Finding{{
				Code:     "GDS_CONTEXT_PATH_INVALID",
				Severity: domain.SeverityHigh,
				Message:  fmt.Sprintf("Cannot resolve context path %s: %v", path, err),
				Evidence: map[string]any{"path": path},
			}},
			Class: domain.ExitInput,
		}
	}
	result := Context{
		Workspace:  WorkspaceContext{Path: resolvedPath},
		Repository: RepositoryContext{Roles: []string{}},
		Mode:       ModeContext{Kind: "unknown"},
		Policy:     PolicyContext{Provenance: "not-proven"},
		Agent: AgentContext{
			Harness: resolver.getenv("GDS_HARNESS"), SkillProfiles: []string{"core"},
		},
		Boundaries:   []BoundaryContext{},
		Capabilities: capabilities.ContextSet(),
	}
	now := resolver.now().UTC()
	result.Freshness, _ = freshness.DefaultPolicy().Evaluate(
		freshness.LocalContext, now, now, "local:git-and-projections", true,
	)
	if result.Agent.Harness == "" {
		result.Agent.Harness = "unknown"
	}

	info, err := resolver.git.RepositoryInfo(ctx, resolvedPath)
	if err != nil {
		return Outcome{
			Context: result,
			Findings: []domain.Finding{{
				Code:     "GDS_CONTEXT_NO_REPOSITORY",
				Severity: domain.SeverityHigh,
				Message:  fmt.Sprintf("No inspectable Git repository contains %s.", resolvedPath),
				Evidence: map[string]any{"path": resolvedPath, "error": err.Error()},
			}},
			Class: domain.ExitNotProven,
		}
	}
	result.Workspace.GitWorktreeRoot = info.WorktreeRoot
	result.Workspace.CommonGitDir = info.CommonGitDir
	result.Mode.Kind = "standalone"
	result.Boundaries = append(result.Boundaries, BoundaryContext{
		Path: info.WorktreeRoot, MutationBoundary: true,
	})

	findings := []domain.Finding{}
	var repositoryAnchor *domain.RepositoryAnchor
	anchorExists, anchorStatErr := manifest.Exists(info.WorktreeRoot)
	if anchorStatErr != nil {
		findings = append(findings, domain.Finding{
			Code:     "GDS_CONTEXT_MANIFEST_UNREADABLE",
			Severity: domain.SeverityHigh,
			Message:  fmt.Sprintf("Cannot inspect the repository anchor: %v", anchorStatErr),
			Evidence: map[string]any{"root": info.WorktreeRoot},
		})
	} else if !anchorExists {
		findings = append(findings, domain.Finding{
			Code:     "GDS_CONTEXT_MANIFEST_MISSING",
			Severity: domain.SeverityMedium,
			Message:  "The Git repository has no .gds/repository.yaml anchor.",
			Evidence: map[string]any{"root": info.WorktreeRoot},
		})
	} else {
		anchorValue, anchorFindings := resolver.manifests.LoadRepository(info.WorktreeRoot)
		findings = append(findings, anchorFindings...)
		if len(anchorFindings) == 0 {
			applyAnchor(&result, anchorValue)
			repositoryAnchor = &anchorValue
		}
	}

	if info.SuperprojectRoot != "" {
		result.Mode.Kind = "embedded-submodule"
		superprojectBoundary := BoundaryContext{
			Path: info.SuperprojectRoot, MutationBoundary: true,
		}
		if exists, _ := manifest.Exists(info.SuperprojectRoot); exists {
			superproject, superprojectFindings := resolver.manifests.LoadRepository(info.SuperprojectRoot)
			if len(superprojectFindings) == 0 {
				result.Mode.SuperprojectID = superproject.Repository.ID
				superprojectBoundary.RepositoryID = superproject.Repository.ID
			}
		}
		result.Boundaries = append(result.Boundaries, superprojectBoundary)
	}

	resolveEstate(resolver, &result, &findings)
	policyFindingCount := len(findings)
	document := resolveAppliedPolicy(resolver, &result, &findings, info.WorktreeRoot)
	if document != nil && repositoryAnchor != nil && result.Policy.Digest != "" &&
		len(findings) == policyFindingCount {
		provenanceFindings := resolver.prover.Verify(
			ctx, info.WorktreeRoot, result.Estate.Root, *repositoryAnchor, *document,
		)
		findings = append(findings, provenanceFindings...)
		if len(provenanceFindings) == 0 {
			result.Policy.Provenance = "verified"
		}
	}

	return Outcome{Context: result, Findings: findings, Class: domain.ClassifyFindings(findings)}
}

func resolveAppliedPolicy(
	resolver *Resolver,
	result *Context,
	findings *[]domain.Finding,
	root string,
) *bundleLockDocument {
	bundleLock := filepath.Join(root, ".gds", "bundle.lock.yaml")
	info, err := os.Lstat(bundleLock)
	if errors.Is(err, os.ErrNotExist) {
		*findings = append(*findings, contextFinding(
			"GDS_CONTEXT_BUNDLE_MISSING", domain.SeverityMedium,
			"The repository has no pinned .gds/bundle.lock.yaml yet.", bundleLock, nil,
		))
		return nil
	}
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
		info.Size() > maxAppliedContextFileBytes {
		*findings = append(*findings, contextFinding(
			"GDS_CONTEXT_BUNDLE_UNREADABLE", domain.SeverityHigh,
			"The repository bundle lock is not a readable regular file.", bundleLock, err,
		))
		return nil
	}
	value, err := serialization.DecodeFile(bundleLock)
	if err != nil {
		*findings = append(*findings, contextFinding(
			"GDS_CONTEXT_BUNDLE_INVALID", domain.SeverityHigh,
			"The repository bundle lock cannot be decoded.", bundleLock, err,
		))
		return nil
	}
	if schemaFindings := resolver.schemas.Validate("bundle-lock", value, bundleLock); len(schemaFindings) != 0 {
		*findings = append(*findings, schemaFindings...)
		return nil
	}
	raw, err := os.ReadFile(bundleLock)
	if err != nil {
		*findings = append(*findings, contextFinding(
			"GDS_CONTEXT_BUNDLE_UNREADABLE", domain.SeverityHigh,
			"The repository bundle lock cannot be read.", bundleLock, err,
		))
		return nil
	}
	var document bundleLockDocument
	if err := serialization.DecodeInto(bundleLock, raw, &document); err != nil {
		*findings = append(*findings, contextFinding(
			"GDS_CONTEXT_BUNDLE_INVALID", domain.SeverityHigh,
			"The repository bundle lock cannot bind to its typed contract.", bundleLock, err,
		))
		return nil
	}
	result.Policy.BundleLockPresent = true
	result.Policy.BundleVersion = document.Bundle.Version
	result.Policy.BundleDigest = document.Bundle.Digest
	result.Policy.InputDigest = document.Projection.InputDigest
	result.Policy.OutputDigest = document.Projection.OutputDigest

	for _, file := range document.Projection.Files {
		path := filepath.Join(root, filepath.FromSlash(file.Path))
		fileInfo, fileErr := os.Lstat(path)
		if fileErr != nil || fileInfo.Mode()&os.ModeSymlink != 0 || !fileInfo.Mode().IsRegular() ||
			fileInfo.Size() > maxAppliedContextFileBytes {
			*findings = append(*findings, contextFinding(
				"GDS_CONTEXT_PROJECTION_NOT_APPLIED", domain.SeverityHigh,
				"A bundle-locked projection is missing or not a regular file.", path, fileErr,
			))
			continue
		}
		content, readErr := os.ReadFile(path)
		if readErr != nil {
			*findings = append(*findings, contextFinding(
				"GDS_CONTEXT_PROJECTION_NOT_APPLIED", domain.SeverityHigh,
				"A bundle-locked projection cannot be read.", path, readErr,
			))
			continue
		}
		observed := fmt.Sprintf("sha256:%x", sha256.Sum256(content))
		if observed != file.Digest {
			*findings = append(*findings, domain.Finding{
				Code: "GDS_CONTEXT_PROJECTION_DIGEST_MISMATCH", Severity: domain.SeverityHigh,
				Message: "A bundle-locked projection differs from its applied digest.",
				Evidence: map[string]any{
					"path": path, "expected": file.Digest, "observed": observed,
				},
			})
			continue
		}
		if file.Path == ".gds/compiled-policy.json" {
			resolveCompiledPolicy(resolver, result, findings, path, content, document.Bundle.Version)
		}
	}
	if result.Policy.Digest == "" {
		*findings = append(*findings, contextFinding(
			"GDS_CONTEXT_POLICY_NOT_APPLIED", domain.SeverityHigh,
			"The bundle lock does not prove an applied compiled policy.", bundleLock, nil,
		))
	}
	return &document
}

func resolveCompiledPolicy(
	resolver *Resolver,
	result *Context,
	findings *[]domain.Finding,
	path string,
	content []byte,
	bundleVersion string,
) {
	value, err := serialization.Decode(path, content)
	if err != nil {
		*findings = append(*findings, contextFinding(
			"GDS_CONTEXT_POLICY_INVALID", domain.SeverityHigh,
			"The applied compiled policy cannot be decoded.", path, err,
		))
		return
	}
	if schemaFindings := resolver.schemas.Validate("compiled-policy", value, path); len(schemaFindings) != 0 {
		*findings = append(*findings, schemaFindings...)
		return
	}
	object, ok := value.(map[string]any)
	metadata, metadataOK := object["compiled_policy"].(map[string]any)
	if !ok || !metadataOK || metadata["repository_id"] != result.Repository.ID ||
		metadata["bundle_version"] != bundleVersion {
		*findings = append(*findings, contextFinding(
			"GDS_CONTEXT_POLICY_IDENTITY_MISMATCH", domain.SeverityHigh,
			"The applied compiled policy identity or bundle version is inconsistent.", path, nil,
		))
		return
	}
	digest, ok := metadata["digest"].(string)
	if !ok || digest == "" {
		*findings = append(*findings, contextFinding(
			"GDS_CONTEXT_POLICY_INVALID", domain.SeverityHigh,
			"The applied compiled policy has no digest.", path, nil,
		))
		return
	}
	result.Policy.Digest = digest
}

func contextFinding(
	code string,
	severity domain.Severity,
	message string,
	path string,
	err error,
) domain.Finding {
	evidence := map[string]any{"path": path}
	if err != nil {
		evidence["error"] = err.Error()
	}
	return domain.Finding{Code: code, Severity: severity, Message: message, Evidence: evidence}
}

func applyAnchor(result *Context, anchor domain.RepositoryAnchor) {
	result.Repository = RepositoryContext{
		ID: anchor.Repository.ID, Roles: append([]string(nil), anchor.Repository.Roles...),
		Lifecycle:          anchor.Repository.Lifecycle,
		VisibilityContract: anchor.Classification.VisibilityContract,
		ContextProfile:     anchor.Agent.ContextProfile,
	}
	result.Boundaries[0].RepositoryID = anchor.Repository.ID
	profiles := map[string]struct{}{"core": {}}
	for _, role := range anchor.Repository.Roles {
		switch role {
		case "control-plane":
			profiles["estate-admin"] = struct{}{}
		case "module":
			profiles["module"] = struct{}{}
		case "portfolio-registry":
			profiles["portfolio"] = struct{}{}
		}
	}
	result.Agent.SkillProfiles = result.Agent.SkillProfiles[:0]
	for profile := range profiles {
		result.Agent.SkillProfiles = append(result.Agent.SkillProfiles, profile)
	}
	sort.Strings(result.Agent.SkillProfiles)
}

func resolveEstate(resolver *Resolver, result *Context, findings *[]domain.Finding) {
	configuredRoot := resolver.getenv("GDS_ESTATE_ROOT")
	if configuredRoot != "" {
		root, err := resolveDirectory(configuredRoot)
		if err != nil {
			*findings = append(*findings, domain.Finding{
				Code:     "GDS_CONTEXT_ESTATE_NOT_REGISTERED",
				Severity: domain.SeverityHigh,
				Message:  fmt.Sprintf("Configured GDS_ESTATE_ROOT is invalid: %v", err),
				Evidence: map[string]any{"path": configuredRoot},
			})
			return
		}
		exists, err := manifest.Exists(root)
		if err != nil || !exists {
			*findings = append(*findings, domain.Finding{
				Code:     "GDS_CONTEXT_ESTATE_NOT_REGISTERED",
				Severity: domain.SeverityHigh,
				Message:  "Configured GDS_ESTATE_ROOT has no readable repository anchor.",
				Evidence: map[string]any{"path": root},
			})
			return
		}
		anchor, anchorFindings := resolver.manifests.LoadRepository(root)
		if len(anchorFindings) != 0 || !hasRole(anchor.Repository.Roles, "control-plane") {
			*findings = append(*findings, anchorFindings...)
			*findings = append(*findings, domain.Finding{
				Code:     "GDS_CONTEXT_ESTATE_NOT_REGISTERED",
				Severity: domain.SeverityHigh,
				Message:  "Configured GDS_ESTATE_ROOT is not a verified control-plane repository.",
				Evidence: map[string]any{"path": root},
			})
			return
		}
		result.Estate = EstateContext{Registered: true, Root: root}
		return
	}
	for _, role := range result.Repository.Roles {
		if role == "control-plane" {
			result.Estate = EstateContext{
				Registered: true, Root: result.Workspace.GitWorktreeRoot,
			}
			return
		}
	}
	registrationPath, err := estateregistry.DefaultPath(resolver.getenv, resolver.userHome)
	if err != nil {
		*findings = append(*findings, domain.Finding{
			Code:     "GDS_CONTEXT_ESTATE_NOT_REGISTERED",
			Severity: domain.SeverityHigh,
			Message:  fmt.Sprintf("Device-local estate registration path is invalid: %v", err),
		})
		return
	}
	if _, err := os.Lstat(registrationPath); errors.Is(err, os.ErrNotExist) {
		*findings = append(*findings, domain.Finding{
			Code:     "GDS_CONTEXT_ESTATE_NOT_REGISTERED",
			Severity: domain.SeverityMedium,
			Message:  "No trusted device-local estate registration was found for this repository.",
			Evidence: map[string]any{
				"repository_id":     result.Repository.ID,
				"registration_path": registrationPath,
			},
		})
		return
	} else if err != nil {
		*findings = append(*findings, domain.Finding{
			Code:     "GDS_CONTEXT_ESTATE_NOT_REGISTERED",
			Severity: domain.SeverityHigh,
			Message:  fmt.Sprintf("Device-local estate registration cannot be inspected: %v", err),
			Evidence: map[string]any{"registration_path": registrationPath},
		})
		return
	}
	registration, registrationFindings := estateregistry.Load(registrationPath, resolver.schemas)
	if len(registrationFindings) != 0 {
		*findings = append(*findings, registrationFindings...)
		*findings = append(*findings, domain.Finding{
			Code:     "GDS_CONTEXT_ESTATE_NOT_REGISTERED",
			Severity: domain.SeverityHigh,
			Message:  "Device-local estate registration is not trusted.",
			Evidence: map[string]any{"registration_path": registrationPath},
		})
		return
	}
	root := registration.Document.Estate.Root
	estateAnchor, anchorFindings := resolver.manifests.LoadRepository(root)
	if len(anchorFindings) != 0 ||
		estateAnchor.Repository.ID != registration.Document.Estate.RepositoryID ||
		!hasRole(estateAnchor.Repository.Roles, "control-plane") {
		*findings = append(*findings, anchorFindings...)
		*findings = append(*findings, domain.Finding{
			Code:     "GDS_CONTEXT_ESTATE_NOT_REGISTERED",
			Severity: domain.SeverityHigh,
			Message:  "Registered estate root is not the expected control-plane identity.",
			Evidence: map[string]any{
				"registration_path":      registrationPath,
				"root":                   root,
				"expected_repository_id": registration.Document.Estate.RepositoryID,
			},
		})
		return
	}
	anchorEvidence, err := anchor.Observe(root)
	if err != nil || anchorEvidence.File.State != "regular" ||
		anchorEvidence.File.ContentDigest != registration.Document.Estate.AnchorDigest {
		*findings = append(*findings, domain.Finding{
			Code:     "GDS_CONTEXT_ESTATE_NOT_REGISTERED",
			Severity: domain.SeverityHigh,
			Message:  "Registered estate anchor digest no longer matches the trusted locator.",
			Evidence: map[string]any{
				"registration_path":      registrationPath,
				"root":                   root,
				"expected_anchor_digest": registration.Document.Estate.AnchorDigest,
			},
		})
		return
	}
	result.Estate = EstateContext{Registered: true, Root: root}
}

func resolveDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory")
	}
	return filepath.Clean(resolved), nil
}

func hasRole(roles []string, expected string) bool {
	for _, role := range roles {
		if role == expected {
			return true
		}
	}
	return false
}
