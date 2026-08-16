package controller

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/estate"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/reconciler"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

type reconciliationReader struct{ inventory githubprovider.Inventory }

type reconciliationAudit struct{ err error }

func (audit reconciliationAudit) Record(
	context.Context,
	string,
	string,
	reconciler.Result,
	time.Time,
) (string, string, error) {
	if audit.err != nil {
		return "", "", audit.err
	}
	return "audit_01KX7BV07RHD6KRA4Z4J0KCHGR", "sha256:audit", nil
}

func (reader reconciliationReader) ListInstallationRepositories(
	context.Context,
	int,
) (githubprovider.Inventory, error) {
	return reader.inventory, nil
}

func TestReconciliationRunnerPersistsObservationsAndJournal(t *testing.T) {
	store := reconciliationStore(t)
	desired := reconciliationEstate(t)
	now := time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)
	readers := map[string]reconciler.InstallationReader{
		"installation:github-personal": reconciliationReader{inventory: githubprovider.Inventory{
			InstallationID: "installation:github-personal", ObservedAt: now,
			TotalCount: 1, RequestIDs: []string{"request-personal"},
			Repositories: []githubprovider.Repository{{
				ID: 1, Owner: "example-user", Name: "personal", FullName: "example-user/personal",
				Private: true, Visibility: "private", DefaultBranch: "main",
			}},
		}},
		"installation:github-organization": reconciliationReader{inventory: githubprovider.Inventory{
			InstallationID: "installation:github-organization", ObservedAt: now,
			TotalCount: 1, RequestIDs: []string{"request-organization"},
			Repositories: []githubprovider.Repository{{
				ID: 2, Owner: "example-org", Name: "organization",
				FullName: "example-org/organization", Private: true,
				Visibility: "private", DefaultBranch: "main",
			}},
		}},
		"installation:github-example-media": reconciliationReader{inventory: githubprovider.Inventory{
			InstallationID: "installation:github-example-media", ObservedAt: now,
			TotalCount: 1, RequestIDs: []string{"request-example-media"},
			Repositories: []githubprovider.Repository{{
				ID: 3, Owner: "example-media", Name: "example-media",
				FullName: "example-media/example-media", Private: true,
				Visibility: "private", DefaultBranch: "main",
			}},
		}},
		"installation:github-guild": reconciliationReader{inventory: githubprovider.Inventory{
			InstallationID: "installation:github-guild", ObservedAt: now,
			TotalCount: 1, RequestIDs: []string{"request-guild"},
			Repositories: []githubprovider.Repository{{
				ID: 4, Owner: "example-guild", Name: "guild",
				FullName: "example-guild/guild", Private: true,
				Visibility: "private", DefaultBranch: "main",
			}},
		}},
		"installation:github-opennetwork": reconciliationReader{inventory: githubprovider.Inventory{
			InstallationID: "installation:github-opennetwork", ObservedAt: now,
		}},
	}
	run, err := (ReconciliationRunner{
		Store: store, Config: desired, Readers: readers,
		Concurrency: 2, MaxRepositories: 2000, Now: func() time.Time { return now },
		Audit: reconciliationAudit{},
	}).Run(context.Background())
	if err != nil || run.Status != "succeeded" || len(run.Result.Inventory.Repositories) != 4 ||
		run.AuditSnapshotID == "" || run.AuditDigest == "" {
		t.Fatalf("run=%+v err=%v", run, err)
	}
	observation, err := store.GetRepositoryObservation(
		context.Background(), "installation:github-personal", 1,
	)
	if err != nil || observation.AccessState != "available" ||
		observation.RequestID != "request-personal" {
		t.Fatalf("observation=%+v err=%v", observation, err)
	}
	record, err := store.GetReconciliation(context.Background(), run.ReconciliationID)
	if err != nil || record.Status != "succeeded" || len(record.Result) == 0 {
		t.Fatalf("record=%+v err=%v", record, err)
	}
}

func TestReconciliationRunnerCannotSucceedWithoutSignedAudit(t *testing.T) {
	store := reconciliationStore(t)
	desired := reconciliationEstate(t)
	now := time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)
	readers := map[string]reconciler.InstallationReader{
		"installation:github-personal": reconciliationReader{inventory: githubprovider.Inventory{
			InstallationID: "installation:github-personal", ObservedAt: now,
		}},
		"installation:github-organization": reconciliationReader{inventory: githubprovider.Inventory{
			InstallationID: "installation:github-organization", ObservedAt: now,
		}},
	}
	run, err := (ReconciliationRunner{
		Store: store, Config: desired, Readers: readers,
		MaxRepositories: 2000, Now: func() time.Time { return now },
		Audit: reconciliationAudit{err: errors.New("private signing detail")},
	}).Run(context.Background())
	if err != nil || run.Status != "partial" ||
		!reconcileResultHasFinding(run.Result, "GDS_AUDIT_SNAPSHOT_FAILED") {
		t.Fatalf("run=%+v err=%v", run, err)
	}
}

func reconcileResultHasFinding(result reconciler.Result, code string) bool {
	for _, finding := range result.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}

func reconciliationStore(t *testing.T) *state.Store {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := state.Initialize(context.Background(), filepath.Join(root, "state.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func reconciliationEstate(t *testing.T) estate.Config {
	t.Helper()
	_, current, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(current), "..", ".."))
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	desired, findings := estate.Load(root, schemas)
	if len(findings) != 0 {
		t.Fatalf("findings=%+v", findings)
	}
	return desired
}
