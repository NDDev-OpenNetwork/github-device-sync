package repository

import (
	"fmt"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
)

// A repository deletion is irreversible and the question it turns on is not
// "is this checkout clean" but "does any work exist here that exists nowhere
// else". Those are different questions, and the deletion path only ever asked
// the first one.
//
// Every observable thing is classified into exactly one of four states, and the
// fourth is the point: `unknown` blocks. A page that could not be read, a
// permission that was refused, a rate limit -- none of those mean "nothing
// found", and treating them that way is how a destructive operation proceeds on
// an absence of evidence rather than on evidence of absence.

// RetirementEvidenceSchema is the versioned contract this document satisfies.
const RetirementEvidenceSchema = "RepositoryRetirementEvidence/v1"

// Classification is what one observed item means for retirement.
const (
	// ClassificationCompleted -- the work landed and is reachable from somewhere
	// that survives the deletion.
	ClassificationCompleted = "completed"
	// ClassificationPreserved -- the work does not survive here, and the operator
	// explicitly said so.
	ClassificationPreserved = "preserved"
	// ClassificationBlocking -- unfinished work that would be destroyed.
	ClassificationBlocking = "blocking"
	// ClassificationUnknown -- it could not be observed. Indistinguishable from
	// blocking by construction, and treated as blocking for that reason.
	ClassificationUnknown = "unknown"
)

// RetirementItem is one classified observation.
type RetirementItem struct {
	Kind           string `json:"kind"`
	Identity       string `json:"identity"`
	Classification string `json:"classification"`
	Detail         string `json:"detail,omitempty"`
}

// LocalObservation is what the device holds.
type LocalObservation struct {
	WorktreeRoot        string             `json:"worktree_root"`
	Worktrees           []LocalWorktree    `json:"worktrees"`
	Refs                []LocalRefSnapshot `json:"refs"`
	UnpushedCommits     int                `json:"unpushed_commits"`
	UnpushedCommitsRead bool               `json:"unpushed_commits_read"`
}

type LocalWorktree struct {
	Path     string `json:"path"`
	Branch   string `json:"branch,omitempty"`
	Detached bool   `json:"detached"`
	Locked   bool   `json:"locked"`
	Prunable bool   `json:"prunable"`
	Primary  bool   `json:"primary"`
}

type LocalRefSnapshot struct {
	Name    string `json:"name"`
	OID     string `json:"oid"`
	Remote  bool   `json:"remote"`
	Default bool   `json:"default"`
}

// RemoteObservation is what the provider holds. Each `*Read` flag records
// whether the enumeration completed, because an empty list and an unread one are
// the same bytes and opposite meanings.
type RemoteObservation struct {
	Branches                    []RemoteBranch      `json:"branches"`
	BranchesRead                bool                `json:"branches_read"`
	PullRequests                []RemotePullRequest `json:"pull_requests"`
	PullRequestsRead            bool                `json:"pull_requests_read"`
	OpenIssues                  []RemoteIssue       `json:"open_issues"`
	OpenIssuesRead              bool                `json:"open_issues_read"`
	UnresolvedReviewThreads     int                 `json:"unresolved_review_threads"`
	UnresolvedReviewThreadsRead bool                `json:"unresolved_review_threads_read"`
	DefaultBranch               string              `json:"default_branch"`
}

type RemoteBranch struct {
	Name    string `json:"name"`
	SHA     string `json:"sha"`
	Default bool   `json:"default"`
}

type RemotePullRequest struct {
	Number  int    `json:"number"`
	State   string `json:"state"`
	Draft   bool   `json:"draft"`
	Merged  bool   `json:"merged"`
	HeadRef string `json:"head_ref"`
}

type RemoteIssue struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
}

// PreservationDeclaration is the operator saying, per identity, that something
// is intentionally not being kept.
//
// It is a list of exact identities rather than a blanket flag on purpose: a
// switch that waves past everything at once would restore precisely the
// behaviour this evidence exists to remove.
type PreservationDeclaration struct {
	Identities []string `json:"identities"`
}

