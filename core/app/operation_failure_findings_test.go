package app

import (
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
)

// The envelope is the caller's only surface. An operation error that carries
// findings must put them there: keeping them inside the Go error means every
// consumer outside this process -- the CLI, an agent reading `--json`, CI --
// sees the summary and nothing else, which is the state that hid three stacked
// defects in `gds module update-pin`.
func TestOperationFailureEnvelopeCarriesTheUnderlyingFindings(t *testing.T) {
	operationError := &operations.Error{
		Code: "GDS_PLAN_INVALID", Class: domain.ExitValidation,
		Message: "Plan failed schema or semantic validation.",
		Findings: []domain.Finding{{
			Code: "GDS_INSTANCE_INVALID", Severity: domain.SeverityHigh,
			Message: "in-memory-plan violates plan: at '/steps/0/parameters/gitlink_pin/gitmodules_name': " +
				"'modules/agent-runtime' does not match pattern '^[A-Za-z0-9._-]+$'",
			Evidence: map[string]any{"schema": "plan"},
		}},
	}

	envelope := operationFailureEnvelope("gds module update-pin plan", operationError)

	if envelope.Result == "succeeded" {
		t.Fatal("a failed operation produced a success envelope")
	}
	if len(envelope.Findings) < 2 {
		t.Fatalf("envelope dropped the underlying findings: %+v", envelope.Findings)
	}
	// The summary keeps its position and its code, so callers that key on
	// GDS_PLAN_INVALID keep working.
	if envelope.Findings[0].Code != "GDS_PLAN_INVALID" {
		t.Fatalf("summary finding is not first: %+v", envelope.Findings)
	}
	joined := ""
	for _, finding := range envelope.Findings {
		joined += finding.Message + " | "
	}
	if !strings.Contains(joined, "gitmodules_name") ||
		!strings.Contains(joined, "does not match pattern") {
		t.Fatalf("envelope does not name the field or the rule: %s", joined)
	}
	var carried *domain.Finding
	for index := range envelope.Findings {
		if envelope.Findings[index].Code == "GDS_INSTANCE_INVALID" {
			carried = &envelope.Findings[index]
		}
	}
	if carried == nil || carried.Evidence == nil {
		t.Fatal("the carried finding lost its evidence")
	}
}

// An operation error without findings must not gain an empty one: the summary
// alone is the correct shape when nothing more is known.
func TestOperationFailureEnvelopeWithoutFindingsReportsOnlyTheSummary(t *testing.T) {
	envelope := operationFailureEnvelope("gds complete apply", &operations.Error{
		Code: "GDS_OPERATION_NOT_VERIFIABLE", Class: domain.ExitConflict,
		Message: "Operation is in \"failed\" state, not succeeded.",
	})
	if len(envelope.Findings) != 1 || envelope.Findings[0].Code != "GDS_OPERATION_NOT_VERIFIABLE" {
		t.Fatalf("unexpected findings: %+v", envelope.Findings)
	}
}
