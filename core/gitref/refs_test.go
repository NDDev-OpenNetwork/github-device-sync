package gitref

import "testing"

func TestValidBranchNameMatchesBoundedGitBranchContract(t *testing.T) {
	t.Parallel()
	valid := []string{
		"main", "feature/principal-audit", "release/v1.2.3", "topic/-child", "a_b.c-d",
	}
	for _, value := range valid {
		if !ValidBranchName(value) {
			t.Errorf("valid branch rejected: %q", value)
		}
	}
	invalid := []string{
		"", "HEAD", "-option", ".hidden", "topic/.hidden", "topic/", "topic.",
		"topic..next", "topic//next", "topic@{next", "topic.lock", "topic/child.lock",
		"topic name", "topic~next", "topic\\next", "topic:next", "тема",
	}
	for _, value := range invalid {
		if ValidBranchName(value) {
			t.Errorf("invalid branch accepted: %q", value)
		}
	}
}

func TestValidateLocalBranchRefRequiresQualifiedSafeBranch(t *testing.T) {
	t.Parallel()
	if err := ValidateLocalBranchRef("refs/heads/task/valid"); err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{
		"task/valid", "refs/tags/task", "refs/heads/HEAD", "refs/heads/-option",
		"refs/heads/task/.hidden",
	} {
		if err := ValidateLocalBranchRef(value); err == nil {
			t.Errorf("invalid local branch ref accepted: %q", value)
		}
	}
}
