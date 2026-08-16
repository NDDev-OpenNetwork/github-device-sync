package githubchange

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
)

type Reader interface {
	GetBranchRef(context.Context, string, string, string) (githubprovider.RefResult, githubprovider.ResponseMeta, error)
	GetContent(context.Context, string, string, string, string) (githubprovider.ContentState, githubprovider.ResponseMeta, error)
	CompareBranches(context.Context, string, string, string, string) (githubprovider.BranchComparison, githubprovider.ResponseMeta, error)
	ListOpenPullRequests(context.Context, string, string, string, string) ([]githubprovider.DraftPullRequest, githubprovider.ResponseMeta, error)
}

type Writer interface {
	Scope() githubprovider.RepositoryMutationScope
	CreateBranch(context.Context, string, string) (githubprovider.RefResult, githubprovider.MutationMeta, error)
	FastForwardBranch(context.Context, string, string) (githubprovider.RefResult, githubprovider.MutationMeta, error)
	PutContent(context.Context, githubprovider.ContentUpdate) (githubprovider.ContentResult, githubprovider.MutationMeta, error)
	CreateDraftPullRequest(context.Context, string, string, string, string) (githubprovider.DraftPullRequest, githubprovider.MutationMeta, error)
}

type Handler struct {
	Reader Reader
	Writer Writer
	Scope  githubprovider.RepositoryMutationScope
	Action string
}

type BranchState struct {
	Exists bool   `json:"exists"`
	SHA    string `json:"sha,omitempty"`
}

type ContentState struct {
	Exists        bool   `json:"exists"`
	BlobSHA       string `json:"blob_sha,omitempty"`
	ContentDigest string `json:"content_digest,omitempty"`
	BranchSHA     string `json:"branch_sha,omitempty"`
}

type ChangeSetState struct {
	Comparison        githubprovider.BranchComparison  `json:"comparison"`
	Digest            string                           `json:"digest"`
	ObservationDigest string                           `json:"observation_digest"`
	Pull              *githubprovider.DraftPullRequest `json:"pull,omitempty"`
}

type Evidence struct {
	Branch     *BranchState                 `json:"branch,omitempty"`
	Content    *ContentState                `json:"content,omitempty"`
	ChangeSet  *ChangeSetState              `json:"change_set,omitempty"`
	Mutation   *githubprovider.MutationMeta `json:"mutation,omitempty"`
	Idempotent bool                         `json:"idempotent"`
}

