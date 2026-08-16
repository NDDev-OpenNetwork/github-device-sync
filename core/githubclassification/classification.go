// Package githubclassification reconciles repository custom-property values.
package githubclassification

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
)

const Action = "github-custom-properties"

type Reader interface {
	GetCustomPropertyValues(context.Context, string, string) ([]githubprovider.CustomPropertyValue, githubprovider.ResponseMeta, error)
}

type Writer interface {
	Scope() githubprovider.RepositoryMutationScope
	SetCustomProperties(context.Context, []githubprovider.CustomPropertyValue) (githubprovider.MutationMeta, error)
}

type Scope struct {
	ReadInstallationID   string `json:"read_installation_id"`
	MutationCapabilityID string `json:"mutation_capability_id"`
	ProviderRepositoryID int64  `json:"provider_repository_id"`
	Owner                string `json:"owner"`
	Name                 string `json:"name"`
}

type Parameters struct {
	Scope    Scope                                `json:"scope"`
	Expected []githubprovider.CustomPropertyValue `json:"expected"`
	Desired  []githubprovider.CustomPropertyValue `json:"desired"`
}

type Evidence struct {
	Values     []githubprovider.CustomPropertyValue `json:"values"`
	Digest     string                               `json:"digest"`
	Mutation   *githubprovider.MutationMeta         `json:"mutation,omitempty"`
	Idempotent bool                                 `json:"idempotent"`
}

type Handler struct {
	Reader Reader
	Writer Writer
	Scope  githubprovider.RepositoryMutationScope
}

func OperationParameters(value Parameters) map[string]any {
	return map[string]any{"github_classification": value}
}

func StepParameters(step operations.Step) (Parameters, error) {
	if len(step.Parameters) != 1 {
		return Parameters{}, errors.New("GitHub classification step must contain one parameter domain")
	}
	raw, found := step.Parameters["github_classification"]
	if !found {
		return Parameters{}, errors.New("github_classification parameters are missing")
	}
	payload, err := json.Marshal(raw)
	if err != nil {
		return Parameters{}, fmt.Errorf("encode GitHub classification parameters: %w", err)
	}
	var value Parameters
	if err := json.Unmarshal(payload, &value); err != nil {
		return Parameters{}, fmt.Errorf("decode GitHub classification parameters: %w", err)
	}
	value.Expected, err = normalizeValues(value.Expected)
	if err != nil {
		return Parameters{}, err
	}
	value.Desired, err = normalizeValues(value.Desired)
	if err != nil {
		return Parameters{}, err
	}
	if step.Action != Action || value.Scope.ReadInstallationID == "" ||
		value.Scope.MutationCapabilityID == "" || value.Scope.ProviderRepositoryID <= 0 ||
		value.Scope.Owner == "" || value.Scope.Name == "" || reflect.DeepEqual(value.Expected, value.Desired) {
		return Parameters{}, errors.New("GitHub classification parameters are invalid")
	}
	return value, nil
}

func (handler *Handler) Apply(ctx context.Context, step operations.Step) (operations.ApplyEvidence, error) {
	parameters, err := StepParameters(step)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	if err := handler.validateBinding(parameters.Scope, true); err != nil {
		return operations.ApplyEvidence{}, err
	}
	before, err := handler.observe(ctx, parameters.Scope)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	beforeEvidence, err := evidence(before, nil, false)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	if reflect.DeepEqual(before, parameters.Desired) {
		beforeEvidence.Idempotent = true
		return operations.ApplyEvidence{Before: beforeEvidence, After: beforeEvidence}, nil
	}
	if !reflect.DeepEqual(before, parameters.Expected) {
		return operations.ApplyEvidence{Before: beforeEvidence}, errors.New(
			"GitHub custom-property state changed after planning; mutation was not attempted",
		)
	}
	updates := propertyUpdates(parameters.Expected, parameters.Desired)
	meta, err := handler.Writer.SetCustomProperties(ctx, updates)
	if err != nil {
		return operations.ApplyEvidence{Before: beforeEvidence}, err
	}
	after, err := handler.observe(ctx, parameters.Scope)
	if err != nil || !reflect.DeepEqual(after, parameters.Desired) {
		return operations.ApplyEvidence{Before: beforeEvidence}, errors.New(
			"GitHub custom-property mutation completed without the exact planned state",
		)
	}
	afterEvidence, err := evidence(after, &meta, false)
	if err != nil {
		return operations.ApplyEvidence{Before: beforeEvidence}, err
	}
	return operations.ApplyEvidence{Before: beforeEvidence, After: afterEvidence}, nil
}

func (handler *Handler) Verify(ctx context.Context, step operations.Step, recorded json.RawMessage) error {
	parameters, err := StepParameters(step)
	if err != nil {
		return err
	}
	if err := handler.validateBinding(parameters.Scope, false); err != nil {
		return err
	}
	var recordedEvidence Evidence
	if err := json.Unmarshal(recorded, &recordedEvidence); err != nil {
		return fmt.Errorf("decode recorded GitHub classification evidence: %w", err)
	}
	recordedEvidence.Values, err = normalizeValues(recordedEvidence.Values)
	if err != nil || !reflect.DeepEqual(recordedEvidence.Values, parameters.Desired) {
		return errors.New("recorded GitHub custom-property evidence is invalid")
	}
	digest, err := canonicaljson.Digest(recordedEvidence.Values)
	if err != nil || digest != recordedEvidence.Digest {
		return errors.New("recorded GitHub custom-property digest is invalid")
	}
	current, err := handler.observe(ctx, parameters.Scope)
	if err != nil || !reflect.DeepEqual(current, parameters.Desired) {
		return errors.New("current GitHub custom-property state differs from the planned state")
	}
	return nil
}

