package app

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/harness"
)

func TestHarnessObservationsUseConsumedEngineInExternalEstate(t *testing.T) {
	_, current, _, _ := runtime.Caller(0)
	source := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	root := t.TempDir()
	engine := filepath.Join(root, "modules", "github-device-sync")
	for _, directory := range []string{"harnesses", "skills", "docs", "tests/harness"} {
		if err := copyAppTestTree(source, engine, directory); err != nil {
			t.Fatal(err)
		}
	}
	runCompletionGraphGit(t, root, "init", "-q")
	runCompletionGraphGit(t, root, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.invalid",
		"-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "fixture")
	// An estate-local decoy must not replace the consumed catalogue.
	if err := os.Mkdir(filepath.Join(root, "harnesses"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "harnesses", "capability-registry.yaml"), []byte("invalid: true\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	for _, name := range []string{"agy", "claude", "codex", "cursor-agent", "devin", "grok", "opencode", "pi"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n[ \"$1\" = --version ] || exit 42\nprintf 'fixture 1.2.3\\n'\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	home := t.TempDir()
	t.Setenv("HOME", home)
	services, err := NewServices(DefaultClock)
	if err != nil {
		t.Fatal(err)
	}
	request := harness.RenderRequest{SkillProfile: "core", Scope: "project"}
	target := t.TempDir()
	t.Run("detect-one-and-all", func(t *testing.T) {
		one := services.DetectHarness(t.Context(), root, "devin")
		if one.ExitClass != domain.ExitSuccess || one.Data.(harness.RuntimeObservation).Version != "fixture 1.2.3" {
			t.Fatalf("external detection: %+v", one)
		}
		all := services.DetectHarness(t.Context(), root, "all")
		if len(all.Data.(harness.RuntimeDetectionReport).Harnesses) != len(harness.CanonicalIDs) {
			t.Fatalf("external catalogue not observed: %+v", all)
		}
	})
	t.Run("render-inspect-and-doctor", func(t *testing.T) {
		baseline, findings := harness.NewAdapter(source, "codex", services.Schemas)
		if len(findings) != 0 {
			t.Fatal(findings)
		}
		want, findings := baseline.Render(request)
		if len(findings) != 0 {
			t.Fatal(findings)
		}
		render := services.RenderHarnessAdapter(t.Context(), root, "codex", request)
		if render.ExitClass != domain.ExitSuccess || render.Data.(harness.AdapterCandidate).CandidateDigest != want.CandidateDigest {
			t.Fatalf("external rendering differs from canonical source: %+v", render)
		}
		inspect := services.InspectHarnessAdapter(t.Context(), root, "codex", target, request)
		if inspect.ExitClass != domain.ExitSuccess || inspect.Data.(harness.AdapterInspection).CandidateDigest != want.CandidateDigest {
			t.Fatalf("external inspection: %+v", inspect)
		}
		doctor := services.DoctorHarnessAdapter(t.Context(), root, "codex", target, request)
		if doctor.Data == nil || doctor.Data.(harness.AdapterDoctorReport).Runtime.Version != "fixture 1.2.3" {
			t.Fatalf("external doctor: %+v", doctor)
		}
	})
	t.Run("installed-only-plan-remains-refused", func(t *testing.T) {
		plan := services.PlanHarnessAdapter(t.Context(), root, "devin", target, request, "install")
		if !appHasFinding(plan, "GDS_HARNESS_PROJECT_SKILLS_NOT_PROVEN") || plan.Mutation.Attempted {
			t.Fatalf("unproven configuration was permitted: %+v", plan)
		}
	})
	t.Run("evaluation-without-model-execution", func(t *testing.T) {
		result := services.EvaluateHarnessAdapter(t.Context(), root, "codex", harness.EvalOptions{
			SkillProfile: "core", ModelLabel: "not-proven", ExecutionProfile: "read-only",
		})
		run, ok := result.Data.(harness.EvalRun)
		if !ok || run.Profile.ID != "codex" || appHasFinding(result, "GDS_INPUT_READ_FAILED") || result.Mutation.Attempted {
			t.Fatalf("external evaluation did not resolve the consumed contract: %+v", result)
		}
	})
	t.Run("device-reconciliation", func(t *testing.T) {
		result := services.ReconcileDeviceHarnesses(t.Context(), HarnessSyncOptions{
			Path: root, DevicePath: filepath.Join(source, "estate", "devices", "example-user-ubuntu-1.yaml"),
			TargetRoot: target, SkillProfile: "core", Scope: "project",
		})
		if _, ok := result.Data.(HarnessSyncData); !ok || appHasFinding(result, "GDS_INPUT_READ_FAILED") {
			t.Fatalf("external reconciliation did not inspect the consumed catalogue: %+v", result)
		}
	})
	for _, directory := range []string{home, target} {
		entries, err := os.ReadDir(directory)
		if err != nil || len(entries) != 0 {
			t.Fatalf("read-only operation changed %s: %v %v", directory, entries, err)
		}
	}
}
