package operations

import (
	"errors"
	"fmt"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
)

type Error struct {
	Code              string
	Class             domain.ExitClass
	Message           string
	OperationID       string
	MutationAttempted bool
	Findings          []domain.Finding
	Cause             error
}

func (operationError *Error) Error() string {
	if operationError.Cause == nil {
		return operationError.Message
	}
	return fmt.Sprintf("%s: %v", operationError.Message, operationError.Cause)
}

func (operationError *Error) Unwrap() error { return operationError.Cause }

func newError(code string, class domain.ExitClass, message string, cause error) *Error {
	return &Error{Code: code, Class: class, Message: message, Cause: cause}
}

// planValidationError carries the findings that proved a plan invalid.
//
// Reporting only their count discards the one thing that makes the refusal
// actionable. `plan.Validate` names the exact instance path and the exact
// violated constraint, and the caller cannot re-derive either: the plan it would
// have to inspect is the one that was just refused. Three independent defects
// stacked inside `gds module update-pin` behind "1 findings" -- each reachable
// only once the previous was gone, each found by rebuilding the plan in a
// throwaway program and calling Validate directly.
func planValidationError(code string, message string, findings []domain.Finding) *Error {
	operationError := newError(code, domain.ExitValidation, message, findingsCause(findings))
	operationError.Findings = findings
	return operationError
}

// findingsCause renders findings for `Error()`, which is what a caller that logs
// the error without unwrapping sees. The first finding is named in full because
// it is usually the one to fix; the rest are counted, so the string stays one
// line without pretending the others are not there.
func findingsCause(findings []domain.Finding) error {
	switch len(findings) {
	case 0:
		return nil
	case 1:
		return errors.New(findings[0].Message)
	default:
		return fmt.Errorf("%s (and %d more findings)", findings[0].Message, len(findings)-1)
	}
}
