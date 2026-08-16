package githubclassification

import (
	"strings"
	"testing"

	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
)

func TestNormalizeValuesUsesProviderCustomPropertyContract(t *testing.T) {
	t.Parallel()
	valid, err := normalizeValues([]githubprovider.CustomPropertyValue{{
		Name: "$tier#", Value: strings.Repeat("a", 75),
	}})
	if err != nil || len(valid) != 1 {
		t.Fatalf("valid values=%#v err=%v", valid, err)
	}
	for _, value := range []githubprovider.CustomPropertyValue{
		{Name: "tier.name", Value: "one"},
		{Name: "tier", Value: strings.Repeat("a", 76)},
		{Name: "tier", Value: "contains\"quote"},
		{Name: "tier", Value: "café"},
	} {
		if _, err := normalizeValues([]githubprovider.CustomPropertyValue{value}); err == nil {
			t.Errorf("invalid custom-property value accepted: %#v", value)
		}
	}
}
