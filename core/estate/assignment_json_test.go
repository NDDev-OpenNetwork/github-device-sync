package estate

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestAssignmentJSONOmitsFalseArchived(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(Assignment{
		ProviderID: 1, Owner: "example-user", Name: "example",
		IdentityState: "unassigned", ManagementMode: "observe-only",
	})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"archived"`)) {
		t.Fatalf("false archived must omit the key so historical audit payloads remashal: %s", raw)
	}
}

func TestAssignmentJSONIncludesTrueArchived(t *testing.T) {
	t.Parallel()
	raw, err := json.Marshal(Assignment{
		ProviderID: 1, Owner: "example-user", Name: "retired", Archived: true,
		IdentityState: "unassigned", ManagementMode: "observe-only",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(raw, []byte(`"archived":true`)) {
		t.Fatalf("true archived must remain visible: %s", raw)
	}
}

func TestHistoricalAssignmentRemarshalsWithoutArchivedKey(t *testing.T) {
	t.Parallel()
	historical := []byte(
		`{"provider_id":1,"owner":"example-user","name":"example",` +
			`"identity_state":"unassigned","management_mode":"observe-only",` +
			`"portfolios":null,"policy_profiles":null,"rollout_ring":""}`,
	)
	var assignment Assignment
	if err := json.Unmarshal(historical, &assignment); err != nil {
		t.Fatal(err)
	}
	fresh, err := json.Marshal(assignment)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(historical, fresh) {
		t.Fatalf("historical assignment remarshal changed payload\n historical=%s\n fresh=%s", historical, fresh)
	}
}
