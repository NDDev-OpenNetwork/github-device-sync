package git

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

// `git status` answers "is this checkout clean". Deciding a repository is
// finished needs "does any work exist here that exists nowhere else", and no
// status field reports that. A branch nobody pushed, a tag nobody pushed, a
// commit reachable from a local ref and from no remote-tracking ref: all of it
// is invisible to a clean status and all of it disappears with the repository.
//
// These are reads over the same bounded runner as everything else. They add
// nothing to what Git may be asked to do; they ask for what was always
// observable and never observed.

// LocalRef is one local branch or tag.
type LocalRef struct {
	Name string `json:"name"`
	OID  string `json:"oid"`
}

// LocalRefs enumerates every local branch and tag.
func (runner *Runner) LocalRefs(ctx context.Context, directory string) ([]LocalRef, error) {
	return runner.refs(ctx, directory, "refs/heads", "refs/tags")
}

// RemoteTrackingRefs enumerates what this device believes the remotes hold.
//
// It is what makes "this branch exists only here" answerable locally: a local
// ref with no counterpart under `refs/remotes` is work nobody else has a copy
// of. It reflects the last fetch rather than the provider's current state, so a
// retirement decision pairs it with the provider enumeration rather than
// trusting it alone.
func (runner *Runner) RemoteTrackingRefs(ctx context.Context, directory string) ([]LocalRef, error) {
	return runner.refs(ctx, directory, "refs/remotes/origin", "refs/remotes/upstream")
}

func (runner *Runner) refs(ctx context.Context, directory string, namespaces ...string) ([]LocalRef, error) {
	arguments := append(
		[]string{"for-each-ref", "--format=%(refname)%09%(objectname)"}, namespaces...,
	)
	result, err := runner.run(ctx, directory, map[int]struct{}{0: {}}, arguments...)
	if err != nil {
		return nil, err
	}
	refs := []LocalRef{}
	for _, line := range nonEmptyLines(result.Stdout) {
		name, oid, found := strings.Cut(line, "\t")
		if !found || name == "" || !safeObjectID(oid) {
			return nil, fmt.Errorf("git ref listing is not exact")
		}
		refs = append(refs, LocalRef{Name: name, OID: oid})
	}
	return refs, nil
}

// UnpushedCommitCount counts commits reachable from some local ref and from no
// remote-tracking ref.
//
// A repository with no remotes at all yields every commit it holds, which is the
// correct answer rather than an error: nothing in it is published, so all of it
// is work that exists only here.
func (runner *Runner) UnpushedCommitCount(ctx context.Context, directory string) (int, error) {
	result, err := runner.run(
		ctx, directory, map[int]struct{}{0: {}},
		"rev-list", "--count", "--all", "--not", "--remotes",
	)
	if err != nil {
		return 0, err
	}
	lines := nonEmptyLines(result.Stdout)
	if len(lines) != 1 {
		return 0, fmt.Errorf("git rev-list count is not exact")
	}
	count, err := strconv.Atoi(strings.TrimSpace(lines[0]))
	if err != nil || count < 0 {
		return 0, fmt.Errorf("git rev-list count is not a number")
	}
	return count, nil
}
