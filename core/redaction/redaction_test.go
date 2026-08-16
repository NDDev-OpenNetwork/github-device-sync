package redaction

import (
	"strings"
	"testing"
)

func TestStringRedactsCredentialFormsWithoutDestroyingContext(t *testing.T) {
	githubToken := "ghp_" + "1234567890abcdefghijklmnopqrstuv"
	input := strings.Join([]string{
		"fatal: https://user:password@example.test/repo",
		"Authorization" + ": Bearer " + "private-value",
		"token=private-query&ref=main",
		`{"password":"private-json"}`,
		githubToken,
	}, "\n")
	result := String(input)
	for _, secret := range []string{
		"user:password", "private-value", "private-query", "private-json", githubToken,
	} {
		if strings.Contains(result, secret) {
			t.Fatalf("secret %q remained in %q", secret, result)
		}
	}
	if !strings.Contains(result, "example.test/repo") || !strings.Contains(result, "ref=main") {
		t.Fatalf("diagnostic context was lost: %q", result)
	}
}
