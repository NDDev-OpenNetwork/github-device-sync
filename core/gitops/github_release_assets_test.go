package gitops

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
)

type failingAssetMutator struct {
	deleteCalls int
	deleteErr   error
}

func (mutator *failingAssetMutator) CreateRelease(context.Context, githubprovider.ReleaseInput) (githubprovider.Release, githubprovider.MutationMeta, error) {
	return githubprovider.Release{ID: 7, TagName: "v1.4.0", TargetCommitish: strings.Repeat("a", 40),
		Name: "Release v1.4.0", Draft: true}, githubprovider.MutationMeta{}, nil
}

func (mutator *failingAssetMutator) UploadReleaseAsset(context.Context, int64, githubprovider.ReleaseAssetInput) (githubprovider.ReleaseAsset, githubprovider.MutationMeta, error) {
	return githubprovider.ReleaseAsset{}, githubprovider.MutationMeta{}, errors.New("injected upload failure")
}

func (mutator *failingAssetMutator) UpdateRelease(context.Context, int64, githubprovider.ReleaseInput) (githubprovider.Release, githubprovider.MutationMeta, error) {
	return githubprovider.Release{}, githubprovider.MutationMeta{}, errors.New("unexpected publish")
}

func (mutator *failingAssetMutator) DeleteRelease(context.Context, int64) (githubprovider.MutationMeta, error) {
	mutator.deleteCalls++
	return githubprovider.MutationMeta{}, mutator.deleteErr
}

func (mutator *failingAssetMutator) Scope() githubprovider.RepositoryMutationScope {
	return githubprovider.RepositoryMutationScope{RepositoryID: 42, Owner: "example", Name: "provider"}
}

func (mutator *failingAssetMutator) GetReleaseByTag(context.Context, string) (githubprovider.Release, error) {
	return githubprovider.Release{}, errors.New("unexpected observation")
}

func (mutator *failingAssetMutator) ListReleaseAssets(context.Context, int64) ([]githubprovider.ReleaseAsset, error) {
	return nil, errors.New("unexpected observation")
}

func TestGitHubReleaseHandlerCleansDraftAfterPartialAssetUpload(t *testing.T) {
	bytes := []byte("artifact")
	path := filepath.Join(t.TempDir(), "provider.tar.gz")
	if err := os.WriteFile(path, bytes, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(bytes))
	step := operations.Step{Action: PublishGitHubReleaseAction, Parameters: map[string]any{
		"module_release": map[string]any{
			"module_root": "/module/provider", "version": "1.4.0", "tag_style": "v-semver",
			"tag_ref": "refs/tags/v1.4.0", "commit_oid": strings.Repeat("a", 40),
			"assets": []any{map[string]any{"path": path, "name": "provider.tar.gz",
				"size": float64(len(bytes)), "sha256": digest}},
		},
	}}
	for name, cleanupErr := range map[string]error{"cleanup succeeds": nil, "cleanup fails": errors.New("delete failed")} {
		t.Run(name, func(t *testing.T) {
			mutator := &failingAssetMutator{deleteErr: cleanupErr}
			handler, err := NewPublishGitHubReleaseHandler(mutator)
			if err != nil {
				t.Fatal(err)
			}
			_, err = handler.Apply(context.Background(), step)
			if err == nil || mutator.deleteCalls != 1 {
				t.Fatalf("err=%v delete_calls=%d", err, mutator.deleteCalls)
			}
			if cleanupErr != nil && !strings.Contains(err.Error(), "explicit recovery is required") {
				t.Fatalf("cleanup failure was not explicit: %v", err)
			}
		})
	}
}

func TestGitHubReleaseHandlerRejectsAssetByteDriftAndCleansDraft(t *testing.T) {
	bytes := []byte("artifact")
	path := filepath.Join(t.TempDir(), "provider.tar.gz")
	if err := os.WriteFile(path, []byte("changed"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(bytes))
	mutator := &failingAssetMutator{}
	handler, _ := NewPublishGitHubReleaseHandler(mutator)
	step := operations.Step{Action: PublishGitHubReleaseAction, Parameters: map[string]any{
		"module_release": map[string]any{
			"module_root": "/module/provider", "version": "1.4.0", "tag_style": "v-semver",
			"tag_ref": "refs/tags/v1.4.0", "commit_oid": strings.Repeat("a", 40),
			"assets": []any{map[string]any{"path": path, "name": "provider.tar.gz",
				"size": float64(len(bytes)), "sha256": digest}},
		},
	}}
	if _, err := handler.Apply(context.Background(), step); err == nil || mutator.deleteCalls != 1 {
		t.Fatalf("asset drift was not rejected and cleaned: err=%v deletes=%d", err, mutator.deleteCalls)
	}
}
