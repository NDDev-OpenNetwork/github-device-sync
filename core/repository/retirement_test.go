package repository

import (
	"strings"
	"testing"
)

func retirableFixture() (LocalObservation, RemoteObservation) {
	local := LocalObservation{
		WorktreeRoot: "/w",
		Worktrees:    []LocalWorktree{{Path: "/w", Branch: "main", Primary: true}},
		Refs: []LocalRefSnapshot{
			{Name: "refs/heads/main", OID: "a", Remote: true, Default: true},
		},
		UnpushedCommits: 0, UnpushedCommitsRead: true,
	}
	remote := RemoteObservation{
		Branches:     []RemoteBranch{{Name: "main", SHA: "a", Default: true}},
		BranchesRead: true,
		PullRequests: []RemotePullRequest{
			{Number: 1, State: "closed", Merged: true, HeadRef: "task/one"},
		},
		PullRequestsRead: true,
		OpenIssues:       []RemoteIssue{}, OpenIssuesRead: true,
		UnresolvedReviewThreads: 0, UnresolvedReviewThreadsRead: true,
		DefaultBranch: "main",
	}
	return local, remote
}

func classify(local LocalObservation, remote RemoteObservation) RetirementEvidence {
	return ClassifyRetirement("repo_x", "example-org", "example", local, remote,
		PreservationDeclaration{})
}

// The one positive case. Only a repository where everything observable is
// completed becomes retirable, and it must actually be reachable -- a contract
// that never opens is containment with extra steps.
func TestOnlyAFullyCompletedRepositoryIsRetirable(t *testing.T) {
	t.Parallel()
	evidence := classify(retirableFixture())
	if !evidence.Retirable || evidence.Blocking != 0 || evidence.Unknown != 0 {
		t.Fatalf("evidence = %#v", evidence)
	}
	if len(RetirementFindings(evidence)) != 0 {
		t.Fatalf("findings = %#v", RetirementFindings(evidence))
	}
	if evidence.Schema != "RepositoryRetirementEvidence/v1" {
		t.Fatalf("schema = %q", evidence.Schema)
	}
}

// The negative matrix. Each case must leave the repository unretirable, because
// each is work that the deletion would destroy or a question that was not
// answered.
func TestEveryUnfinishedShapeBlocksRetirement(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name    string
		mutate  func(*LocalObservation, *RemoteObservation)
		unknown bool
	}{
		{name: "a second worktree occupies a branch", mutate: func(local *LocalObservation, _ *RemoteObservation) {
			local.Worktrees = append(local.Worktrees, LocalWorktree{Path: "/w2", Branch: "task/one"})
		}},
		{name: "a detached secondary worktree", mutate: func(local *LocalObservation, _ *RemoteObservation) {
			local.Worktrees = append(local.Worktrees, LocalWorktree{Path: "/w2", Detached: true})
		}},
		{name: "a locked secondary worktree", mutate: func(local *LocalObservation, _ *RemoteObservation) {
			local.Worktrees = append(local.Worktrees, LocalWorktree{Path: "/w2", Locked: true})
		}},
		{name: "a prunable secondary worktree", mutate: func(local *LocalObservation, _ *RemoteObservation) {
			local.Worktrees = append(local.Worktrees, LocalWorktree{Path: "/w2", Prunable: true})
		}},
		{name: "an unpushed local ref", mutate: func(local *LocalObservation, _ *RemoteObservation) {
			local.Refs = append(local.Refs, LocalRefSnapshot{Name: "refs/heads/task/one", OID: "b"})
		}},
		{name: "commits that exist only on this device", mutate: func(local *LocalObservation, _ *RemoteObservation) {
			local.UnpushedCommits = 3
		}},
		{name: "the local commit set was not enumerated", unknown: true,
			mutate: func(local *LocalObservation, _ *RemoteObservation) {
				local.UnpushedCommitsRead = false
			}},
		{name: "a remote branch other than the default", mutate: func(_ *LocalObservation, remote *RemoteObservation) {
			remote.Branches = append(remote.Branches, RemoteBranch{Name: "task/one", SHA: "b"})
		}},
		{name: "an open pull request", mutate: func(_ *LocalObservation, remote *RemoteObservation) {
			remote.PullRequests = append(remote.PullRequests,
				RemotePullRequest{Number: 2, State: "open", HeadRef: "task/two"})
		}},
		{name: "a draft pull request", mutate: func(_ *LocalObservation, remote *RemoteObservation) {
			remote.PullRequests = append(remote.PullRequests,
				RemotePullRequest{Number: 2, State: "open", Draft: true, HeadRef: "task/two"})
		}},
		{name: "a pull request closed without merging", mutate: func(_ *LocalObservation, remote *RemoteObservation) {
			remote.PullRequests = append(remote.PullRequests,
				RemotePullRequest{Number: 2, State: "closed", Merged: false, HeadRef: "task/two"})
		}},
		{name: "an unresolved review conversation", mutate: func(_ *LocalObservation, remote *RemoteObservation) {
			remote.UnresolvedReviewThreads = 1
		}},
		{name: "review resolution was not observed", unknown: true,
			mutate: func(_ *LocalObservation, remote *RemoteObservation) {
				remote.UnresolvedReviewThreadsRead = false
			}},
		{name: "an open issue", mutate: func(_ *LocalObservation, remote *RemoteObservation) {
			remote.OpenIssues = append(remote.OpenIssues, RemoteIssue{Number: 9, Title: "still open"})
		}},
		{name: "the branch list was not enumerated", unknown: true,
			mutate: func(_ *LocalObservation, remote *RemoteObservation) {
				remote.BranchesRead = false
			}},
		{name: "the pull-request list was not enumerated", unknown: true,
			mutate: func(_ *LocalObservation, remote *RemoteObservation) {
				remote.PullRequestsRead = false
			}},
		{name: "the issue list was not enumerated", unknown: true,
			mutate: func(_ *LocalObservation, remote *RemoteObservation) {
				remote.OpenIssuesRead = false
			}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			local, remote := retirableFixture()
			testCase.mutate(&local, &remote)
			evidence := classify(local, remote)
			if evidence.Retirable {
				t.Fatalf("retirable despite %s: %#v", testCase.name, evidence.Items)
			}
			findings := RetirementFindings(evidence)
			if len(findings) == 0 {
				t.Fatal("no finding was reported")
			}
			// Unknown is its own code: "we could not look" and "we looked and
			// found work" call for different actions.
			wanted := "GDS_REPOSITORY_RETIREMENT_WORK_REMAINS"
			if testCase.unknown {
				wanted = "GDS_REPOSITORY_RETIREMENT_EVIDENCE_INCOMPLETE"
			}
			found := false
			for _, finding := range findings {
				if finding.Code == wanted {
					found = true
				}
			}
			if !found {
				t.Fatalf("findings = %#v, wanted %s", findings, wanted)
			}
		})
	}
}