// RetirementEvidence is the complete answer, with the decision it implies.
type RetirementEvidence struct {
	Schema       string            `json:"schema"`
	RepositoryID string            `json:"repository_id"`
	Owner        string            `json:"owner"`
	Name         string            `json:"name"`
	Local        LocalObservation  `json:"local"`
	Remote       RemoteObservation `json:"remote"`
	Items        []RetirementItem  `json:"items"`
	Blocking     int               `json:"blocking"`
	Unknown      int               `json:"unknown"`
	Retirable    bool              `json:"retirable"`
}

// ClassifyRetirement builds the evidence and decides whether retirement is
// provable.
func ClassifyRetirement(
	repositoryID string,
	owner string,
	name string,
	local LocalObservation,
	remote RemoteObservation,
	preserved PreservationDeclaration,
) RetirementEvidence {
	evidence := RetirementEvidence{
		Schema: RetirementEvidenceSchema, RepositoryID: repositoryID,
		Owner: owner, Name: name, Local: local, Remote: remote,
		Items: []RetirementItem{},
	}
	declared := map[string]struct{}{}
	for _, identity := range preserved.Identities {
		declared[identity] = struct{}{}
	}
	add := func(kind, identity, classification, detail string) {
		if classification == ClassificationBlocking {
			if _, ok := declared[identity]; ok {
				classification, detail = ClassificationPreserved, "explicitly preserved by the operator"
			}
		}
		evidence.Items = append(evidence.Items, RetirementItem{
			Kind: kind, Identity: identity, Classification: classification, Detail: detail,
		})
	}

	for _, worktree := range local.Worktrees {
		identity := "worktree:" + worktree.Path
		switch {
		case worktree.Primary:
			add("worktree", identity, ClassificationCompleted, "the primary checkout")
		case worktree.Locked:
			add("worktree", identity, ClassificationBlocking, "locked, so its state was not observed")
		case worktree.Prunable:
			add("worktree", identity, ClassificationBlocking, "prunable, so its state was not observed")
		case worktree.Detached:
			add("worktree", identity, ClassificationBlocking, "detached, so no branch records what it holds")
		default:
			add("worktree", identity, ClassificationBlocking,
				"a second checkout occupies branch "+worktree.Branch)
		}
	}

	for _, ref := range local.Refs {
		identity := "ref:" + ref.Name
		switch {
		case ref.Default:
			add("ref", identity, ClassificationCompleted, "the default branch")
		case ref.Remote:
			add("ref", identity, ClassificationCompleted, "published on the provider")
		default:
			add("ref", identity, ClassificationBlocking, "exists only on this device")
		}
	}

	switch {
	case !local.UnpushedCommitsRead:
		add("commits", "commits:unpushed", ClassificationUnknown, "the local commit set was not enumerated")
	case local.UnpushedCommits > 0:
		add("commits", "commits:unpushed", ClassificationBlocking, fmt.Sprintf(
			"%d commits are reachable from a local ref and from no remote", local.UnpushedCommits,
		))
	default:
		add("commits", "commits:unpushed", ClassificationCompleted, "every local commit is published")
	}

	if !remote.BranchesRead {
		add("branch", "branches", ClassificationUnknown, "the branch list was not enumerated")
	}
	for _, branch := range remote.Branches {
		identity := "branch:" + branch.Name
		if branch.Default || branch.Name == remote.DefaultBranch {
			add("branch", identity, ClassificationCompleted, "the default branch")
			continue
		}
		add("branch", identity, ClassificationBlocking, "a non-default branch survives on the provider")
	}

	if !remote.PullRequestsRead {
		add("pull-request", "pull-requests", ClassificationUnknown,
			"the pull-request list was not enumerated")
	}
	for _, request := range remote.PullRequests {
		identity := fmt.Sprintf("pull-request:%d", request.Number)
		switch {
		case request.Merged:
			add("pull-request", identity, ClassificationCompleted, "merged")
		case request.State == "open" && request.Draft:
			add("pull-request", identity, ClassificationBlocking, "open and still a draft")
		case request.State == "open":
			add("pull-request", identity, ClassificationBlocking, "open")
		default:
			add("pull-request", identity, ClassificationBlocking,
				"closed without merging, so its commits landed nowhere")
		}
	}

	if !remote.OpenIssuesRead {
		add("issue", "issues", ClassificationUnknown, "the issue list was not enumerated")
	}
	for _, issue := range remote.OpenIssues {
		add("issue", fmt.Sprintf("issue:%d", issue.Number), ClassificationBlocking, "open")
	}

	switch {
	case !remote.UnresolvedReviewThreadsRead:
		add("review", "review-threads", ClassificationUnknown,
			"review-thread resolution was not observed")
	case remote.UnresolvedReviewThreads > 0:
		add("review", "review-threads", ClassificationBlocking, fmt.Sprintf(
			"%d review conversations are unresolved", remote.UnresolvedReviewThreads,
		))
	default:
		add("review", "review-threads", ClassificationCompleted, "every review conversation is resolved")
	}

	sort.SliceStable(evidence.Items, func(left, right int) bool {
		if evidence.Items[left].Kind != evidence.Items[right].Kind {
			return evidence.Items[left].Kind < evidence.Items[right].Kind
		}
		return evidence.Items[left].Identity < evidence.Items[right].Identity
	})
	for _, item := range evidence.Items {
		switch item.Classification {
		case ClassificationBlocking:
			evidence.Blocking++
		case ClassificationUnknown:
			evidence.Unknown++
		}
	}
	evidence.Retirable = evidence.Blocking == 0 && evidence.Unknown == 0
	return evidence
}

