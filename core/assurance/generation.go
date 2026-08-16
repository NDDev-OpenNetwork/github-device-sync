package assurance

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/bundle"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/compiler"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/estate"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/module"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/portfolio"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/projections"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/rollout"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

// harnessSourceTreeDigest gives the scale harness a deterministic bundle
// identity. The harness generates synthetic fixture repositories to measure
// projection throughput; it has no canonical source tree of its own to digest,
// and it proves nothing about provenance. Deriving the value from the harness
// input keeps repeated runs comparable without pretending to be an observation.
func harnessSourceTreeDigest(sourceCommit string) string {
	digest := sha256.Sum256([]byte("gds-assurance-harness\x00" + sourceCommit))
	return "sha256:" + hex.EncodeToString(digest[:])
}

func generateProjections(
	ctx context.Context,
	root string,
	sourceCommit string,
	fixtures []fixtureRepository,
	compiled estate.CompiledInventory,
	workers int,
	schemas *validation.Set,
) (string, error) {
	loader := compiler.NewLoader(schemas)
	sources, findings := loader.Load(root)
	if len(findings) != 0 {
		return "", findingError("load projection policies", findings)
	}
	// The scenario compiles synthetic anchors against the real policy tree, so
	// it needs the real owner register too. Without it every owner-matched
	// profile is unresolvable, and the run would measure a compiler that cannot
	// see the estate rather than the scenario.
	owners, ownerFindings := loader.LoadOwners(root)
	if len(ownerFindings) != 0 {
		return "", findingError("load estate owner register", ownerFindings)
	}
	assignments := make(map[int64]estate.Assignment, len(compiled.Repositories))
	for _, assignment := range compiled.Repositories {
		assignments[assignment.ProviderID] = assignment
	}
	type job struct{ index int }
	jobs := make(chan job)
	outputDigests := make([]string, len(fixtures))
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	errCh := make(chan error, 1)
	type workerResources struct {
		generator *projections.Generator
		compiler  *compiler.Compiler
	}
	resources := make([]workerResources, workers)
	for worker := 0; worker < workers; worker++ {
		workerSchemas, err := validation.NewSchemaSet()
		if err != nil {
			return "", fmt.Errorf("initialize projection worker %d schemas: %w", worker+1, err)
		}
		generator, err := projections.New(workerSchemas)
		if err != nil {
			return "", fmt.Errorf("initialize projection worker %d generator: %w", worker+1, err)
		}
		resources[worker] = workerResources{
			generator: generator, compiler: compiler.New(workerSchemas).WithOwners(owners),
		}
	}
	var wait sync.WaitGroup
	for worker, resource := range resources {
		wait.Add(1)
		go func(worker int, resource workerResources) {
			defer wait.Done()
			for item := range jobs {
				fixture := fixtures[item.index]
				assignment, found := assignments[fixture.Observed.ProviderID]
				if !found {
					sendWorkerError(errCh, cancel, fmt.Errorf(
						"missing assignment for provider repository %d", fixture.Observed.ProviderID,
					))
					return
				}
				anchor := syntheticAnchor(fixture, assignment, item.index)
				result := resource.compiler.Compile(
					anchor, sources, compiler.DevelopmentBundleVersion,
				)
				if len(result.Findings) != 0 {
					sendWorkerError(
						errCh, cancel, fmt.Errorf(
							"worker %d projection %d policy: %w", worker+1, item.index+1,
							findingError("compile projection policy", result.Findings),
						),
					)
					return
				}
				bundleCandidate, err := resource.generator.DevelopmentBundle(
					result.Document, sourceCommit, harnessSourceTreeDigest(sourceCommit),
				)
				if err != nil {
					sendWorkerError(errCh, cancel, err)
					return
				}
				candidate, candidateFindings := resource.generator.Generate(
					anchor, result.Document, bundleCandidate,
				)
				if len(candidateFindings) != 0 || candidate.OutputDigest == "" {
					sendWorkerError(
						errCh, cancel, findingError("generate projection", candidateFindings),
					)
					return
				}
				outputDigests[item.index] = candidate.OutputDigest
			}
		}(worker, resource)
	}
	feedDone := make(chan struct{})
	go func() {
		defer close(feedDone)
		defer close(jobs)
		for index := range fixtures {
			select {
			case jobs <- job{index: index}:
			case <-workerCtx.Done():
				return
			}
		}
	}()
	wait.Wait()
	<-feedDone
	select {
	case workerErr := <-errCh:
		return "", workerErr
	default:
	}
	for index, digest := range outputDigests {
		if digest == "" {
			return "", fmt.Errorf("projection %d has no output digest", index+1)
		}
	}
	return canonicaljson.Digest(outputDigests)
}

func sendWorkerError(channel chan<- error, cancel context.CancelFunc, err error) {
	select {
	case channel <- err:
	default:
	}
	cancel()
}