func (handler *Handler) observe(ctx context.Context, scope Scope) ([]githubprovider.CustomPropertyValue, error) {
	values, _, err := handler.Reader.GetCustomPropertyValues(ctx, scope.Owner, scope.Name)
	if err != nil {
		return nil, err
	}
	return normalizeValues(values)
}

func (handler *Handler) validateBinding(scope Scope, requireWriter bool) error {
	if handler == nil || handler.Reader == nil {
		return errors.New("GitHub classification handler binding is incomplete")
	}
	bound := handler.Scope
	if handler.Writer != nil {
		writerScope := handler.Writer.Scope()
		if bound.RepositoryID != 0 && !reflect.DeepEqual(bound, writerScope) {
			return errors.New("GitHub classification handler and writer scopes differ")
		}
		bound = writerScope
	} else if requireWriter {
		return errors.New("GitHub classification mutation writer is unavailable")
	}
	if bound.RepositoryID != scope.ProviderRepositoryID ||
		!strings.EqualFold(bound.Owner, scope.Owner) || !strings.EqualFold(bound.Name, scope.Name) {
		return errors.New("GitHub classification writer identity differs from the immutable plan")
	}
	return nil
}

func normalizeValues(values []githubprovider.CustomPropertyValue) ([]githubprovider.CustomPropertyValue, error) {
	if len(values) > 100 {
		return nil, errors.New("GitHub custom-property value count exceeds the contract")
	}
	result := make([]githubprovider.CustomPropertyValue, len(values))
	seen := map[string]struct{}{}
	for index, value := range values {
		if !githubprovider.ValidCustomPropertyName(value.Name) {
			return nil, errors.New("GitHub custom-property name is invalid")
		}
		if _, duplicate := seen[value.Name]; duplicate {
			return nil, errors.New("GitHub custom-property values contain duplicates")
		}
		seen[value.Name] = struct{}{}
		normalized, err := normalizeValue(value.Value)
		if err != nil {
			return nil, err
		}
		result[index] = githubprovider.CustomPropertyValue{Name: value.Name, Value: normalized}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Name < result[right].Name })
	return result, nil
}

func normalizeValue(value any) (any, error) {
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case string:
		if !githubprovider.ValidCustomPropertyValue(typed) {
			return nil, errors.New("GitHub custom-property string value is invalid")
		}
		return typed, nil
	case []string:
		return normalizeStringList(typed)
	case []any:
		values := make([]string, len(typed))
		for index, item := range typed {
			stringValue, ok := item.(string)
			if !ok {
				return nil, errors.New("GitHub custom-property multi-select value is invalid")
			}
			values[index] = stringValue
		}
		return normalizeStringList(values)
	default:
		return nil, errors.New("GitHub custom-property value type is invalid")
	}
}

func normalizeStringList(values []string) ([]string, error) {
	if len(values) > 200 {
		return nil, errors.New("GitHub custom-property multi-select value is too large")
	}
	result := append([]string(nil), values...)
	seen := map[string]struct{}{}
	for _, value := range result {
		if value == "" || !githubprovider.ValidCustomPropertyValue(value) {
			return nil, errors.New("GitHub custom-property multi-select item is invalid")
		}
		if _, duplicate := seen[value]; duplicate {
			return nil, errors.New("GitHub custom-property multi-select items contain duplicates")
		}
		seen[value] = struct{}{}
	}
	sort.Strings(result)
	return result, nil
}

func propertyUpdates(expected, desired []githubprovider.CustomPropertyValue) []githubprovider.CustomPropertyValue {
	expectedByName := make(map[string]any, len(expected))
	desiredByName := make(map[string]any, len(desired))
	for _, value := range expected {
		expectedByName[value.Name] = value.Value
	}
	for _, value := range desired {
		desiredByName[value.Name] = value.Value
	}
	names := make([]string, 0, len(expectedByName)+len(desiredByName))
	seen := map[string]struct{}{}
	for name := range expectedByName {
		names = append(names, name)
		seen[name] = struct{}{}
	}
	for name := range desiredByName {
		if _, found := seen[name]; !found {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	updates := make([]githubprovider.CustomPropertyValue, 0, len(names))
	for _, name := range names {
		expectedValue, expectedFound := expectedByName[name]
		desiredValue, desiredFound := desiredByName[name]
		if expectedFound == desiredFound && reflect.DeepEqual(expectedValue, desiredValue) {
			continue
		}
		if !desiredFound {
			desiredValue = nil
		}
		updates = append(updates, githubprovider.CustomPropertyValue{Name: name, Value: desiredValue})
	}
	return updates
}

func evidence(values []githubprovider.CustomPropertyValue, meta *githubprovider.MutationMeta, idempotent bool) (Evidence, error) {
	digest, err := canonicaljson.Digest(values)
	return Evidence{Values: values, Digest: digest, Mutation: meta, Idempotent: idempotent}, err
}
