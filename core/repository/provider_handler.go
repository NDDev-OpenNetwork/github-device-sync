package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
)

type ProviderReader interface {
	GetRepository(context.Context, string, string, string) (githubprovider.Repository, githubprovider.ResponseMeta, bool, error)
}

type ProviderWriter interface {
	Scope() githubprovider.RepositoryMutationScope
	RenameRepository(context.Context, string) (githubprovider.Repository, githubprovider.MutationMeta, error)
	ArchiveRepository(context.Context) (githubprovider.Repository, githubprovider.MutationMeta, error)
	DeleteRepository(context.Context) (githubprovider.MutationMeta, error)
}

type ProviderEvidence struct {
	State    *githubprovider.Repository   `json:"state,omitempty"`
	Digest   string                       `json:"digest,omitempty"`
	Mutation *githubprovider.MutationMeta `json:"mutation,omitempty"`
	Deleted  bool                         `json:"deleted"`
}

type ProviderHandler struct {
	Readers map[string]ProviderReader
	Writer  ProviderWriter
	Scope   githubprovider.RepositoryMutationScope
}

func ProviderDigest(repository githubprovider.Repository) (string, error) {
	return canonicaljson.Digest(repository)
}

func (handler *ProviderHandler) Apply(
	ctx context.Context,
	step operations.Step,
) (operations.ApplyEvidence, error) {
	transition, err := StepTransition(step)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	if err := handler.validateBinding(transition, true); err != nil {
		return operations.ApplyEvidence{}, err
	}
	before, err := handler.observe(
		ctx, transition.CurrentInstallation, transition.CurrentOwner, transition.CurrentName,
	)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	beforeDigest, err := ProviderDigest(before)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	beforeEvidence := ProviderEvidence{State: &before, Digest: beforeDigest}
	if before.ID != transition.ProviderRepositoryID ||
		beforeDigest != transition.ExpectedProviderDigest ||
		!lifecycleMatches(before, transition.CurrentLifecycle) {
		return operations.ApplyEvidence{Before: beforeEvidence}, errors.New(
			"GitHub repository lifecycle state changed after planning; provider mutation was not attempted",
		)
	}
	var meta githubprovider.MutationMeta
	switch transition.Operation {
	case RenameOperation:
		_, meta, err = handler.Writer.RenameRepository(ctx, transition.TargetName)
	case TransferOperation:
		err = errors.New(TransferApplyBlocker)
	case ArchiveOperation:
		_, meta, err = handler.Writer.ArchiveRepository(ctx)
	case DeleteOperation:
		meta, err = handler.Writer.DeleteRepository(ctx)
	default:
		err = errors.New("unsupported GitHub repository lifecycle operation")
	}
	if err != nil {
		return operations.ApplyEvidence{Before: beforeEvidence}, err
	}
	if transition.Operation == DeleteOperation {
		after := ProviderEvidence{Mutation: &meta, Deleted: true}
		if err := handler.verifyAbsent(ctx, transition); err != nil {
			return operations.ApplyEvidence{Before: beforeEvidence, After: after}, err
		}
		return operations.ApplyEvidence{Before: beforeEvidence, After: after}, nil
	}
	afterRepository, err := handler.observe(
		ctx, transition.TargetInstallation, transition.TargetOwner, transition.TargetName,
	)
	if err != nil {
		return operations.ApplyEvidence{Before: beforeEvidence}, fmt.Errorf(
			"GitHub lifecycle mutation returned but target observation failed: %w", err,
		)
	}
	if afterRepository.ID != transition.ProviderRepositoryID ||
		!lifecycleMatches(afterRepository, transition.TargetLifecycle) {
		return operations.ApplyEvidence{Before: beforeEvidence}, errors.New(
			"GitHub lifecycle mutation did not produce the exact target identity and lifecycle",
		)
	}
	afterDigest, err := ProviderDigest(afterRepository)
	if err != nil {
		return operations.ApplyEvidence{Before: beforeEvidence}, err
	}
	afterEvidence := ProviderEvidence{State: &afterRepository, Digest: afterDigest, Mutation: &meta}
	return operations.ApplyEvidence{Before: beforeEvidence, After: afterEvidence}, nil
}

