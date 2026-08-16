// Package assurance runs the bounded, offline GDS scale and recovery scenario
// used by the C10 acceptance gate.
package assurance

import "time"

const (
	DefaultRepositoryCount         = 2000
	DefaultForkCount               = 1000
	DefaultSharedModuleCount       = 4
	DefaultModuleConsumerCount     = 1000
	DefaultWebhookDeliveryCount    = 1000
	DefaultReconciliationWorkers   = 2
	DefaultProjectionWorkers       = 4
	DefaultContextSamples          = 20
	DefaultRepositoryStatusSamples = 20
)

type Options struct {
	Root                      string
	StateDirectory            string
	RepositoryCount           int
	ForkCount                 int
	SharedModuleCount         int
	ModuleConsumerCount       int
	WebhookDeliveryCount      int
	ReconciliationConcurrency int
	ProjectionConcurrency     int
	ContextSamples            int
	RepositoryStatusSamples   int
	RequireCleanWorktree      bool
}

type Report struct {
	SchemaVersion int         `json:"schema_version"`
	AssuranceID   string      `json:"assurance_id"`
	StartedAt     time.Time   `json:"started_at"`
	FinishedAt    time.Time   `json:"finished_at"`
	Environment   Environment `json:"environment"`
	Source        Source      `json:"source"`
	Scenario      Scenario    `json:"scenario"`
	Bounds        Bounds      `json:"bounds"`
	Checks        []Check     `json:"checks"`
	Metrics       []Metric    `json:"metrics"`
	Status        string      `json:"status"`
	ResultDigest  string      `json:"result_digest"`
}

type Environment struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	GoVersion    string `json:"go_version"`
	CPUCount     int    `json:"cpu_count"`
}

type Source struct {
	Commit        string `json:"commit"`
	WorktreeClean bool   `json:"worktree_clean"`
}

type Scenario struct {
	Repositories      int `json:"repositories"`
	Installations     int `json:"installations"`
	Forks             int `json:"forks"`
	SharedModules     int `json:"shared_modules"`
	ModuleConsumers   int `json:"module_consumers"`
	WebhookDeliveries int `json:"webhook_deliveries"`
	LifecycleClasses  int `json:"lifecycle_classes"`
	AccessStates      int `json:"access_states"`
}

type Bounds struct {
	ReconciliationConcurrency int  `json:"reconciliation_concurrency"`
	ProjectionConcurrency     int  `json:"projection_concurrency"`
	MaxRepositories           int  `json:"max_repositories"`
	RequireCleanWorktree      bool `json:"require_clean_worktree"`
	ExternalNetwork           bool `json:"external_network"`
	ExternalMutations         bool `json:"external_mutations"`
}

type Check struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Summary string `json:"summary"`
}

type Metric struct {
	ID         string  `json:"id"`
	Unit       string  `json:"unit"`
	Comparison string  `json:"comparison"`
	Observed   float64 `json:"observed"`
	Limit      float64 `json:"limit"`
	Passed     bool    `json:"passed"`
}

type budget struct {
	Unit       string
	Comparison string
	Limit      float64
	Evidence   budgetEvidence
}

type budgetEvidence string

const (
	budgetEvidenceWallClock budgetEvidence = "wall-clock"
	budgetEvidenceStable    budgetEvidence = "stable"
)

var budgets = map[string]budget{
	"context-p95-ms":                         {Unit: "milliseconds", Comparison: "at-most", Limit: 2000, Evidence: budgetEvidenceWallClock},
	"repository-status-p95-ms":               {Unit: "milliseconds", Comparison: "at-most", Limit: 2000, Evidence: budgetEvidenceWallClock},
	"inventory-compile-ms":                   {Unit: "milliseconds", Comparison: "at-most", Limit: 5000, Evidence: budgetEvidenceWallClock},
	"reconciliation-ms":                      {Unit: "milliseconds", Comparison: "at-most", Limit: 30000, Evidence: budgetEvidenceWallClock},
	"projection-generation-ms":               {Unit: "milliseconds", Comparison: "at-most", Limit: 60000, Evidence: budgetEvidenceWallClock},
	"webhook-throughput-per-second":          {Unit: "operations-per-second", Comparison: "at-least", Limit: 100, Evidence: budgetEvidenceWallClock},
	"queue-max-lag-ms":                       {Unit: "milliseconds", Comparison: "at-most", Limit: 30000, Evidence: budgetEvidenceWallClock},
	"restart-recovery-ms":                    {Unit: "milliseconds", Comparison: "at-most", Limit: 5000, Evidence: budgetEvidenceWallClock},
	"rollout-plan-ms":                        {Unit: "milliseconds", Comparison: "at-most", Limit: 2000, Evidence: budgetEvidenceWallClock},
	"portfolio-plan-ms":                      {Unit: "milliseconds", Comparison: "at-most", Limit: 5000, Evidence: budgetEvidenceWallClock},
	"peak-heap-bytes":                        {Unit: "bytes", Comparison: "at-most", Limit: 512 << 20, Evidence: budgetEvidenceStable},
	"state-db-bytes":                         {Unit: "bytes", Comparison: "at-most", Limit: 64 << 20, Evidence: budgetEvidenceStable},
	"api-read-calls-per-full-reconciliation": {Unit: "count", Comparison: "at-most", Limit: 5, Evidence: budgetEvidenceStable},
}

func metric(id string, observed float64) Metric {
	configured := budgets[id]
	passed := observed <= configured.Limit
	if configured.Comparison == "at-least" {
		passed = observed >= configured.Limit
	}
	return Metric{
		ID: id, Unit: configured.Unit, Comparison: configured.Comparison,
		Observed: observed, Limit: configured.Limit, Passed: passed,
	}
}
