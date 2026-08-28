package harness

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func TestAllCanonicalAdaptersRenderDeterministically(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	for _, harnessID := range CanonicalIDs {
		t.Run(harnessID, func(t *testing.T) {
			adapter, findings := NewAdapter(root, harnessID, schemas)
			if len(findings) != 0 {
				t.Fatalf("new adapter findings: %+v", findings)
			}
			request := RenderRequest{SkillProfile: "core", Scope: "project"}
			if provisional, ok := adapter.(*profileAdapter); ok &&
				provisional.profile.Projection.SkillStrategy == "not-proven" {
				_, findings := adapter.Render(request)
				if !containsHarnessFinding(findings, "GDS_HARNESS_PROJECT_SKILLS_NOT_PROVEN") {
					t.Fatalf("unproven skeleton rendered without a closed failure: %+v", findings)
				}
				return
			}
			first, findings := adapter.Render(request)
			if len(findings) != 0 {
				t.Fatalf("first render findings: %+v", findings)
			}
			second, findings := adapter.Render(request)
			if len(findings) != 0 {
				t.Fatalf("second render findings: %+v", findings)
			}
			firstJSON, _ := json.Marshal(first)
			secondJSON, _ := json.Marshal(second)
			if string(firstJSON) != string(secondJSON) {
				t.Fatal("adapter render is not deterministic")
			}
			if first.CandidateDigest == "" || first.RegistryDigest == "" || len(first.Files) == 0 {
				t.Fatalf("incomplete candidate: %+v", first)
			}
			lockSuffix := harnessID + "-core.lock.json"
			if !hasAdapterFileSuffix(first.Files, lockSuffix) {
				t.Fatalf("candidate has no lock ending in %q", lockSuffix)
			}
			for _, file := range first.Files {
				if harnessID != "codex" && strings.HasSuffix(file.Path, "agents/openai.yaml") {
					t.Fatalf("Codex-only sidecar leaked into %s projection: %s", harnessID, file.Path)
				}
			}
		})
	}
}

func TestAdapterExcludesExplicitSkillsWithoutNativeEnforcement(t *testing.T) {
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	for _, harnessID := range []string{
		"antigravity", "cursor", "grok-build", "mimocode", "opencode",
	} {
		adapter, findings := NewAdapter(root, harnessID, schemas)
		if len(findings) != 0 {
			t.Fatalf("%s adapter findings: %+v", harnessID, findings)
		}
		candidate, findings := adapter.Render(RenderRequest{SkillProfile: "core", Scope: "project"})
		if len(findings) != 0 {
			t.Fatalf("%s render findings: %+v", harnessID, findings)
		}
		for _, required := range []string{"gds-complete-work", "gds-handoff-work"} {
			if !containsString(candidate.ExcludedExplicit, required) {
				t.Fatalf("%s did not exclude %s: %+v", harnessID, required, candidate.ExcludedExplicit)
			}
			for _, file := range candidate.Files {
				if strings.Contains(file.Path, "/"+required+"/") {
					t.Fatalf("%s projected excluded skill %s at %s", harnessID, required, file.Path)
				}
			}
		}
	}
}

func TestAdapterPlanBindsExactObservedTarget(t *testing.T) {
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	adapter, findings := NewAdapter(root, "codex", schemas)
	if len(findings) != 0 {
		t.Fatalf("adapter findings: %+v", findings)
	}
	target := t.TempDir()
	request := RenderRequest{SkillProfile: "core", Scope: "project"}
	inspection, findings := adapter.Inspect(target, request)
	if len(findings) != 0 {
		t.Fatalf("inspection findings: %+v", findings)
	}
	if inspection.Drift != len(inspection.Files) || inspection.Drift == 0 {
		t.Fatalf("missing target drift=%d files=%d", inspection.Drift, len(inspection.Files))
	}
	plan, findings := adapter.PlanInstall(target, request)
	if len(findings) != 0 {
		t.Fatalf("plan findings: %+v", findings)
	}
	if plan.Operation != "install" || !plan.RequiresApproval || plan.PlanDigest == "" ||
		plan.BeforeFingerprint != inspection.Fingerprint {
		t.Fatalf("invalid plan: %+v inspection=%+v", plan, inspection)
	}
}

