package app

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/estate"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/githubmutationruntime"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/githubruntime"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/gitops"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestModuleReleaseChecksBindTrustedActionsSuccessToExactCommit(t *testing.T) {
	root := appTestRepositoryRoot(t)
	runtimePath := appTestRuntimeConfig(t, root)
	oid := strings.Repeat("a", 40)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasPrefix(request.URL.Path, "/app/installations/"):
			_, _ = writer.Write([]byte(`{"token":"ghs_read","expires_at":"2099-01-01T00:00:00Z",` +
				`"permissions":{"actions":"read","administration":"read","checks":"read",` +
				`"contents":"read","metadata":"read","pull_requests":"read"},"repository_selection":"all"}`))
		case request.URL.Path == "/repos/example-org/provider/commits/"+oid+"/check-runs":
			_, _ = fmt.Fprintf(writer, `{"total_count":1,"check_runs":[{"id":77,"name":"ci-gate",`+
				`"head_sha":%q,"status":"completed","conclusion":"success",`+
				`"completed_at":"2026-08-10T01:00:00Z",`+
				`"details_url":"https://github.com/example-org/provider/actions/runs/7/job/77",`+
				`"external_id":"job-77","app":{"id":15368,"slug":"github-actions"}}]}`, oid)
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	services, err := NewServices(DefaultClock)
	if err != nil {
		t.Fatal(err)
	}
	client := server.Client()
	client.Timeout = 5 * time.Second
	services.GitHubRuntimeBuildOptions = githubruntime.BuildOptions{
		BaseURL: server.URL + "/", HTTPClient: client, AllowInsecureLoopback: true,
	}
	checks, findings := services.moduleReleaseChecks(context.Background(), root, domain.RepositoryAnchor{
		Provider: domain.GitHubLocator{
			Type: "github", Owner: "example-org", Name: "provider",
			Installation: "installation:github-organization",
		},
		Release: domain.ReleasePolicy{Mode: "github-release", RequiredChecks: []string{"ci-gate"}},
	}, oid, runtimePath)
	if len(findings) != 0 || len(checks) != 1 || checks[0].ID != 77 || checks[0].HeadSHA != oid {
		t.Fatalf("checks=%#v findings=%#v", checks, findings)
	}
	foreign := checks[0]
	foreign.DetailsURL = "https://github.com/other/provider/actions/runs/7/job/77"
	if trustedSuccessfulActionsCheck(foreign, domain.RepositoryAnchor{
		Provider: domain.GitHubLocator{Owner: "example-org", Name: "provider"},
	}, oid) {
		t.Fatal("foreign repository check evidence was trusted")
	}
	trusted := checks[0]
	for name, mutate := range map[string]func(*githubprovider.CheckRun){
		"failed":               func(run *githubprovider.CheckRun) { run.Conclusion = "failure" },
		"stale sha":            func(run *githubprovider.CheckRun) { run.HeadSHA = strings.Repeat("b", 40) },
		"foreign app":          func(run *githubprovider.CheckRun) { run.AppID = 1 },
		"missing run identity": func(run *githubprovider.CheckRun) { run.RunID = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := trusted
			mutate(&candidate)
			if _, err := selectRequiredReleaseChecks(
				[]string{"ci-gate"}, []githubprovider.CheckRun{candidate}, domain.RepositoryAnchor{
					Provider: domain.GitHubLocator{Owner: "example-org", Name: "provider"},
				}, oid,
			); err == nil {
				t.Fatal("untrusted check evidence was accepted")
			}
		})
	}
	if _, err := selectRequiredReleaseChecks(
		[]string{"ci-gate"}, []githubprovider.CheckRun{trusted, trusted}, domain.RepositoryAnchor{
			Provider: domain.GitHubLocator{Owner: "example-org", Name: "provider"},
		}, oid,
	); err == nil {
		t.Fatal("ambiguous duplicate check evidence was accepted")
	}
}