func exerciseSharedModules(options Options) (int, error) {
	modules := make([]domain.RepositoryAnchor, options.SharedModuleCount)
	for index := range modules {
		modules[index] = sharedModuleAnchor(index)
	}
	relationships := 0
	for index := 0; index < options.ModuleConsumerCount; index++ {
		consumer := domain.RepositoryAnchor{Repository: domain.RepositoryIdentity{
			ID: repositoryID(index + 1), Roles: []string{"project"}, Lifecycle: "active",
		}}
		selected := modules[index%len(modules)]
		updated, findings := module.AddGitSubmoduleRelationship(
			consumer, selected, selected.Provider.Name, moduleTopology(selected),
		)
		if len(findings) != 0 || len(updated.Relationships) != 1 ||
			updated.Relationships[0].Target != selected.Repository.ID {
			return 0, findingError("build shared module relationship", findings)
		}
		relationships++
	}
	return relationships, nil
}

func exercisePortfolioPlan(
	compiled estate.CompiledInventory,
	sourceCommit string,
	now time.Time,
	schemas *validation.Set,
) (float64, int, error) {
	subplans := make([]portfolio.Subplan, len(compiled.Repositories))
	for index, assignment := range compiled.Repositories {
		subplan := portfolio.Subplan{
			RepositoryID: repositoryID(index + 1),
			Path:         fmt.Sprintf("/fixture/%s-%04d", assignment.Name, index+1),
			Status:       "ready", HeadOID: sourceCommit,
			ManifestDigest: digestFixture("manifest", index),
			PolicyDigest:   digestFixture("policy", index), FindingCodes: []string{},
		}
		if index == len(compiled.Repositories)-1 {
			subplan.Status = "blocked"
			subplan.HeadOID, subplan.ManifestDigest, subplan.PolicyDigest = "", "", ""
			subplan.FindingCodes = []string{"GDS_ASSURANCE_ISOLATED_FIXTURE_FAILURE"}
		}
		subplans[index] = subplan
	}
	started := time.Now()
	plan, findings := portfolio.Build(portfolio.BuildInput{
		PlanID: "plan_01J00000000000000000000000", CreatedAt: now,
		Portfolio: "portfolio:personal-projects", Operation: "policy-rollout",
		Intent:   "Verify bounded aggregate planning without applying mutations.",
		Subplans: subplans,
	}, schemas)
	duration := milliseconds(time.Since(started))
	if len(findings) != 0 || plan.ReadyCount != len(subplans)-1 || plan.BlockedCount != 1 {
		return 0, 0, fmt.Errorf(
			"portfolio plan mismatch: ready=%d blocked=%d findings=%s",
			plan.ReadyCount, plan.BlockedCount, findingCodes(findings),
		)
	}
	return duration, plan.BlockedCount, nil
}

func exerciseRollout(
	compiled estate.CompiledInventory,
	now time.Time,
	schemas *validation.Set,
) (float64, int, error) {
	targets := make([]string, len(compiled.Repositories))
	for index := range targets {
		targets[index] = repositoryID(index + 1)
	}
	started := time.Now()
	plan, findings := rollout.Build(rollout.BuildInput{
		RolloutID: "rollout_01J00000000000000000000000", CreatedAt: now,
		Envelope: bundle.ReleaseEnvelope{
			SchemaVersion: domain.SchemaVersion, BundleVersion: "1.0.0", ReleaseSequence: 1,
			Channel: "canary", SourceCommit: "0123456789abcdef0123456789abcdef01234567",
			ManifestDigest: digestFixture("manifest", 0),
			ArtifactDigest: digestFixture("artifact", 0),
		},
		RepositoryIDs: targets,
		Rings: []rollout.RingSpec{
			{ID: "canary", MaxRepositories: 5},
			{ID: "representative", CumulativePercent: 1},
			{ID: "early", CumulativePercent: 10},
			{ID: "general", CumulativePercent: 100},
		},
		MaxFailureRate: 0.02,
	}, schemas)
	if len(findings) != 0 || len(plan.Waves) < 2 ||
		(len(compiled.Repositories) == DefaultRepositoryCount && len(plan.Waves) != 4) {
		return 0, 0, fmt.Errorf(
			"rollout plan mismatch: waves=%d findings=%s",
			len(plan.Waves), findingCodes(findings),
		)
	}
	results := make([]rollout.TargetResult, len(plan.Waves[0].RepositoryIDs))
	for index, repositoryID := range plan.Waves[0].RepositoryIDs {
		results[index] = rollout.TargetResult{RepositoryID: repositoryID, Status: "succeeded"}
	}
	results[0].SecurityFailure = true
	decision, gateFindings := rollout.EvaluateWave(plan, 0, results)
	duration := milliseconds(time.Since(started))
	if decision.Action != "pause" || !hasFinding(gateFindings, "GDS_ROLLOUT_GATE_FAILED") {
		return 0, 0, fmt.Errorf("rollout security failure did not pause the next wave")
	}
	return duration, len(plan.Waves), nil
}
