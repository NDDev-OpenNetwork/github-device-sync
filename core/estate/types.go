// Package estate loads and compiles private desired estate configuration.
package estate

type Config struct {
	Root          Root
	Installations []Installation
	Mutations     []MutationCapability
	Owners        []Owner
	Selectors     []Selector
}

type Root struct {
	SchemaVersion        int       `json:"schema_version"`
	Estate               Estate    `json:"estate"`
	Installations        []string  `json:"installations"`
	MutationCapabilities []string  `json:"mutation_capabilities"`
	PolicyOrder          []string  `json:"policy_order"`
	Rollout              Rollout   `json:"rollout"`
	State                State     `json:"state"`
	Discovery            Discovery `json:"discovery"`
}

type MutationCapability struct {
	SchemaVersion int                     `json:"schema_version"`
	Mutation      MutationIdentity        `json:"mutation"`
	Scope         MutationScope           `json:"scope"`
	Operations    []string                `json:"operations"`
	Permissions   InstallationPermissions `json:"permissions"`
	Gates         MutationGates           `json:"gates"`
	Credentials   InstallationCredentials `json:"credentials"`
}

type MutationIdentity struct {
	ID           string `json:"id"`
	Installation string `json:"installation"`
	Provider     string `json:"provider"`
}

type MutationScope struct {
	ManagementModes []string `json:"management_modes"`
	Lifecycles      []string `json:"lifecycles"`
}

type MutationGates struct {
	AutoMerge     string `json:"auto_merge"`
	Delete        string `json:"delete"`
	Force         string `json:"force"`
	Permissions   string `json:"permissions"`
	RulesetBypass string `json:"ruleset_bypass"`
	Visibility    string `json:"visibility"`
}

type Estate struct {
	ID                   string `json:"id"`
	Name                 string `json:"name"`
	DefaultBundleChannel string `json:"default_bundle_channel"`
}

type Rollout struct {
	DefaultRing            string `json:"default_ring"`
	MutationMode           string `json:"mutation_mode"`
	MaxParallelObservation int    `json:"max_parallel_observation"`
	MaxParallelGitNetwork  int    `json:"max_parallel_git_network"`
	MaxParallelMutation    int    `json:"max_parallel_mutation"`
}

type State struct {
	LocalBackend string `json:"local_backend"`
	XDGNamespace string `json:"xdg_namespace"`
}

type Discovery struct {
	DiscoverAllRepositories bool   `json:"discover_all_repositories"`
	DefaultManagementMode   string `json:"default_management_mode"`
}

type Installation struct {
	SchemaVersion int                     `json:"schema_version"`
	Installation  InstallationIdentity    `json:"installation"`
	Management    InstallationManagement  `json:"management"`
	Permissions   InstallationPermissions `json:"permissions"`
	Credentials   InstallationCredentials `json:"credentials"`
}

type InstallationIdentity struct {
	ID                      string `json:"id"`
	Provider                string `json:"provider"`
	AccountType             string `json:"account_type"`
	AccountLogin            string `json:"account_login"`
	AppInstallationIDSource string `json:"app_installation_id_source"`
}

type InstallationManagement struct {
	DiscoverAllRepositories bool   `json:"discover_all_repositories"`
	DefaultMode             string `json:"default_mode"`
}

type InstallationPermissions struct {
	RepositorySelection string            `json:"repository_selection"`
	Repository          map[string]string `json:"repository"`
	Organization        map[string]string `json:"organization"`
}

type InstallationCredentials struct {
	Strategy  string `json:"strategy"`
	SecretRef string `json:"secret_ref"`
}

type Owner struct {
	SchemaVersion  int                 `json:"schema_version"`
	Owner          OwnerIdentity       `json:"owner"`
	Defaults       OwnerDefaults       `json:"defaults"`
	Classification OwnerClassification `json:"classification"`
}

type OwnerIdentity struct {
	ID            string `json:"id"`
	Installation  string `json:"installation"`
	ProviderLogin string `json:"provider_login"`
}

type OwnerDefaults struct {
	PolicyProfile string `json:"policy_profile"`
	RolloutRing   string `json:"rollout_ring"`
}

type OwnerClassification struct {
	ForkPortfolio   string `json:"fork_portfolio"`
	SourcePortfolio string `json:"source_portfolio"`
}

type Selector struct {
	SchemaVersion int                `json:"schema_version"`
	Selector      SelectorIdentity   `json:"selector"`
	Match         SelectorMatch      `json:"match"`
	Assign        SelectorAssignment `json:"assign"`
}

type SelectorIdentity struct {
	ID       string `json:"id"`
	Priority int    `json:"priority"`
}

type SelectorMatch struct {
	Owner        string   `json:"owner"`
	NamePrefixes []string `json:"name_prefixes,omitempty"`
	Fork         *bool    `json:"fork,omitempty"`
	Archived     *bool    `json:"archived,omitempty"`
	Visibility   []string `json:"visibility,omitempty"`
}

type SelectorAssignment struct {
	ManagementMode string   `json:"management_mode"`
	Portfolios     []string `json:"portfolios"`
	PolicyProfiles []string `json:"policy_profiles"`
	RolloutRing    string   `json:"rollout_ring"`
}

type ObservedRepository struct {
	ProviderID    int64  `json:"provider_id"`
	Owner         string `json:"owner"`
	Name          string `json:"name"`
	Fork          bool   `json:"fork"`
	Archived      bool   `json:"archived"`
	Visibility    string `json:"visibility"`
	DefaultBranch string `json:"default_branch"`
}

type Assignment struct {
	ProviderID      int64    `json:"provider_id"`
	Owner           string   `json:"owner"`
	Name            string   `json:"name"`
	OwnerID         string   `json:"owner_id,omitempty"`
	InstallationID  string   `json:"installation_id,omitempty"`
	IdentityState   string   `json:"identity_state"`
	ManagementMode  string   `json:"management_mode"`
	Portfolios      []string `json:"portfolios"`
	PolicyProfiles  []string `json:"policy_profiles"`
	RolloutRing     string   `json:"rollout_ring"`
	MatchedSelector string   `json:"matched_selector,omitempty"`
}

type CompiledInventory struct {
	EstateID     string       `json:"estate_id"`
	Repositories []Assignment `json:"repositories"`
}
