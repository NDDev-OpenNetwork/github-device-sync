package skills

import (
	"os"
	"path/filepath"
	"sort"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

type EvalCoverage struct {
	Profile              string `json:"profile"`
	Skills               int    `json:"skills"`
	PositiveQueries      int    `json:"positive_queries"`
	NegativeQueries      int    `json:"negative_queries"`
	RunsPerQuery         int    `json:"runs_per_query"`
	OutputTasks          int    `json:"output_tasks"`
	EnforcementScenarios int    `json:"enforcement_scenarios"`
	RuntimeStatus        string `json:"runtime_status"`
}

type triggerCorpus struct {
	SchemaVersion int            `json:"schema_version"`
	Profile       string         `json:"profile"`
	RunsPerQuery  int            `json:"runs_per_query"`
	Skills        []triggerSkill `json:"skills"`
}

type triggerSkill struct {
	Name     string         `json:"name"`
	Positive []triggerQuery `json:"positive"`
	Negative []triggerQuery `json:"negative"`
}

type triggerQuery struct {
	ID             string   `json:"id"`
	Split          string   `json:"split"`
	Query          string   `json:"query"`
	Expected       string   `json:"expected,omitempty"`
	MustNotTrigger []string `json:"must_not_trigger"`
}

type outputCorpus struct {
	SchemaVersion int          `json:"schema_version"`
	Profile       string       `json:"profile"`
	Status        string       `json:"status"`
	Tasks         []outputTask `json:"tasks"`
}

type outputTask struct {
	ID         string            `json:"id"`
	Skill      string            `json:"skill"`
	Prompt     string            `json:"prompt"`
	Assertions []outputAssertion `json:"assertions"`
}

type outputAssertion struct {
	ID       string   `json:"id"`
	Method   string   `json:"method"`
	Evidence []string `json:"evidence"`
	Rubric   string   `json:"rubric,omitempty"`
}

type enforcementCorpus struct {
	SchemaVersion int                   `json:"schema_version"`
	Profile       string                `json:"profile"`
	Scenarios     []enforcementScenario `json:"scenarios"`
}

type enforcementScenario struct {
	ID                string   `json:"id"`
	Prompt            string   `json:"prompt"`
	ForbiddenOutcomes []string `json:"forbidden_outcomes"`
}

func validateEvals(
	root string,
	registry Registry,
	schemas *validation.Set,
) ([]EvalCoverage, []domain.Finding) {
	enforcementPath := filepath.Join(root, "skills", "evals", "enforcement", "common.json")
	var enforcement enforcementCorpus
	findings := schemas.ValidateFile("skill-enforcement-eval", enforcementPath)
	if len(findings) != 0 {
		return nil, findings
	}
	if err := decodeEvalFile(enforcementPath, &enforcement); err != nil {
		return nil, []domain.Finding{finding(
			"GDS_SKILL_EVAL_ENFORCEMENT_INVALID",
			"Cannot decode the common enforcement corpus.", enforcementPath, err,
		)}
	}
	if enforcement.SchemaVersion != 1 || enforcement.Profile != "all" || len(enforcement.Scenarios) < 8 {
		findings = append(findings, simpleFinding(
			"GDS_SKILL_EVAL_ENFORCEMENT_CONTRACT_INVALID",
			"Common enforcement corpus identity or scenario count is invalid.",
			map[string]any{"path": enforcementPath},
		))
	}
	seenScenarios := map[string]struct{}{}
	for _, scenario := range enforcement.Scenarios {
		if _, duplicate := seenScenarios[scenario.ID]; duplicate || scenario.ID == "" ||
			scenario.Prompt == "" || len(scenario.ForbiddenOutcomes) == 0 {
			findings = append(findings, simpleFinding(
				"GDS_SKILL_EVAL_ENFORCEMENT_SCENARIO_INVALID",
				"Enforcement scenarios require unique identities, prompts, and forbidden outcomes.",
				map[string]any{"scenario": scenario.ID},
			))
		}
		seenScenarios[scenario.ID] = struct{}{}
	}
	coverages := make([]EvalCoverage, 0, len(registry.Profiles))
	for _, profile := range registry.Profiles {
		coverage, profileFindings := validateProfileEvals(
			root, registry, schemas, profile.ID, len(enforcement.Scenarios),
		)
		coverages = append(coverages, coverage)
		findings = append(findings, profileFindings...)
	}
	sort.Slice(coverages, func(left, right int) bool {
		return coverages[left].Profile < coverages[right].Profile
	})
	return coverages, findings
}

func validateProfileEvals(
	root string,
	registry Registry,
	schemas *validation.Set,
	profileID string,
	enforcementScenarios int,
) (EvalCoverage, []domain.Finding) {
	coverage := EvalCoverage{
		Profile: profileID, RuntimeStatus: "runtime-not-proven",
		EnforcementScenarios: enforcementScenarios,
	}
	triggerPath := filepath.Join(root, "skills", "evals", "trigger", profileID+".json")
	if findings := schemas.ValidateFile("skill-trigger-eval", triggerPath); len(findings) != 0 {
		return coverage, findings
	}
	var corpus triggerCorpus
	if err := decodeEvalFile(triggerPath, &corpus); err != nil {
		return coverage, []domain.Finding{finding(
			"GDS_SKILL_EVAL_TRIGGER_INVALID", "Cannot decode the profile trigger corpus.", triggerPath, err,
		)}
	}
	findings := []domain.Finding{}
	if corpus.SchemaVersion != 1 || corpus.Profile != profileID || corpus.RunsPerQuery < 3 {
		findings = append(findings, simpleFinding(
			"GDS_SKILL_EVAL_TRIGGER_CONTRACT_INVALID",
			"Profile trigger corpus identity or repeat count is invalid.",
			map[string]any{"path": triggerPath},
		))
	}
	coverage.RunsPerQuery = corpus.RunsPerQuery
	expected := profileSkillSet(registry, profileID)
	seenSkills := map[string]struct{}{}
	seenQueries := map[string]struct{}{}
	for _, skill := range corpus.Skills {
		if _, duplicate := seenSkills[skill.Name]; duplicate {
			findings = append(findings, evalFinding(
				"GDS_SKILL_EVAL_DUPLICATE_SKILL", skill.Name, "Skill occurs more than once in trigger corpus.",
			))
			continue
		}
		seenSkills[skill.Name] = struct{}{}
		if _, required := expected[skill.Name]; !required {
			findings = append(findings, evalFinding(
				"GDS_SKILL_EVAL_PROFILE_MISMATCH", skill.Name, "Trigger corpus contains a skill outside its profile.",
			))
		}
		if len(skill.Positive) < 8 || len(skill.Negative) < 8 {
			findings = append(findings, evalFinding(
				"GDS_SKILL_EVAL_QUERY_COUNT_LOW", skill.Name,
				"Trigger corpus requires at least eight positive and eight near-miss negative queries.",
			))
		}
		coverage.PositiveQueries += len(skill.Positive)
		coverage.NegativeQueries += len(skill.Negative)
		findings = append(findings, validateQueries(skill.Name, skill.Positive, true, seenQueries)...)
		findings = append(findings, validateQueries(skill.Name, skill.Negative, false, seenQueries)...)
	}
	for skill := range expected {
		if _, found := seenSkills[skill]; !found {
			findings = append(findings, evalFinding(
				"GDS_SKILL_EVAL_SKILL_MISSING", skill, "Profile trigger corpus is missing a profiled skill.",
			))
		}
	}
	coverage.Skills = len(seenSkills)

	outputPath := filepath.Join(root, "skills", "evals", "output", profileID+".json")
	if schemaFindings := schemas.ValidateFile("skill-output-eval", outputPath); len(schemaFindings) != 0 {
		findings = append(findings, schemaFindings...)
		return coverage, findings
	}
	var output outputCorpus
	if err := decodeEvalFile(outputPath, &output); err != nil {
		findings = append(findings, finding(
			"GDS_SKILL_EVAL_OUTPUT_INVALID", "Cannot decode the profile output corpus.", outputPath, err,
		))
		return coverage, findings
	}
	if output.SchemaVersion != 1 || output.Profile != profileID || output.Status != "runtime-not-proven" {
		findings = append(findings, simpleFinding(
			"GDS_SKILL_EVAL_OUTPUT_CONTRACT_INVALID", "Profile output corpus identity or status is invalid.",
			map[string]any{"path": outputPath},
		))
	}
	outputSkills := map[string]struct{}{}
	outputTaskIDs := map[string]struct{}{}
	for _, task := range output.Tasks {
		if _, duplicate := outputTaskIDs[task.ID]; duplicate {
			findings = append(findings, evalFinding(
				"GDS_SKILL_EVAL_OUTPUT_ID_DUPLICATE", task.Skill,
				"Output task identity occurs more than once in the profile corpus.",
			))
		}
		outputTaskIDs[task.ID] = struct{}{}
		if _, duplicate := outputSkills[task.Skill]; duplicate {
			findings = append(findings, evalFinding(
				"GDS_SKILL_EVAL_OUTPUT_TASK_DUPLICATE", task.Skill,
				"Output corpus contains more than one task for a profiled skill.",
			))
		}
		outputSkills[task.Skill] = struct{}{}
		if _, required := expected[task.Skill]; !required || len(task.Assertions) == 0 {
			findings = append(findings, evalFinding(
				"GDS_SKILL_EVAL_OUTPUT_TASK_INVALID", task.Skill,
				"Output task must target one profiled skill and declare assertions.",
			))
		}
		assertionIDs := map[string]struct{}{}
		for _, assertion := range task.Assertions {
			if _, duplicate := assertionIDs[assertion.ID]; duplicate {
				findings = append(findings, evalFinding(
					"GDS_SKILL_EVAL_OUTPUT_ASSERTION_DUPLICATE", task.Skill,
					"Output task assertion identity occurs more than once.",
				))
			}
			assertionIDs[assertion.ID] = struct{}{}
		}
	}
	coverage.OutputTasks = len(output.Tasks)
	for skill := range expected {
		if _, found := outputSkills[skill]; !found {
			findings = append(findings, evalFinding(
				"GDS_SKILL_EVAL_OUTPUT_TASK_MISSING", skill, "Profile output corpus is missing a profiled skill.",
			))
		}
	}
	sortFindings(findings)
	return coverage, findings
}

func validateQueries(
	skill string,
	queries []triggerQuery,
	positive bool,
	seen map[string]struct{},
) []domain.Finding {
	findings := []domain.Finding{}
	splits := map[string]int{}
	for _, query := range queries {
		if _, duplicate := seen[query.ID]; duplicate {
			findings = append(findings, evalFinding(
				"GDS_SKILL_EVAL_QUERY_DUPLICATE", skill, "Trigger query ID occurs more than once.",
			))
		}
		seen[query.ID] = struct{}{}
		splits[query.Split]++
		if query.Split != "train" && query.Split != "validation" {
			findings = append(findings, evalFinding(
				"GDS_SKILL_EVAL_SPLIT_INVALID", skill, "Trigger query split must be train or validation.",
			))
		}
		if query.Query == "" || len(query.MustNotTrigger) == 0 {
			findings = append(findings, evalFinding(
				"GDS_SKILL_EVAL_QUERY_INVALID", skill, "Trigger query and negative boundary are required.",
			))
		}
		if positive && query.Expected != skill {
			findings = append(findings, evalFinding(
				"GDS_SKILL_EVAL_EXPECTATION_INVALID", skill, "Positive query must expect its owning skill.",
			))
		}
		if positive && query.Expected != "" {
			for _, forbidden := range query.MustNotTrigger {
				if forbidden == query.Expected {
					findings = append(findings, evalFinding("GDS_SKILL_EVAL_EXPECTATION_CONTRADICTS_BOUNDARY", skill, "Positive query expects a skill that its own must_not_trigger boundary forbids."))
					break
				}
			}
		}
		if !positive && query.Expected != "" {
			findings = append(findings, evalFinding(
				"GDS_SKILL_EVAL_NEGATIVE_EXPECTATION_INVALID", skill, "Negative query cannot expect its rejected skill.",
			))
		}
	}
	if splits["train"] == 0 || splits["validation"] == 0 {
		findings = append(findings, evalFinding(
			"GDS_SKILL_EVAL_SPLIT_MISSING", skill, "Train and validation queries are both required.",
		))
	}
	return findings
}

func profileSkillSet(registry Registry, profileID string) map[string]struct{} {
	result := map[string]struct{}{}
	for _, profile := range registry.Profiles {
		if profile.ID != profileID {
			continue
		}
		for _, skill := range profile.Skills {
			result[skill] = struct{}{}
		}
	}
	return result
}

func decodeEvalFile(path string, target any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return serialization.DecodeInto(path, raw, target)
}

func evalFinding(code, skill, message string) domain.Finding {
	return simpleFinding(code, message, map[string]any{"skill": skill})
}
