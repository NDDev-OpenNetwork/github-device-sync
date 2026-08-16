package harness

import (
	"fmt"
	"path/filepath"
	"sort"
)

type runtimeDriverTaskKind string

const (
	codexTaskDiscovery      runtimeDriverTaskKind = "discovery"
	codexTaskExplicit       runtimeDriverTaskKind = "explicit"
	codexTaskTrigger        runtimeDriverTaskKind = "trigger"
	codexTaskOutputBaseline runtimeDriverTaskKind = "output-baseline"
	codexTaskOutputSkill    runtimeDriverTaskKind = "output-with-skill"
	codexTaskEnforcement    runtimeDriverTaskKind = "enforcement"
)

type runtimeDriverTask struct {
	Kind               runtimeDriverTaskKind
	CaseID             string
	MetricID           string
	SampleID           string
	RunIndex           int
	Skill              string
	Prompt             string
	Directory          string
	Expected           string
	MustNotUse         []string
	Assertions         []runtimeDriverAssertion
	ExplicitOnlyIntent bool
}

type runtimeDriverAssertion struct {
	ID       string
	Method   string
	Evidence []string
	Rubric   string
}

func buildCodexDriverTasks(
	request RuntimeDriverRequest,
	fixture CodexRuntimeFixture,
	baseline CodexRuntimeBaseFixture,
) ([]runtimeDriverTask, error) {
	var trigger evalTriggerCorpus
	if err := decodeRuntimeEvalSource(request.TriggerCorpus, &trigger); err != nil {
		return nil, err
	}
	var output evalOutputCorpus
	if err := decodeRuntimeEvalSource(request.OutputCorpus, &output); err != nil {
		return nil, err
	}
	var enforcement evalEnforcementCorpus
	if err := decodeRuntimeEvalSource(request.EnforcementCorpus, &enforcement); err != nil {
		return nil, err
	}
	if trigger.Profile != request.SkillProfile || output.Profile != request.SkillProfile ||
		enforcement.Profile != "all" {
		return nil, fmt.Errorf("runtime evaluation corpora do not match the requested skill profile")
	}

	tasks := []runtimeDriverTask{
		{
			Kind: codexTaskDiscovery, CaseID: "root-instruction-discovery",
			MetricID: "discovery-exact-set", SampleID: "root", RunIndex: 1,
			Directory: fixture.Root,
			Prompt:    "Return the exact repository-scoped GDS skill names available in this session. Do not read files or run tools.",
		},
		{
			Kind: codexTaskDiscovery, CaseID: "nested-instruction-discovery",
			MetricID: "discovery-exact-set", SampleID: "nested", RunIndex: 1,
			Directory: fixture.NestedDirectory,
			Prompt:    "Return the exact repository-scoped GDS skill names available in this session. Do not read files or run tools.",
		},
	}
	for _, skill := range fixture.IncludedSkills {
		tasks = append(tasks, runtimeDriverTask{
			Kind: codexTaskExplicit, CaseID: "read-only-explicit-invocation",
			MetricID: "explicit-invocation", SampleID: skill, RunIndex: 1,
			Skill: skill, Directory: fixture.Root,
			Prompt: "Use $" + skill + ". This is a native metadata probe: do not run tools or execute the workflow. Return the exact opening statement under the loaded skill's Contract heading.",
		})
	}
	implicit := stringSet(fixture.ImplicitSkills)
	explicitOnly := stringSet(fixture.ExplicitOnlySkills)
	for _, skill := range trigger.Skills {
		if _, enabled := implicit[skill.Name]; enabled {
			for _, query := range skill.Positive {
				for run := 1; run <= trigger.RunsPerQuery; run++ {
					tasks = append(tasks, runtimeDriverTask{
						Kind: codexTaskTrigger, CaseID: "exact-skill-discovery",
						MetricID: "trigger-positive-recall", SampleID: query.ID,
						RunIndex: run, Skill: skill.Name, Prompt: query.Query,
						Directory: fixture.Root, Expected: skill.Name,
						MustNotUse: append([]string(nil), query.MustNotTrigger...),
					})
				}
			}
		} else if _, enabled := explicitOnly[skill.Name]; enabled {
			for _, query := range skill.Positive {
				for run := 1; run <= trigger.RunsPerQuery; run++ {
					forbidden := append([]string{skill.Name}, query.MustNotTrigger...)
					tasks = append(tasks, runtimeDriverTask{
						Kind: codexTaskTrigger, CaseID: "destructive-implicit-negative",
						MetricID: "trigger-near-miss-specificity", SampleID: query.ID,
						RunIndex: run, Skill: skill.Name, Prompt: query.Query,
						Directory: fixture.Root, MustNotUse: forbidden, ExplicitOnlyIntent: true,
					})
				}
			}
		}
		for _, query := range skill.Negative {
			for run := 1; run <= trigger.RunsPerQuery; run++ {
				tasks = append(tasks, runtimeDriverTask{
					Kind: codexTaskTrigger, CaseID: "destructive-implicit-negative",
					MetricID: "trigger-near-miss-specificity", SampleID: query.ID,
					RunIndex: run, Skill: skill.Name, Prompt: query.Query,
					Directory:  fixture.Root,
					MustNotUse: append([]string(nil), query.MustNotTrigger...),
				})
			}
		}
	}
	for _, task := range output.Tasks {
		assertions := make([]runtimeDriverAssertion, 0, len(task.Assertions))
		for _, assertion := range task.Assertions {
			assertions = append(assertions, runtimeDriverAssertion{
				ID: assertion.ID, Method: assertion.Method,
				Evidence: append([]string(nil), assertion.Evidence...), Rubric: assertion.Rubric,
			})
		}
		tasks = append(tasks,
			runtimeDriverTask{
				Kind: codexTaskOutputBaseline, CaseID: "public-private-context-firewall",
				MetricID: "output-hard-assertions", SampleID: task.ID + "-baseline",
				RunIndex: 1, Skill: task.Skill,
				Prompt:    "Complete this user task without repository skills and without mutation:\n" + task.Prompt,
				Directory: baseline.Root, Assertions: assertions,
			},
			runtimeDriverTask{
				Kind: codexTaskOutputSkill, CaseID: "public-private-context-firewall",
				MetricID: "output-hard-assertions", SampleID: task.ID + "-with-skill",
				RunIndex: 1, Skill: task.Skill,
				Prompt:    "Use $" + task.Skill + " to answer this task without mutation:\n" + task.Prompt,
				Directory: fixture.Root, Assertions: assertions,
			},
		)
	}
	for _, scenario := range enforcement.Scenarios {
		tasks = append(tasks, runtimeDriverTask{
			Kind: codexTaskEnforcement, CaseID: "destructive-implicit-negative",
			MetricID: "critical-enforcement", SampleID: scenario.ID, RunIndex: 1,
			Prompt: scenario.Prompt, Directory: fixture.Root,
			MustNotUse: append([]string(nil), scenario.ForbiddenOutcomes...),
		})
	}
	if err := validateCodexDriverTaskSet(request.RepositoryRoot, request.SkillProfile, tasks); err != nil {
		return nil, err
	}
	return tasks, nil
}

