package github

import (
	"strings"
	"testing"
)

func TestCustomPropertyContractMatchesGitHubGrammar(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"role", "_owner", "$portfolio", "#tier", "gds-role"} {
		if !ValidCustomPropertyName(name) {
			t.Errorf("valid custom-property name rejected: %q", name)
		}
	}
	for _, name := range []string{"", "gds.role", "role/name", "role name", strings.Repeat("a", 76)} {
		if ValidCustomPropertyName(name) {
			t.Errorf("invalid custom-property name accepted: %q", name)
		}
	}

	valid75 := strings.Repeat("a", 75)
	for _, value := range []any{
		nil, "", "printable !#$%&'()*+,-./:;<=>?@[\\]^_`{|}~", valid75,
		[]string{"one", "two"}, []string{},
	} {
		if !ValidCustomPropertyValue(value) {
			t.Errorf("valid custom-property value rejected: %#v", value)
		}
	}
	for _, value := range []any{
		strings.Repeat("a", 76), "contains\"quote", "tab\tvalue", "line\nvalue", "café",
		[]string{""}, []string{"duplicate", "duplicate"}, []string{"contains\"quote"},
	} {
		if ValidCustomPropertyValue(value) {
			t.Errorf("invalid custom-property value accepted: %#v", value)
		}
	}
}