func DigestContent(content []byte) string {
	digest := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func ChangeSetDigest(scope Scope, files []PlannedFile) (string, error) {
	canonicalScope := scope
	canonicalScope.ChangeSetDigest = ""
	return canonicaljson.Digest(struct {
		Scope Scope         `json:"scope"`
		Files []PlannedFile `json:"files"`
	}{Scope: canonicalScope, Files: SortedFiles(files)})
}

func (handler *Handler) Apply(ctx context.Context, step operations.Step) (operations.ApplyEvidence, error) {
	parameters, err := StepParameters(step)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	if err := handler.validateBinding(step.Action, parameters.Scope, true); err != nil {
		return operations.ApplyEvidence{}, err
	}
	switch step.Action {
	case BranchAction:
		return handler.applyBranch(ctx, parameters)
	case ContentAction:
		return handler.applyContent(ctx, parameters)
	case DraftPRAction:
		return handler.applyPullRequest(ctx, parameters)
	default:
		return operations.ApplyEvidence{}, errors.New("unsupported GitHub change action")
	}
}

func (handler *Handler) Verify(ctx context.Context, step operations.Step, recorded json.RawMessage) error {
	parameters, err := StepParameters(step)
	if err != nil {
		return err
	}
	if err := handler.validateBinding(step.Action, parameters.Scope, false); err != nil {
		return err
	}
	var evidence Evidence
	if err := json.Unmarshal(recorded, &evidence); err != nil {
		return fmt.Errorf("decode recorded GitHub change evidence: %w", err)
	}
	switch step.Action {
	case BranchAction:
		if evidence.Branch == nil || !evidence.Branch.Exists || evidence.Branch.SHA != parameters.Branch.TargetSHA {
			return errors.New("recorded GitHub branch evidence is invalid")
		}
		current, err := handler.observeBranch(ctx, parameters.Scope, parameters.Scope.HeadBranch)
		if err != nil || !current.Exists {
			return errors.New("GitHub change branch is no longer available")
		}
		return nil
	case ContentAction:
		if evidence.Content == nil || evidence.Content.ContentDigest != parameters.Content.ContentDigest {
			return errors.New("recorded GitHub content evidence is invalid")
		}
		current, err := handler.observeContent(ctx, parameters.Scope, parameters.Content.Path)
		if err != nil || !current.Exists || current.ContentDigest != parameters.Content.ContentDigest {
			return errors.New("current GitHub content differs from the planned change")
		}
		return nil
	case DraftPRAction:
		if evidence.ChangeSet == nil || evidence.ChangeSet.Pull == nil {
			return errors.New("recorded GitHub draft-PR evidence is incomplete")
		}
		observationDigest, err := canonicaljson.Digest(evidence.ChangeSet.Comparison)
		if err != nil || observationDigest != evidence.ChangeSet.ObservationDigest {
			return errors.New("recorded GitHub change-set observation digest is invalid")
		}
		current, err := handler.observeChangeSet(ctx, parameters)
		if err != nil || current.Digest != evidence.ChangeSet.Digest ||
			current.ObservationDigest != evidence.ChangeSet.ObservationDigest {
			return errors.New("current GitHub change set differs from recorded evidence")
		}
		pulls, _, err := handler.Reader.ListOpenPullRequests(
			ctx, parameters.Scope.Owner, parameters.Scope.Name,
			parameters.Scope.HeadBranch, parameters.Scope.BaseBranch,
		)
		if err != nil || len(pulls) != 1 || pulls[0].ID != evidence.ChangeSet.Pull.ID ||
			!exactPull(pulls[0], *parameters.PullRequest, current.Comparison.HeadSHA) {
			return errors.New("current GitHub draft pull request differs from the planned change")
		}
		return nil
	default:
		return errors.New("unsupported GitHub change action")
	}
}

func (handler *Handler) applyBranch(
	ctx context.Context,
	parameters OperationParameters,
) (operations.ApplyEvidence, error) {
	if err := handler.requireBase(ctx, parameters.Scope); err != nil {
		return operations.ApplyEvidence{}, err
	}
	before, err := handler.observeBranch(ctx, parameters.Scope, parameters.Scope.HeadBranch)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	beforeEvidence := Evidence{Branch: &before}
	change := parameters.Branch
	if change.ExpectedState == "present" && before.Exists && before.SHA == change.TargetSHA {
		beforeEvidence.Idempotent = true
		return operations.ApplyEvidence{Before: beforeEvidence, After: beforeEvidence}, nil
	}
	if (change.ExpectedState == "missing" && before.Exists) ||
		(change.ExpectedState == "present" && (!before.Exists || before.SHA != change.ExpectedSHA)) {
		return operations.ApplyEvidence{Before: beforeEvidence}, errors.New(
			"GitHub change branch state changed after planning; mutation was not attempted",
		)
	}
	var meta githubprovider.MutationMeta
	if change.ExpectedState == "missing" {
		_, meta, err = handler.Writer.CreateBranch(ctx, parameters.Scope.HeadBranch, change.TargetSHA)
	} else {
		_, meta, err = handler.Writer.FastForwardBranch(ctx, parameters.Scope.HeadBranch, change.TargetSHA)
	}
	if err != nil {
		return operations.ApplyEvidence{Before: beforeEvidence}, err
	}
	after, err := handler.observeBranch(ctx, parameters.Scope, parameters.Scope.HeadBranch)
	if err != nil || !after.Exists || after.SHA != change.TargetSHA {
		return operations.ApplyEvidence{Before: beforeEvidence}, errors.New(
			"GitHub branch mutation completed without the exact planned target",
		)
	}
	afterEvidence := Evidence{Branch: &after, Mutation: &meta}
	return operations.ApplyEvidence{Before: beforeEvidence, After: afterEvidence}, nil
}

func (handler *Handler) applyContent(
	ctx context.Context,
	parameters OperationParameters,
) (operations.ApplyEvidence, error) {
	if err := handler.requireBase(ctx, parameters.Scope); err != nil {
		return operations.ApplyEvidence{}, err
	}
	if branch, err := handler.observeBranch(ctx, parameters.Scope, parameters.Scope.HeadBranch); err != nil || !branch.Exists {
		return operations.ApplyEvidence{}, errors.New("GitHub change branch is unavailable before content mutation")
	}
	before, err := handler.observeContent(ctx, parameters.Scope, parameters.Content.Path)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	beforeEvidence := Evidence{Content: &before}
	if before.Exists && before.ContentDigest == parameters.Content.ContentDigest {
		beforeEvidence.Idempotent = true
		return operations.ApplyEvidence{Before: beforeEvidence, After: beforeEvidence}, nil
	}
	change := parameters.Content
	if (change.ExpectedState == "missing" && before.Exists) ||
		(change.ExpectedState == "regular" && (!before.Exists || before.BlobSHA != change.ExpectedSHA)) {
		return operations.ApplyEvidence{Before: beforeEvidence}, errors.New(
			"GitHub content state changed after planning; mutation was not attempted",
		)
	}
	content, err := DecodeContent(*change)
	if err != nil {
		return operations.ApplyEvidence{Before: beforeEvidence}, err
	}
	result, meta, err := handler.Writer.PutContent(ctx, githubprovider.ContentUpdate{
		Path: change.Path, Message: change.Message, Content: content,
		Branch: parameters.Scope.HeadBranch, ExpectedSHA: change.ExpectedSHA,
	})
	if err != nil {
		return operations.ApplyEvidence{Before: beforeEvidence}, err
	}
	after, err := handler.observeContent(ctx, parameters.Scope, change.Path)
	if err != nil || !after.Exists || after.BlobSHA != result.BlobSHA ||
		after.ContentDigest != change.ContentDigest {
		return operations.ApplyEvidence{Before: beforeEvidence}, errors.New(
			"GitHub content mutation completed without the exact planned content",
		)
	}
	branch, err := handler.observeBranch(ctx, parameters.Scope, parameters.Scope.HeadBranch)
	if err != nil || !branch.Exists || branch.SHA != result.CommitSHA {
		return operations.ApplyEvidence{Before: beforeEvidence}, errors.New(
			"GitHub content commit is not the current change-branch head",
		)
	}
	after.BranchSHA = branch.SHA
	afterEvidence := Evidence{Content: &after, Mutation: &meta}
	return operations.ApplyEvidence{Before: beforeEvidence, After: afterEvidence}, nil
}

func (handler *Handler) applyPullRequest(
	ctx context.Context,
	parameters OperationParameters,
) (operations.ApplyEvidence, error) {
	changeSet, err := handler.observeChangeSet(ctx, parameters)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	beforePulls, _, err := handler.Reader.ListOpenPullRequests(
		ctx, parameters.Scope.Owner, parameters.Scope.Name,
		parameters.Scope.HeadBranch, parameters.Scope.BaseBranch,
	)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	beforeEvidence := Evidence{ChangeSet: &changeSet}
	if len(beforePulls) == 1 {
		if !exactPull(beforePulls[0], *parameters.PullRequest, changeSet.Comparison.HeadSHA) {
			return operations.ApplyEvidence{Before: beforeEvidence}, errors.New(
				"an open GitHub pull request already exists with different immutable content",
			)
		}
		changeSet.Pull = &beforePulls[0]
		beforeEvidence.ChangeSet = &changeSet
		beforeEvidence.Idempotent = true
		return operations.ApplyEvidence{Before: beforeEvidence, After: beforeEvidence}, nil
	}
	pull, meta, err := handler.Writer.CreateDraftPullRequest(
		ctx, parameters.PullRequest.Title, parameters.PullRequest.Body,
		parameters.Scope.HeadBranch, parameters.Scope.BaseBranch,
	)
	if err != nil {
		return operations.ApplyEvidence{Before: beforeEvidence}, err
	}
	afterPulls, _, err := handler.Reader.ListOpenPullRequests(
		ctx, parameters.Scope.Owner, parameters.Scope.Name,
		parameters.Scope.HeadBranch, parameters.Scope.BaseBranch,
	)
	if err != nil || len(afterPulls) != 1 || afterPulls[0].ID != pull.ID ||
		!exactPull(afterPulls[0], *parameters.PullRequest, changeSet.Comparison.HeadSHA) {
		return operations.ApplyEvidence{Before: beforeEvidence}, errors.New(
			"GitHub draft pull-request mutation did not produce the exact planned result",
		)
	}
	changeSet.Pull = &afterPulls[0]
	afterEvidence := Evidence{ChangeSet: &changeSet, Mutation: &meta}
	return operations.ApplyEvidence{Before: beforeEvidence, After: afterEvidence}, nil
}

func (handler *Handler) requireBase(ctx context.Context, scope Scope) error {
	state, err := handler.observeBranch(ctx, scope, scope.BaseBranch)
	if err != nil || !state.Exists || state.SHA != scope.BaseSHA {
		return errors.New("GitHub base branch changed after planning")
	}
	return nil
}

func (handler *Handler) observeBranch(ctx context.Context, scope Scope, branch string) (BranchState, error) {
	result, _, err := handler.Reader.GetBranchRef(ctx, scope.Owner, scope.Name, branch)
	if err != nil {
		if providerAbsent(err) {
			return BranchState{}, nil
		}
		return BranchState{}, err
	}
	return BranchState{Exists: true, SHA: result.SHA}, nil
}

func (handler *Handler) observeContent(ctx context.Context, scope Scope, path string) (ContentState, error) {
	result, _, err := handler.Reader.GetContent(ctx, scope.Owner, scope.Name, path, scope.HeadBranch)
	if err != nil {
		if providerAbsent(err) {
			return ContentState{}, nil
		}
		return ContentState{}, err
	}
	return ContentState{
		Exists: true, BlobSHA: result.BlobSHA, ContentDigest: DigestContent(result.Content),
	}, nil
}

func (handler *Handler) observeChangeSet(ctx context.Context, parameters OperationParameters) (ChangeSetState, error) {
	if err := handler.requireBase(ctx, parameters.Scope); err != nil {
		return ChangeSetState{}, err
	}
	branch, err := handler.observeBranch(ctx, parameters.Scope, parameters.Scope.HeadBranch)
	if err != nil || !branch.Exists {
		return ChangeSetState{}, errors.New("GitHub change branch is unavailable")
	}
	comparison, _, err := handler.Reader.CompareBranches(
		ctx, parameters.Scope.Owner, parameters.Scope.Name,
		parameters.Scope.BaseBranch, parameters.Scope.HeadBranch,
	)
	if err != nil {
		return ChangeSetState{}, err
	}
	if comparison.Status != "ahead" || comparison.BehindBy != 0 || comparison.AheadBy < 1 ||
		comparison.BaseSHA != parameters.Scope.BaseSHA ||
		comparison.MergeBaseSHA != parameters.Scope.BaseSHA || comparison.HeadSHA != branch.SHA {
		return ChangeSetState{}, errors.New("GitHub branch comparison is not an exact forward change set")
	}
	files := SortedFiles(parameters.PullRequest.Files)
	if len(comparison.Files) != len(files) {
		return ChangeSetState{}, errors.New("GitHub change set contains unexpected files")
	}
	for index, expected := range files {
		observed := comparison.Files[index]
		if observed.Path != expected.Path || observed.Status != expected.Status || observed.PreviousPath != "" {
			return ChangeSetState{}, errors.New("GitHub change-set file differs from the immutable plan")
		}
		content, err := handler.observeContent(ctx, parameters.Scope, expected.Path)
		if err != nil || !content.Exists || content.ContentDigest != expected.ContentDigest {
			return ChangeSetState{}, errors.New("GitHub change-set content digest differs from the immutable plan")
		}
	}
	digest, err := ChangeSetDigest(parameters.Scope, files)
	if err != nil || digest != parameters.Scope.ChangeSetDigest {
		return ChangeSetState{}, errors.New("GitHub change-set digest differs from the immutable plan")
	}
	observationDigest, err := canonicaljson.Digest(comparison)
	if err != nil {
		return ChangeSetState{}, err
	}
	return ChangeSetState{
		Comparison: comparison, Digest: digest, ObservationDigest: observationDigest,
	}, nil
}

func (handler *Handler) validateBinding(action string, scope Scope, requireWriter bool) error {
	if handler == nil || handler.Reader == nil || handler.Action != action {
		return errors.New("GitHub change handler binding is incomplete")
	}
	bound := handler.Scope
	if handler.Writer != nil {
		writerScope := handler.Writer.Scope()
		if bound.RepositoryID != 0 && !reflect.DeepEqual(bound, writerScope) {
			return errors.New("GitHub change handler and writer scopes differ")
		}
		bound = writerScope
	} else if requireWriter {
		return errors.New("GitHub change mutation writer is unavailable")
	}
	if bound.RepositoryID != scope.ProviderRepositoryID ||
		!strings.EqualFold(bound.Owner, scope.Owner) || !strings.EqualFold(bound.Name, scope.Name) {
		return errors.New("GitHub change writer identity differs from the immutable plan")
	}
	return nil
}

func exactPull(value githubprovider.DraftPullRequest, expected PullRequestChange, headSHA string) bool {
	return value.Draft && value.State == "open" && value.Title == expected.Title &&
		value.Body == expected.Body && value.HeadSHA == headSHA
}

func providerAbsent(err error) bool {
	var apiError *githubprovider.APIError
	return errors.As(err, &apiError) && apiError.Kind == githubprovider.ErrorNotFoundOrInaccessible
}
