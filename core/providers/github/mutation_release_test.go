package github

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func releaseJSON(id int64, owner, name, tag, target string, immutable bool) string {
	return fmt.Sprintf(
		`{"id":%d,"node_id":"node-release-%d","tag_name":%q,"target_commitish":%q,`+
			`"name":%q,"body":"Immutable release","html_url":%q,`+
			`"draft":false,"prerelease":false,"immutable":%t}`,
		id, id, tag, target, "Release "+tag,
		"https://github.com/"+owner+"/"+name+"/releases/tag/"+tag, immutable,
	)
}

func TestCreateReleaseSendsPinnedRequestAndDecodesImmutableRelease(t *testing.T) {
	var requests atomic.Int32
	target := strings.Repeat("a", 40)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Method+" "+request.URL.Path != "POST /repos/example/repository/releases" {
			http.NotFound(writer, request)
			return
		}
		if request.Header.Get("Authorization") != "Bearer mutation-token" ||
			request.Header.Get("Content-Type") != "application/json" ||
			request.Header.Get("Accept") != "application/vnd.github+json" ||
			request.Header.Get("X-GitHub-Api-Version") != APIVersion {
			t.Errorf("unexpected headers=%#v", request.Header)
		}
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
		}
		if body["tag_name"] != "v1.4.0" || body["target_commitish"] != target ||
			body["name"] != "Release v1.4.0" || body["draft"] != false ||
			body["prerelease"] != false || body["make_latest"] != "true" {
			t.Errorf("unexpected release body=%#v", body)
		}
		_, _ = writer.Write([]byte(releaseJSON(21, "example", "repository", "v1.4.0", target, true)))
	}))
	defer server.Close()
	mutator := mutationTestMutator(t, server, []string{MutationRepositoryRelease}, nil, nil)
	repository, err := mutator.BindRepository(RepositoryMutationScope{
		RepositoryID: 42, Owner: "example", Name: "repository",
		Operations: []string{MutationRepositoryRelease},
	})
	if err != nil {
		t.Fatal(err)
	}
	release, meta, err := repository.CreateRelease(context.Background(), ReleaseInput{
		TagName: "v1.4.0", TargetCommitish: target, Name: "Release v1.4.0",
		Body: "Immutable release", MakeLatest: "true",
	})
	if err != nil {
		t.Fatal(err)
	}
	if release.ID != 21 || release.TagName != "v1.4.0" || release.TargetCommitish != target ||
		release.NodeID != "node-release-21" || !release.Immutable || release.Draft ||
		release.HTMLURL != "https://github.com/example/repository/releases/tag/v1.4.0" ||
		meta.RepositoryID != 42 || requests.Load() != 1 {
		t.Fatalf("release=%#v meta=%#v requests=%d", release, meta, requests.Load())
	}
}

func TestCreateReleaseRejectsResponseThatDoesNotEchoRequest(t *testing.T) {
	target := strings.Repeat("a", 40)
	other := strings.Repeat("b", 40)
	for name, payload := range map[string]string{
		"tag-mismatch":    releaseJSON(21, "example", "repository", "v9.9.9", target, true),
		"target-mismatch": releaseJSON(21, "example", "repository", "v1.4.0", other, true),
	} {
		t.Run(name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte(payload))
			}))
			defer server.Close()
			mutator := mutationTestMutator(t, server, []string{MutationRepositoryRelease}, nil, nil)
			repository, err := mutator.BindRepository(RepositoryMutationScope{
				RepositoryID: 42, Owner: "example", Name: "repository",
				Operations: []string{MutationRepositoryRelease},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = repository.CreateRelease(context.Background(), ReleaseInput{
				TagName: "v1.4.0", TargetCommitish: target, Name: "Release v1.4.0",
			})
			var apiError *APIError
			if !errors.As(err, &apiError) || apiError.Kind != ErrorResponse {
				t.Fatalf("mismatched response was not rejected as invalid: %v", err)
			}
		})
	}
}

func TestCreateReleaseValidatesInputBeforeRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	mutator := mutationTestMutator(t, server, []string{MutationRepositoryRelease}, nil, nil)
	repository, err := mutator.BindRepository(RepositoryMutationScope{
		RepositoryID: 42, Owner: "example", Name: "repository",
		Operations: []string{MutationRepositoryRelease},
	})
	if err != nil {
		t.Fatal(err)
	}
	target := strings.Repeat("a", 40)
	for name, input := range map[string]ReleaseInput{
		"bad-tag":         {TagName: "vv1.4.0", TargetCommitish: target, Name: "Release"},
		"non-hex-target":  {TagName: "v1.4.0", TargetCommitish: strings.Repeat("z", 40), Name: "Release"},
		"short-target":    {TagName: "v1.4.0", TargetCommitish: strings.Repeat("a", 39), Name: "Release"},
		"empty-name":      {TagName: "v1.4.0", TargetCommitish: target, Name: ""},
		"control-name":    {TagName: "v1.4.0", TargetCommitish: target, Name: "Release\nv1.4.0"},
		"bad-make-latest": {TagName: "v1.4.0", TargetCommitish: target, Name: "Release", MakeLatest: "maybe"},
		"null-in-body":    {TagName: "v1.4.0", TargetCommitish: target, Name: "Release", Body: "x\x00y"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, _, err := repository.CreateRelease(context.Background(), input); err == nil {
				t.Fatalf("invalid release input %q was accepted", name)
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("rejected release inputs made %d provider requests", requests.Load())
	}
}

func TestReleaseInputAcceptsDeclaredNumericSemVerTag(t *testing.T) {
	err := validateReleaseInput(ReleaseInput{
		TagName: "1.4.0", TargetCommitish: strings.Repeat("a", 40), Name: "Release 1.4.0",
	})
	if err != nil {
		t.Fatalf("numeric SemVer tag was rejected: %v", err)
	}
}

func TestCreateReleaseDoesNotExposeTokenOrResponseBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("X-GitHub-Request-Id", "request-secret")
		writer.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = writer.Write([]byte(`{"message":"private-repository-name mutation-token"}`))
	}))
	defer server.Close()
	mutator := mutationTestMutator(t, server, []string{MutationRepositoryRelease}, nil, nil)
	repository, err := mutator.BindRepository(RepositoryMutationScope{
		RepositoryID: 42, Owner: "example", Name: "repository",
		Operations: []string{MutationRepositoryRelease},
	})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = repository.CreateRelease(context.Background(), ReleaseInput{
		TagName: "v1.4.0", TargetCommitish: strings.Repeat("a", 40), Name: "Release v1.4.0",
	})
	var apiError *APIError
	if !errors.As(err, &apiError) || apiError.Kind != ErrorValidation ||
		strings.Contains(err.Error(), "mutation-token") ||
		strings.Contains(err.Error(), "private-repository-name") {
		t.Fatalf("unsafe API error: %#v %v", apiError, err)
	}
}

