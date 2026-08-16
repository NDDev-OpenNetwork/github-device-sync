package github

import (
	"context"
	"fmt"
	"net/http"
	"regexp"
	"sort"
)

var customPropertyNamePattern = regexp.MustCompile(`^[A-Za-z0-9_#$-]{1,75}$`)

type CustomPropertyValue struct {
	Name  string `json:"property_name"`
	Value any    `json:"value"`
}

func (mutator *RepositoryMutator) SetCustomProperties(
	ctx context.Context,
	values []CustomPropertyValue,
) (MutationMeta, error) {
	if len(values) == 0 || len(values) > 100 {
		return MutationMeta{}, fmt.Errorf("GitHub custom property update count is invalid")
	}
	properties := make([]map[string]any, len(values))
	seen := map[string]struct{}{}
	for index, property := range values {
		if !ValidCustomPropertyName(property.Name) {
			return MutationMeta{}, fmt.Errorf("GitHub custom property name is invalid")
		}
		if _, duplicate := seen[property.Name]; duplicate {
			return MutationMeta{}, fmt.Errorf("GitHub custom property update contains duplicates")
		}
		seen[property.Name] = struct{}{}
		if !ValidCustomPropertyValue(property.Value) {
			return MutationMeta{}, fmt.Errorf("GitHub custom property value is invalid")
		}
		properties[index] = map[string]any{
			"property_name": property.Name, "value": property.Value,
		}
	}
	sort.Slice(properties, func(left, right int) bool {
		return properties[left]["property_name"].(string) < properties[right]["property_name"].(string)
	})
	target, err := mutator.endpoint("properties/values")
	if err != nil {
		return MutationMeta{}, err
	}
	response, meta, err := mutator.mutate(
		ctx, MutationCustomProperties, http.MethodPatch, target,
		map[string]any{"properties": properties},
	)
	if err != nil {
		return meta, err
	}
	if response.StatusCode != http.StatusNoContent || len(response.Body) != 0 {
		return meta, invalidMutationResponse(response, nil)
	}
	return meta, nil
}

// ValidCustomPropertyName implements GitHub's documented repository custom
// property name grammar.
func ValidCustomPropertyName(value string) bool {
	return customPropertyNamePattern.MatchString(value)
}

// ValidCustomPropertyValue validates null, string, and multi-select repository
// custom property values before they reach the provider boundary.
func ValidCustomPropertyValue(value any) bool {
	switch typed := value.(type) {
	case nil:
		return true
	case string:
		return validCustomPropertyText(typed)
	case []string:
		if len(typed) > 200 {
			return false
		}
		seen := map[string]struct{}{}
		for _, item := range typed {
			if item == "" || !validCustomPropertyText(item) {
				return false
			}
			if _, duplicate := seen[item]; duplicate {
				return false
			}
			seen[item] = struct{}{}
		}
		return true
	default:
		return false
	}
}

func validCustomPropertyText(value string) bool {
	if len(value) > 75 {
		return false
	}
	for index := 0; index < len(value); index++ {
		if value[index] < 0x20 || value[index] > 0x7e || value[index] == '"' {
			return false
		}
	}
	return true
}
