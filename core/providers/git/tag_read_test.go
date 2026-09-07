package git

import (
	"context"
	"testing"
)

func TestObserveVersionArtifactReadsLightweightAndAnnotatedTags(t *testing.T) {
	for _, annotated := range []bool{false, true} {
		t.Run(map[bool]string{false: "lightweight", true: "annotated"}[annotated], func(t *testing.T) {
			fixture := fastForwardFixture(t)
			runner, err := NewMutationRunner()
			if err != nil {
				t.Fatal(err)
			}
			args := []string{"tag", "v1.2.3", fixture.firstOID}
			if annotated {
				args = []string{"tag", "-a", "v1.2.3", fixture.firstOID, "-m", "version artifact"}
			}
			runFetchGit(t, fixture.client, args...)
			runFetchGit(t, fixture.client, "push", "-q", "origin", "refs/tags/v1.2.3")
			artifact, err := runner.ObserveVersionArtifact(context.Background(), fixture.client, "refs/tags/v1.2.3")
			if err != nil || artifact.CommitOID != fixture.firstOID || artifact.TagRef != "refs/tags/v1.2.3" {
				t.Fatalf("artifact=%#v err=%v", artifact, err)
			}
			if annotated == (artifact.TagOID == artifact.CommitOID) {
				t.Fatalf("tag object was not preserved: %#v", artifact)
			}
			runFetchGit(t, fixture.client, "tag", "-d", "v1.2.3")
			if _, err := runner.ObserveVersionArtifact(context.Background(), fixture.client, "refs/tags/v1.2.3"); err == nil {
				t.Fatal("unmaterialized tag accepted")
			}
		})
	}
}