func TestAdapterMaterializeVerifyAndRemoveLifecycle(t *testing.T) {
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	adapter, findings := NewAdapter(root, "codex", schemas)
	if len(findings) != 0 {
		t.Fatalf("adapter findings: %+v", findings)
	}
	request := RenderRequest{SkillProfile: "core", Scope: "project"}
	candidate, findings := adapter.Render(request)
	if len(findings) != 0 {
		t.Fatalf("render findings: %+v", findings)
	}
	target := t.TempDir()
	materializer, err := NewAdapterMaterializer(target, candidate)
	if err != nil {
		t.Fatal(err)
	}
	materializeStep := operations.Step{
		StepID: "install", RepositoryID: "repo_fixture", Action: MaterializeAdapterAction,
		Parameters: AdapterParameters(AdapterPlan{
			Operation: "install", Harness: candidate.Harness, TargetRoot: target,
			CandidateDigest: candidate.CandidateDigest, Files: candidate.Files,
		}),
	}
	if _, err := materializer.Apply(context.Background(), materializeStep); err != nil {
		t.Fatal(err)
	}
	if err := materializer.Verify(context.Background(), materializeStep, nil); err != nil {
		t.Fatal(err)
	}
	inspection, findings := adapter.Inspect(target, request)
	if len(findings) != 0 || inspection.Drift != 0 {
		t.Fatalf("installed inspection=%+v findings=%+v", inspection, findings)
	}
	removePlan, findings := adapter.PlanRemove(target, request)
	if len(findings) != 0 {
		t.Fatalf("remove plan findings: %+v", findings)
	}
	installed := removePlan.candidate
	remover, err := NewAdapterRemover(target, installed)
	if err != nil {
		t.Fatal(err)
	}
	removeStep := operations.Step{
		StepID: "remove", RepositoryID: "repo_fixture", Action: RemoveAdapterAction,
		Parameters: AdapterParameters(AdapterPlan{
			Operation: "remove", Harness: installed.Harness, TargetRoot: target,
			CandidateDigest: installed.CandidateDigest, Files: installed.Files,
		}),
	}
	if _, err := remover.Apply(context.Background(), removeStep); err != nil {
		t.Fatal(err)
	}
	if err := remover.Verify(context.Background(), removeStep, nil); err != nil {
		t.Fatal(err)
	}
}

