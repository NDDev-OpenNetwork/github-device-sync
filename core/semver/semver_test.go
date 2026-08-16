package semver

import "testing"

func TestParseAndCompareSemVer(t *testing.T) {
	valid := []string{
		"0.0.0", "1.2.3", "1.2.3-alpha", "1.2.3-alpha.1", "1.2.3+build.7",
		"999999999999999999999.0.0",
	}
	for _, value := range valid {
		if !Valid(value) {
			t.Fatalf("valid SemVer rejected: %s", value)
		}
	}
	invalid := []string{"v1.2.3", "1.2", "01.2.3", "1.2.3-01", "1.2.3-", "1.2.3+"}
	for _, value := range invalid {
		if Valid(value) {
			t.Fatalf("invalid SemVer accepted: %s", value)
		}
	}
	ordered := []string{
		"1.0.0-alpha", "1.0.0-alpha.1", "1.0.0-alpha.beta", "1.0.0-beta",
		"1.0.0-beta.2", "1.0.0-beta.11", "1.0.0-rc.1", "1.0.0",
	}
	for index := 1; index < len(ordered); index++ {
		compared, valid := Compare(ordered[index-1], ordered[index])
		if !valid || compared >= 0 {
			t.Fatalf("SemVer order failed: %s < %s", ordered[index-1], ordered[index])
		}
	}
	if compared, valid := Compare("1.0.0+one", "1.0.0+two"); !valid || compared != 0 {
		t.Fatalf("build metadata changed precedence: %d %v", compared, valid)
	}
}
