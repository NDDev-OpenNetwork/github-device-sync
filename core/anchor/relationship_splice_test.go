package anchor

import (
	"fmt"
	"strings"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

// The anchor is a canonical source people write reasoning into. Onboarding the
// fourth module through EncodeCandidate deleted fourteen comment lines from it,
// including the whole record of why this repository routes CI to a self-hosted
// fleet while an open issue was still weighing exactly that. These tests hold
// the property that made the loss possible: a relationship change must move the
// relationship block and nothing else.

const authoredAnchor = `schema_version: 1

# The identity below is issued once and never regenerated.
repository:
  id: "repo_01JEXAMPZ0000000000000000C"
  display_name: "example"
  roles:
    - "control-plane"
    - "superproject"
  lifecycle: "active"

provider:
  type: "github"
  installation: "installation:github-organization"
  repository_id: 1000000001
  owner: "example-org"
  name: "example"

classification:
  portfolios:
    - "portfolio:estate-control-plane"
  visibility_contract: "private"
  data_classification: "private"

policy:
  profiles:
    - "repository-default"
  rollout_ring: "canary-control-plane"

git:
  default_branch: "main"
  integration: "pull-request"
  branch_model: "task-branches"
  handoff_pr: "preferred"
  cleanup: "merged-only"

ci:
  profile: "go"
  go_version: "1.26.5"
  build_command: "go build ./..."
  test_command: "go test ./..."
  timeout_minutes: 30
  workflow_ref: "example-org/ci-workflows/.github/workflows/go-ci.yml@4387c591651fc5477145eb02093bcf2f04432f79"
  # Self-hosted deliberately: this repository is private, so every Actions
  # minute is billable, and the estate fleet runs them at no metered cost.
  # An earlier attempt failed one throughput metric through contention rather
  # than capacity; the fix was in the job graph, not the budget.
  runner: "self-hosted-example"

agent:
  context_profile: "control-plane"
  generated_agents: true
  serena:
    enabled: true
    provenance_required: true

relationships:
  - type: "git-submodule-consumer"
    target: "repo_01JEXAMPZ0000000000000000K"
    gitmodules_name: "modules/ci-workflows"
  - type: "workflow-module-consumer"
    target: "repo_01JEXAMPZ0000000000000000K"

# A bundle release carries the whole tree, not one artifact.
release:
  mode: "bundle"
`

func spliceSchemas(t *testing.T) *validation.Set {
	t.Helper()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	return schemas
}

func decodeAuthored(t *testing.T, schemas *validation.Set) domain.RepositoryAnchor {
	t.Helper()
	candidate, findings := DecodeCandidate(Path, []byte(authoredAnchor), schemas)
	if len(findings) != 0 {
		t.Fatalf("authored fixture is invalid: %#v", findings)
	}
	return candidate.Anchor
}

func TestSpliceRelationshipsPreservesEverythingOutsideTheBlock(t *testing.T) {
	schemas := spliceSchemas(t)
	updated := decodeAuthored(t, schemas)
	updated.Relationships = append(updated.Relationships, domain.Relationship{
		Type: "git-submodule-consumer", Target: "repo_01JEXAMPZ0000000000000000R",
		GitmodulesName: "modules/agent-runtime",
	})

	candidate, findings := SpliceRelationships([]byte(authoredAnchor), updated, schemas)
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
	result := string(candidate.Raw)

	before := strings.Count(authoredAnchor, "#")
	if after := strings.Count(result, "#"); after != before {
		t.Fatalf("comment markers: authored %d, spliced %d", before, after)
	}
	for _, comment := range []string{
		"# The identity below is issued once and never regenerated.",
		"# Self-hosted deliberately: this repository is private, so every Actions",
		"# than capacity; the fix was in the job graph, not the budget.",
		"# A bundle release carries the whole tree, not one artifact.",
	} {
		if !strings.Contains(result, comment) {
			t.Fatalf("comment lost: %s", comment)
		}
	}
	if !strings.Contains(result, `    gitmodules_name: "modules/agent-runtime"`) {
		t.Fatal("the added relationship is absent")
	}
	for _, separated := range []string{
		"\n\nprovider:",
		"\n\nrelationships:",
		"\n\n# A bundle release carries the whole tree, not one artifact.\nrelease:",
	} {
		if !strings.Contains(result, separated) {
			t.Fatalf("blank line structure was not preserved around %q", strings.TrimSpace(separated))
		}
	}
}

// The diff a reviewer reads must be the change that was made. Anything else in
// it is noise that hides the part worth checking.
func TestSpliceRelationshipsTouchesOnlyTheBlockItRewrites(t *testing.T) {
	schemas := spliceSchemas(t)
	updated := decodeAuthored(t, schemas)
	updated.Relationships = append(updated.Relationships, domain.Relationship{
		Type: "git-submodule-consumer", Target: "repo_01JEXAMPZ0000000000000000R",
		GitmodulesName: "modules/agent-runtime",
	})
	candidate, findings := SpliceRelationships([]byte(authoredAnchor), updated, schemas)
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
	authoredLines := strings.Split(authoredAnchor, "\n")
	splicedLines := strings.Split(string(candidate.Raw), "\n")
	if len(splicedLines) != len(authoredLines)+3 {
		t.Fatalf("line count: authored %d, spliced %d", len(authoredLines), len(splicedLines))
	}
	added := 0
	for _, line := range splicedLines {
		found := false
		for _, original := range authoredLines {
			if line == original {
				found = true
				break
			}
		}
		if !found {
			added++
		}
	}
	if added != 2 {
		t.Fatalf("unexpected new distinct lines: %d", added)
	}
}

func TestSpliceRelationshipsIsIdempotent(t *testing.T) {
	schemas := spliceSchemas(t)
	updated := decodeAuthored(t, schemas)
	candidate, findings := SpliceRelationships([]byte(authoredAnchor), updated, schemas)
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
	if string(candidate.Raw) != authoredAnchor {
		t.Fatalf("rewriting an unchanged relationship set altered the file:\n%s", string(candidate.Raw))
	}
}

func TestSpliceRelationshipsRemovesWithoutDisturbingNeighbours(t *testing.T) {
	schemas := spliceSchemas(t)
	updated := decodeAuthored(t, schemas)
	updated.Relationships = updated.Relationships[:1]
	candidate, findings := SpliceRelationships([]byte(authoredAnchor), updated, schemas)
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
	result := string(candidate.Raw)
	if strings.Contains(result, "workflow-module-consumer") {
		t.Fatal("the removed relationship survived")
	}
	if !strings.Contains(result, "# A bundle release carries the whole tree, not one artifact.") {
		t.Fatal("the comment after the block was consumed by the removal")
	}
	if strings.Count(result, "#") != strings.Count(authoredAnchor, "#") {
		t.Fatal("removal changed the comment count")
	}
}

func TestSpliceRelationshipsAddsAnAbsentBlock(t *testing.T) {
	schemas := spliceSchemas(t)
	withoutBlock := authoredAnchor[:strings.Index(authoredAnchor, "relationships:")] +
		authoredAnchor[strings.Index(authoredAnchor, "# A bundle release"):]
	candidate, findings := DecodeCandidate(Path, []byte(withoutBlock), schemas)
	if len(findings) != 0 {
		t.Fatalf("fixture without relationships is invalid: %#v", findings)
	}
	updated := candidate.Anchor
	updated.Relationships = []domain.Relationship{{
		Type: "git-submodule-consumer", Target: "repo_01JEXAMPZ0000000000000000R",
		GitmodulesName: "modules/agent-runtime",
	}}
	spliced, findings := SpliceRelationships([]byte(withoutBlock), updated, schemas)
	if len(findings) != 0 {
		t.Fatalf("findings = %#v", findings)
	}
	result := string(spliced.Raw)
	if !strings.Contains(result, `    gitmodules_name: "modules/agent-runtime"`) {
		t.Fatal("the relationship block was not inserted")
	}
	if !strings.Contains(result, "# A bundle release carries the whole tree, not one artifact.") {
		t.Fatal("insertion disturbed the surrounding document")
	}
}

// A splice that lands wrong must fail rather than ship. The postcondition
// compares the decoded result against the caller's intended anchor, so a
// mutation the block cannot express is caught instead of silently dropped.
func TestSpliceRelationshipsRejectsChangesTheBlockCannotCarry(t *testing.T) {
	schemas := spliceSchemas(t)
	updated := decodeAuthored(t, schemas)
	updated.Repository.DisplayName = "renamed-outside-the-block"
	_, findings := SpliceRelationships([]byte(authoredAnchor), updated, schemas)
	if len(findings) == 0 {
		t.Fatal("a change outside the relationship block was accepted")
	}
	if findings[0].Code != "GDS_ANCHOR_RELATIONSHIP_SPLICE_FAILED" {
		t.Fatalf("finding = %s", findings[0].Code)
	}
}

// The schema bounds a relationship's shape but not how many an anchor declares,
// and this function is exported. Refusing an unbounded set keeps the rendered
// block, and the allocation sized from it, inside a stated limit.
func TestSpliceRelationshipsRejectsAnUnboundedSet(t *testing.T) {
	schemas := spliceSchemas(t)
	updated := decodeAuthored(t, schemas)
	updated.Relationships = make([]domain.Relationship, maxAnchorRelationships+1)
	for index := range updated.Relationships {
		updated.Relationships[index] = domain.Relationship{
			Type: "git-submodule-consumer", Target: "repo_01JEXAMPZ0000000000000000K",
			GitmodulesName: fmt.Sprintf("modules/m%d", index),
		}
	}
	_, findings := SpliceRelationships([]byte(authoredAnchor), updated, schemas)
	if len(findings) == 0 || findings[0].Code != "GDS_ANCHOR_RELATIONSHIP_COUNT_INVALID" {
		t.Fatalf("findings = %#v", findings)
	}
}