func TestAdapterRemoveBlocksManualDrift(t *testing.T) {
	root, _ := filepath.Abs(filepath.Join("..", ".."))
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	adapter, findings := NewAdapter(root, "codex", schemas)
	if len(findings) != 0 {
		t.Fatalf("adapter findings: %+v", findings)
	}
	request := RenderRequest{SkillProfile: "core", Scope: "project"}
	candidate, findings := adapter.Render(request)
	if len(findings) != 0 {
		t.Fatalf("render findings: %+v", findings)
	}
	target := t.TempDir()
	materializer, err := NewAdapterMaterializer(target, candidate)
	if err != nil {
		t.Fatal(err)
	}
	install := operations.Step{
		StepID: "install", RepositoryID: "repo_fixture", Action: MaterializeAdapterAction,
		Parameters: AdapterParameters(AdapterPlan{
			Operation: "install", Harness: candidate.Harness, TargetRoot: target,
			CandidateDigest: candidate.CandidateDigest, Files: candidate.Files,
		}),
	}
	if _, err := materializer.Apply(context.Background(), install); err != nil {
		t.Fatal(err)
	}
	removePlan, findings := adapter.PlanRemove(target, request)
	if len(findings) != 0 {
		t.Fatalf("remove plan findings: %+v", findings)
	}
	driftPath := ""
	for _, file := range removePlan.candidate.Files {
		if strings.HasSuffix(file.Path, "SKILL.md") {
			driftPath = filepath.Join(target, filepath.FromSlash(file.Path))
			break
		}
	}
	if driftPath == "" {
		t.Fatal("no projected skill found")
	}
	if err := os.WriteFile(driftPath, []byte("manual drift\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	remover, err := NewAdapterRemover(target, removePlan.candidate)
	if err != nil {
		t.Fatal(err)
	}
	remove := operations.Step{
		StepID: "remove", RepositoryID: "repo_fixture", Action: RemoveAdapterAction,
		Parameters: AdapterParameters(AdapterPlan{
			Operation: "remove", Harness: removePlan.candidate.Harness, TargetRoot: target,
			CandidateDigest: removePlan.candidate.CandidateDigest, Files: removePlan.candidate.Files,
		}),
	}
	if _, err := remover.Apply(context.Background(), remove); err == nil {
		t.Fatal("remove accepted manual drift")
	}
	content, err := os.ReadFile(driftPath)
	if err != nil || string(content) != "manual drift\n" {
		t.Fatalf("drift was not preserved: %q %v", content, err)
	}
}

func TestAdapterUpdateRemovesStaleFilesAndRollbackRestoresPriorSet(t *testing.T) {
	target := t.TempDir()
	previous := adapterTestCandidate("previous", map[string]string{
		".agents/skills/gds-one/SKILL.md":     "old shared\n",
		".agents/skills/gds-retired/SKILL.md": "retired\n",
	})
	installAdapterTestCandidate(t, target, previous)
	desired := adapterTestCandidate("desired", map[string]string{
		".agents/skills/gds-one/SKILL.md": "new shared\n",
		".agents/skills/gds-new/SKILL.md": "new\n",
	})
	update, err := NewAdapterUpdater(target, "", "update", desired, previous)
	if err != nil {
		t.Fatal(err)
	}
	updateStep := adapterTransitionTestStep(
		"update", UpdateAdapterAction, target, "", desired, previous,
	)
	if _, err := update.Apply(context.Background(), updateStep); err != nil {
		t.Fatal(err)
	}
	if err := update.Verify(context.Background(), updateStep, nil); err != nil {
		t.Fatal(err)
	}
	assertAdapterTestFile(t, target, ".agents/skills/gds-one/SKILL.md", "new shared\n")
	assertAdapterTestFile(t, target, ".agents/skills/gds-new/SKILL.md", "new\n")
	assertAdapterTestMissing(t, target, ".agents/skills/gds-retired/SKILL.md")

	rollbackSource := filepath.Join(t.TempDir(), "prior")
	rollback, err := NewAdapterUpdater(
		target, rollbackSource, "rollback", previous, desired,
	)
	if err != nil {
		t.Fatal(err)
	}
	rollbackStep := adapterTransitionTestStep(
		"rollback", RollbackAdapterAction, target, rollbackSource, previous, desired,
	)
	if _, err := rollback.Apply(context.Background(), rollbackStep); err != nil {
		t.Fatal(err)
	}
	if err := rollback.Verify(context.Background(), rollbackStep, nil); err != nil {
		t.Fatal(err)
	}
	assertAdapterTestFile(t, target, ".agents/skills/gds-one/SKILL.md", "old shared\n")
	assertAdapterTestFile(t, target, ".agents/skills/gds-retired/SKILL.md", "retired\n")
	assertAdapterTestMissing(t, target, ".agents/skills/gds-new/SKILL.md")
}

func TestAdapterUpdatePreservesConcurrentStaleFileDrift(t *testing.T) {
	target := t.TempDir()
	stalePath := ".agents/skills/gds-retired/SKILL.md"
	sharedPath := ".agents/skills/gds-one/SKILL.md"
	newPath := ".agents/skills/gds-new/SKILL.md"
	previous := adapterTestCandidate("previous", map[string]string{
		sharedPath: "old shared\n", stalePath: "retired\n",
	})
	installAdapterTestCandidate(t, target, previous)
	desired := adapterTestCandidate("desired", map[string]string{
		sharedPath: "new shared\n", newPath: "new\n",
	})
	update, err := NewAdapterUpdater(target, "", "update", desired, previous)
	if err != nil {
		t.Fatal(err)
	}
	update.beforeStaleRemoval = func() {
		if err := os.WriteFile(
			filepath.Join(target, filepath.FromSlash(stalePath)), []byte("concurrent\n"), 0o644,
		); err != nil {
			t.Fatal(err)
		}
	}
	step := adapterTransitionTestStep("update", UpdateAdapterAction, target, "", desired, previous)
	if _, err := update.Apply(context.Background(), step); err == nil {
		t.Fatal("update accepted concurrent stale-file drift")
	}
	assertAdapterTestFile(t, target, sharedPath, "old shared\n")
	assertAdapterTestFile(t, target, stalePath, "concurrent\n")
	assertAdapterTestMissing(t, target, newPath)
}

func TestAdapterTransitionRejectsUnmanagedPathCollision(t *testing.T) {
	target := t.TempDir()
	previous := adapterTestCandidate("previous", map[string]string{
		"managed/old": "old\n",
	})
	installAdapterTestCandidate(t, target, previous)
	if err := os.MkdirAll(filepath.Join(target, "managed"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "managed", "new"), []byte("user\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	desired := adapterTestCandidate("desired", map[string]string{
		"managed/old": "new old\n", "managed/new": "managed\n",
	})
	adapter := &profileAdapter{profile: CapabilityProfile{ID: "codex"}}
	_, findings := adapter.planTransition("update", target, "", desired, previous)
	if len(findings) != 1 || findings[0].Code != "GDS_HARNESS_UPDATE_COLLISION" {
		t.Fatalf("unexpected findings: %+v", findings)
	}
	assertAdapterTestFile(t, target, "managed/new", "user\n")
}

func adapterTestCandidate(label string, values map[string]string) AdapterCandidate {
	contents := make(map[string][]byte, len(values))
	files := make([]AdapterFile, 0, len(values))
	for path, value := range values {
		content := []byte(value)
		contents[path] = content
		files = append(files, AdapterFile{Path: path, Digest: bytesDigest(content), Size: len(content)})
	}
	slices.SortFunc(files, func(left, right AdapterFile) int {
		return strings.Compare(left.Path, right.Path)
	})
	return AdapterCandidate{
		Harness: "codex", CandidateDigest: bytesDigest([]byte(label)), Files: files, contents: contents,
	}
}

func installAdapterTestCandidate(t *testing.T, target string, candidate AdapterCandidate) {
	t.Helper()
	handler, err := NewAdapterMaterializer(target, candidate)
	if err != nil {
		t.Fatal(err)
	}
	step := operations.Step{
		StepID: "install", RepositoryID: "repo_fixture", Action: MaterializeAdapterAction,
		Parameters: AdapterParameters(AdapterPlan{
			Operation: "install", Harness: candidate.Harness, TargetRoot: target,
			CandidateDigest: candidate.CandidateDigest, Files: candidate.Files,
		}),
	}
	if _, err := handler.Apply(context.Background(), step); err != nil {
		t.Fatal(err)
	}
}

func adapterTransitionTestStep(
	operation string,
	action string,
	target string,
	source string,
	desired AdapterCandidate,
	previous AdapterCandidate,
) operations.Step {
	return operations.Step{
		StepID: operation, RepositoryID: "repo_fixture", Action: action,
		Parameters: AdapterParameters(AdapterPlan{
			Operation: operation, Harness: desired.Harness, TargetRoot: target, SourceRoot: source,
			CandidateDigest: desired.CandidateDigest, Files: desired.Files,
			PreviousCandidateDigest: previous.CandidateDigest, PreviousFiles: previous.Files,
		}),
	}
}

func assertAdapterTestFile(t *testing.T, root, relative, expected string) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil || string(content) != expected {
		t.Fatalf("%s content=%q err=%v", relative, content, err)
	}
}

func assertAdapterTestMissing(t *testing.T, root, relative string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(relative))); !os.IsNotExist(err) {
		t.Fatalf("%s should be missing, err=%v", relative, err)
	}
}

func hasAdapterFileSuffix(files []AdapterFile, suffix string) bool {
	for _, file := range files {
		if strings.HasSuffix(file.Path, suffix) {
			return true
		}
	}
	return false
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