func (handler *ProviderHandler) Verify(
	ctx context.Context,
	step operations.Step,
	recorded json.RawMessage,
) error {
	transition, err := StepTransition(step)
	if err != nil {
		return err
	}
	if err := handler.validateBinding(transition, false); err != nil {
		return err
	}
	var evidence ProviderEvidence
	if err := json.Unmarshal(recorded, &evidence); err != nil {
		return fmt.Errorf("decode GitHub repository lifecycle evidence: %w", err)
	}
	if transition.Operation == DeleteOperation {
		if !evidence.Deleted || evidence.Mutation == nil || evidence.Mutation.RepositoryID != transition.ProviderRepositoryID {
			return errors.New("recorded GitHub repository deletion evidence is incomplete")
		}
		return handler.verifyAbsent(ctx, transition)
	}
	if evidence.State == nil || evidence.Mutation == nil || evidence.State.ID != transition.ProviderRepositoryID {
		return errors.New("recorded GitHub repository lifecycle evidence is incomplete")
	}
	recordedDigest, err := ProviderDigest(*evidence.State)
	if err != nil || recordedDigest != evidence.Digest {
		return errors.New("recorded GitHub repository lifecycle digest is invalid")
	}
	current, err := handler.observe(
		ctx, transition.TargetInstallation, transition.TargetOwner, transition.TargetName,
	)
	if err != nil {
		return err
	}
	if current.ID != transition.ProviderRepositoryID || !lifecycleMatches(current, transition.TargetLifecycle) {
		return errors.New("current GitHub repository lifecycle does not match the operation target")
	}
	return nil
}

func (handler *ProviderHandler) observe(
	ctx context.Context,
	installation string,
	owner string,
	name string,
) (githubprovider.Repository, error) {
	reader := handler.Readers[installation]
	if reader == nil {
		return githubprovider.Repository{}, errors.New("GitHub lifecycle read installation is unavailable")
	}
	repository, _, notModified, err := reader.GetRepository(ctx, owner, name, "")
	if err != nil {
		return githubprovider.Repository{}, err
	}
	if notModified {
		return githubprovider.Repository{}, errors.New("unexpected not-modified lifecycle observation")
	}
	if !strings.EqualFold(repository.Owner, owner) || !strings.EqualFold(repository.Name, name) {
		return githubprovider.Repository{}, errors.New("GitHub lifecycle observation locator mismatch")
	}
	return repository, nil
}

func (handler *ProviderHandler) verifyAbsent(ctx context.Context, transition ProviderTransition) error {
	_, err := handler.observe(
		ctx, transition.CurrentInstallation, transition.CurrentOwner, transition.CurrentName,
	)
	if err == nil {
		return errors.New("GitHub repository still exists after deletion")
	}
	var apiError *githubprovider.APIError
	if !errors.As(err, &apiError) || apiError.Kind != githubprovider.ErrorNotFoundOrInaccessible {
		return fmt.Errorf("GitHub repository deletion absence is not proven: %w", err)
	}
	return nil
}

func (handler *ProviderHandler) validateBinding(transition ProviderTransition, requireWriter bool) error {
	if handler == nil || len(handler.Readers) == 0 || transition.MutationCapabilityID == "" ||
		transition.ExpectedProviderDigest == "" {
		return errors.New("GitHub repository lifecycle handler binding is incomplete")
	}
	scope := handler.Scope
	if handler.Writer != nil {
		writerScope := handler.Writer.Scope()
		if scope.RepositoryID != 0 && !reflect.DeepEqual(scope, writerScope) {
			return errors.New("GitHub repository lifecycle handler and writer scopes differ")
		}
		scope = writerScope
	} else if requireWriter {
		return errors.New("GitHub repository lifecycle writer is unavailable")
	}
	if scope.RepositoryID != transition.ProviderRepositoryID ||
		!strings.EqualFold(scope.Owner, transition.CurrentOwner) ||
		!strings.EqualFold(scope.Name, transition.CurrentName) {
		return errors.New("GitHub repository lifecycle writer identity differs from the immutable plan")
	}
	return nil
}

func lifecycleMatches(repository githubprovider.Repository, lifecycle string) bool {
	return repository.Archived == (lifecycle == "archived")
}