// TestModuleReleaseGitHubReleaseHandlerCreatesImmutableRelease drives the exact
// single-step github-release apply and verify against a mock GitHub server: the
// canonical estate mutation capability is built through the separate mutation
// runtime, bound to the module repository, and the handler must issue one
// POST /repos/{owner}/{name}/releases with the exact tag and target commit.
func TestModuleReleaseGitHubReleaseHandlerCreatesImmutableRelease(t *testing.T) {
	root, desired, schemas := appModuleReleaseEstate(t)
	readConfig, err := githubruntime.Load(appModulePrivateFixture(t, filepath.Join(
		root, "tests", "fixtures", "schemas", "v1", "valid-github-runtime.yaml",
	)), desired, schemas)
	if err != nil {
		t.Fatal(err)
	}
	mutationConfig, err := githubmutationruntime.Load(appModulePrivateFixture(t, filepath.Join(
		root, "tests", "fixtures", "schemas", "v1", "valid-github-mutation-runtime.yaml",
	)), desired, schemas)
	if err != nil {
		t.Fatal(err)
	}
	appModuleReleaseKeys(t)
	target := strings.Repeat("a", 40)
	var releaseRequests atomic.Int32
	assetBytes := []byte("provider artifact bytes")
	assetPath := filepath.Join(t.TempDir(), "provider.tar.gz")
	if err := os.WriteFile(assetPath, assetBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	assetDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(assetBytes))
	var observedAssetDrift atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case strings.HasPrefix(request.URL.Path, "/app/installations/"):
			_, _ = fmt.Fprintf(writer,
				`{"token":"ghs_mutation","expires_at":%q,"permissions":{"administration":"write",`+
					`"contents":"write","custom_properties":"write","metadata":"read",`+
					`"pull_requests":"write","workflows":"write"},"repository_selection":"selected"}`,
				time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
			)
		case request.Method == http.MethodPost &&
			request.URL.Path == "/repos/example-org/public-module-fork/releases":
			releaseRequests.Add(1)
			var body map[string]any
			if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
				t.Errorf("decode release body: %v", err)
			}
			if body["tag_name"] != "v1.4.0" || body["target_commitish"] != target ||
				body["name"] != "Release v1.4.0" || body["make_latest"] != "false" ||
				body["draft"] != true || body["prerelease"] != false {
				t.Errorf("unexpected release body=%#v", body)
			}
			_, _ = fmt.Fprintf(writer,
				`{"id":7,"node_id":"RE_7","tag_name":"v1.4.0","target_commitish":%q,`+
					`"name":"Release v1.4.0","body":"","html_url":%q,"draft":true,`+
					`"prerelease":false,"immutable":false}`,
				target, "https://github.com/example-org/public-module-fork/releases/tag/untagged-0123456789abcdefabcd",
			)
		case request.Method == http.MethodPost &&
			request.URL.Path == "/repos/example-org/public-module-fork/releases/7/assets":
			if request.URL.Query().Get("name") != "provider.tar.gz" ||
				request.Header.Get("Content-Type") != "application/octet-stream" {
				t.Errorf("unexpected asset request: %s %#v", request.URL.String(), request.Header)
			}
			body, _ := io.ReadAll(request.Body)
			if !bytes.Equal(body, assetBytes) {
				t.Errorf("unexpected asset bytes")
			}
			_, _ = writer.Write([]byte(`{"id":8,"name":"provider.tar.gz","size":23,"state":"uploaded","digest":"` + assetDigest + `",` +
				`"browser_download_url":"https://github.com/example-org/public-module-fork/releases/download/untagged-0123456789abcdefabcd/provider.tar.gz"}`))
		case request.Method == http.MethodPatch &&
			request.URL.Path == "/repos/example-org/public-module-fork/releases/7":
			_, _ = fmt.Fprintf(writer,
				`{"id":7,"node_id":"RE_7","tag_name":"v1.4.0","target_commitish":%q,`+
					`"name":"Release v1.4.0","body":"","html_url":%q,"draft":false,`+
					`"prerelease":false,"immutable":true}`,
				target, "https://github.com/example-org/public-module-fork/releases/tag/v1.4.0",
			)
		case request.Method == http.MethodGet &&
			request.URL.Path == "/repos/example-org/public-module-fork/releases/tags/v1.4.0":
			_, _ = fmt.Fprintf(writer,
				`{"id":7,"node_id":"RE_7","tag_name":"v1.4.0","target_commitish":%q,`+
					`"name":"Release v1.4.0","body":"","html_url":%q,"draft":false,`+
					`"prerelease":false,"immutable":true}`,
				target, "https://github.com/example-org/public-module-fork/releases/tag/v1.4.0",
			)
		case request.Method == http.MethodGet &&
			request.URL.Path == "/repos/example-org/public-module-fork/releases/7/assets":
			observedDigest := assetDigest
			if observedAssetDrift.Load() {
				observedDigest = "sha256:" + strings.Repeat("b", 64)
			}
			_, _ = writer.Write([]byte(`[{"id":8,"name":"provider.tar.gz","size":23,"state":"uploaded","digest":"` + observedDigest + `",` +
				`"browser_download_url":"https://github.com/example-org/public-module-fork/releases/download/v1.4.0/provider.tar.gz"}]`))
		default:
			http.NotFound(writer, request)
		}
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = 5 * time.Second
	mutators, err := githubmutationruntime.BuildMutators(mutationConfig, readConfig, desired, githubmutationruntime.BuildOptions{
		BaseURL: server.URL + "/", HTTPClient: client, AllowInsecureLoopback: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	factory, found := mutators["mutation:github-organization"]
	if !found {
		t.Fatal("organization mutation capability was not built")
	}
	writer, err := factory.BindRepository(githubprovider.RepositoryMutationScope{
		RepositoryID: 123456789, Owner: "example-org", Name: "public-module-fork",
		Operations: []string{githubprovider.MutationRepositoryRelease},
	})
	if err != nil {
		t.Fatal(err)
	}
	handler, err := gitops.NewPublishGitHubReleaseHandler(writer)
	if err != nil {
		t.Fatal(err)
	}
	step := operations.Step{
		StepID: "publish-github-release", RepositoryID: "repo_01JEXAMPZ0000000000000000C",
		Action: gitops.PublishGitHubReleaseAction, RequiresApproval: true,
		Compensation: operations.Compensation{Mode: "manual"},
		Parameters: map[string]any{"module_release": map[string]any{
			"module_root": "/module/public-module-fork", "version": "1.4.0",
			"tag_ref": "refs/tags/v1.4.0", "commit_oid": target,
			"assets": []any{map[string]any{"path": assetPath, "name": "provider.tar.gz",
				"size": float64(len(assetBytes)), "sha256": assetDigest}},
		}},
	}
	evidence, err := handler.Apply(context.Background(), step)
	if err != nil {
		t.Fatalf("apply: %v", err)
	}
	if releaseRequests.Load() != 1 {
		t.Fatalf("release requests=%d", releaseRequests.Load())
	}
	afterRaw, err := json.Marshal(evidence.After)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(afterRaw), `"tag_name":"v1.4.0"`) ||
		!strings.Contains(string(afterRaw), `"target_commitish":"`+target+`"`) {
		t.Fatalf("after evidence=%s", afterRaw)
	}
	if err := handler.Verify(context.Background(), step, afterRaw); err != nil {
		t.Fatalf("verify: %v", err)
	}
	observedAssetDrift.Store(true)
	if err := handler.Verify(context.Background(), step, afterRaw); err == nil {
		t.Fatal("live verification accepted provider asset digest drift")
	}
	observedAssetDrift.Store(false)
	// A standalone verify uses the read-only observer, so it needs no mutation
	// authority and still re-reads the same live release.
	readOnly, err := gitops.NewVerifyGitHubReleaseHandler(writer)
	if err != nil {
		t.Fatal(err)
	}
	if err := readOnly.Verify(context.Background(), step, afterRaw); err != nil {
		t.Fatalf("read-only verify: %v", err)
	}
}

