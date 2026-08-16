package git

import (
	"context"
	"testing"
)

func sourceDigest(t *testing.T, runner *Runner, directory string, paths []string) string {
	t.Helper()
	digest, err := runner.SourceTreeDigest(context.Background(), directory, paths)
	if err != nil {
		t.Fatalf("SourceTreeDigest: %v", err)
	}
	return digest
}

// TestSourceTreeDigestIsContentAddressedNotCommitAddressed is the property that
// removes the self-reference: identical source bytes must produce an identical
// identity no matter which commit, or how many commits, carried them. A commit
// oracle cannot do this, which is why generation used to need a follow-up
// re-pin commit whose only job was to teach the first commit its own SHA.
func TestSourceTreeDigestIsContentAddressedNotCommitAddressed(t *testing.T) {
	if testing.Short() {
		t.Skip("uses the local git executable")
	}
	runner, err := NewRunner()
	if err != nil {
		t.Fatal(err)
	}
	paths := []string{"tracked.txt"}

	first := createCommittedRepository(t)
	firstDigest := sourceDigest(t, runner, first, paths)

	// A second repository with byte-identical sources but an unrelated history.
	second := createCommittedRepository(t)
	runGit(t, second, "commit", "--allow-empty", "-m", "unrelated history")
	secondDigest := sourceDigest(t, runner, second, paths)

	if firstDigest != secondDigest {
		t.Fatalf("identical sources produced different identities: %s vs %s", firstDigest, secondDigest)
	}

	firstHead, err := runner.HeadOID(context.Background(), first)
	if err != nil {
		t.Fatal(err)
	}
	secondHead, err := runner.HeadOID(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	if firstHead == secondHead {
		t.Fatal("the two repositories were expected to have different heads")
	}
}

// TestSourceTreeDigestBindsStagedSourcesBeforeTheyAreCommitted is what makes a
// single atomic commit possible: the generator binds to an edit that is staged
// but not yet committed, because committing first is exactly the step that
// forced the second commit.
func TestSourceTreeDigestBindsStagedSourcesBeforeTheyAreCommitted(t *testing.T) {
	if testing.Short() {
		t.Skip("uses the local git executable")
	}
	runner, err := NewRunner()
	if err != nil {
		t.Fatal(err)
	}
	directory := createCommittedRepository(t)
	paths := []string{"tracked.txt"}
	committed := sourceDigest(t, runner, directory, paths)

	writeFile(t, directory, "tracked.txt", "changed before any commit\n")
	runGit(t, directory, "add", "tracked.txt")
	staged := sourceDigest(t, runner, directory, paths)
	if staged == committed {
		t.Fatal("a staged source edit did not change the source identity")
	}

	// CommittedSourceOID refuses this state outright, which is the behaviour
	// that made generation a two-commit ceremony.
	if _, err := runner.CommittedSourceOID(context.Background(), directory, paths); err == nil {
		t.Fatal("CommittedSourceOID was expected to refuse an uncommitted source")
	}

	// Committing the staged content must not move the identity: the bytes did
	// not change, only the commit that carries them.
	runGit(t, directory, "commit", "-m", "carry the staged source")
	if afterCommit := sourceDigest(t, runner, directory, paths); afterCommit != staged {
		t.Fatalf("committing staged content moved the identity: %s vs %s", afterCommit, staged)
	}
}

// TestSourceTreeDigestHonoursTheDeclaredBoundary keeps the input set narrow:
// only declared paths count, and a shared name prefix is not membership.
func TestSourceTreeDigestHonoursTheDeclaredBoundary(t *testing.T) {
	if testing.Short() {
		t.Skip("uses the local git executable")
	}
	runner, err := NewRunner()
	if err != nil {
		t.Fatal(err)
	}
	directory := createCommittedRepository(t)
	paths := []string{"tracked.txt"}
	baseline := sourceDigest(t, runner, directory, paths)

	writeFile(t, directory, "unrelated.txt", "outside the declared boundary\n")
	runGit(t, directory, "add", "unrelated.txt")
	if outside := sourceDigest(t, runner, directory, paths); outside != baseline {
		t.Fatal("a file outside the declared source paths changed the identity")
	}
}

// TestDeclaredSourceBoundaryRejectsSiblingPrefixes pins the prefix rule, so a
// declared `core/app` never silently absorbs `core/apply`.
func TestDeclaredSourceBoundaryRejectsSiblingPrefixes(t *testing.T) {
	declared := []string{"core/app", "go.mod"}
	for _, testCase := range []struct {
		relative string
		within   bool
	}{
		{"core/app/services.go", true},
		{"core/app", true},
		{"go.mod", true},
		{"core/apply/services.go", false},
		{"core/application.go", false},
		{"go.module", false},
		{"core/domain/repository.go", false},
	} {
		if withinDeclaredSourcePaths(testCase.relative, declared) != testCase.within {
			t.Fatalf("%q membership = %v, want %v",
				testCase.relative, !testCase.within, testCase.within)
		}
	}
}
