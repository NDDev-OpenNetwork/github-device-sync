package domain

type RepositoryAnchor struct {
	SchemaVersion  int                      `json:"schema_version" yaml:"schema_version"`
	Repository     RepositoryIdentity       `json:"repository" yaml:"repository"`
	Provider       GitHubLocator            `json:"provider" yaml:"provider"`
	Classification RepositoryClassification `json:"classification" yaml:"classification"`
	// Product is what the repository is for, in the repository's own terms. It
	// exists because the agent projections carried scope, boundaries, safety and
	// done-criteria but never one sentence about what the code does, so an agent
	// arrived knowing what it was forbidden to do and nothing about the product.
	Product       *ProductFacts      `json:"product,omitempty" yaml:"product,omitempty"`
	Policy        RepositoryPolicy   `json:"policy" yaml:"policy"`
	Git           GitPolicy          `json:"git" yaml:"git"`
	CI            *CIPolicy          `json:"ci,omitempty" yaml:"ci,omitempty"`
	Verification  VerificationPolicy `json:"verification,omitempty" yaml:"verification,omitempty"`
	Agent         AgentPolicy        `json:"agent" yaml:"agent"`
	Relationships []Relationship     `json:"relationships,omitempty" yaml:"relationships,omitempty"`
	Module        *ModulePolicy      `json:"module,omitempty" yaml:"module,omitempty"`
	Fork          *ForkPolicy        `json:"fork,omitempty" yaml:"fork,omitempty"`
	Release       ReleasePolicy      `json:"release" yaml:"release"`
}

// ProductFacts is the product-first half of an agent brief: what this
// repository delivers, what it can do, and where a given class of change lives.
type ProductFacts struct {
	Purpose      string              `json:"purpose" yaml:"purpose"`
	Capabilities []string            `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`
	Entrypoints  []ProductEntrypoint `json:"entrypoints,omitempty" yaml:"entrypoints,omitempty"`
}

// ProductEntrypoint answers "where do I change X" without reading the tree.
type ProductEntrypoint struct {
	Change string `json:"change" yaml:"change"`
	Path   string `json:"path" yaml:"path"`
}

type CIPolicy struct {
	Profile        string `json:"profile" yaml:"profile"`
	GoVersion      string `json:"go_version,omitempty" yaml:"go_version,omitempty"`
	BuildCommand   string `json:"build_command,omitempty" yaml:"build_command,omitempty"`
	TestCommand    string `json:"test_command,omitempty" yaml:"test_command,omitempty"`
	TimeoutMinutes int    `json:"timeout_minutes,omitempty" yaml:"timeout_minutes,omitempty"`
	WorkflowRef    string `json:"workflow_ref,omitempty" yaml:"workflow_ref,omitempty"`
	// Runner is the label the generated caller passes to the reusable workflow.
	// It is declared rather than defaulted because runner choice is a visibility
	// decision: a self-hosted label on a public repository would let a fork's
	// pull request execute on estate hardware.
	Runner string `json:"runner,omitempty" yaml:"runner,omitempty"`
}

type RepositoryIdentity struct {
	ID          string   `json:"id" yaml:"id"`
	DisplayName string   `json:"display_name,omitempty" yaml:"display_name,omitempty"`
	Roles       []string `json:"roles" yaml:"roles"`
	Lifecycle   string   `json:"lifecycle" yaml:"lifecycle"`
}

type GitHubLocator struct {
	Type         string        `json:"type" yaml:"type"`
	Installation string        `json:"installation" yaml:"installation"`
	RepositoryID int64         `json:"repository_id" yaml:"repository_id"`
	Owner        string        `json:"owner" yaml:"owner"`
	Name         string        `json:"name" yaml:"name"`
	Aliases      []GitHubAlias `json:"aliases,omitempty" yaml:"aliases,omitempty"`
}

type GitHubAlias struct {
	Owner string `json:"owner" yaml:"owner"`
	Name  string `json:"name" yaml:"name"`
}

type RepositoryClassification struct {
	Portfolios         []string `json:"portfolios" yaml:"portfolios"`
	VisibilityContract string   `json:"visibility_contract" yaml:"visibility_contract"`
	DataClassification string   `json:"data_classification" yaml:"data_classification"`
}

type RepositoryPolicy struct {
	Profiles    []string `json:"profiles" yaml:"profiles"`
	RolloutRing string   `json:"rollout_ring" yaml:"rollout_ring"`
}

type GitPolicy struct {
	DefaultBranch string `json:"default_branch" yaml:"default_branch"`
	Integration   string `json:"integration" yaml:"integration"`
	BranchModel   string `json:"branch_model" yaml:"branch_model"`
	HandoffPR     string `json:"handoff_pr" yaml:"handoff_pr"`
	Cleanup       string `json:"cleanup" yaml:"cleanup"`
}

