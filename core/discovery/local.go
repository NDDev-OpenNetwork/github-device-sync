// Package discovery finds local Git boundaries without cloning or contacting a
// provider.
package discovery

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/manifest"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
)

type Options struct {
	MaxDepth        int
	MaxRepositories int
	Concurrency     int
	// IncludeArchived keeps repositories whose anchor declares
	// lifecycle: "archived". They are excluded by default: an archived
	// repository is read-only at the provider and observe-only in the estate, so
	// listing it beside live work is noise that grows with every project ever
	// retired. Nothing is hidden silently -- the inventory summary reports how
	// many were excluded.
	IncludeArchived bool
}

type Result struct {
	Root       string           `json:"root"`
	Boundaries []Boundary       `json:"boundaries"`
	Findings   []domain.Finding `json:"-"`
}

type Boundary struct {
	Path             string   `json:"path"`
	CommonGitDir     string   `json:"common_git_dir"`
	SuperprojectRoot string   `json:"superproject_root,omitempty"`
	RepositoryID     string   `json:"repository_id,omitempty"`
	Roles            []string `json:"roles"`
	Lifecycle        string   `json:"lifecycle,omitempty"`
	ProviderOwner    string   `json:"provider_owner,omitempty"`
	ProviderName     string   `json:"provider_name,omitempty"`
	AnchorState      string   `json:"anchor_state"`
}

type Local struct {
	git       *gitprovider.Runner
	manifests *manifest.Loader
}

type inspection struct {
	boundary Boundary
	findings []domain.Finding
}

func NewLocal(git *gitprovider.Runner, manifests *manifest.Loader) *Local {
	return &Local{git: git, manifests: manifests}
}

