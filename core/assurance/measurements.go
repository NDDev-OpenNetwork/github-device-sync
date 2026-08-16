package assurance

import (
	"context"
	"fmt"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/compiler"
	contextresolver "github.com/NDDev-OpenNetwork/github-device-sync/core/context"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/manifest"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/projections"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func measureContext(
	ctx context.Context,
	root string,
	git *gitprovider.Runner,
	schemas *validation.Set,
	samples int,
) (float64, error) {
	projector, err := projections.New(schemas)
	if err != nil {
		return 0, err
	}
	prover := contextresolver.NewCanonicalPolicyProver(git, compiler.New(schemas), projector)
	resolver := contextresolver.NewResolver(
		git, manifest.NewLoader(schemas), schemas, prover,
	)
	durations := make([]time.Duration, 0, samples)
	for sample := 0; sample < samples; sample++ {
		started := time.Now()
		outcome := resolver.Resolve(ctx, root)
		durations = append(durations, time.Since(started))
		if outcome.Class != domain.ExitSuccess || len(outcome.Findings) != 0 ||
			outcome.Context.Repository.ID == "" {
			return 0, fmt.Errorf(
				"context sample %d failed: %s", sample+1, findingCodes(outcome.Findings),
			)
		}
	}
	return percentileMilliseconds(durations, 0.95), nil
}

func measureStatus(
	ctx context.Context,
	root string,
	git *gitprovider.Runner,
	samples int,
) (float64, string, bool, error) {
	durations := make([]time.Duration, 0, samples)
	worktreeClean := true
	for sample := 0; sample < samples; sample++ {
		started := time.Now()
		status, err := git.InspectStatus(ctx, root)
		durations = append(durations, time.Since(started))
		if err != nil || status.Repository.WorktreeRoot != root || status.Classification == "" {
			return 0, "", false, fmt.Errorf("Git status sample %d failed: %w", sample+1, err)
		}
		worktreeClean = worktreeClean && statusIsClean(status)
	}
	sourceCommit, err := git.HeadOID(ctx, root)
	if err != nil {
		return 0, "", false, err
	}
	return percentileMilliseconds(durations, 0.95), sourceCommit, worktreeClean, nil
}

func statusIsClean(status gitprovider.Status) bool {
	return status.Changes.Staged == 0 && status.Changes.Unstaged == 0 &&
		status.Changes.Untracked == 0 && status.Changes.Conflicted == 0 &&
		status.Changes.SubmoduleChanges == 0
}

func percentileMilliseconds(values []time.Duration, percentile float64) float64 {
	ordered := append([]time.Duration(nil), values...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left] < ordered[right] })
	index := int(float64(len(ordered))*percentile+0.999999) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(ordered) {
		index = len(ordered) - 1
	}
	return milliseconds(ordered[index])
}

type memorySampler struct {
	stop chan struct{}
	done chan struct{}
	peak atomic.Uint64
	once sync.Once
}

func newMemorySampler() *memorySampler {
	return &memorySampler{stop: make(chan struct{}), done: make(chan struct{})}
}

func (sampler *memorySampler) Start(ctx context.Context) {
	var initial runtime.MemStats
	runtime.ReadMemStats(&initial)
	sampler.peak.Store(initial.HeapAlloc)
	go func() {
		defer close(sampler.done)
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				var current runtime.MemStats
				runtime.ReadMemStats(&current)
				for observed := sampler.peak.Load(); current.HeapAlloc > observed; observed = sampler.peak.Load() {
					if sampler.peak.CompareAndSwap(observed, current.HeapAlloc) {
						break
					}
				}
			case <-sampler.stop:
				return
			case <-ctx.Done():
				return
			}
		}
	}()
}

func (sampler *memorySampler) Stop() uint64 {
	sampler.once.Do(func() { close(sampler.stop) })
	<-sampler.done
	return sampler.peak.Load()
}
