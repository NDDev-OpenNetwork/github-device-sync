package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
)

type lifecycleFixture struct {
	repositories map[string]githubprovider.Repository
	scope        githubprovider.RepositoryMutationScope
	writes       int
}

func (fixture *lifecycleFixture) GetRepository(
	_ context.Context,
	owner string,
	name string,
	_ string,
) (githubprovider.Repository, githubprovider.ResponseMeta, bool, error) {
	repository, found := fixture.repositories[strings.ToLower(owner+"/"+name)]
	if !found {
		return githubprovider.Repository{}, githubprovider.ResponseMeta{}, false, &githubprovider.APIError{
			Kind: githubprovider.ErrorNotFoundOrInaccessible, StatusCode: 404,
		}
	}
	return repository, githubprovider.ResponseMeta{RequestID: "read"}, false, nil
}

func (fixture *lifecycleFixture) Scope() githubprovider.RepositoryMutationScope { return fixture.scope }

func (fixture *lifecycleFixture) RenameRepository(
	_ context.Context,
	name string,
) (githubprovider.Repository, githubprovider.MutationMeta, error) {
	repository := fixture.current()
	delete(fixture.repositories, strings.ToLower(repository.Owner+"/"+repository.Name))
	repository.Name = name
	repository.FullName = repository.Owner + "/" + name
	repository.HTMLURL = "https://github.com/" + repository.FullName
	fixture.repositories[strings.ToLower(repository.FullName)] = repository
	fixture.writes++
	return repository, fixture.meta(), nil
}

func (fixture *lifecycleFixture) ArchiveRepository(
	context.Context,
) (githubprovider.Repository, githubprovider.MutationMeta, error) {
	repository := fixture.current()
	repository.Archived = true
	fixture.repositories[strings.ToLower(repository.FullName)] = repository
	fixture.writes++
	return repository, fixture.meta(), nil
}

func (fixture *lifecycleFixture) DeleteRepository(context.Context) (githubprovider.MutationMeta, error) {
	repository := fixture.current()
	delete(fixture.repositories, strings.ToLower(repository.FullName))
	fixture.writes++
	return fixture.meta(), nil
}

func (fixture *lifecycleFixture) current() githubprovider.Repository {
	return fixture.repositories[strings.ToLower(fixture.scope.Owner+"/"+fixture.scope.Name)]
}

func (fixture *lifecycleFixture) meta() githubprovider.MutationMeta {
	return githubprovider.MutationMeta{
		RepositoryID: fixture.scope.RepositoryID, StatusCode: 200, RequestID: "write",
	}
}

func TestProviderHandlerAppliesAndVerifiesRename(t *testing.T) {
	fixture := lifecycleTestFixture(false)
	transition := lifecycleTransition(t, fixture, RenameOperation)
	transition.TargetName = "renamed"
	step := operations.Step{
		RepositoryID: transition.RepositoryID, Action: ProviderLifecycleAction,
		Parameters: Parameters(transition),
	}
	handler := &ProviderHandler{
		Readers: map[string]ProviderReader{transition.CurrentInstallation: fixture},
		Writer:  fixture,
	}
	evidence, err := handler.Apply(context.Background(), step)
	if err != nil || fixture.writes != 1 {
		t.Fatalf("evidence=%#v err=%v writes=%d", evidence, err, fixture.writes)
	}
	raw, err := json.Marshal(evidence.After)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.Verify(context.Background(), step, raw); err != nil {
		t.Fatal(err)
	}
}

func TestProviderHandlerBlocksStaleRepositoryBeforeWrite(t *testing.T) {
	fixture := lifecycleTestFixture(false)
	transition := lifecycleTransition(t, fixture, ArchiveOperation)
	transition.TargetLifecycle = "archived"
	repository := fixture.current()
	repository.DefaultBranch = "changed"
	fixture.repositories[strings.ToLower(repository.FullName)] = repository
	handler := &ProviderHandler{
		Readers: map[string]ProviderReader{transition.CurrentInstallation: fixture}, Writer: fixture,
	}
	_, err := handler.Apply(context.Background(), operations.Step{
		RepositoryID: transition.RepositoryID, Action: ProviderLifecycleAction,
		Parameters: Parameters(transition),
	})
	if err == nil || fixture.writes != 0 {
		t.Fatalf("err=%v writes=%d", err, fixture.writes)
	}
}