func TestCreateReleasePermissionContractRejectsMissingContentsWriteBeforeRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	defer server.Close()
	now := func() time.Time { return time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC) }
	contractPermissions := map[string]string{"contents": "write", "metadata": "read"}
	contract, err := NewPermissionContract(contractPermissions, map[string]string{}, "selected")
	if err != nil {
		t.Fatal(err)
	}
	for name, tokenPermissions := range map[string]map[string]string{
		"missing": {"metadata": "read"},
		"excess":  {"contents": "write", "metadata": "read", "pull_requests": "write"},
		"weaker":  {"contents": "read", "metadata": "read"},
	} {
		t.Run(name, func(t *testing.T) {
			tokens := tokenSourceFunc(func(context.Context, string) (InstallationToken, error) {
				return InstallationToken{
					Value: "mutation-token", ExpiresAt: now().Add(time.Hour),
					Permissions: clonePermissions(tokenPermissions), RepositorySelection: "selected",
				}, nil
			})
			httpClient := *server.Client()
			httpClient.Timeout = 2 * time.Second
			mutator, err := NewMutator(MutatorConfig{
				Client: Config{
					BaseURL: server.URL + "/", HTTPClient: &httpClient,
					TokenSource: tokens, InstallationID: "mutation:test",
					AllowInsecureLoopback: true, Now: now, PermissionContract: contract,
				},
				Operations: []string{MutationRepositoryRelease}, MinimumSpacing: time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			repository, err := mutator.BindRepository(RepositoryMutationScope{
				RepositoryID: 42, Owner: "example", Name: "repository",
				Operations: []string{MutationRepositoryRelease},
			})
			if err != nil {
				t.Fatal(err)
			}
			_, _, err = repository.CreateRelease(context.Background(), ReleaseInput{
				TagName: "v1.4.0", TargetCommitish: strings.Repeat("a", 40), Name: "Release v1.4.0",
			})
			var apiError *APIError
			if !errors.As(err, &apiError) || apiError.Kind != ErrorPermissionContract {
				t.Fatalf("permission mismatch error=%v", err)
			}
		})
	}
	if requests.Load() != 0 {
		t.Fatalf("permission mismatch made %d provider requests", requests.Load())
	}
}

func TestReleaseAssetUploadPublishAndDraftCleanupAreRepositoryBound(t *testing.T) {
	targetOID := strings.Repeat("a", 40)
	assetBytes := []byte("artifact")
	assetDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(assetBytes))
	var methods []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		methods = append(methods, request.Method+" "+request.URL.RequestURI())
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/repos/example/repository/releases/21/assets":
			if request.URL.Query().Get("name") != "provider.tar.gz" ||
				request.Header.Get("Content-Type") != "application/octet-stream" {
				t.Errorf("unexpected upload request: %s %#v", request.URL.String(), request.Header)
			}
			body, _ := io.ReadAll(request.Body)
			if !bytes.Equal(body, assetBytes) {
				t.Errorf("uploaded bytes differ")
			}
			_, _ = writer.Write([]byte(`{"id":22,"name":"provider.tar.gz","size":8,"state":"uploaded","digest":"` + assetDigest + `",` +
				`"browser_download_url":"https://github.com/example/repository/releases/download/v1.4.0/provider.tar.gz"}`))
		case request.Method == http.MethodPatch && request.URL.Path == "/repos/example/repository/releases/21":
			_, _ = writer.Write([]byte(releaseJSON(21, "example", "repository", "v1.4.0", targetOID, true)))
		case request.Method == http.MethodDelete && request.URL.Path == "/repos/example/repository/releases/21":
			writer.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	mutator := mutationTestMutator(t, server, []string{MutationRepositoryRelease}, nil, nil)
	repository, err := mutator.BindRepository(RepositoryMutationScope{
		RepositoryID: 42, Owner: "example", Name: "repository",
		Operations: []string{MutationRepositoryRelease},
	})
	if err != nil {
		t.Fatal(err)
	}
	asset, _, err := repository.UploadReleaseAsset(context.Background(), 21, ReleaseAssetInput{
		Name: "provider.tar.gz", Bytes: assetBytes, SHA256: assetDigest,
	})
	if err != nil || asset.ID != 22 || asset.SHA256 != assetDigest {
		t.Fatalf("asset=%#v err=%v", asset, err)
	}
	release, _, err := repository.UpdateRelease(context.Background(), 21, ReleaseInput{
		TagName: "v1.4.0", TargetCommitish: targetOID, Name: "Release v1.4.0", MakeLatest: "true",
	})
	if err != nil || release.ID != 21 || release.Draft || !release.Immutable {
		t.Fatalf("release=%#v err=%v", release, err)
	}
	if _, err := repository.DeleteRelease(context.Background(), 21); err != nil {
		t.Fatal(err)
	}
	if len(methods) != 3 {
		t.Fatalf("methods=%v", methods)
	}
}

func TestDraftReleaseUsesGitHubsUntaggedURLUntilPublication(t *testing.T) {
	target := strings.Repeat("a", 40)
	raw := releaseResponse{
		ID: 21, NodeID: "node-release-21", TagName: "v1.4.0", TargetCommitish: target,
		Name: "Release v1.4.0", HTMLURL: "https://github.com/example/repository/releases/tag/untagged-0123456789abcdefabcd",
		Draft: true,
	}
	release, err := normalizeRelease(raw, "example", "repository")
	if err != nil || !release.Draft {
		t.Fatalf("draft release=%#v err=%v", release, err)
	}
	raw.HTMLURL = "https://github.com/example/repository/releases/tag/untagged-invalid"
	if _, err := normalizeRelease(raw, "example", "repository"); err == nil {
		t.Fatal("malformed draft URL was accepted")
	}
	raw.HTMLURL = "https://github.com/%zz"
	if _, err := normalizeRelease(raw, "example", "repository"); err == nil {
		t.Fatal("unparseable draft URL was accepted")
	}
}