type VerificationPolicy struct {
	Commands VerificationCommands `json:"commands,omitempty" yaml:"commands,omitempty"`
	Required []string             `json:"required,omitempty" yaml:"required,omitempty"`
	// RequiredContexts is the set of status check contexts the repository's
	// protected branch enforces, as the anchor claims them.
	//
	// It is a separate vocabulary from Commands on purpose. A required context
	// is a check run name -- "govulncheck", "ci-gate", "GDS fast / go (1.26.7)"
	// -- and not a command, so no derivation connects the two. Stating the set
	// is what makes it comparable with what the provider actually enforces; the
	// alternative, inferring a gate from the commands beside it, would produce a
	// confident answer with nothing behind it.
	RequiredContexts []string `json:"required_contexts,omitempty" yaml:"required_contexts,omitempty"`
}

type VerificationCommands struct {
	Bootstrap     []string `json:"bootstrap,omitempty" yaml:"bootstrap,omitempty"`
	Lint          []string `json:"lint,omitempty" yaml:"lint,omitempty"`
	Typecheck     []string `json:"typecheck,omitempty" yaml:"typecheck,omitempty"`
	Test          []string `json:"test,omitempty" yaml:"test,omitempty"`
	Build         []string `json:"build,omitempty" yaml:"build,omitempty"`
	Compatibility []string `json:"compatibility,omitempty" yaml:"compatibility,omitempty"`
	Package       []string `json:"package,omitempty" yaml:"package,omitempty"`
	Fast          []string `json:"fast,omitempty" yaml:"fast,omitempty"`
	PRRequired    []string `json:"pr-required,omitempty" yaml:"pr-required,omitempty"`
	Full          []string `json:"full,omitempty" yaml:"full,omitempty"`
	Release       []string `json:"release,omitempty" yaml:"release,omitempty"`
}

type AgentPolicy struct {
	ContextProfile  string       `json:"context_profile" yaml:"context_profile"`
	GeneratedAgents bool         `json:"generated_agents" yaml:"generated_agents"`
	Serena          SerenaPolicy `json:"serena" yaml:"serena"`
}

type SerenaPolicy struct {
	Enabled            bool `json:"enabled" yaml:"enabled"`
	ProvenanceRequired bool `json:"provenance_required" yaml:"provenance_required"`
}

type Relationship struct {
	Type            string `json:"type" yaml:"type"`
	Target          string `json:"target" yaml:"target"`
	GitmodulesName  string `json:"gitmodules_name,omitempty" yaml:"gitmodules_name,omitempty"`
	PinManagement   string `json:"pin_management,omitempty" yaml:"pin_management,omitempty"`
	Materialization string `json:"materialization,omitempty" yaml:"materialization,omitempty"`
}

type ModulePolicy struct {
	Contract      string            `json:"contract" yaml:"contract"`
	Consumption   []string          `json:"consumption" yaml:"consumption"`
	Compatibility string            `json:"compatibility" yaml:"compatibility"`
	PinPolicy     string            `json:"pin_policy" yaml:"pin_policy"`
	Publication   ModulePublication `json:"publication" yaml:"publication"`
}

type ModulePublication struct {
	Registry      string `json:"registry,omitempty" yaml:"registry,omitempty"`
	GitHubRelease string `json:"github_release" yaml:"github_release"`
}

type ForkPolicy struct {
	Upstream            ForkUpstream `json:"upstream" yaml:"upstream"`
	Policy              string       `json:"policy" yaml:"policy"`
	SyncBranch          string       `json:"sync_branch" yaml:"sync_branch"`
	PreserveForkCommits bool         `json:"preserve_fork_commits" yaml:"preserve_fork_commits"`
	AllowForceSync      bool         `json:"allow_force_sync" yaml:"allow_force_sync"`
}

type ForkUpstream struct {
	Provider     string `json:"provider" yaml:"provider"`
	RepositoryID int64  `json:"repository_id" yaml:"repository_id"`
	Owner        string `json:"owner" yaml:"owner"`
	Name         string `json:"name" yaml:"name"`
}

type ReleasePolicy struct {
	Mode     string `json:"mode" yaml:"mode"`
	TagStyle string `json:"tag_style,omitempty" yaml:"tag_style,omitempty"`
	// RequiredChecks names the provider check-run contexts that must have one
	// trusted exact-commit success before this repository may be released.
	//
	// These are provider check names such as "ci-gate", a different namespace from
	// VerificationPolicy.Required, which names local command lanes ("test",
	// "lint"). Conflating the two silently gates a release on a check name that
	// can never exist. An empty list means the release is not gated on provider
	// checks; it is then gated on signed harness evidence where a module bridge
	// declares one, which is where a private module's QA actually lives.
	RequiredChecks []string `json:"required_checks,omitempty" yaml:"required_checks,omitempty"`
}
