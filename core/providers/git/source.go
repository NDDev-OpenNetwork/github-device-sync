package git

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// CommittedSourceOID returns the newest commit that materially established the
// declared canonical source paths. Merge commits that preserve one parent's
// exact source boundary are transparent, so hosted synthetic merge refs resolve
// to the same source commit as the reviewed head. It rejects uncommitted source
// changes so a generated projection cannot claim provenance from a commit that
// does not contain its inputs.
func (runner *Runner) CommittedSourceOID(
	ctx context.Context,
	directory string,
	paths []string,
) (string, error) {
	if len(paths) == 0 {
		return "", fmt.Errorf("canonical source path set is empty")
	}
	for _, path := range paths {
		if !safeRepositoryPath(path) {
			return "", fmt.Errorf("canonical source path %q is invalid", path)
		}
	}

	statusArguments := []string{
		"status", "--porcelain=v2", "-z", "--untracked-files=all", "--",
	}
	statusArguments = append(statusArguments, paths...)
	status, err := runner.Run(ctx, directory, statusArguments...)
	if err != nil {
		return "", fmt.Errorf("inspect canonical source state: %w", err)
	}
	if len(status.Stdout) != 0 {
		return "", fmt.Errorf(
			"canonical projection sources contain uncommitted changes; commit them before generation",
		)
	}

	oid, err := runner.newestSourceOID(ctx, directory, "HEAD", paths)
	if err != nil {
		return "", err
	}
	return runner.collapseEquivalentSourceMerges(ctx, directory, oid, paths)
}

func (runner *Runner) newestSourceOID(
	ctx context.Context,
	directory string,
	revision string,
	paths []string,
) (string, error) {
	arguments := []string{"rev-list", "--max-count=1", revision, "--"}
	arguments = append(arguments, paths...)
	result, err := runner.Run(ctx, directory, arguments...)
	if err != nil {
		return "", fmt.Errorf("resolve canonical source commit: %w", err)
	}
	oid := strings.TrimSpace(string(result.Stdout))
	if len(oid) != 40 && len(oid) != 64 {
		return "", fmt.Errorf("unexpected canonical source object id %q", oid)
	}
	return oid, nil
}

func (runner *Runner) collapseEquivalentSourceMerges(
	ctx context.Context,
	directory string,
	oid string,
	paths []string,
) (string, error) {
	const maxMergeDepth = 128
	for depth := 0; depth < maxMergeDepth; depth++ {
		result, err := runner.Run(
			ctx, directory, "rev-list", "--parents", "--max-count=1", oid,
		)
		if err != nil {
			return "", fmt.Errorf("resolve canonical source parents: %w", err)
		}
		fields := strings.Fields(string(result.Stdout))
		if len(fields) == 0 || fields[0] != oid {
			return "", fmt.Errorf("unexpected canonical source parent record %q", result.Stdout)
		}
		if len(fields) <= 2 {
			return oid, nil
		}

		equivalentParent := ""
		for _, parent := range fields[1:] {
			arguments := []string{"diff", "--quiet", oid, parent, "--"}
			arguments = append(arguments, paths...)
			difference, diffErr := runner.run(
				ctx, directory, map[int]struct{}{0: {}, 1: {}}, arguments...,
			)
			if diffErr != nil {
				return "", fmt.Errorf("compare canonical source merge parent: %w", diffErr)
			}
			if difference.ExitCode == 0 {
				equivalentParent = parent
				break
			}
		}
		if equivalentParent == "" {
			return oid, nil
		}
		oid, err = runner.newestSourceOID(ctx, directory, equivalentParent, paths)
		if err != nil {
			return "", err
		}
	}
	return "", fmt.Errorf("canonical source merge ancestry exceeds %d levels", maxMergeDepth)
}

// CommitTime returns the committer time of a commit. ok is false when the commit
// cannot be resolved locally (e.g. a shallow clone lacking the object) or its
// timestamp cannot be parsed, so callers can skip a temporal check rather than
// fail closed on an unresolvable commit.
func (runner *Runner) CommitTime(ctx context.Context, directory string, commit string) (time.Time, bool) {
	result, err := runner.Run(ctx, directory, "rev-list", "--max-count=1", "--format=%cI", commit)
	if err != nil {
		return time.Time{}, false
	}
	// `rev-list --format` prints a "commit <oid>" header line followed by the
	// formatted body; take the first line that parses as an RFC 3339 timestamp.
	for _, line := range strings.Split(string(result.Stdout), "\n") {
		if committed, parseErr := time.Parse(time.RFC3339, strings.TrimSpace(line)); parseErr == nil {
			return committed, true
		}
	}
	return time.Time{}, false
}