// TestModuleReleaseGitHubReleaseHandlerRejectsForeignEvidence proves that
// verification fails when the recorded release does not echo the exact step.
func TestModuleReleaseGitHubReleaseHandlerRejectsForeignEvidence(t *testing.T) {
	handler, err := gitops.NewVerifyGitHubReleaseHandler(stubReleaseObserver{
		scope: githubprovider.RepositoryMutationScope{Owner: "example-org", Name: "public-module-fork"},
	})
	if err != nil {
		t.Fatal(err)
	}
	target := strings.Repeat("a", 40)
	step := operations.Step{
		Action: gitops.PublishGitHubReleaseAction,
		Parameters: map[string]any{"module_release": map[string]any{
			"module_root": "/module/public-module-fork", "version": "1.4.0",
			"tag_ref": "refs/tags/v1.4.0", "commit_oid": target,
			"assets": []any{map[string]any{"path": "/tmp/provider.tar.gz", "name": "provider.tar.gz",
				"size": float64(7), "sha256": "sha256:" + strings.Repeat("a", 64)}},
		}},
	}
	// Evidence for a different tag must be rejected.
	foreign := []byte(`{"release":{"id":7,"tag_name":"v9.9.9","target_commitish":"` + target +
		`","draft":false,"immutable":true},"assets":[{"id":8,"name":"provider.tar.gz","size":7,` +
		`"state":"uploaded","sha256":"sha256:` + strings.Repeat("a", 64) + `"}]}`)
	if err := handler.Verify(context.Background(), step, foreign); err == nil {
		t.Fatal("verification accepted evidence for a different tag")
	}
	// The version-tag handler must refuse a github-release step outright.
	versionHandler, err := gitops.NewPublishGitHubReleaseHandler(nil)
	if err != nil {
		t.Fatal(err)
	}
	wrong := operations.Step{
		Action: gitops.PublishVersionTagAction,
		Parameters: map[string]any{"module_release": map[string]any{
			"module_root": "/module/public-module-fork", "version": "1.4.0",
			"tag_ref": "refs/tags/v1.4.0", "commit_oid": target,
		}},
	}
	if _, err := versionHandler.Apply(context.Background(), wrong); err == nil {
		t.Fatal("github-release handler accepted a version-tag step")
	}
}

