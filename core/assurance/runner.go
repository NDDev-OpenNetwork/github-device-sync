package assurance

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/estate"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/identity"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

// assuranceTimeout bounds a hung run; it is not a performance gate. Performance
// is judged separately by the oracle modes (deterministic, relative,
// absolute-calibrated, informational) against a variance-backed policy and an
// exact runner digest, so this value must never double as a speed assertion.
//
// It was 3 minutes, which the shared self-hosted runner reached: the same
// workload measured 81.6s, 85.3s, 104.8s and 176.3s across consecutive runs on
// `main`, leaving under four seconds of headroom in the worst case. A bound that
// tight reports a busy runner rather than a stuck one. 10 minutes keeps a hang
// obvious while clearing the observed legitimate worst case several times over.
const assuranceTimeout = 10 * time.Minute

func Run(ctx context.Context, options Options, schemas *validation.Set) (Report, error) {
	if schemas == nil {
		return Report{}, fmt.Errorf("assurance requires embedded schemas")
	}
	options, err := normalizeOptions(options)
	if err != nil {
		return Report{}, err
	}
	root, err := resolveRoot(options.Root)
	if err != nil {
		return Report{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, assuranceTimeout)
	defer cancel()

	startedAt := time.Now().UTC()
	assuranceID, err := identity.New("assurance", startedAt, nil)
	if err != nil {
		return Report{}, err
	}
	memory := newMemorySampler()
	memory.Start(ctx)
	defer memory.Stop()

	git, err := gitprovider.NewRunner()
	if err != nil {
		return Report{}, fmt.Errorf("initialize Git reader: %w", err)
	}
	contextP95, err := measureContext(ctx, root, git, schemas, options.ContextSamples)
	if err != nil {
		return Report{}, err
	}
	statusP95, sourceCommit, worktreeClean, err := measureStatus(
		ctx, root, git, options.RepositoryStatusSamples,
	)
	if err != nil {
		return Report{}, err
	}
	if options.RequireCleanWorktree && !worktreeClean {
		return Report{}, fmt.Errorf("assurance evidence requires a clean source worktree")
	}

	config, findings := estate.Load(root, schemas)
	if len(findings) != 0 {
		return Report{}, findingError("load canonical estate", findings)
	}
	fixtures := buildFixtureRepositories(options)
	observed := make([]estate.ObservedRepository, len(fixtures))
	for index, fixture := range fixtures {
		observed[index] = fixture.Observed
	}
	compileStarted := time.Now()
	compiled, findings := estate.Compile(config, observed)
	inventoryCompileMS := milliseconds(time.Since(compileStarted))
	if len(findings) != 0 || len(compiled.Repositories) != options.RepositoryCount {
		return Report{}, fmt.Errorf(
			"compile synthetic estate: repositories=%d findings=%s",
			len(compiled.Repositories), findingCodes(findings),
		)
	}
	forkAssignments := countForkAssignments(compiled)
	if forkAssignments != options.ForkCount {
		return Report{}, fmt.Errorf(
			"fork assignment mismatch: got %d want %d", forkAssignments, options.ForkCount,
		)
	}

	projectionStarted := time.Now()
	projectionDigest, err := generateProjections(
		ctx, root, sourceCommit, fixtures, compiled, options.ProjectionConcurrency, schemas,
	)
	projectionMS := milliseconds(time.Since(projectionStarted))
	if err != nil {
		return Report{}, err
	}
	if projectionDigest == "" {
		return Report{}, fmt.Errorf("projection generation returned an empty aggregate digest")
	}

	moduleRelationships, err := exerciseSharedModules(options)
	if err != nil {
		return Report{}, err
	}
	portfolioMS, blockedSubplans, err := exercisePortfolioPlan(
		compiled, sourceCommit, startedAt, schemas,
	)
	if err != nil {
		return Report{}, err
	}
	rolloutMS, waveCount, err := exerciseRollout(compiled, startedAt, schemas)
	if err != nil {
		return Report{}, err
	}

	stateResult, err := exerciseDurableState(ctx, options, config, fixtures)
	if err != nil {
		return Report{}, err
	}
	if err := exerciseKillSwitches(); err != nil {
		return Report{}, err
	}
	finalStatus, err := git.InspectStatus(ctx, root)
	if err != nil {
		return Report{}, fmt.Errorf("recheck source worktree: %w", err)
	}
	finalCommit, err := git.HeadOID(ctx, root)
	if err != nil {
		return Report{}, fmt.Errorf("recheck source commit: %w", err)
	}
	if finalCommit != sourceCommit ||
		(options.RequireCleanWorktree && !statusIsClean(finalStatus)) {
		return Report{}, fmt.Errorf("assurance source state changed during execution")
	}

	peakHeap := memory.Stop()
	checks := []Check{
		passedCheck("context-resolution", fmt.Sprintf("%d root context samples resolved without findings", options.ContextSamples)),
		passedCheck("repository-status", fmt.Sprintf("%d machine-readable Git status samples completed", options.RepositoryStatusSamples)),
		passedCheck("estate-compilation", fmt.Sprintf("%d repositories compiled deterministically", len(compiled.Repositories))),
		passedCheck("fork-classification", fmt.Sprintf("%d forks matched explicit portfolio selectors", forkAssignments)),
		passedCheck("mixed-lifecycles", "active, maintenance, frozen, and archived repository anchors generated"),
		passedCheck("shared-module-relationships", fmt.Sprintf("%d typed consumers reference %d shared modules", moduleRelationships, options.SharedModuleCount)),
		passedCheck("projection-generation", fmt.Sprintf("%d standalone projections generated with aggregate digest %s", options.RepositoryCount, projectionDigest)),
		passedCheck("portfolio-planning", fmt.Sprintf("%d independent subplans retained %d isolated failure", options.RepositoryCount, blockedSubplans)),
		passedCheck("rollout-planning", fmt.Sprintf("%d bounded waves generated and a security failure paused advancement", waveCount)),
		passedCheck("webhook-load", fmt.Sprintf("%d deliveries processed with replay and conflict checks", options.WebhookDeliveryCount)),
		passedCheck("reconciliation-persistence", fmt.Sprintf("%d observations persisted in SQLite WAL state", options.RepositoryCount)),
		passedCheck("worker-restart", "state, webhook results, and reconciliation cursor survived close and reopen"),
		passedCheck("mixed-access-states", "available, inaccessible, auth-failed, not-found, and unknown remained distinct"),
		passedCheck("installation-outage-isolation", "one installation outage preserved the unrelated installation result"),
		passedCheck("kill-switch-contract", "all four switches load strictly and invalid values fail closed"),
		passedCheck("bounded-resource-contract", "network and external mutations remained disabled under fixed worker bounds"),
	}
	metrics := []Metric{
		metric("context-p95-ms", contextP95),
		metric("repository-status-p95-ms", statusP95),
		metric("inventory-compile-ms", inventoryCompileMS),
		metric("reconciliation-ms", stateResult.ReconciliationMS),
		metric("projection-generation-ms", projectionMS),
		metric("webhook-throughput-per-second", stateResult.WebhookThroughput),
		metric("queue-max-lag-ms", stateResult.QueueMaxLagMS),
		metric("restart-recovery-ms", stateResult.RestartMS),
		metric("rollout-plan-ms", rolloutMS),
		metric("portfolio-plan-ms", portfolioMS),
		metric("peak-heap-bytes", float64(peakHeap)),
		metric("state-db-bytes", float64(stateResult.DatabaseBytes)),
		metric("api-read-calls-per-full-reconciliation", float64(stateResult.FullReconciliationCalls)),
	}
	report := Report{
		SchemaVersion: domain.SchemaVersion, AssuranceID: assuranceID,
		StartedAt: startedAt, FinishedAt: time.Now().UTC(),
		Environment: Environment{
			OS: runtime.GOOS, Architecture: runtime.GOARCH,
			GoVersion: runtime.Version(), CPUCount: runtime.NumCPU(),
		},
		Source: Source{Commit: sourceCommit, WorktreeClean: worktreeClean},
		Scenario: Scenario{
			Repositories: options.RepositoryCount, Installations: len(config.Installations), Forks: options.ForkCount,
			SharedModules: options.SharedModuleCount, ModuleConsumers: options.ModuleConsumerCount,
			WebhookDeliveries: options.WebhookDeliveryCount, LifecycleClasses: 4, AccessStates: 5,
		},
		Bounds: Bounds{
			ReconciliationConcurrency: options.ReconciliationConcurrency,
			ProjectionConcurrency:     options.ProjectionConcurrency,
			MaxRepositories:           options.RepositoryCount,
			RequireCleanWorktree:      options.RequireCleanWorktree,
			ExternalNetwork:           false, ExternalMutations: false,
		},
		Checks: checks, Metrics: metrics, Status: "pass",
	}
	for _, observedMetric := range report.Metrics {
		if !observedMetric.Passed {
			report.Status = "fail"
		}
	}
	report.ResultDigest, err = reportDigest(report)
	if err != nil {
		return Report{}, err
	}
	if findings := Validate(report, schemas); len(findings) != 0 {
		return Report{}, findingError("validate assurance report", findings)
	}
	return report, nil
}

func normalizeOptions(options Options) (Options, error) {
	if options.Root == "" {
		options.Root = "."
	}
	defaults := []struct {
		target *int
		value  int
	}{
		{&options.RepositoryCount, DefaultRepositoryCount},
		{&options.ForkCount, DefaultForkCount},
		{&options.SharedModuleCount, DefaultSharedModuleCount},
		{&options.ModuleConsumerCount, DefaultModuleConsumerCount},
		{&options.WebhookDeliveryCount, DefaultWebhookDeliveryCount},
		{&options.ReconciliationConcurrency, DefaultReconciliationWorkers},
		{&options.ProjectionConcurrency, DefaultProjectionWorkers},
		{&options.ContextSamples, DefaultContextSamples},
		{&options.RepositoryStatusSamples, DefaultRepositoryStatusSamples},
	}
	for _, item := range defaults {
		if *item.target == 0 {
			*item.target = item.value
		}
	}
	if options.RepositoryCount < 8 || options.RepositoryCount > 2000 ||
		options.ForkCount < 0 || options.ForkCount > options.RepositoryCount ||
		options.SharedModuleCount < 1 || options.SharedModuleCount > 100 ||
		options.ModuleConsumerCount < 1 || options.ModuleConsumerCount > options.RepositoryCount ||
		options.WebhookDeliveryCount < 1 || options.WebhookDeliveryCount > 10000 ||
		options.ReconciliationConcurrency < 1 || options.ReconciliationConcurrency > 16 ||
		options.ProjectionConcurrency < 1 || options.ProjectionConcurrency > 16 ||
		options.ContextSamples < 1 || options.ContextSamples > 100 ||
		options.RepositoryStatusSamples < 1 || options.RepositoryStatusSamples > 100 {
		return Options{}, fmt.Errorf("assurance options exceed fixed safety bounds")
	}
	return options, nil
}

func resolveRoot(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve assurance root: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("assurance root is not a directory")
	}
	return filepath.Clean(resolved), nil
}

