// Package domain defines portable GDS values without CLI or provider coupling.
package domain

import "fmt"

const SchemaVersion = 1

type ExitClass string

const (
	ExitSuccess           ExitClass = "success"
	ExitValidation        ExitClass = "validation"
	ExitNotProven         ExitClass = "not-proven"
	ExitInput             ExitClass = "input"
	ExitStale             ExitClass = "stale"
	ExitApproval          ExitClass = "approval"
	ExitAuthorization     ExitClass = "authorization"
	ExitConflict          ExitClass = "conflict"
	ExitPolicy            ExitClass = "policy"
	ExitPartial           ExitClass = "partial"
	ExitProviderTransient ExitClass = "provider-transient"
	ExitSecurity          ExitClass = "security"
	ExitUnsupported       ExitClass = "unsupported"
	ExitInternal          ExitClass = "internal"
)

var exitCodes = map[ExitClass]int{
	ExitSuccess:           0,
	ExitValidation:        2,
	ExitNotProven:         3,
	ExitInput:             4,
	ExitStale:             5,
	ExitApproval:          6,
	ExitAuthorization:     7,
	ExitConflict:          8,
	ExitPolicy:            9,
	ExitPartial:           10,
	ExitProviderTransient: 11,
	ExitSecurity:          12,
	ExitUnsupported:       13,
	ExitInternal:          14,
}

type Severity string

const (
	SeverityCritical Severity = "critical"
	SeverityHigh     Severity = "high"
	SeverityMedium   Severity = "medium"
	SeverityLow      Severity = "low"
	SeverityInfo     Severity = "info"
)

type Finding struct {
	Code        string         `json:"code"`
	Severity    Severity       `json:"severity"`
	Message     string         `json:"message"`
	Evidence    map[string]any `json:"evidence,omitempty"`
	Remediation *Remediation   `json:"remediation,omitempty"`
}

type Remediation struct {
	Command string `json:"command,omitempty"`
}

type Mutation struct {
	Attempted bool `json:"attempted"`
	Completed bool `json:"completed"`
}

type Envelope struct {
	SchemaVersion int            `json:"schema_version"`
	Command       string         `json:"command"`
	Result        string         `json:"result"`
	ExitClass     ExitClass      `json:"exit_class"`
	ExitCode      int            `json:"exit_code"`
	OperationID   string         `json:"operation_id,omitempty"`
	Scope         map[string]any `json:"scope"`
	Data          any            `json:"data,omitempty"`
	Findings      []Finding      `json:"findings"`
	Mutation      Mutation       `json:"mutation"`
}

func NewEnvelope(command string, class ExitClass, data any, findings ...Finding) Envelope {
	code, ok := exitCodes[class]
	if !ok {
		class = ExitInternal
		code = exitCodes[class]
		findings = append(findings, Finding{
			Code:     "GDS_EXIT_CLASS_INVALID",
			Severity: SeverityCritical,
			Message:  "The command returned an unknown exit class.",
		})
	}
	result := "failed"
	switch class {
	case ExitSuccess:
		result = "succeeded"
	case ExitNotProven:
		result = "not-proven"
	case ExitApproval, ExitAuthorization, ExitConflict, ExitPolicy, ExitSecurity,
		ExitStale, ExitUnsupported:
		result = "blocked"
	case ExitPartial:
		result = "partial"
	}
	if findings == nil {
		findings = []Finding{}
	}
	return Envelope{
		SchemaVersion: SchemaVersion,
		Command:       command,
		Result:        result,
		ExitClass:     class,
		ExitCode:      code,
		Scope:         map[string]any{},
		Data:          data,
		Findings:      findings,
		Mutation:      Mutation{},
	}
}

func Success(command string, data any, findings ...Finding) Envelope {
	return NewEnvelope(command, ExitSuccess, data, findings...)
}

func InternalError(command string, err error) Envelope {
	return NewEnvelope(command, ExitInternal, nil, Finding{
		Code:     "GDS_INTERNAL_ERROR",
		Severity: SeverityCritical,
		Message:  fmt.Sprintf("Unhandled internal error: %v", err),
	})
}
