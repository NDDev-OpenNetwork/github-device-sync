package cli

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/app"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/githubruntime"
)

type versionPinFixture struct {
	module, consumer      sessionFixtureState
	target, tagOID, state string
	services              *app.Services
	runtime               string
}

func newVersionPinFixture(t *testing.T, publication bool) versionPinFixture {
	t.Helper()
	module := sessionFixtureWithPolicies(t, "never", "direct", false)
	raw, err := os.ReadFile(filepath.Join(repositoryRoot(t), "tests/fixtures/schemas/v1/valid-module-fork-repository.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	const moduleID = "repo_01JEXAMPZ0000000000000000D"
	source := strings.Replace(string(raw), "repo_01JEXAMPZ0000000000000000C", moduleID, 1)
	if !publication {
		source = strings.Replace(source, `github_release: "required"`, `github_release: "optional"`, 1)
	}
	source += "\nverification:\n  commands:\n    compatibility:\n      - \"git diff --exit-code\"\n  required:\n    - \"compatibility\"\n"
	if err := os.WriteFile(filepath.Join(module.client, ".gds/repository.yaml"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, module.client, "add", ".gds/repository.yaml")
	runSessionGit(t, module.client, "commit", "-qm", "versioned module contract")
	runSessionGit(t, module.client, "tag", "-a", "v1.2.3", "-m", "artifact one")
	runSessionGit(t, module.client, "push", "-q", "origin", "main", "refs/tags/v1.2.3")
	target := runSessionGit(t, module.client, "rev-parse", "HEAD")
	tagOID := runSessionGit(t, module.client, "rev-parse", "refs/tags/v1.2.3")
	runSessionGit(t, module.client, "checkout", "-q", "--detach", "v1.2.3")
	consumer := sessionFixtureWithPolicies(t, "never", "direct", false)
	anchorPath := filepath.Join(consumer.client, ".gds/repository.yaml")
	raw, err = os.ReadFile(anchorPath)
	if err != nil {
		t.Fatal(err)
	}
	source = strings.Replace(string(raw), "\nrelease:\n", "\nrelationships:\n  - type: \"git-submodule-consumer\"\n    target: \""+moduleID+"\"\n    gitmodules_name: \"module\"\n\nrelease:\n", 1)
	if err := os.WriteFile(anchorPath, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(consumer.client, ".gitmodules"), []byte("[submodule \"module\"]\n path = modules/module\n url = https://github.com/example-org/public-module-fork.git\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runSessionGit(t, consumer.client, "add", ".gds/repository.yaml", ".gitmodules")
	runSessionGit(t, consumer.client, "update-index", "--add", "--cacheinfo", "160000,"+module.firstOID+",modules/module")
	runSessionGit(t, consumer.client, "commit", "-qm", "typed consumer")
	runSessionGit(t, consumer.client, "switch", "-qc", "task/version-pin")
	if err := os.MkdirAll(filepath.Join(consumer.client, "modules/module"), 0o755); err != nil {
		t.Fatal(err)
	}
	services, err := app.NewServices(app.DefaultClock)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("GDS_ESTATE_ROOT", testEstateRoot(t))
	return versionPinFixture{module: module, consumer: consumer, target: target, tagOID: tagOID, state: sessionStatePath(t), services: services}
}

func (f versionPinFixture) command(t *testing.T, mode, id string, extra ...string) (int, domain.Envelope, string) {
	t.Helper()
	args := []string{"--json", "--cwd", f.consumer.client, "module", "update-pin", mode}
	if mode == "--plan" {
		args = append(args, "--module", f.module.client, "--name", "module", "--version", "1.2.3")
	} else {
		args = append(args, id)
	}
	args = append(args, "--state-path", f.state, "--device-id", syncTestDeviceID, "--session-id", "version-artifact")
	if f.runtime != "" {
		args = append(args, "--runtime-config", f.runtime)
	}
	return executeJSONWithServices(t, f.services, append(args, extra...)...)
}

func TestVersionArtifactPinLifecycle(t *testing.T) {
	f := newVersionPinFixture(t, false)
	// A published version remains selectable after origin/main advances.
	runSessionGit(t, f.module.client, "switch", "-q", "main")
	runSessionGit(t, f.module.client, "commit", "--allow-empty", "-qm", "later development")
	runSessionGit(t, f.module.client, "push", "-q", "origin", "main")
	runSessionGit(t, f.module.client, "checkout", "-q", "--detach", "v1.2.3")
	code, plan, stderr := f.command(t, "--plan", "")
	if code != 0 {
		t.Fatalf("plan=%#v stderr=%s", plan, stderr)
	}
	code, applied, stderr := f.command(t, "--apply", syncPlanID(t, plan.Data))
	if code != 0 || !applied.Mutation.Completed {
		t.Fatalf("apply=%#v stderr=%s", applied, stderr)
	}
	if diff := runSessionGit(t, f.consumer.client, "diff", "--cached", "--name-only"); diff != "modules/module" {
		t.Fatalf("unexpected mutation %q", diff)
	}
	if pinned := runSessionGit(t, f.consumer.client, "rev-parse", ":modules/module"); pinned != f.target {
		t.Fatalf("pinned %s instead of selected version %s", pinned, f.target)
	}
	code, verified, stderr := f.command(t, "--verify", applied.OperationID)
	if code != 0 {
		t.Fatalf("verify=%#v stderr=%s", verified, stderr)
	}
	// Retag at the same commit: peeled SHA alone would wrongly verify this.
	runSessionGit(t, f.module.client, "tag", "-f", "-a", "v1.2.3", "-m", "replacement artifact")
	runSessionGit(t, f.module.client, "push", "-q", "--force", "origin", "refs/tags/v1.2.3")
	code, verified, _ = f.command(t, "--verify", applied.OperationID)
	if code == 0 || verified.Mutation.Attempted {
		t.Fatalf("retag accepted by verify: %#v", verified)
	}
}

func TestVersionArtifactPinRefusesStaleAndUnsafePlans(t *testing.T) {
	for _, scenario := range []string{"moved-tag", "missing-tag", "dirty-module", "dirty-consumer", "changed-consumer-head", "version-override"} {
		t.Run(scenario, func(t *testing.T) {
			f := newVersionPinFixture(t, false)
			code, plan, stderr := f.command(t, "--plan", "")
			if code != 0 {
				t.Fatalf("plan=%#v stderr=%s", plan, stderr)
			}
			extra := []string{}
			switch scenario {
			case "moved-tag":
				runSessionGit(t, f.module.client, "tag", "-f", "-a", "v1.2.3", "-m", "changed object")
				runSessionGit(t, f.module.client, "push", "-q", "--force", "origin", "refs/tags/v1.2.3")
			case "missing-tag":
				runSessionGit(t, f.module.client, "push", "-q", "origin", ":refs/tags/v1.2.3")
			case "dirty-module":
				if err := os.WriteFile(filepath.Join(f.module.client, "unrelated.txt"), []byte("dirty"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "dirty-consumer":
				if err := os.WriteFile(filepath.Join(f.consumer.client, "unrelated.txt"), []byte("dirty"), 0o644); err != nil {
					t.Fatal(err)
				}
			case "changed-consumer-head":
				runSessionGit(t, f.consumer.client, "commit", "--allow-empty", "-qm", "unrelated new head")
			case "version-override":
				extra = []string{"--version", "2.0.0"}
			}
			code, result, _ := f.command(t, "--apply", syncPlanID(t, plan.Data), extra...)
			if code == 0 || result.Mutation.Completed {
				t.Fatalf("unsafe apply accepted: %#v", result)
			}
			if diff := runSessionGit(t, f.consumer.client, "diff", "--cached", "--name-only"); diff != "" {
				t.Fatalf("refusal modified index: %q", diff)
			}
		})
	}
}

func TestVersionArtifactPinBindsPublishedReleaseAssets(t *testing.T) {
	f := newVersionPinFixture(t, true)
	// Reuse the read-only runtime credential fixture; replace its transport only.
	f.services, f.runtime = moduleReleaseReadServices(t)
	var mode atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" && !strings.HasPrefix(r.URL.Path, "/app/installations/") {
			t.Errorf("unexpected mutation %s %s", r.Method, r.URL.Path)
			w.WriteHeader(405)
			return
		}
		switch r.URL.Path {
		case "/app/installations/900001/access_tokens":
			fmt.Fprint(w, `{"token":"ghs_read","expires_at":"2099-01-01T00:00:00Z","permissions":{"actions":"read","administration":"read","checks":"read","contents":"read","metadata":"read","pull_requests":"read"},"repository_selection":"all"}`)
		case "/repos/example-org/public-module-fork":
			fmt.Fprint(w, `{"id":123456789,"node_id":"R_fixture","name":"public-module-fork","full_name":"example-org/public-module-fork","private":false,"visibility":"public","fork":false,"archived":false,"disabled":false,"default_branch":"main","html_url":"https://github.com/example-org/public-module-fork","owner":{"login":"example-org"}}`)
		case "/repos/example-org/public-module-fork/git/ref/tags/v1.2.3":
			fmt.Fprintf(w, `{"ref":"refs/tags/v1.2.3","object":{"sha":%q,"type":"tag"}}`, f.tagOID)
		case "/repos/example-org/public-module-fork/releases/tags/v1.2.3":
			if mode.Load() == 1 {
				http.NotFound(w, r)
				return
			}
			fmt.Fprintf(w, `{"id":71,"node_id":"RE_fixture","tag_name":"v1.2.3","target_commitish":"main","name":"v1.2.3","body":"","html_url":"https://github.com/example-org/public-module-fork/releases/tag/v1.2.3","draft":%t,"prerelease":false,"immutable":true}`, mode.Load() == 2)
		case "/repos/example-org/public-module-fork/releases/71/assets":
			if mode.Load() == 3 {
				fmt.Fprint(w, `[]`)
				return
			}
			digest := strings.Repeat("a", 64)
			if mode.Load() == 4 {
				digest = strings.Repeat("b", 64)
			}
			if mode.Load() == 5 {
				digest = ""
			}
			if mode.Load() == 6 {
				w.Header().Set("Link", `<https://api.github.com/next>; rel="next"`)
			}
			fmt.Fprintf(w, `[{"id":72,"name":"module.tar.gz","size":12,"state":"uploaded","browser_download_url":"https://github.com/example-org/public-module-fork/releases/download/v1.2.3/module.tar.gz","digest":"sha256:%s"}]`, digest)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = 5 * time.Second
	f.services.GitHubRuntimeBuildOptions = githubruntime.BuildOptions{BaseURL: server.URL + "/", HTTPClient: client, AllowInsecureLoopback: true}
	for _, failure := range []int32{1, 2, 3, 5, 6} {
		mode.Store(failure)
		code, result, _ := f.command(t, "--plan", "")
		if code == 0 {
			t.Fatalf("publication failure %d accepted: %#v", failure, result)
		}
	}
	mode.Store(0)
	code, plan, stderr := f.command(t, "--plan", "")
	if code != 0 {
		t.Fatalf("valid release plan=%#v stderr=%s", plan, stderr)
	}
	mode.Store(4)
	code, result, _ := f.command(t, "--apply", syncPlanID(t, plan.Data))
	if code == 0 || result.Mutation.Completed {
		t.Fatalf("changed asset accepted: %#v", result)
	}
	mode.Store(0)
	code, plan, stderr = f.command(t, "--plan", "")
	if code != 0 {
		t.Fatalf("fresh plan=%#v stderr=%s", plan, stderr)
	}
	code, applied, stderr := f.command(t, "--apply", syncPlanID(t, plan.Data))
	if code != 0 || !applied.Mutation.Completed {
		t.Fatalf("apply=%#v stderr=%s", applied, stderr)
	}
	code, result, stderr = f.command(t, "--verify", applied.OperationID)
	if code != 0 {
		t.Fatalf("verify=%#v stderr=%s", result, stderr)
	}
	mode.Store(4)
	code, result, _ = f.command(t, "--verify", applied.OperationID)
	if code == 0 {
		t.Fatalf("changed asset accepted by verify: %#v", result)
	}
}

func TestVersionArtifactPinRejectsUnselectedAndUnverifiedSources(t *testing.T) {
	for _, scenario := range []string{"missing-version", "wrong-checkout", "failed-compatibility"} {
		t.Run(scenario, func(t *testing.T) {
			f := newVersionPinFixture(t, false)
			extra := []string{}
			switch scenario {
			case "missing-version":
				extra = []string{"--version", ""}
			case "wrong-checkout":
				runSessionGit(t, f.module.client, "switch", "-q", "main")
				runSessionGit(t, f.module.client, "commit", "--allow-empty", "-qm", "not the released commit")
			case "failed-compatibility":
				path := filepath.Join(f.module.client, ".gds/repository.yaml")
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				raw = []byte(strings.Replace(string(raw), "git diff --exit-code", "false", 1))
				if err := os.WriteFile(path, raw, 0o644); err != nil {
					t.Fatal(err)
				}
				runSessionGit(t, f.module.client, "commit", "-qam", "failing compatibility contract")
				runSessionGit(t, f.module.client, "tag", "-f", "-a", "v1.2.3", "-m", "incompatible version")
				runSessionGit(t, f.module.client, "push", "-q", "--force", "origin", "refs/tags/v1.2.3")
			}
			code, result, _ := f.command(t, "--plan", "", extra...)
			if code == 0 || result.Mutation.Attempted {
				t.Fatalf("unverified source accepted: %#v", result)
			}
		})
	}
}
