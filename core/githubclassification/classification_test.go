package githubclassification

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

type classificationFixture struct {
	values  []githubprovider.CustomPropertyValue
	scope   githubprovider.RepositoryMutationScope
	writes  int
	updates []githubprovider.CustomPropertyValue
}

func TestClassificationHandlerAppliesExactValuesAndUnsetsRemovedProperties(t *testing.T) {
	fixture := &classificationFixture{
		values: []githubprovider.CustomPropertyValue{
			{Name: "gds-lifecycle", Value: "legacy"},
			{Name: "gds-obsolete", Value: "remove"},
			{Name: "unrelated", Value: "preserve"},
		},
		scope: githubprovider.RepositoryMutationScope{
			RepositoryID: 42, Owner: "example", Name: "repository",
			Operations: []string{githubprovider.MutationCustomProperties},
		},
	}
	desired := []githubprovider.CustomPropertyValue{
		{Name: "gds-lifecycle", Value: "active"},
		{Name: "gds-roles", Value: []string{"module", "project"}},
		{Name: "unrelated", Value: "preserve"},
	}
	step := classificationStep(fixture.values, desired)
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)
	plan, err := operations.NewPlan(
		"plan_01KX7BV07RHD6KRA4Z4J0KCHGR", now, now.Add(15*time.Minute),
		operations.PlanInput{
			Operation: "reconcile-github-classification",
			Actor:     operations.Actor{Type: "agent-session", SessionID: "test-session"},
			Preconditions: []operations.Precondition{{
				RepositoryID: step.RepositoryID, HeadOID: strings.Repeat("a", 40),
				ManifestDigest: "sha256:" + strings.Repeat("a", 64),
				PolicyDigest:   "sha256:" + strings.Repeat("b", 64),
			}},
			Steps: []operations.Step{step}, ApprovalClass: "github-classification-write",
		},
	)
	if err != nil || len(plan.Validate(schemas)) != 0 {
		t.Fatalf("plan err=%v findings=%#v", err, plan.Validate(schemas))
	}
	handler := &Handler{Reader: fixture, Writer: fixture}
	result, err := handler.Apply(context.Background(), step)
	if err != nil || fixture.writes != 1 {
		t.Fatalf("result=%#v err=%v writes=%d", result, err, fixture.writes)
	}
	wantUpdates := []githubprovider.CustomPropertyValue{
		{Name: "gds-lifecycle", Value: "active"},
		{Name: "gds-obsolete", Value: nil},
		{Name: "gds-roles", Value: []string{"module", "project"}},
	}
	if !reflect.DeepEqual(fixture.updates, wantUpdates) {
		t.Fatalf("updates=%#v", fixture.updates)
	}
	raw, _ := json.Marshal(result.After)
	verify := &Handler{Reader: fixture, Scope: fixture.scope}
	if err := verify.Verify(context.Background(), step, raw); err != nil {
		t.Fatal(err)
	}
}

func TestClassificationHandlerBlocksStaleValues(t *testing.T) {
	fixture := &classificationFixture{
		values: []githubprovider.CustomPropertyValue{{Name: "gds-lifecycle", Value: "changed"}},
		scope: githubprovider.RepositoryMutationScope{
			RepositoryID: 42, Owner: "example", Name: "repository",
			Operations: []string{githubprovider.MutationCustomProperties},
		},
	}
	step := classificationStep(
		[]githubprovider.CustomPropertyValue{{Name: "gds-lifecycle", Value: "active"}},
		[]githubprovider.CustomPropertyValue{{Name: "gds-lifecycle", Value: "archived"}},
	)
	if _, err := (&Handler{Reader: fixture, Writer: fixture}).Apply(context.Background(), step); err == nil || fixture.writes != 0 {
		t.Fatalf("err=%v writes=%d", err, fixture.writes)
	}
}

func (fixture *classificationFixture) Scope() githubprovider.RepositoryMutationScope {
	return fixture.scope
}

func (fixture *classificationFixture) GetCustomPropertyValues(
	context.Context, string, string,
) ([]githubprovider.CustomPropertyValue, githubprovider.ResponseMeta, error) {
	values, err := normalizeValues(fixture.values)
	return values, githubprovider.ResponseMeta{}, err
}

func (fixture *classificationFixture) SetCustomProperties(
	_ context.Context, values []githubprovider.CustomPropertyValue,
) (githubprovider.MutationMeta, error) {
	fixture.updates = append([]githubprovider.CustomPropertyValue(nil), values...)
	current, _ := normalizeValues(fixture.values)
	byName := map[string]any{}
	for _, value := range current {
		byName[value.Name] = value.Value
	}
	for _, update := range values {
		if update.Value == nil {
			delete(byName, update.Name)
		} else {
			byName[update.Name] = update.Value
		}
	}
	fixture.values = make([]githubprovider.CustomPropertyValue, 0, len(byName))
	for name, value := range byName {
		fixture.values = append(fixture.values, githubprovider.CustomPropertyValue{Name: name, Value: value})
	}
	fixture.values, _ = normalizeValues(fixture.values)
	fixture.writes++
	return githubprovider.MutationMeta{RepositoryID: 42, StatusCode: 204}, nil
}

func classificationStep(expected, desired []githubprovider.CustomPropertyValue) operations.Step {
	return operations.Step{
		StepID: "set-classification", RepositoryID: "repo_01JEXAMPZ0000000000000000C",
		Action: Action, RequiresApproval: true,
		Compensation: operations.Compensation{Mode: "manual"},
		Parameters: OperationParameters(Parameters{
			Scope: Scope{
				ReadInstallationID: "installation:read", MutationCapabilityID: "mutation:write",
				ProviderRepositoryID: 42, Owner: "example", Name: "repository",
			},
			Expected: expected, Desired: desired,
		}),
	}
}

func (fixture *classificationFixture) String() string {
	return fmt.Sprintf("writes=%d values=%d", fixture.writes, len(fixture.values))
}