func TestModuleReleaseGitHubReleaseHandlerAcceptsDeclaredNumericTagStyle(t *testing.T) {
	target := strings.Repeat("a", 40)
	handler, err := gitops.NewVerifyGitHubReleaseHandler(stubReleaseObserver{
		release: githubprovider.Release{
			ID: 7, TagName: "1.4.0", TargetCommitish: target, Immutable: true,
			HTMLURL: "https://github.com/example/provider/releases/tag/1.4.0",
		},
		assets: []githubprovider.ReleaseAsset{{
			ID: 8, Name: "provider.tar.gz", Size: 7, State: "uploaded",
			SHA256: "sha256:" + strings.Repeat("a", 64),
		}},
		scope: githubprovider.RepositoryMutationScope{Owner: "example", Name: "provider"},
	})
	if err != nil {
		t.Fatal(err)
	}
	step := operations.Step{
		Action: gitops.PublishGitHubReleaseAction,
		Parameters: map[string]any{"module_release": map[string]any{
			"module_root": "/module/provider", "version": "1.4.0", "tag_style": "semver",
			"tag_ref": "refs/tags/1.4.0", "commit_oid": target,
			"assets": []any{map[string]any{"path": "/tmp/provider.tar.gz", "name": "provider.tar.gz",
				"size": float64(7), "sha256": "sha256:" + strings.Repeat("a", 64)}},
		}},
	}
	evidence := []byte(`{"release":{"id":7,"tag_name":"1.4.0","target_commitish":"` + target +
		`","draft":false,"immutable":true,"html_url":"https://github.com/example/provider/releases/tag/1.4.0"},` +
		`"assets":[{"id":8,"name":"provider.tar.gz","size":7,"state":"uploaded","sha256":"sha256:` + strings.Repeat("a", 64) + `"}]}`)
	if err := handler.Verify(context.Background(), step, evidence); err != nil {
		t.Fatalf("numeric tag evidence was rejected: %v", err)
	}
}

