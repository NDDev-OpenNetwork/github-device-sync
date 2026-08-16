package harness

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
)

type runtimeEvalPlan struct {
	Samples map[string]map[string]int
}

type evalRegistry struct {
	SchemaVersion int             `json:"schema_version"`
	Namespace     string          `json:"namespace"`
	Budgets       json.RawMessage `json:"budgets"`
	Profiles      []struct {
		ID     string   `json:"id"`
		Scope  string   `json:"scope"`
		Skills []string `json:"skills"`
	} `json:"profiles"`
	Plugins json.RawMessage `json:"plugins"`
	Skills  []struct {
		Name       string          `json:"name"`
		Path       string          `json:"path"`
		Invocation string          `json:"invocation"`
		Mutation   string          `json:"mutation"`
		Interface  json.RawMessage `json:"interface"`
	} `json:"skills"`
}

type evalTriggerCorpus struct {
	SchemaVersion int    `json:"schema_version"`
	Profile       string `json:"profile"`
	RunsPerQuery  int    `json:"runs_per_query"`
	Skills        []struct {
		Name     string             `json:"name"`
		Positive []evalTriggerQuery `json:"positive"`
		Negative []evalTriggerQuery `json:"negative"`
	} `json:"skills"`
}

type evalTriggerQuery struct {
	ID             string   `json:"id"`
	Split          string   `json:"split"`
	Query          string   `json:"query"`
	Expected       string   `json:"expected,omitempty"`
	MustNotTrigger []string `json:"must_not_trigger"`
}

type evalOutputCorpus struct {
	SchemaVersion int    `json:"schema_version"`
	Profile       string `json:"profile"`
	Status        string `json:"status"`
	Tasks         []struct {
		ID         string `json:"id"`
		Skill      string `json:"skill"`
		Prompt     string `json:"prompt"`
		Assertions []struct {
			ID       string   `json:"id"`
			Method   string   `json:"method"`
			Evidence []string `json:"evidence"`
			Rubric   string   `json:"rubric,omitempty"`
		} `json:"assertions"`
	} `json:"tasks"`
}

type evalEnforcementCorpus struct {
	SchemaVersion int    `json:"schema_version"`
	Profile       string `json:"profile"`
	Scenarios     []struct {
		ID                string   `json:"id"`
		Prompt            string   `json:"prompt"`
		ForbiddenOutcomes []string `json:"forbidden_outcomes"`
	} `json:"scenarios"`
}

func loadRuntimeEvalPlan(root, profile string) (runtimeEvalPlan, []domain.Finding) {
	plan := runtimeEvalPlan{Samples: map[string]map[string]int{
		"discovery-exact-set":           {"root": 1, "nested": 1},
		"explicit-invocation":           {},
		"trigger-positive-recall":       {},
		"trigger-near-miss-specificity": {},
		"output-hard-assertions":        {},
		"critical-enforcement":          {},
	}}
	var registry evalRegistry
	if err := decodeRuntimeEvalSource(filepath.Join(root, "skills", "registry.yaml"), &registry); err != nil {
		return plan, []domain.Finding{evalFinding(
			"GDS_HARNESS_EVAL_PLAN_INVALID", "Cannot decode the canonical skill registry.", err,
		)}
	}
	foundProfile := false
	profileSkills := map[string]struct{}{}
	for _, item := range registry.Profiles {
		if item.ID != profile {
			continue
		}
		foundProfile = true
		for _, skill := range item.Skills {
			profileSkills[skill] = struct{}{}
			plan.Samples["explicit-invocation"][skill] = 1
		}
	}
	if !foundProfile {
		return plan, []domain.Finding{runtimeEvidenceFinding(
			"GDS_HARNESS_EVAL_PROFILE_UNKNOWN",
			"Runtime evaluation profile is absent from the canonical skill registry.",
			map[string]any{"profile": profile},
		)}
	}
	invocations := map[string]string{}
	for _, skill := range registry.Skills {
		invocations[skill.Name] = skill.Invocation
	}
	var trigger evalTriggerCorpus
	if err := decodeRuntimeEvalSource(
		filepath.Join(root, "skills", "evals", "trigger", profile+".json"), &trigger,
	); err != nil || trigger.Profile != profile || trigger.RunsPerQuery < 1 {
		return plan, []domain.Finding{evalFinding(
			"GDS_HARNESS_EVAL_TRIGGER_PLAN_INVALID",
			"Cannot decode a matching executable trigger corpus.", err,
		)}
	}
	for _, skill := range trigger.Skills {
		if _, included := profileSkills[skill.Name]; !included {
			continue
		}
		if invocations[skill.Name] == "implicit" {
			for _, query := range skill.Positive {
				plan.Samples["trigger-positive-recall"][query.ID] = trigger.RunsPerQuery
			}
		} else if invocations[skill.Name] == "explicit-only" {
			for _, query := range skill.Positive {
				plan.Samples["trigger-near-miss-specificity"][query.ID] = trigger.RunsPerQuery
			}
		}
		for _, query := range skill.Negative {
			plan.Samples["trigger-near-miss-specificity"][query.ID] = trigger.RunsPerQuery
		}
	}
	var output evalOutputCorpus
	if err := decodeRuntimeEvalSource(
		filepath.Join(root, "skills", "evals", "output", profile+".json"), &output,
	); err != nil || output.Profile != profile || len(output.Tasks) == 0 {
		return plan, []domain.Finding{evalFinding(
			"GDS_HARNESS_EVAL_OUTPUT_PLAN_INVALID",
			"Cannot decode a matching executable output corpus.", err,
		)}
	}
	for _, task := range output.Tasks {
		plan.Samples["output-hard-assertions"][task.ID+"-baseline"] = 1
		plan.Samples["output-hard-assertions"][task.ID+"-with-skill"] = 1
	}
	var enforcement evalEnforcementCorpus
	if err := decodeRuntimeEvalSource(
		filepath.Join(root, "skills", "evals", "enforcement", "common.json"), &enforcement,
	); err != nil || enforcement.Profile != "all" || len(enforcement.Scenarios) == 0 {
		return plan, []domain.Finding{evalFinding(
			"GDS_HARNESS_EVAL_ENFORCEMENT_PLAN_INVALID",
			"Cannot decode a matching executable enforcement corpus.", err,
		)}
	}
	for _, scenario := range enforcement.Scenarios {
		plan.Samples["critical-enforcement"][scenario.ID] = 1
	}
	for metric, samples := range plan.Samples {
		if len(samples) == 0 && metric != "trigger-positive-recall" {
			return plan, []domain.Finding{runtimeEvidenceFinding(
				"GDS_HARNESS_EVAL_PLAN_EMPTY",
				"Every runtime metric requires one or more canonical samples.",
				map[string]any{"metric": metric, "profile": profile},
			)}
		}
	}
	return plan, nil
}

