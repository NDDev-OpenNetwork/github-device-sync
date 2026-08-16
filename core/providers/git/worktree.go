package git

import (
	"context"
	"errors"
	"path/filepath"
	"regexp"
)

// Materializing a commit as a throwaway worktree is a Git write, so it belongs
// on the mutation runner rather than behind the read-only runner's
// `validateReadOnlyCommand` gate. It writes nothing to the repository's history
// and nothing to any existing checkout: it registers an entry under
// `.git/worktrees` and populates a directory the caller owns.
//
// That distinction is what makes it safe to run against a module another agent
// is working in. Their branch, index and working tree are untouched; only the
// registration is added, and only until it is removed.

var worktreeCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)

// AddDetachedWorktree checks an exact commit out at target, detached.
//
// The commit is required in full-length form. A short prefix or a ref name would
// let the caller ask for something whose meaning depends on the repository's
// current state, and the point of this call is to materialize a commit that was
// decided elsewhere.
func (runner *MutationRunner) AddDetachedWorktree(
	ctx context.Context,
	repositoryDirectory string,
	target string,
	commit string,
) error {
	if !worktreeCommit.MatchString(commit) {
		return errors.New("worktree commit must be an exact 40-character object name")
	}
	if !filepath.IsAbs(target) || filepath.Clean(target) != target {
		return errors.New("worktree target must be an exact absolute path")
	}
	_, err := runner.run(
		ctx, repositoryDirectory, map[int]struct{}{0: {}},
		"worktree", "add", "--detach", target, commit,
	)
	return err
}

// RemoveWorktree unregisters a worktree and prunes the leftover administrative
// entry.
//
// Both steps run and both errors are joined rather than short-circuited: a
// removal that failed halfway leaves a registration in somebody else's Git
// store, and reporting only the first failure hides which half is still there.
func (runner *MutationRunner) RemoveWorktree(
	ctx context.Context,
	repositoryDirectory string,
	target string,
) error {
	_, removeErr := runner.run(
		ctx, repositoryDirectory, map[int]struct{}{0: {}},
		"worktree", "remove", "--force", target,
	)
	_, pruneErr := runner.run(
		ctx, repositoryDirectory, map[int]struct{}{0: {}}, "worktree", "prune",
	)
	return errors.Join(removeErr, pruneErr)
}
