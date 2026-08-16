package github

import "fmt"

// RulesetStage names the exact step of the default-branch ruleset upsert that
// refused a mutation.
//
// Every guard on this path used to return a bare error. A live apply that
// failed was therefore indistinguishable across seven different causes, and
// because none of them carried safe operation evidence, the operation journal
// recorded a failure with no stage at all whenever the refusal was local rather
// than a provider response. Naming the stage is what makes a real reconcile
// diagnosable without reproducing it against the provider.
type RulesetStage string

const (
	// RulesetStageContractValidation rejects a desired ruleset that violates the
	// typed contract before anything is observed or encoded.
	RulesetStageContractValidation RulesetStage = "contract_validation"
	// RulesetStageObservationBinding rejects an update whose desired identity
	// does not bind to the lossless observation it must preserve.
	RulesetStageObservationBinding RulesetStage = "observation_binding"
	// RulesetStageDesiredDecode covers decoding the preserved provider payload
	// that an update is required to carry through untouched.
	RulesetStageDesiredDecode RulesetStage = "desired_decode"
	// RulesetStageExternalFieldMerge covers replacing only the owned rule inside
	// the preserved payload while every external field survives.
	RulesetStageExternalFieldMerge RulesetStage = "external_field_merge"
	// RulesetStageRequestEncode covers building the provider request target.
	RulesetStageRequestEncode RulesetStage = "request_encode"
	// RulesetStageProviderRequest covers the mutation request itself.
	RulesetStageProviderRequest RulesetStage = "provider_request"
	// RulesetStageResponseDecode covers reading the provider response body.
	RulesetStageResponseDecode RulesetStage = "response_decode"
	// RulesetStagePostcondition covers proving the response describes exactly the
	// ruleset that was requested.
	RulesetStagePostcondition RulesetStage = "postcondition"
)

// RulesetStageError reports which stage of the ruleset upsert refused, using a
// stable reason code. Reason and Field are bounded, non-secret identifiers
// chosen from a closed set in this file; neither carries provider payload,
// credentials, or repository content.
type RulesetStageError struct {
	Stage  RulesetStage
	Reason string
	Field  string
	Cause  error
}

func (failure *RulesetStageError) Error() string {
	message := fmt.Sprintf("GitHub ruleset upsert refused at %s (%s)", failure.Stage, failure.Reason)
	if failure.Field != "" {
		message += ", field " + failure.Field
	}
	if failure.Cause != nil {
		message += ": " + failure.Cause.Error()
	}
	return message
}

func (failure *RulesetStageError) Unwrap() error { return failure.Cause }

// SafeProviderFailureCode lets a staged local refusal surface through the same
// detail_code channel an invalid provider response already uses.
func (failure *RulesetStageError) SafeProviderFailureCode() string {
	return string(failure.Stage) + "/" + failure.Reason
}

// SafeOperationFailureEvidence is what the operation engine persists. It
// deliberately excludes the wrapped cause text, because a decode error can
// quote the payload it failed to read.
func (failure *RulesetStageError) SafeOperationFailureEvidence() map[string]any {
	evidence := map[string]any{
		"provider": "github",
		"mutation": MutationRepositoryRuleset,
		"stage":    string(failure.Stage),
		"reason":   failure.Reason,
	}
	if failure.Field != "" {
		evidence["field"] = failure.Field
	}
	return evidence
}

func rulesetStageFailure(stage RulesetStage, reason string) error {
	return &RulesetStageError{Stage: stage, Reason: reason}
}

func rulesetStageFieldFailure(stage RulesetStage, reason string, field string) error {
	return &RulesetStageError{Stage: stage, Reason: reason, Field: field}
}

func rulesetStageCause(stage RulesetStage, reason string, cause error) error {
	return &RulesetStageError{Stage: stage, Reason: reason, Cause: cause}
}
