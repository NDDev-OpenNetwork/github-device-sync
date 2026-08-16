package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// trackedRulesetContract loads the repository's real tracked default-branch
// ruleset. The live reconcile builds its desired state from this exact file, so
// a fixture that paraphrases it cannot prove the live path.
func trackedRulesetContract(t *testing.T) []byte {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", "..", ".."))
	raw, err := os.ReadFile(filepath.Join(root, ".github", "rulesets", "branch-main.json"))
	if err != nil {
		t.Fatalf("read tracked ruleset contract: %v", err)
	}
	return raw
}

// TestTrackedContractUpdatesTheLiveRuleset drives the exact shape the live
// reconcile uses: the desired state decoded from the tracked contract, carrying
// every rule the contract declares, against the observed live payload. The
// existing shape test only submits the single owned rule, so it never exercised
// a desired state holding the four rules GDS preserves rather than owns.
func TestTrackedContractUpdatesTheLiveRuleset(t *testing.T) {
	fixture := liveRulesetFixture(t)
	desired, err := DecodeRulesetDocument(trackedRulesetContract(t))
	if err != nil {
		t.Fatalf("decode tracked contract: %v", err)
	}

	var submitted map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodPut {
			if err := json.NewDecoder(request.Body).Decode(&submitted); err != nil {
				t.Errorf("decode update payload: %v", err)
			}
		}
		_, _ = writer.Write(fixture)
	}))
	defer server.Close()

	mutator := mutationTestMutator(t, server, []string{MutationRepositoryRuleset}, nil, nil)
	repository, err := mutator.BindRepository(RepositoryMutationScope{
		RepositoryID: 42, Owner: "example", Name: "repository",
		Operations: []string{MutationRepositoryRuleset},
	})
	if err != nil {
		t.Fatal(err)
	}
	current, _, err := repository.GetRepositoryRuleset(context.Background(), 9)
	if err != nil {
		t.Fatal(err)
	}

	// The planner binds the observed provider identity into the desired state
	// before the step is stored; a tracked contract cannot predeclare it.
	desired.ID = current.ID

	if _, _, err := repository.UpsertDefaultBranchRuleset(context.Background(), desired, &current); err != nil {
		t.Fatalf("tracked-contract update failed: %v", err)
	}
	if submitted == nil {
		t.Fatal("no update payload was submitted")
	}
}
