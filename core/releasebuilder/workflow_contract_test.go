package releasebuilder

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"go.yaml.in/yaml/v4"
)

type releaseWorkflowPolicy struct {
	Jobs map[string]struct {
		Permissions map[string]string `yaml:"permissions"`
		Steps       []struct {
			Name             string `yaml:"name"`
			Uses             string `yaml:"uses"`
			Run              string `yaml:"run"`
			WorkingDirectory string `yaml:"working-directory"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

func validateReleasePrivilegeBoundary(raw []byte) error {
	var workflow releaseWorkflowPolicy
	if err := yaml.Unmarshal(raw, &workflow); err != nil {
		return fmt.Errorf("parse release workflow: %w", err)
	}
	if len(workflow.Jobs) == 0 {
		return fmt.Errorf("release workflow has no jobs")
	}
	privileged := 0
	for jobName, job := range workflow.Jobs {
		hasReleaseAuthority := job.Permissions["contents"] == "write" ||
			job.Permissions["id-token"] == "write" ||
			job.Permissions["attestations"] == "write"
		if !hasReleaseAuthority {
			continue
		}
		privileged++
		if job.Permissions["contents"] == "write" &&
			(job.Permissions["id-token"] == "write" || job.Permissions["attestations"] == "write") {
			return fmt.Errorf("privileged job %q combines publication and attestation authority", jobName)
		}
		for _, step := range job.Steps {
			uses := strings.TrimSpace(step.Uses)
			if strings.HasPrefix(uses, "./") || strings.HasPrefix(uses, "actions/checkout@") {
				return fmt.Errorf("privileged job %q step %q executes candidate checkout/action %q", jobName, step.Name, uses)
			}
			if strings.Contains(step.WorkingDirectory, "github.workspace") {
				return fmt.Errorf("privileged job %q step %q uses candidate working directory", jobName, step.Name)
			}
			run := strings.ToLower(step.Run)
			for _, forbidden := range []string{
				"go run", "go test", "go build", "./scripts/", "scripts/",
				"$github_workspace", "${{ github.workspace }}", "core/cmd/", "./gds",
			} {
				if strings.Contains(run, forbidden) {
					return fmt.Errorf("privileged job %q step %q contains candidate execution marker %q", jobName, step.Name, forbidden)
				}
			}
		}
	}
	if privileged == 0 {
		return fmt.Errorf("release workflow has no privileged job")
	}
	return nil
}

func TestHostedReleaseWorkflowUsesOutputOutsideSourceRoot(t *testing.T) {
	t.Parallel()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	workflowPath := filepath.Join(repositoryRoot, ".github", "workflows", "release-bundle.yml")
	workflow, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(workflow)
	for _, required := range []string{
		`RELEASE_OUTPUT_ROOT: ${{ runner.temp }}/gds-release-output`,
		`RELEASE_DIRECTORY: ${{ runner.temp }}/gds-release-output/release`,
		`GO_BINARY="$(go env GOROOT)/bin/go"`,
		`test -x "$GO_BINARY"`,
		`--go-binary "$GO_BINARY"`,
		`--output "$RELEASE_DIRECTORY"`,
		`--verify-directory "$RELEASE_DIRECTORY"`,
		`> "$RELEASE_OUTPUT_ROOT/attestation-subjects.sha256"`,
		`path: ${{ runner.temp }}/gds-release-output`,
		`${{ runner.temp }}/gds-release-output/release-evidence`,
		`HARNESS_EVIDENCE_TRUST_POLICY_DIGEST: ${{ vars.HARNESS_EVIDENCE_TRUST_POLICY_DIGEST }}`,
		`stable/frozen requires signed active-five harness evidence`,
		`--harness-evidence-directory $EVIDENCE_INPUT_ROOT/records`,
		`RELEASE_SEQUENCE: ${{ inputs.release_sequence }}`,
		`canary) release_flags=(--prerelease) ;;`,
		`name: Record failed release evidence`,
		`release-failure-envelope.json`,
		`superseded_by:null`,
	} {
		if !strings.Contains(content, required) {
			t.Fatalf("hosted workflow is missing output contract %q", required)
		}
	}
	if strings.Contains(content, "RELEASE_SEQUENCE: ${{ github.run_number }}") {
		t.Fatal("release sequence regressed to repository-local workflow run numbering")
	}
	if strings.Contains(content, `$GITHUB_WORKSPACE/$RELEASE_DIRECTORY`) {
		t.Fatal("hosted workflow still writes release output beneath the source root")
	}
	if strings.Contains(content, `path: |
            ${{ runner.temp }}/gds-release-output/release`) {
		t.Fatal("hosted workflow uploads multiple roots and can preserve a runner-wide ancestor")
	}

	outputParent := t.TempDir()
	_, output, err := validateRequest(Request{
		Root: repositoryRoot, OutputDirectory: filepath.Join(outputParent, "release"),
		Version: "1.2.3", ReleaseSequence: 1, Channel: "canary", MinimumCLIVersion: "1.0.0",
	})
	if err != nil {
		t.Fatalf("workflow-equivalent output rejected by builder: %v", err)
	}
	if output != filepath.Join(outputParent, "release") {
		t.Fatalf("validated output = %q", output)
	}
}

func TestHostedReleaseWorkflowPinsAllActionsBySHA(t *testing.T) {
	t.Parallel()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	workflowPath := filepath.Join(repositoryRoot, ".github", "workflows", "release-bundle.yml")
	workflow, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(workflow), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "- uses:") {
			continue
		}
		ref := strings.TrimSpace(strings.TrimPrefix(trimmed, "- uses:"))
		if strings.Contains(ref, "./") {
			continue
		}
		at := strings.LastIndex(ref, "@")
		if at < 0 {
			t.Fatalf("workflow action %q has no @ pin", ref)
		}
		sha := ref[at+1:]
		if len(sha) != 40 || strings.Trim(sha, "0123456789abcdef") != "" {
			t.Fatalf("workflow action %q is not pinned to a 40-char SHA (got %q)", ref, sha)
		}
	}
}

func TestHostedReleaseWorkflowInstallsLockedPythonDependenciesBeforeReleaseGate(t *testing.T) {
	t.Parallel()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	workflowPath := filepath.Join(repositoryRoot, ".github", "workflows", "release-bundle.yml")
	workflow, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(workflow)
	install := strings.Index(content, `"$GDS_TEST_PYTHON" -m pip install --quiet --require-hashes -r requirements/test.txt`)
	validate := strings.Index(content, "scripts/validate_release.sh")
	if install < 0 || validate < 0 || install > validate {
		t.Fatal("release workflow must install hash-locked Python dependencies before validation")
	}
	if !strings.Contains(content, "GDS_TEST_PYTHON: ${{ runner.temp }}/gds-release-python/bin/python") ||
		!strings.Contains(content, `python3 -m venv "${GDS_TEST_PYTHON%/bin/python}"`) ||
		!strings.Contains(content, `export PATH="${GDS_TEST_PYTHON%/python}:$PATH"`) {
		t.Fatal("release workflow must run tests with the Python environment it populated")
	}
}

// The release chain runs on the estate's own fleet. What matters is that all
// three jobs agree on one provider: provenance describes the environment of the
// run that produced it, so a chain split across providers would attest an
// environment the build did not happen in. That is worse than either provider
// used consistently, and it is the only thing this test can usefully police.
func TestReleaseWorkflowUsesOneRunnerProviderThroughout(t *testing.T) {
	t.Parallel()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	workflowPath := filepath.Join(repositoryRoot, ".github", "workflows", "release-bundle.yml")
	workflow, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(workflow)

	buildStart := strings.Index(content, "name: build immutable bundle")
	attestStart := strings.Index(content, "name: attest immutable bundle")
	publishStart := strings.Index(content, "name: publish immutable bundle")
	if buildStart < 0 || attestStart <= buildStart || publishStart <= attestStart {
		t.Fatal("cannot find ordered build, attest, and publish jobs in release workflow")
	}
	runners := map[string]string{}
	for name, section := range map[string]string{
		"build":   content[buildStart:attestStart],
		"attest":  content[attestStart:publishStart],
		"publish": content[publishStart:],
	} {
		index := strings.Index(section, "runs-on:")
		if index < 0 {
			t.Fatalf("%s job declares no runner", name)
		}
		line := section[index:]
		if end := strings.IndexByte(line, '\n'); end >= 0 {
			line = line[:end]
		}
		runners[name] = strings.TrimSpace(strings.TrimPrefix(line, "runs-on:"))
	}
	if runners["build"] != runners["attest"] || runners["attest"] != runners["publish"] {
		t.Fatalf("release jobs must share one runner provider, got %#v", runners)
	}
}

func TestHostedReleaseWorkflowEnforcesPrivilegeSeparation(t *testing.T) {
	t.Parallel()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	workflowPath := filepath.Join(repositoryRoot, ".github", "workflows", "release-bundle.yml")
	workflow, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(workflow)

	// The build job must NOT hold OIDC or write authority.
	buildJobMarker := "name: build immutable bundle"
	buildStart := strings.Index(content, buildJobMarker)
	if buildStart < 0 {
		t.Fatal("cannot find build job in workflow")
	}
	attestJobMarker := "name: attest immutable bundle"
	attestStart := strings.Index(content, attestJobMarker)
	publishJobMarker := "name: publish immutable bundle"
	publishStart := strings.Index(content, publishJobMarker)
	if attestStart < 0 || publishStart <= attestStart {
		t.Fatal("cannot find ordered attest and publish jobs in workflow")
	}
	buildSection := content[buildStart:attestStart]
	if strings.Contains(buildSection, "id-token: write") {
		t.Fatal("build job has id-token: write — privilege separation violated")
	}
	if strings.Contains(buildSection, "attestations: write") {
		t.Fatal("build job has attestations: write — privilege separation violated")
	}

	// The digest gate must appear before the first attestation in the attest job.
	attestSection := content[attestStart:publishStart]
	digestGate := strings.Index(attestSection, "sha256sum -c")
	firstAttest := strings.Index(attestSection, "uses: actions/attest@")
	if digestGate < 0 {
		t.Fatal("attest job is missing the SHA256SUMS digest gate")
	}
	if firstAttest < 0 {
		t.Fatal("attest job is missing actions/attest")
	}
	if digestGate > firstAttest {
		t.Fatal("digest gate must run BEFORE the first attestation (OIDC token request)")
	}
}

func TestHostedReleaseWorkflowPrivilegedJobsAreCandidateExecutionFree(t *testing.T) {
	t.Parallel()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	workflowPath := filepath.Join(repositoryRoot, ".github", "workflows", "release-bundle.yml")
	workflow, err := os.ReadFile(workflowPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateReleasePrivilegeBoundary(workflow); err != nil {
		t.Fatal(err)
	}
}

func TestReleasePrivilegeBoundaryRejectsCandidateExecution(t *testing.T) {
	t.Parallel()
	fixtures := map[string]string{
		"checkout": `jobs:
  publish:
    permissions: {contents: write}
    steps:
      - uses: actions/checkout@0123456789012345678901234567890123456789
`,
		"local-action": `jobs:
  attest:
    permissions: {id-token: write}
    steps:
      - uses: ./.github/actions/release
`,
		"candidate-go": `jobs:
  attest:
    permissions: {attestations: write}
    steps:
      - run: go run ./core/cmd/gds-release-builder
`,
		"repository-script": `jobs:
  publish:
    permissions: {contents: write}
    steps:
      - run: scripts/publish.sh
`,
		"combined-authority": `jobs:
  publish:
    permissions: {contents: write, id-token: write, attestations: write}
    steps:
      - run: sha256sum -c SHA256SUMS
`,
	}
	for name, fixture := range fixtures {
		name, fixture := name, fixture
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := validateReleasePrivilegeBoundary([]byte(fixture)); err == nil {
				t.Fatal("candidate execution fixture passed privileged workflow policy")
			}
		})
	}
}
