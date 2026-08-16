package app

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
	repositoryworkflow "github.com/NDDev-OpenNetwork/github-device-sync/core/repository"
)

// RetirementReader is the provider surface a retirement decision needs.
//
// Narrower than the client on purpose: this is the exact set of questions whose
// answers can make a deletion refuse, and naming them here keeps a future caller
// from quietly widening what a deletion is allowed to consult.
type RetirementReader interface {
	ListBranches(context.Context, string, string) ([]githubprovider.ObservedBranch, githubprovider.ResponseMeta, error)
	ListPullRequests(context.Context, string, string) ([]githubprovider.ObservedPullRequest, githubprovider.ResponseMeta, error)
	ListOpenIssues(context.Context, string, string) ([]githubprovider.ObservedIssue, githubprovider.ResponseMeta, error)
	CountUnresolvedReviewThreads(context.Context, string, string) (int, githubprovider.ResponseMeta, error)
}

// gatherRetirementEvidence observes everything a deletion would destroy.
//
// Failures do not propagate as errors. Every collection carries a `*Read` flag,
// and a failed enumeration leaves it false, which classifies as `unknown`, which
// blocks. That is the whole design: a refused permission, an exceeded page bound
// and a rate limit must not be distinguishable from "nothing was found", because
// the deletion is irreversible and only one of those readings is safe.
func (services *Services) gatherRetirementEvidence(
	ctx context.Context,
	root string,
	transition repositoryworkflow.ProviderTransition,
	defaultBranch string,
	reader RetirementReader,
	preserved repositoryworkflow.PreservationDeclaration,
) repositoryworkflow.RetirementEvidence {
	local := repositoryworkflow.LocalObservation{WorktreeRoot: root}

	if status, err := services.Git.InspectStatus(ctx, root); err == nil {
		for _, worktree := range status.Worktrees {
			local.Worktrees = append(local.Worktrees, repositoryworkflow.LocalWorktree{
				Path: worktree.Path, Branch: worktree.Branch, Detached: worktree.Detached,
				Locked: worktree.Locked, Prunable: worktree.Prunable,
				Primary: filepath.Clean(worktree.Path) == filepath.Clean(root),
			})
		}
	}
	if refs, err := services.Git.LocalRefs(ctx, root); err == nil {
		published := map[string]struct{}{}
		if tracking, trackingErr := services.Git.RemoteTrackingRefs(ctx, root); trackingErr == nil {
			for _, ref := range tracking {
				// `refs/remotes/origin/task/one` publishes `refs/heads/task/one`.
				// Matched by name and not by OID on purpose: a local branch ahead
				// of its remote counterpart still holds commits nobody else has,
				// and `UnpushedCommitCount` is what reports those.
				for _, prefix := range []string{"refs/remotes/origin/", "refs/remotes/upstream/"} {
					if strings.HasPrefix(ref.Name, prefix) {
						published["refs/heads/"+strings.TrimPrefix(ref.Name, prefix)] = struct{}{}
					}
				}
			}
		}
		for _, ref := range refs {
			_, isPublished := published[ref.Name]
			local.Refs = append(local.Refs, repositoryworkflow.LocalRefSnapshot{
				Name: ref.Name, OID: ref.OID, Remote: isPublished,
				Default: ref.Name == "refs/heads/"+defaultBranch,
			})
		}
	}
	if count, err := services.Git.UnpushedCommitCount(ctx, root); err == nil {
		local.UnpushedCommits, local.UnpushedCommitsRead = count, true
	}

	remote := repositoryworkflow.RemoteObservation{DefaultBranch: defaultBranch}
	if reader != nil {
		owner, name := transition.CurrentOwner, transition.CurrentName
		if branches, _, err := reader.ListBranches(ctx, owner, name); err == nil {
			remote.BranchesRead = true
			for _, branch := range branches {
				remote.Branches = append(remote.Branches, repositoryworkflow.RemoteBranch{
					Name: branch.Name, SHA: branch.SHA, Default: branch.Name == defaultBranch,
				})
			}
		}
		if requests, _, err := reader.ListPullRequests(ctx, owner, name); err == nil {
			remote.PullRequestsRead = true
			for _, request := range requests {
				remote.PullRequests = append(remote.PullRequests, repositoryworkflow.RemotePullRequest{
					Number: request.Number, State: request.State, Draft: request.Draft,
					Merged: request.Merged, HeadRef: request.HeadRef,
				})
			}
		}
		if issues, _, err := reader.ListOpenIssues(ctx, owner, name); err == nil {
			remote.OpenIssuesRead = true
			for _, issue := range issues {
				remote.OpenIssues = append(remote.OpenIssues, repositoryworkflow.RemoteIssue{
					Number: issue.Number, Title: issue.Title,
				})
			}
		}
		if count, _, err := reader.CountUnresolvedReviewThreads(ctx, owner, name); err == nil {
			remote.UnresolvedReviewThreads, remote.UnresolvedReviewThreadsRead = count, true
		}
	}

	return repositoryworkflow.ClassifyRetirement(
		transition.RepositoryID, transition.CurrentOwner, transition.CurrentName,
		local, remote, preserved,
	)
}

// retirementDigest binds the provider state and the retirement claim together.
//
// One digest rather than two fields: the precondition the plan carries is
// "the repository the provider holds, and what it was found to contain, are both
// still what they were". Splitting them would let one be revalidated without the
// other.
func retirementDigest(providerDigest string, evidence repositoryworkflow.RetirementEvidence) (string, error) {
	return canonicaljson.Digest(struct {
		Provider   string   `json:"provider"`
		Retirement []string `json:"retirement"`
	}{providerDigest, repositoryworkflow.RetirementDigestInput(evidence)})
}

// retirementFindings reports why a deletion cannot proceed.
func retirementFindings(evidence repositoryworkflow.RetirementEvidence) []domain.Finding {
	return repositoryworkflow.RetirementFindings(evidence)
}

// retirementReaderFor narrows a provider client to the retirement questions.
//
// A nil client yields a nil reader rather than a non-nil interface holding a nil
// pointer, so the caller's `reader != nil` check means what it reads as. Getting
// that wrong here would turn "no provider configured" into a panic at the moment
// of a deletion.
func retirementReaderFor(reader repositoryworkflow.ProviderReader) RetirementReader {
	client, ok := reader.(*githubprovider.Client)
	if !ok || client == nil {
		return nil
	}
	return client
}