func TestInspectModuleReleaseAssetsRejectsUnsafeAndAmbiguousFiles(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "provider.tar.gz")
	if err := os.WriteFile(first, []byte("artifact"), 0o600); err != nil {
		t.Fatal(err)
	}
	assets, err := inspectModuleReleaseAssets([]string{first})
	if err != nil || len(assets) != 1 || assets[0].Name != "provider.tar.gz" ||
		!strings.HasPrefix(assets[0].SHA256, "sha256:") {
		t.Fatalf("assets=%#v err=%v", assets, err)
	}
	secondRoot := t.TempDir()
	second := filepath.Join(secondRoot, "provider.tar.gz")
	if err := os.WriteFile(second, []byte("other"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectModuleReleaseAssets([]string{first, second}); err == nil {
		t.Fatal("duplicate release asset basename was accepted")
	}
	link := filepath.Join(root, "linked.tar.gz")
	if err := os.Symlink(first, link); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectModuleReleaseAssets([]string{link}); err == nil {
		t.Fatal("symlink release asset was accepted")
	}
	unsafe := filepath.Join(root, "unsafe.tar.gz")
	if err := os.WriteFile(unsafe, []byte("unsafe"), 0o666); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(unsafe, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectModuleReleaseAssets([]string{unsafe}); err == nil {
		t.Fatal("group/world-writable release asset was accepted")
	}
}

func appModuleReleaseEstate(t *testing.T) (string, estate.Config, *validation.Set) {
	t.Helper()
	root := appTestRepositoryRoot(t)
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	desired, findings := estate.Load(root, schemas)
	if len(findings) != 0 {
		t.Fatalf("estate findings=%+v", findings)
	}
	return root, desired, schemas
}

func appModulePrivateFixture(t *testing.T, source string) string {
	t.Helper()
	raw, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), filepath.Base(source))
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func appModuleReleaseKeys(t *testing.T) {
	t.Helper()
	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	privatePEM := pem.EncodeToMemory(&pem.Block{
		Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey),
	})
	t.Setenv("GDS_GITHUB_MUTATION_APP_PERSONAL_KEY", string(privatePEM))
	t.Setenv("GDS_GITHUB_MUTATION_APP_ORGANIZATION_KEY", string(privatePEM))
	t.Setenv("GDS_GITHUB_MUTATION_APP_EXAMPLE_MEDIA_KEY", string(privatePEM))
}

// The release gate resolves provider contexts from two namespaces with a strict
// precedence. release.required_checks is authoritative because it can name any
// context; verification.required is a compatibility fallback that works only
// because the active module repositories publish a check run named after their
// required lane. Reading the fallback when an explicit list exists would silently
// gate on the wrong namespace, so the precedence is pinned here.
func TestRequiredReleaseCheckContextsPrefersExplicitProviderContexts(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		anchor   domain.RepositoryAnchor
		expected []string
	}{
		{
			name: "explicit provider contexts win over local lanes",
			anchor: domain.RepositoryAnchor{
				Verification: domain.VerificationPolicy{Required: []string{"test"}},
				Release:      domain.ReleasePolicy{RequiredChecks: []string{"ci-gate"}},
			},
			expected: []string{"ci-gate"},
		},
		{
			name: "local lanes are the fallback when nothing explicit is declared",
			anchor: domain.RepositoryAnchor{
				Verification: domain.VerificationPolicy{Required: []string{"test"}},
			},
			expected: []string{"test"},
		},
		{
			name:     "no declaration means no provider gate",
			anchor:   domain.RepositoryAnchor{},
			expected: nil,
		},
		{
			name: "a context outside the lane enum is expressible only explicitly",
			anchor: domain.RepositoryAnchor{
				Release: domain.ReleasePolicy{RequiredChecks: []string{"CodeQL / CodeQL (go)"}},
			},
			expected: []string{"CodeQL / CodeQL (go)"},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			resolved := requiredReleaseCheckContexts(testCase.anchor)
			if len(resolved) != len(testCase.expected) {
				t.Fatalf("resolved=%#v expected=%#v", resolved, testCase.expected)
			}
			for index := range resolved {
				if resolved[index] != testCase.expected[index] {
					t.Fatalf("resolved=%#v expected=%#v", resolved, testCase.expected)
				}
			}
		})
	}
}

// stubReleaseObserver serves a fixed live release so a test can exercise
// verification without a live provider. Verification now always re-reads live
// state, so every verify test must declare what the provider currently holds.
type stubReleaseObserver struct {
	release githubprovider.Release
	assets  []githubprovider.ReleaseAsset
	scope   githubprovider.RepositoryMutationScope
	err     error
}

