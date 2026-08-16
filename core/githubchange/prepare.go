package githubchange

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
)

type DesiredFile struct {
	Path    string
	Message string
	Content []byte
}

type PrepareInput struct {
	RepositoryID         string
	ReadInstallationID   string
	MutationCapabilityID string
	ProviderRepositoryID int64
	Owner                string
	Name                 string
	BaseBranch           string
	HeadBranch           string
	Title                string
	Body                 string
	Files                []DesiredFile
}

type ObservedFileState struct {
	Path    string       `json:"path"`
	Base    ContentState `json:"base"`
	Current ContentState `json:"current"`
}

type InitialState struct {
	Base       BranchState                       `json:"base"`
	Head       BranchState                       `json:"head"`
	Files      []ObservedFileState               `json:"files"`
	Comparison *githubprovider.BranchComparison  `json:"comparison,omitempty"`
	Pulls      []githubprovider.DraftPullRequest `json:"pulls"`
}

type Prepared struct {
	Build          BuildResult  `json:"build"`
	Initial        InitialState `json:"initial"`
	EvidenceDigest string       `json:"evidence_digest"`
	NoChanges      bool         `json:"no_changes"`
}

func Prepare(ctx context.Context, reader Reader, input PrepareInput) (Prepared, error) {
	if reader == nil || len(input.Files) == 0 || len(input.Files) > maxChangeFiles {
		return Prepared{}, errors.New("GitHub change preparation input is incomplete")
	}
	desired := append([]DesiredFile(nil), input.Files...)
	sort.Slice(desired, func(left, right int) bool { return desired[left].Path < desired[right].Path })
	for index, file := range desired {
		if !safeRelativePath(file.Path) || !singleLine(file.Message, 512) || len(file.Content) > maxContentBytes ||
			(index > 0 && desired[index-1].Path == file.Path) {
			return Prepared{}, errors.New("GitHub change desired file contract is invalid")
		}
	}
	scope := Scope{
		ReadInstallationID:   input.ReadInstallationID,
		MutationCapabilityID: input.MutationCapabilityID,
		ProviderRepositoryID: input.ProviderRepositoryID,
		Owner:                input.Owner, Name: input.Name, BaseBranch: input.BaseBranch,
		HeadBranch: input.HeadBranch,
	}
	base, err := observeBranchAt(ctx, reader, scope, scope.BaseBranch)
	if err != nil || !base.Exists {
		return Prepared{}, errors.New("GitHub change base branch is unavailable")
	}
	scope.BaseSHA = base.SHA
	head, err := observeBranchAt(ctx, reader, scope, scope.HeadBranch)
	if err != nil {
		return Prepared{}, err
	}
	branch := BranchChange{ExpectedState: "missing", TargetSHA: scope.BaseSHA}
	currentRef := scope.BaseBranch
	if head.Exists {
		branch = BranchChange{ExpectedState: "present", ExpectedSHA: head.SHA, TargetSHA: head.SHA}
		currentRef = scope.HeadBranch
	}
	files := make([]ContentInput, 0, len(desired))
	for _, file := range desired {
		baseContent, err := observeContentAt(ctx, reader, scope, file.Path, scope.BaseBranch)
		if err != nil {
			return Prepared{}, err
		}
		if baseContent.Exists && baseContent.ContentDigest == DigestContent(file.Content) {
			continue
		}
		current, err := observeContentAt(ctx, reader, scope, file.Path, currentRef)
		if err != nil {
			return Prepared{}, err
		}
		content := ContentInput{
			Path: file.Path, Message: file.Message, ExpectedState: "missing",
			Content: append([]byte(nil), file.Content...), FinalStatus: "added",
		}
		if current.Exists {
			content.ExpectedState, content.ExpectedSHA = "regular", current.BlobSHA
		}
		if baseContent.Exists {
			content.FinalStatus = "modified"
		}
		files = append(files, content)
	}
	if len(files) == 0 {
		if head.Exists && head.SHA != base.SHA {
			return Prepared{}, errors.New("existing GitHub change branch has drift but the desired projection has no changes")
		}
		pulls, _, err := reader.ListOpenPullRequests(
			ctx, scope.Owner, scope.Name, scope.HeadBranch, scope.BaseBranch,
		)
		if err != nil || len(pulls) != 0 {
			return Prepared{}, errors.New("an open GitHub change pull request exists without a desired change set")
		}
		initial := InitialState{Base: base, Head: head, Files: []ObservedFileState{}, Pulls: []githubprovider.DraftPullRequest{}}
		digest, err := canonicaljson.Digest(initial)
		return Prepared{Initial: initial, EvidenceDigest: digest, NoChanges: true}, err
	}
	built, err := Build(BuildInput{
		RepositoryID: input.RepositoryID, Scope: scope, Branch: branch,
		Title: input.Title, Body: input.Body, Files: files,
	})
	if err != nil {
		return Prepared{}, err
	}
	initial, err := ObserveInitial(ctx, reader, operations.Plan{Steps: built.Steps})
	if err != nil {
		return Prepared{}, err
	}
	digest, err := canonicaljson.Digest(initial)
	if err != nil {
		return Prepared{}, err
	}
	return Prepared{Build: built, Initial: initial, EvidenceDigest: digest}, nil
}