func validateRuntimeEvalCoverage(
	evidence RuntimeEvidence,
	plan runtimeEvalPlan,
) []domain.Finding {
	observed := map[string]map[string]map[int]struct{}{}
	for _, transcript := range evidence.Transcripts {
		if transcript.MetricID == nil || transcript.SampleID == nil || transcript.RunIndex == nil {
			continue
		}
		if _, found := observed[*transcript.MetricID]; !found {
			observed[*transcript.MetricID] = map[string]map[int]struct{}{}
		}
		if _, found := observed[*transcript.MetricID][*transcript.SampleID]; !found {
			observed[*transcript.MetricID][*transcript.SampleID] = map[int]struct{}{}
		}
		observed[*transcript.MetricID][*transcript.SampleID][*transcript.RunIndex] = struct{}{}
	}
	findings := []domain.Finding{}
	for metric, expectedSamples := range plan.Samples {
		actualSamples := observed[metric]
		for sample, expectedRuns := range expectedSamples {
			runs := actualSamples[sample]
			for index := 1; index <= expectedRuns; index++ {
				if _, found := runs[index]; !found {
					findings = append(findings, runtimeEvidenceFinding(
						"GDS_HARNESS_RUNTIME_SAMPLE_MISSING",
						"Canonical runtime evaluation sample is missing an exact required run.",
						map[string]any{"metric": metric, "sample": sample, "run_index": index},
					))
				}
			}
			if len(runs) != expectedRuns {
				findings = append(findings, runtimeEvidenceFinding(
					"GDS_HARNESS_RUNTIME_SAMPLE_RUN_SET_INVALID",
					"Runtime sample run set differs from the canonical corpus.",
					map[string]any{"metric": metric, "sample": sample, "expected_runs": expectedRuns},
				))
			}
		}
		for sample := range actualSamples {
			if _, expected := expectedSamples[sample]; !expected {
				findings = append(findings, runtimeEvidenceFinding(
					"GDS_HARNESS_RUNTIME_SAMPLE_UNEXPECTED",
					"Runtime evidence contains a sample outside the canonical corpus.",
					map[string]any{"metric": metric, "sample": sample},
				))
			}
		}
	}
	sortFindings(findings)
	return findings
}

func decodeRuntimeEvalSource(path string, target any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := serialization.DecodeInto(path, raw, target); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func runtimeEvalPlanCounts(plan runtimeEvalPlan) map[string]int {
	result := map[string]int{}
	for metric, samples := range plan.Samples {
		for _, runs := range samples {
			result[metric] += runs
		}
	}
	return result
}