func (stub stubReleaseObserver) GetReleaseByTag(
	_ context.Context, tagName string,
) (githubprovider.Release, error) {
	if stub.err != nil {
		return githubprovider.Release{}, stub.err
	}
	if stub.release.TagName != tagName {
		return githubprovider.Release{}, fmt.Errorf("no release for tag %q", tagName)
	}
	return stub.release, nil
}

func (stub stubReleaseObserver) ListReleaseAssets(
	_ context.Context, _ int64,
) ([]githubprovider.ReleaseAsset, error) {
	return stub.assets, stub.err
}

func (stub stubReleaseObserver) Scope() githubprovider.RepositoryMutationScope {
	return stub.scope
}

// Verification must prove the live release, not merely the recorded evidence. A
// release that was deleted or whose asset was replaced after apply must fail even
// though the stored evidence is internally consistent.
func TestGitHubReleaseVerificationProvesLiveStateNotOnlyEvidence(t *testing.T) {
	target := strings.Repeat("a", 40)
	digest := "sha256:" + strings.Repeat("a", 64)
	scope := githubprovider.RepositoryMutationScope{Owner: "example", Name: "provider"}
	step := operations.Step{
		Action: gitops.PublishGitHubReleaseAction,
		Parameters: map[string]any{"module_release": map[string]any{
			"module_root": "/module/provider", "version": "1.4.0", "tag_style": "semver",
			"tag_ref": "refs/tags/1.4.0", "commit_oid": target,
			"assets": []any{map[string]any{"path": "/tmp/provider.tar.gz", "name": "provider.tar.gz",
				"size": float64(7), "sha256": digest}},
		}},
	}
	evidence := []byte(`{"release":{"id":7,"tag_name":"1.4.0","target_commitish":"` + target +
		`","draft":false,"immutable":true,"html_url":"https://github.com/example/provider/releases/tag/1.4.0"},` +
		`"assets":[{"id":8,"name":"provider.tar.gz","size":7,"state":"uploaded","sha256":"` + digest + `"}]}`)

	live := githubprovider.Release{
		ID: 7, TagName: "1.4.0", TargetCommitish: target, Immutable: true,
		HTMLURL: "https://github.com/example/provider/releases/tag/1.4.0",
	}
	liveAssets := []githubprovider.ReleaseAsset{{
		ID: 8, Name: "provider.tar.gz", Size: 7, State: "uploaded", SHA256: digest,
	}}

	matching, err := gitops.NewVerifyGitHubReleaseHandler(
		stubReleaseObserver{release: live, assets: liveAssets, scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	if err := matching.Verify(context.Background(), step, evidence); err != nil {
		t.Fatalf("verification rejected a matching live release: %v", err)
	}

	// The release no longer exists.
	deleted, err := gitops.NewVerifyGitHubReleaseHandler(
		stubReleaseObserver{scope: scope, err: errors.New("not found")})
	if err != nil {
		t.Fatal(err)
	}
	if err := deleted.Verify(context.Background(), step, evidence); err == nil {
		t.Fatal("verification accepted a release that no longer exists")
	}

	// The asset bytes were replaced after apply.
	replaced := append([]githubprovider.ReleaseAsset(nil), liveAssets...)
	replaced[0].SHA256 = "sha256:" + strings.Repeat("b", 64)
	tampered, err := gitops.NewVerifyGitHubReleaseHandler(
		stubReleaseObserver{release: live, assets: replaced, scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	if err := tampered.Verify(context.Background(), step, evidence); err == nil {
		t.Fatal("verification accepted replaced live asset bytes")
	}

	// An identical inventory returned in a different order is not drift.
	reordered := []githubprovider.ReleaseAsset{liveAssets[0]}
	ordered, err := gitops.NewVerifyGitHubReleaseHandler(
		stubReleaseObserver{release: live, assets: reordered, scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	if err := ordered.Verify(context.Background(), step, evidence); err != nil {
		t.Fatalf("verification reported drift for an identical inventory: %v", err)
	}

	// A handler with no observer cannot claim a verified release.
	if _, err := gitops.NewVerifyGitHubReleaseHandler(nil); err == nil {
		t.Fatal("verification handler was built without a provider observer")
	}
}