func ObserveInitial(ctx context.Context, reader Reader, plan operations.Plan) (InitialState, error) {
	if reader == nil {
		return InitialState{}, errors.New("GitHub change reader is unavailable")
	}
	if err := ValidatePlan(plan); err != nil {
		return InitialState{}, err
	}
	first, _ := StepParameters(plan.Steps[0])
	last, _ := StepParameters(plan.Steps[len(plan.Steps)-1])
	base, err := observeBranchAt(ctx, reader, first.Scope, first.Scope.BaseBranch)
	if err != nil || !base.Exists || base.SHA != first.Scope.BaseSHA {
		return InitialState{}, errors.New("GitHub change base branch state differs from the immutable plan")
	}
	head, err := observeBranchAt(ctx, reader, first.Scope, first.Scope.HeadBranch)
	if err != nil {
		return InitialState{}, err
	}
	if (first.Branch.ExpectedState == "missing" && head.Exists) ||
		(first.Branch.ExpectedState == "present" && (!head.Exists || head.SHA != first.Branch.ExpectedSHA)) {
		return InitialState{}, errors.New("GitHub change branch state differs from the immutable plan")
	}
	currentRef := first.Scope.BaseBranch
	if head.Exists {
		currentRef = first.Scope.HeadBranch
	}
	files := make([]ObservedFileState, 0, len(plan.Steps)-2)
	allowed := make(map[string]PlannedFile, len(last.PullRequest.Files))
	for _, file := range last.PullRequest.Files {
		allowed[file.Path] = file
	}
	for _, step := range plan.Steps[1 : len(plan.Steps)-1] {
		parameters, _ := StepParameters(step)
		change := parameters.Content
		baseContent, err := observeContentAt(ctx, reader, first.Scope, change.Path, first.Scope.BaseBranch)
		if err != nil {
			return InitialState{}, err
		}
		if (change.FinalStatus == "added" && baseContent.Exists) ||
			(change.FinalStatus == "modified" && (!baseContent.Exists || baseContent.ContentDigest == change.ContentDigest)) {
			return InitialState{}, errors.New("GitHub change base-relative file status differs from the immutable plan")
		}
		current, err := observeContentAt(ctx, reader, first.Scope, change.Path, currentRef)
		if err != nil {
			return InitialState{}, err
		}
		if (change.ExpectedState == "missing" && current.Exists) ||
			(change.ExpectedState == "regular" && (!current.Exists || current.BlobSHA != change.ExpectedSHA)) {
			return InitialState{}, errors.New("GitHub change content state differs from the immutable plan")
		}
		files = append(files, ObservedFileState{Path: change.Path, Base: baseContent, Current: current})
	}
	initial := InitialState{Base: base, Head: head, Files: files, Pulls: []githubprovider.DraftPullRequest{}}
	if head.Exists && head.SHA != base.SHA {
		comparison, _, err := reader.CompareBranches(
			ctx, first.Scope.Owner, first.Scope.Name, first.Scope.BaseBranch, first.Scope.HeadBranch,
		)
		if err != nil || comparison.Status != "ahead" || comparison.BehindBy != 0 ||
			comparison.BaseSHA != base.SHA || comparison.MergeBaseSHA != base.SHA || comparison.HeadSHA != head.SHA {
			return InitialState{}, errors.New("existing GitHub change branch is not an exact forward change set")
		}
		for _, observed := range comparison.Files {
			expected, found := allowed[observed.Path]
			if !found || observed.Status != expected.Status || observed.PreviousPath != "" {
				return InitialState{}, errors.New("existing GitHub change branch contains an unexpected file")
			}
		}
		initial.Comparison = &comparison
	}
	pulls, _, err := reader.ListOpenPullRequests(
		ctx, first.Scope.Owner, first.Scope.Name, first.Scope.HeadBranch, first.Scope.BaseBranch,
	)
	if err != nil || len(pulls) > 1 {
		return InitialState{}, errors.New("GitHub change pull-request state is unavailable or ambiguous")
	}
	if len(pulls) == 1 && (!head.Exists || !exactPull(pulls[0], *last.PullRequest, head.SHA)) {
		return InitialState{}, errors.New("existing GitHub change pull request differs from the immutable plan")
	}
	initial.Pulls = pulls
	return initial, nil
}

func InitialEvidenceDigest(ctx context.Context, reader Reader, plan operations.Plan) (string, error) {
	state, err := ObserveInitial(ctx, reader, plan)
	if err != nil {
		return "", err
	}
	return canonicaljson.Digest(state)
}

func observeBranchAt(ctx context.Context, reader Reader, scope Scope, branch string) (BranchState, error) {
	result, _, err := reader.GetBranchRef(ctx, scope.Owner, scope.Name, branch)
	if err != nil {
		if providerAbsent(err) {
			return BranchState{}, nil
		}
		return BranchState{}, err
	}
	return BranchState{Exists: true, SHA: result.SHA}, nil
}

func observeContentAt(ctx context.Context, reader Reader, scope Scope, path string, ref string) (ContentState, error) {
	result, _, err := reader.GetContent(ctx, scope.Owner, scope.Name, path, ref)
	if err != nil {
		if providerAbsent(err) {
			return ContentState{}, nil
		}
		return ContentState{}, err
	}
	if result.Path != path {
		return ContentState{}, fmt.Errorf("GitHub content response path differs from %q", path)
	}
	return ContentState{Exists: true, BlobSHA: result.BlobSHA, ContentDigest: DigestContent(result.Content)}, nil
}
