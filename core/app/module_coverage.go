package app

import (
	"context"
	"os"
	"path/filepath"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/manifest"
	moduleworkflow "github.com/NDDev-OpenNetwork/github-device-sync/core/module"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
)

// ModuleCoverageOptions selects the runtime and, optionally, one module.
type ModuleCoverageOptions struct {
	GitHubReadOptions
	Module string
}

type ModuleCoverageData struct {
	Modules []moduleworkflow.ContextCoverage `json:"modules"`
}

// CoverModules compares what each declared module's anchor claims about its gate
// with what its protected branch enforces.
//
// This reads the provider, so it is a command rather than a validator, for the
// same reason `gds module verify` is one: the answer does not exist in any
// tracked file. Half the modules in this estate track no ruleset document at
// all, and the one that does resolves to a single aggregate `ci-gate` context,
// so comparing tracked declarations against each other would confirm agreement
// between two copies of the same claim and prove nothing about the gate.
//
// It also stays out of this repository's own gates deliberately. A module
// understating its gate is that module's defect; importing it into this
// repository's required checks would block work here on a fix somebody else has
// to make, which is the coupling `gds module verify` was shaped to avoid.
func (services *Services) CoverModules(
	ctx context.Context,
	path string,
	options ModuleCoverageOptions,
) domain.Envelope {
	const command = "gds module coverage"
	_, consumer, findings := services.policyInputs(ctx, path)
	if len(findings) != 0 {
		return domain.NewEnvelope(command, classifyFindings(findings), nil, findings...)
	}
	info, err := services.Git.RepositoryInfo(ctx, path)
	if err != nil {
		return envelopeForError(command, path, err)
	}
	topology, err := services.Git.InspectTopology(ctx, info.WorktreeRoot)
	if err != nil {
		return envelopeForError(command, path, err)
	}
	paths := map[string]string{}
	for _, submodule := range topology.Submodules {
		if submodule.Name != "" {
			paths[submodule.Name] = submodule.Path
		}
	}
	runtime, envelope := services.loadGitHubRuntime(ctx, path, options.GitHubReadOptions, command)
	if envelope != nil {
		return *envelope
	}

	selected := strings.TrimSpace(options.Module)
	data := ModuleCoverageData{Modules: []moduleworkflow.ContextCoverage{}}
	matched := false

	for _, relationship := range consumer.Relationships {
		if relationship.Type != "git-submodule-consumer" {
			continue
		}
		name := relationship.GitmodulesName
		if selected != "" && selected != name {
			continue
		}
		matched = true
		modulePath := filepath.Join(info.WorktreeRoot, filepath.FromSlash(paths[name]))
		if _, statErr := os.Stat(filepath.Join(modulePath, ".gds", "repository.yaml")); statErr != nil {
			// Not checked out is evidence GDS does not have. Saying so is
			// different from saying the module's gate agrees with its anchor.
			findings = append(findings, domain.Finding{
				Code: "GDS_MODULE_COVERAGE_NOT_PROVEN", Severity: domain.SeverityHigh,
				Message:  "A declared module is not checked out, so its anchor could not be read.",
				Evidence: map[string]any{"gitmodules_name": name, "path": modulePath},
			})
			continue
		}
		anchor, anchorFindings := manifest.NewLoader(services.Schemas).LoadRepository(modulePath)
		if len(anchorFindings) != 0 {
			findings = append(findings, anchorFindings...)
			continue
		}
		installationID := normalizeInstallationID(anchor.Provider.Installation, runtime.readers)
		reader, found := runtime.readers[installationID]
		if !found {
			findings = append(findings, domain.Finding{
				Code: "GDS_MODULE_COVERAGE_NOT_PROVEN", Severity: domain.SeverityHigh,
				Message: "A declared module names an installation this runtime cannot read.",
				Evidence: map[string]any{
					"gitmodules_name": name, "installation": anchor.Provider.Installation,
				},
			})
			continue
		}
		enforced, readFindings := enforcedContexts(
			ctx, reader, name, anchor.Provider.Owner, anchor.Provider.Name,
		)
		if len(readFindings) != 0 {
			findings = append(findings, readFindings...)
			continue
		}
		coverage, coverageFindings := moduleworkflow.CompareRequiredContexts(
			name, anchor.Repository.ID, anchor.Verification.RequiredContexts, enforced,
		)
		findings = append(findings, coverageFindings...)
		data.Modules = append(data.Modules, coverage)
	}

	if selected != "" && !matched {
		return domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{
			Code: "GDS_MODULE_COVERAGE_SELECTION_UNKNOWN", Severity: domain.SeverityHigh,
			Message:  "No declared git-submodule-consumer relationship carries that .gitmodules name.",
			Evidence: map[string]any{"module": selected},
		})
	}
	return domain.NewEnvelope(command, classifyFindings(findings), data, findings...)
}

// enforcedContexts collects the required status check contexts actually in force
// on the module's default branch.
//
// Two reads, because neither answers the question alone. The effective-rules
// endpoint reports which rules reach the branch -- from repository and
// organization rulesets alike, with GitHub's own condition matching already
// applied -- but carries no enforcement, so a ruleset in `evaluate` mode is
// indistinguishable there from one that blocks. The ruleset list carries
// enforcement but not what reaches the branch. Keeping only the rules whose
// ruleset is an active branch ruleset is what makes the result a gate rather
// than a report.
//
// Reconstructing this from ruleset documents alone was tried and was wrong: an
// organization ruleset is a 404 on the repository endpoint, and every module in
// this estate inherits one.
func enforcedContexts(
	ctx context.Context,
	reader *githubprovider.Client,
	gitmodulesName string,
	owner string,
	name string,
) ([]string, []domain.Finding) {
	notProven := func(message string, extra map[string]any) []domain.Finding {
		evidence := map[string]any{
			"gitmodules_name": gitmodulesName, "owner": owner, "repository": name,
		}
		for key, value := range extra {
			evidence[key] = value
		}
		return []domain.Finding{{
			Code: "GDS_MODULE_COVERAGE_NOT_PROVEN", Severity: domain.SeverityHigh,
			Message: message, Evidence: evidence,
		}}
	}

	repository, _, _, err := reader.GetRepository(ctx, owner, name, "")
	if err != nil {
		return nil, notProven(
			"A declared module's repository could not be read, so its default branch is unknown.", nil,
		)
	}
	summaries, _, err := reader.ListRepositoryRulesets(ctx, owner, name)
	if err != nil {
		return nil, notProven(
			"A declared module's rulesets could not be read, so its gate is unknown.", nil,
		)
	}
	blocking := map[int64]struct{}{}
	for _, summary := range summaries {
		if summary.Target == "branch" && summary.Enforcement == "active" {
			blocking[summary.ID] = struct{}{}
		}
	}
	rules, _, err := reader.ListBranchRules(ctx, owner, name, repository.DefaultBranch)
	if err != nil {
		return nil, notProven(
			"A declared module's effective branch rules could not be read, so its gate is unknown.",
			map[string]any{"branch": repository.DefaultBranch},
		)
	}
	contexts := []string{}
	for _, rule := range rules {
		if rule.Type != "required_status_checks" {
			continue
		}
		if _, enforced := blocking[rule.RulesetID]; !enforced {
			continue
		}
		for _, check := range rule.Parameters.RequiredStatusChecks {
			contexts = append(contexts, check.Context)
		}
	}
	return contexts, nil
}
