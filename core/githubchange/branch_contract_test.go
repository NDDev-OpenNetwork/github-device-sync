package githubchange

import "testing"

func TestBranchNameUsesCanonicalContract(t *testing.T) {
	t.Parallel()
	if !branchName("task/valid") {
		t.Fatal("valid branch rejected")
	}
	for _, value := range []string{"-option", ".hidden", "task/.hidden", "task.lock"} {
		if branchName(value) {
			t.Errorf("invalid branch accepted: %q", value)
		}
	}
}
