// Package inventory compiles an in-memory observed local inventory. It never
// writes desired configuration or provider state.
package inventory

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/discovery"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
)

type Clock func() time.Time

type Compiler struct {
	discovery *discovery.Local
	git       *gitprovider.Runner
	clock     Clock
}

type Result struct {
	InventoryVersion int              `json:"inventory_version"`
	ObservedAt       string           `json:"observed_at"`
	Source           string           `json:"source"`
	Root             string           `json:"root"`
	Summary          Summary          `json:"summary"`
	Repositories     []Repository     `json:"repositories"`
	Findings         []domain.Finding `json:"-"`
}

type Summary struct {
	Total          int `json:"total"`
	Anchored       int `json:"anchored"`
	Unanchored     int `json:"unanchored"`
	InvalidAnchors int `json:"invalid_anchors"`
	// ExcludedArchived counts repositories left out because their anchor
	// declares lifecycle: "archived". Reporting the count is what keeps the
	// default from being a silent filter.
	ExcludedArchived int            `json:"excluded_archived"`
	Classifications  map[string]int `json:"classifications"`
	Roles            map[string]int `json:"roles"`
}

type Repository struct {
	discovery.Boundary
	Status *gitprovider.Status `json:"status,omitempty"`
}

func NewCompiler(
	discovery *discovery.Local,
	git *gitprovider.Runner,
	clock Clock,
) *Compiler {
	if clock == nil {
		clock = time.Now
	}
	return &Compiler{discovery: discovery, git: git, clock: clock}
}

func (compiler *Compiler) Compile(
	ctx context.Context,
	root string,
	options discovery.Options,
) (Result, error) {
	discovered, err := compiler.discovery.Discover(ctx, root, options)
	if err != nil {
		return Result{}, err
	}
	concurrency := options.Concurrency
	if concurrency <= 0 {
		concurrency = 4
	}
	if concurrency > 16 {
		concurrency = 16
	}

	type inspected struct {
		repository Repository
		finding    *domain.Finding
	}
	jobs := make(chan discovery.Boundary)
	results := make(chan inspected)
	var workers sync.WaitGroup
	for range concurrency {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for boundary := range jobs {
				status, err := compiler.git.InspectStatus(ctx, boundary.Path)
				if err != nil {
					finding := domain.Finding{
						Code:     "GDS_INVENTORY_STATUS_NOT_PROVEN",
						Severity: domain.SeverityMedium,
						Message:  fmt.Sprintf("Cannot inspect repository status at %s.", boundary.Path),
						Evidence: map[string]any{"path": boundary.Path, "error": err.Error()},
					}
					results <- inspected{repository: Repository{Boundary: boundary}, finding: &finding}
					continue
				}
				results <- inspected{
					repository: Repository{Boundary: boundary, Status: &status},
				}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for _, boundary := range discovered.Boundaries {
			select {
			case jobs <- boundary:
			case <-ctx.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()

	repositories := make([]Repository, 0, len(discovered.Boundaries))
	findings := append([]domain.Finding(nil), discovered.Findings...)
	excludedArchived := 0
	for result := range results {
		if !options.IncludeArchived && result.repository.Lifecycle == "archived" {
			excludedArchived++
			if result.finding != nil {
				findings = append(findings, *result.finding)
			}
			continue
		}
		repositories = append(repositories, result.repository)
		if result.finding != nil {
			findings = append(findings, *result.finding)
		}
	}
	if ctx.Err() != nil {
		return Result{}, ctx.Err()
	}
	sort.Slice(repositories, func(left, right int) bool {
		return repositories[left].Path < repositories[right].Path
	})
	sort.Slice(findings, func(left, right int) bool {
		return findings[left].Message < findings[right].Message
	})

	return Result{
		InventoryVersion: 1,
		ObservedAt:       compiler.clock().UTC().Format(time.RFC3339Nano),
		Source:           "local",
		Root:             discovered.Root,
		Summary:          summarize(repositories, excludedArchived),
		Repositories:     repositories,
		Findings:         findings,
	}, nil
}

func summarize(repositories []Repository, excludedArchived int) Summary {
	summary := Summary{
		ExcludedArchived: excludedArchived,
		Total:            len(repositories), Classifications: map[string]int{}, Roles: map[string]int{},
	}
	for _, repository := range repositories {
		switch repository.AnchorState {
		case "valid":
			summary.Anchored++
		case "missing":
			summary.Unanchored++
		default:
			summary.InvalidAnchors++
		}
		for _, role := range repository.Roles {
			summary.Roles[role]++
		}
		if repository.Status != nil {
			summary.Classifications[repository.Status.Classification]++
		} else {
			summary.Classifications["not-proven"]++
		}
	}
	return summary
}