func (discovery *Local) Discover(
	ctx context.Context,
	root string,
	options Options,
) (Result, error) {
	resolvedRoot, err := canonicalDirectory(root)
	if err != nil {
		return Result{}, err
	}
	options = normalizeOptions(options)
	candidates, findings, err := findCandidates(ctx, resolvedRoot, options)
	if err != nil {
		return Result{}, err
	}

	jobs := make(chan string)
	results := make(chan inspection)
	var workers sync.WaitGroup
	for range options.Concurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for candidate := range jobs {
				results <- discovery.inspect(ctx, candidate)
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, candidate := range candidates {
			select {
			case jobs <- candidate:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()

	boundariesByPath := map[string]Boundary{}
	for result := range results {
		findings = append(findings, result.findings...)
		if result.boundary.Path != "" {
			boundariesByPath[result.boundary.Path] = result.boundary
		}
	}
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	boundaries := make([]Boundary, 0, len(boundariesByPath))
	for _, boundary := range boundariesByPath {
		boundaries = append(boundaries, boundary)
	}
	sort.Slice(boundaries, func(left, right int) bool {
		return boundaries[left].Path < boundaries[right].Path
	})
	identityRoots := map[string]Boundary{}
	for _, boundary := range boundaries {
		if boundary.RepositoryID == "" {
			continue
		}
		// A submodule checkout is a copy its superproject pins, not an
		// independent claim on the identity: the superproject decides which
		// commit lives there, and the same repository may legitimately be
		// pinned by several superprojects at once. Only standalone checkouts
		// compete, because two of those are two working copies of one
		// repository with nothing deciding between them, which is the state
		// this finding exists to report.
		if boundary.SuperprojectRoot != "" {
			continue
		}
		previous, found := identityRoots[boundary.RepositoryID]
		if found && previous.CommonGitDir != boundary.CommonGitDir {
			findings = append(findings, domain.Finding{
				Code:     "GDS_CONTEXT_IDENTITY_CONFLICT",
				Severity: domain.SeverityHigh,
				Message:  fmt.Sprintf("Repository identity %s is claimed by distinct Git stores.", boundary.RepositoryID),
				Evidence: map[string]any{
					"repository_id": boundary.RepositoryID,
					"first_path":    previous.Path,
					"second_path":   boundary.Path,
				},
			})
			continue
		}
		identityRoots[boundary.RepositoryID] = boundary
	}
	sort.Slice(findings, func(left, right int) bool {
		return findings[left].Message < findings[right].Message
	})
	return Result{Root: resolvedRoot, Boundaries: boundaries, Findings: findings}, nil
}

func (discovery *Local) inspect(ctx context.Context, candidate string) inspection {
	info, err := discovery.git.RepositoryInfo(ctx, candidate)
	if err != nil {
		return inspection{findings: []domain.Finding{{
			Code:     "GDS_DISCOVERY_GIT_BOUNDARY_INVALID",
			Severity: domain.SeverityMedium,
			Message:  fmt.Sprintf("Cannot inspect Git boundary candidate %s.", candidate),
			Evidence: map[string]any{"path": candidate, "error": err.Error()},
		}}}
	}
	boundary := Boundary{
		Path: info.WorktreeRoot, CommonGitDir: info.CommonGitDir,
		SuperprojectRoot: info.SuperprojectRoot, Roles: []string{}, AnchorState: "missing",
	}
	exists, err := manifest.Exists(info.WorktreeRoot)
	if err != nil {
		boundary.AnchorState = "unreadable"
		return inspection{boundary: boundary, findings: []domain.Finding{{
			Code:     "GDS_DISCOVERY_ANCHOR_UNREADABLE",
			Severity: domain.SeverityMedium,
			Message:  fmt.Sprintf("Cannot inspect repository anchor in %s.", info.WorktreeRoot),
			Evidence: map[string]any{"path": info.WorktreeRoot, "error": err.Error()},
		}}}
	}
	if !exists {
		return inspection{boundary: boundary}
	}
	anchor, findings := discovery.manifests.LoadRepository(info.WorktreeRoot)
	if len(findings) != 0 {
		boundary.AnchorState = "invalid"
		return inspection{boundary: boundary, findings: findings}
	}
	boundary.AnchorState = "valid"
	boundary.RepositoryID = anchor.Repository.ID
	boundary.Roles = append([]string(nil), anchor.Repository.Roles...)
	boundary.Lifecycle = anchor.Repository.Lifecycle
	boundary.ProviderOwner = anchor.Provider.Owner
	boundary.ProviderName = anchor.Provider.Name
	return inspection{boundary: boundary}
}

func findCandidates(
	ctx context.Context,
	root string,
	options Options,
) ([]string, []domain.Finding, error) {
	candidates := map[string]struct{}{}
	findings := []domain.Finding{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			findings = append(findings, domain.Finding{
				Code:     "GDS_DISCOVERY_PATH_UNREADABLE",
				Severity: domain.SeverityMedium,
				Message:  fmt.Sprintf("Cannot inspect %s.", path),
				Evidence: map[string]any{"path": path, "error": walkErr.Error()},
			})
			if entry != nil && entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		depth := pathDepth(relative)
		if entry.IsDir() && path != root {
			if excludedDirectory(entry.Name()) || depth > options.MaxDepth {
				return filepath.SkipDir
			}
		}
		if entry.Name() != ".git" {
			return nil
		}
		candidate, err := filepath.EvalSymlinks(filepath.Dir(path))
		if err != nil {
			candidate = filepath.Dir(path)
		}
		candidates[filepath.Clean(candidate)] = struct{}{}
		if len(candidates) > options.MaxRepositories {
			return fmt.Errorf(
				"repository discovery exceeded configured limit %d", options.MaxRepositories,
			)
		}
		if entry.IsDir() {
			return filepath.SkipDir
		}
		return nil
	})
	if err != nil {
		return nil, findings, err
	}
	ordered := make([]string, 0, len(candidates))
	for path := range candidates {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)
	return ordered, findings, nil
}

func normalizeOptions(options Options) Options {
	if options.MaxDepth <= 0 {
		options.MaxDepth = 8
	}
	if options.MaxRepositories <= 0 {
		options.MaxRepositories = 2000
	}
	if options.Concurrency <= 0 {
		options.Concurrency = 4
	}
	if options.Concurrency > 16 {
		options.Concurrency = 16
	}
	return options
}

func canonicalDirectory(path string) (string, error) {
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
		return "", fmt.Errorf("%s is not a directory", resolved)
	}
	return filepath.Clean(resolved), nil
}

func pathDepth(relative string) int {
	if relative == "." || relative == "" {
		return 0
	}
	return len(strings.Split(filepath.Clean(relative), string(filepath.Separator)))
}

func excludedDirectory(name string) bool {
	switch name {
	case ".cache", ".idea", ".pytest_cache", ".ruff_cache", ".tox", ".venv",
		"__pycache__", "node_modules", "target", "vendor":
		return true
	default:
		return false
	}
}
