package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGovernancePlanRestrictionPreservesAvailableRepositorySettings(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/example/repository":
			_, _ = w.Write([]byte(repositoryJSON(1, "example", "repository", false)))
		case "/repos/example/repository/actions/permissions":
			_, _ = w.Write([]byte(`{"enabled":true,"allowed_actions":"all"}`))
		case "/repos/example/repository/actions/permissions/workflow":
			_, _ = w.Write([]byte(`{"default_workflow_permissions":"read"}`))
		case "/repos/example/repository/immutable-releases":
			http.NotFound(w, r)
		case "/repos/example/repository/rulesets":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"Upgrade to GitHub Pro or make this repository public to enable this feature."}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	now := time.Now()
	client := testClient(t, server, fixedToken("token", now.Add(time.Hour)), nil)
	snapshot, err := client.GetRepositoryGovernance(context.Background(), "example", "repository")
	if err != nil {
		t.Fatalf("unavailable rulesets erased available settings: %v", err)
	}
	if snapshot.Repository.ID != 1 || !snapshot.Actions.Enabled {
		t.Fatalf("available observation lost: %#v", snapshot)
	}
	raw, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatal(err)
	}
	var output map[string]any
	if err := json.Unmarshal(raw, &output); err != nil {
		t.Fatal(err)
	}
	unavailable, ok := output["unavailable"].(map[string]any)
	if !ok || unavailable["github.rulesets"] != "plan-restricted" {
		t.Fatalf("plan restriction is not explicit: %s", raw)
	}
}

func TestPlanRestrictionNeverMasksAuthorizationOrRateLimits(t *testing.T) {
	plan := []byte(`{"message":"Upgrade to GitHub Pro or make this repository public to enable this feature."}`)
	for _, tc := range []struct {
		name   string
		status int
		body   []byte
		meta   ResponseMeta
		want   ErrorKind
	}{
		{"plan", 403, plan, ResponseMeta{}, ErrorCapabilityUnavailable},
		{"authentication", 401, plan, ResponseMeta{}, ErrorAuthentication},
		{"access revoked", 403, []byte(`{"message":"Resource not accessible by integration"}`), ResponseMeta{}, ErrorAuthorization},
		{"malformed", 403, []byte(`not JSON`), ResponseMeta{}, ErrorAuthorization},
		{"secondary throttle", 403, []byte(`{"message":"You have exceeded a secondary rate limit."}`), ResponseMeta{}, ErrorRateLimited},
		{"retry header", 403, plan, ResponseMeta{RetryAfter: time.Second}, ErrorRateLimited},
		{"exhausted quota", 403, plan, ResponseMeta{Rate: Rate{Known: true, Remaining: 0}}, ErrorRateLimited},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyStatus(tc.status, tc.body, tc.meta); got != tc.want {
				t.Fatalf("kind=%s want %s", got, tc.want)
			}
		})
	}
}