func countForkAssignments(inventory estate.CompiledInventory) int {
	count := 0
	for _, assignment := range inventory.Repositories {
		if assignment.MatchedSelector == "personal-forks" ||
			assignment.MatchedSelector == "organization-forks" {
			count++
		}
	}
	return count
}

func milliseconds(duration time.Duration) float64 {
	return float64(duration) / float64(time.Millisecond)
}

func passedCheck(id, summary string) Check {
	return Check{ID: id, Status: "pass", Summary: summary}
}

func digestFixture(kind string, index int) string {
	digest, _ := canonicaljson.Digest(struct {
		Kind  string `json:"kind"`
		Index int    `json:"index"`
	}{Kind: kind, Index: index})
	return digest
}

func findingError(operation string, findings []domain.Finding) error {
	if len(findings) == 0 {
		return fmt.Errorf("%s: no finding evidence", operation)
	}
	return fmt.Errorf("%s: %s: %s", operation, findingCodes(findings), findings[0].Message)
}

func findingCodes(findings []domain.Finding) string {
	if len(findings) == 0 {
		return "none"
	}
	codes := make([]string, 0, len(findings))
	for _, finding := range findings {
		codes = append(codes, finding.Code)
	}
	sort.Strings(codes)
	return fmt.Sprint(codes)
}

func hasFinding(findings []domain.Finding, code string) bool {
	for _, finding := range findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}