func TestProviderHandlerRejectsTransferWithoutProviderWrite(t *testing.T) {
	fixture := lifecycleTestFixture(false)
	transition := lifecycleTransition(t, fixture, TransferOperation)
	transition.TargetOwner = "target-owner"
	transition.TargetInstallation = "installation:target-owner"
	handler := &ProviderHandler{
		Readers: map[string]ProviderReader{
			transition.CurrentInstallation: fixture,
			transition.TargetInstallation:  fixture,
		},
		Writer: fixture,
	}
	_, err := handler.Apply(context.Background(), operations.Step{
		RepositoryID: transition.RepositoryID, Action: ProviderLifecycleAction,
		Parameters: Parameters(transition),
	})
	if err == nil || err.Error() != TransferApplyBlocker || fixture.writes != 0 {
		t.Fatalf("err=%v writes=%d", err, fixture.writes)
	}
}

func TestProviderHandlerRequiresRecordedDeleteAndCurrentAbsence(t *testing.T) {
	fixture := lifecycleTestFixture(true)
	transition := lifecycleTransition(t, fixture, DeleteOperation)
	transition.CurrentLifecycle = "archived"
	transition.TargetLifecycle = "tombstoned"
	transition.AnalysisRoot = "/verified-estate"
	handler := &ProviderHandler{
		Readers: map[string]ProviderReader{transition.CurrentInstallation: fixture}, Writer: fixture,
	}
	step := operations.Step{
		RepositoryID: transition.RepositoryID, Action: ProviderLifecycleAction,
		Parameters: Parameters(transition),
	}
	evidence, err := handler.Apply(context.Background(), step)
	if err != nil || fixture.writes != 1 {
		t.Fatalf("evidence=%#v err=%v writes=%d", evidence, err, fixture.writes)
	}
	raw, err := json.Marshal(evidence.After)
	if err != nil {
		t.Fatal(err)
	}
	if err := handler.Verify(context.Background(), step, raw); err != nil {
		t.Fatal(err)
	}
}

func lifecycleTestFixture(archived bool) *lifecycleFixture {
	repository := githubprovider.Repository{
		ID: 42, NodeID: "R_fixture", Owner: "example", Name: "repository",
		FullName: "example/repository", Private: true, Visibility: "private",
		Archived: archived, DefaultBranch: "main",
		HTMLURL: "https://github.com/example/repository",
		Merge: githubprovider.MergeSettings{
			MergeCommitTitle: "PR_TITLE", MergeCommitMessage: "PR_BODY",
			SquashMergeTitle: "PR_TITLE", SquashMergeMessage: "PR_BODY",
		},
	}
	return &lifecycleFixture{
		repositories: map[string]githubprovider.Repository{"example/repository": repository},
		scope: githubprovider.RepositoryMutationScope{
			RepositoryID: 42, Owner: "example", Name: "repository",
			Operations: []string{githubprovider.MutationRepositoryLifecycle, githubprovider.MutationRepositoryDelete},
		},
	}
}

func lifecycleTransition(t *testing.T, fixture *lifecycleFixture, operation string) ProviderTransition {
	t.Helper()
	repository := fixture.current()
	digest, err := ProviderDigest(repository)
	if err != nil {
		t.Fatal(err)
	}
	return ProviderTransition{
		Operation: operation, RepositoryID: "repo_01JEXAMPZ0000000000000000C",
		ProviderRepositoryID: repository.ID,
		MutationCapabilityID: "mutation:github-personal", ExpectedProviderDigest: digest,
		CurrentInstallation: "installation:github-personal",
		CurrentOwner:        repository.Owner, CurrentName: repository.Name, CurrentLifecycle: "active",
		TargetInstallation: "installation:github-personal",
		TargetOwner:        repository.Owner, TargetName: repository.Name, TargetLifecycle: "active",
	}
}

func (fixture *lifecycleFixture) String() string {
	return fmt.Sprintf("writes=%d repositories=%d", fixture.writes, len(fixture.repositories))
}
