package github

import "testing"

func TestSafeBranchNameUsesCanonicalContract(t *testing.T) {
	t.Parallel()
	if !safeBranchName("task/valid") {
		t.Fatal("valid branch rejected")
	}
	for _, value := range []string{"-option", ".hidden", "task/.hidden", "task.lock"} {
		if safeBranchName(value) {
			t.Errorf("invalid branch accepted: %q", value)
		}
	}
}