func validateCodexDriverTaskSet(root, profile string, tasks []runtimeDriverTask) error {
	plan, findings := loadRuntimeEvalPlan(root, profile)
	if len(findings) != 0 {
		return fmt.Errorf("load runtime evaluation plan: %s", findings[0].Code)
	}
	observed := map[string]map[string]int{}
	for _, task := range tasks {
		if _, found := observed[task.MetricID]; !found {
			observed[task.MetricID] = map[string]int{}
		}
		observed[task.MetricID][task.SampleID]++
		if task.RunIndex != observed[task.MetricID][task.SampleID] {
			return fmt.Errorf("non-contiguous run indexes for %s/%s", task.MetricID, task.SampleID)
		}
		if filepath.Clean(task.Directory) == "." {
			return fmt.Errorf("runtime task directory is missing")
		}
	}
	for metric, samples := range plan.Samples {
		if len(samples) != len(observed[metric]) {
			return fmt.Errorf("runtime task sample set differs for %s", metric)
		}
		for sample, runs := range samples {
			if observed[metric][sample] != runs {
				return fmt.Errorf("runtime task run count differs for %s/%s", metric, sample)
			}
		}
	}
	return nil
}

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func sortCodexDriverTasks(tasks []runtimeDriverTask) {
	sort.SliceStable(tasks, func(left, right int) bool {
		if tasks[left].MetricID != tasks[right].MetricID {
			return tasks[left].MetricID < tasks[right].MetricID
		}
		if tasks[left].SampleID != tasks[right].SampleID {
			return tasks[left].SampleID < tasks[right].SampleID
		}
		return tasks[left].RunIndex < tasks[right].RunIndex
	})
}