// RetirementFindings reports why retirement is not provable.
//
// Unknown is reported under its own code. "We could not look" and "we looked and
// found unfinished work" call for different actions -- a credential or a bound to
// fix versus work to finish -- and one code for both would send the reader to the
// wrong one.
func RetirementFindings(evidence RetirementEvidence) []domain.Finding {
	findings := []domain.Finding{}
	blocking := []string{}
	unknown := []string{}
	for _, item := range evidence.Items {
		switch item.Classification {
		case ClassificationBlocking:
			blocking = append(blocking, item.Identity+" ("+item.Detail+")")
		case ClassificationUnknown:
			unknown = append(unknown, item.Identity+" ("+item.Detail+")")
		}
	}
	if len(unknown) != 0 {
		findings = append(findings, domain.Finding{
			Code: "GDS_REPOSITORY_RETIREMENT_EVIDENCE_INCOMPLETE", Severity: domain.SeverityHigh,
			Message: "Repository retirement evidence is incomplete, so no deletion decision can rest on it.",
			Evidence: map[string]any{
				"repository_id": evidence.RepositoryID, "unknown": unknown,
			},
		})
	}
	if len(blocking) != 0 {
		findings = append(findings, domain.Finding{
			Code: "GDS_REPOSITORY_RETIREMENT_WORK_REMAINS", Severity: domain.SeverityHigh,
			Message: "Unfinished or unpreserved work remains in this repository.",
			Evidence: map[string]any{
				"repository_id": evidence.RepositoryID, "blocking": blocking,
			},
		})
	}
	return findings
}

// RetirementDigestInput reduces the evidence to the claim a plan binds: which
// identities were observed, and what each was found to be.
//
// The observation counts and free text are deliberately excluded. They are for a
// reader; re-observing produces the same classification for the same estate and
// a different rendering of it, and binding the rendering would make every plan
// stale before its handler was called.
func RetirementDigestInput(evidence RetirementEvidence) []string {
	claim := []string{evidence.Schema, evidence.RepositoryID, evidence.Owner, evidence.Name}
	for _, item := range evidence.Items {
		claim = append(claim, strings.Join(
			[]string{item.Kind, item.Identity, item.Classification}, "\x00",
		))
	}
	return claim
}