// Preservation is per identity, never a blanket switch: one flag waving past
// everything at once restores exactly the behaviour this evidence removes.
func TestPreservationIsDeclaredPerIdentity(t *testing.T) {
	t.Parallel()
	local, remote := retirableFixture()
	local.Refs = append(local.Refs, LocalRefSnapshot{Name: "refs/heads/task/one", OID: "b"})
	remote.Branches = append(remote.Branches, RemoteBranch{Name: "task/two", SHA: "c"})

	partial := ClassifyRetirement("repo_x", "o", "n", local, remote,
		PreservationDeclaration{Identities: []string{"ref:refs/heads/task/one"}})
	if partial.Retirable {
		t.Fatal("preserving one identity released an unrelated one")
	}

	both := ClassifyRetirement("repo_x", "o", "n", local, remote,
		PreservationDeclaration{Identities: []string{"ref:refs/heads/task/one", "branch:task/two"}})
	if !both.Retirable {
		t.Fatalf("items = %#v", both.Items)
	}
}

// Unknown can never be preserved away. A declaration says "I know about this and
// accept losing it"; nobody can say that about something they did not observe.
func TestAnUnknownCannotBePreservedAway(t *testing.T) {
	t.Parallel()
	local, remote := retirableFixture()
	remote.BranchesRead = false
	evidence := ClassifyRetirement("repo_x", "o", "n", local, remote,
		PreservationDeclaration{Identities: []string{"branches"}})
	if evidence.Retirable || evidence.Unknown == 0 {
		t.Fatalf("evidence = %#v", evidence.Items)
	}
}

// The digest binds the claim, not its rendering. Re-observing the same estate
// must reproduce the same digest, or every plan is stale before its handler is
// called -- the failure already seen once on the module pin path.
func TestTheDigestInputCarriesTheClaimAndNotItsRendering(t *testing.T) {
	t.Parallel()
	local, remote := retirableFixture()
	first := RetirementDigestInput(classify(local, remote))

	// Same classification, different prose and different counts elsewhere.
	local.WorktreeRoot = "/somewhere/else"
	second := RetirementDigestInput(classify(local, remote))
	if strings.Join(first, "|") != strings.Join(second, "|") {
		t.Fatalf("digest input changed with the rendering:\n%v\n%v", first, second)
	}

	// A different classification is a different claim and must change it.
	remote.OpenIssues = append(remote.OpenIssues, RemoteIssue{Number: 1})
	if strings.Join(RetirementDigestInput(classify(local, remote)), "|") == strings.Join(first, "|") {
		t.Fatal("digest input did not change when an open issue appeared")
	}
}
