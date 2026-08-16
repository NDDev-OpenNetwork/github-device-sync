package reconciler

import (
	"context"
	"errors"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/estate"
	githubprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/github"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

type readerResult struct {
	inventory githubprovider.Inventory
	err       error
}

type sinkResult struct{ err error }

func (sink sinkResult) PersistInventory(context.Context, githubprovider.Inventory) error {
	return sink.err
}

type recordingSink struct{ inventories []githubprovider.Inventory }

func (sink *recordingSink) PersistInventory(_ context.Context, inventory githubprovider.Inventory) error {
	sink.inventories = append(sink.inventories, inventory)
	return nil
}

func (reader readerResult) ListInstallationRepositories(
	context.Context,
	int,
) (githubprovider.Inventory, error) {
	return reader.inventory, reader.err
}

func TestReconcileAllCompilesFiveInstallationsAndTwoThousandRepositories(t *testing.T) {
	t.Parallel()
	config := reconcilerConfig(t)
	personal := providerInventory("installation:github-personal", "example-user", 1, 1000)
	organization := providerInventory("installation:github-organization", "example-org", 1001, 1000)
	exampleMedia := providerInventory("installation:github-example-media", "example-media", 2001, 0)
	guild := providerInventory("installation:github-guild", "example-guild", 3001, 0)
	openNetwork := providerInventory("installation:github-opennetwork", "NDDev-OpenNetwork", 4001, 0)
	result := (Reconciler{
		Config: config, Concurrency: 2,
		Readers: map[string]InstallationReader{
			"installation:github-personal":     readerResult{inventory: personal},
			"installation:github-organization": readerResult{inventory: organization},
			"installation:github-example-media": readerResult{inventory: exampleMedia},
			"installation:github-guild":        readerResult{inventory: guild},
			"installation:github-opennetwork":  readerResult{inventory: openNetwork},
		},
	}).ReconcileAll(context.Background())
	if len(result.Findings) != 0 || len(result.Inventory.Repositories) != 2000 ||
		len(result.Installations) != 5 || len(result.Drift) != 2000 {
		t.Fatalf(
			"repositories=%d installations=%#v drift=%d findings=%#v",
			len(result.Inventory.Repositories), result.Installations, len(result.Drift), result.Findings,
		)
	}
}

func TestReconcileAllIsolatesInstallationFailure(t *testing.T) {
	t.Parallel()
	config := reconcilerConfig(t)
	result := (Reconciler{
		Config: config,
		Readers: map[string]InstallationReader{
			"installation:github-personal": readerResult{
				inventory: providerInventory("installation:github-personal", "example-user", 1, 2),
			},
			"installation:github-organization": readerResult{
				err: errors.New("private provider detail"),
			},
		},
	}).ReconcileAll(context.Background())
	if len(result.Inventory.Repositories) != 2 ||
		!reconcileHasFinding(result, "GDS_RECONCILE_INSTALLATION_NOT_PROVEN") {
		t.Fatalf("result=%#v", result)
	}
	for _, finding := range result.Findings {
		if value, found := finding.Evidence["error"]; found && value != nil {
			t.Fatalf("provider error detail leaked: %#v", finding)
		}
	}
}

func TestReconcileAllClassifiesPermissionMismatchAsCritical(t *testing.T) {
	t.Parallel()
	config := reconcilerConfig(t)
	result := (Reconciler{
		Config: config,
		Readers: map[string]InstallationReader{
			"installation:github-personal": readerResult{
				err: &githubprovider.APIError{Kind: githubprovider.ErrorPermissionContract},
			},
			"installation:github-organization": readerResult{
				inventory: providerInventory(
					"installation:github-organization", "example-org", 1, 1,
				),
			},
		},
	}).ReconcileAll(context.Background())
	if !reconcileHasFinding(result, "GDS_RECONCILE_PERMISSION_CONTRACT_MISMATCH") {
		t.Fatalf("result=%#v", result)
	}
	for _, finding := range result.Findings {
		if finding.Code == "GDS_RECONCILE_PERMISSION_CONTRACT_MISMATCH" &&
			finding.Severity != "critical" {
			t.Fatalf("finding=%#v", finding)
		}
	}
}

func TestReconcileAllRejectsCombinedEstateAboveBound(t *testing.T) {
	t.Parallel()
	config := reconcilerConfig(t)
	sink := &recordingSink{}
	result := (Reconciler{
		Config: config, Sink: sink,
		Readers: map[string]InstallationReader{
			"installation:github-personal": readerResult{
				inventory: providerInventory("installation:github-personal", "example-user", 1, 1001),
			},
			"installation:github-organization": readerResult{
				inventory: providerInventory("installation:github-organization", "example-org", 1002, 1000),
			},
		},
	}).ReconcileAll(context.Background())
	if !reconcileHasFinding(result, "GDS_RECONCILE_ESTATE_LIMIT_EXCEEDED") ||
		len(result.Inventory.Repositories) != 0 || len(sink.inventories) != 0 {
		t.Fatalf("result=%#v", result)
	}
}

func TestReconcileAllPersistsExactGlobalBound(t *testing.T) {
	t.Parallel()
	config := reconcilerConfig(t)
	sink := &recordingSink{}
	result := (Reconciler{
		Config: config, Sink: sink,
		Readers: map[string]InstallationReader{
			"installation:github-personal": readerResult{
				inventory: providerInventory("installation:github-personal", "example-user", 1, 1000),
			},
			"installation:github-organization": readerResult{
				inventory: providerInventory("installation:github-organization", "example-org", 1001, 1000),
			},
		},
	}).ReconcileAll(context.Background())
	if reconcileHasFinding(result, "GDS_RECONCILE_ESTATE_LIMIT_EXCEEDED") ||
		len(result.Inventory.Repositories) != 2000 || len(sink.inventories) != 2 {
		t.Fatalf("result=%#v persisted=%d", result, len(sink.inventories))
	}
}

func TestReconcileAllRejectsRepositoryFromWrongInstallationAccount(t *testing.T) {
	t.Parallel()
	config := reconcilerConfig(t)
	personal := providerInventory("installation:github-personal", "example-org", 1, 1)
	result := (Reconciler{
		Config: config,
		Readers: map[string]InstallationReader{
			"installation:github-personal": readerResult{inventory: personal},
			"installation:github-organization": readerResult{
				inventory: providerInventory(
					"installation:github-organization", "example-org", 2, 1,
				),
			},
		},
	}).ReconcileAll(context.Background())
	if !reconcileHasFinding(result, "GDS_RECONCILE_INSTALLATION_ACCOUNT_MISMATCH") ||
		len(result.Inventory.Repositories) != 1 {
		t.Fatalf("result=%#v", result)
	}
}

func TestReconcileAllHonorsConfiguredRepositoryLimit(t *testing.T) {
	t.Parallel()
	config := reconcilerConfig(t)
	result := (Reconciler{
		Config: config, MaxRepositories: 1,
		Readers: map[string]InstallationReader{
			"installation:github-personal": readerResult{
				inventory: providerInventory("installation:github-personal", "example-user", 1, 1),
			},
			"installation:github-organization": readerResult{
				inventory: providerInventory("installation:github-organization", "example-org", 2, 1),
			},
		},
	}).ReconcileAll(context.Background())
	if !reconcileHasFinding(result, "GDS_RECONCILE_ESTATE_LIMIT_EXCEEDED") {
		t.Fatalf("result=%#v", result)
	}
}

func TestReconcileAllReportsSinkFailureWithoutLeakingDetail(t *testing.T) {
	t.Parallel()
	config := reconcilerConfig(t)
	result := (Reconciler{
		Config: config, Sink: sinkResult{err: errors.New("private database detail")},
		Readers: map[string]InstallationReader{
			"installation:github-personal": readerResult{
				inventory: providerInventory("installation:github-personal", "example-user", 1, 1),
			},
			"installation:github-organization": readerResult{
				inventory: providerInventory("installation:github-organization", "example-org", 2, 1),
			},
		},
	}).ReconcileAll(context.Background())
	if !reconcileHasFinding(result, "GDS_RECONCILE_OBSERVATION_PERSIST_FAILED") ||
		len(result.Inventory.Repositories) != 2 {
		t.Fatalf("result=%#v", result)
	}
	for _, finding := range result.Findings {
		if value, found := finding.Evidence["error"]; found && value != nil {
			t.Fatalf("sink error leaked: %#v", finding)
		}
	}
}

func reconcilerConfig(t *testing.T) estate.Config {
	t.Helper()
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		t.Fatal(err)
	}
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	config, findings := estate.Load(root, schemas)
	if len(findings) != 0 {
		t.Fatalf("estate findings=%#v", findings)
	}
	return config
}

func providerInventory(
	installationID string,
	owner string,
	start int64,
	count int,
) githubprovider.Inventory {
	inventory := githubprovider.Inventory{InstallationID: installationID, TotalCount: count}
	for index := 0; index < count; index++ {
		id := start + int64(index)
		inventory.Repositories = append(inventory.Repositories, githubprovider.Repository{
			ID: id, Owner: owner, Name: "repository", FullName: owner + "/repository",
			Visibility: "private", Private: true, Fork: index%2 == 0,
			DefaultBranch: "main",
		})
	}
	return inventory
}

func reconcileHasFinding(result Result, code string) bool {
	for _, finding := range result.Findings {
		if finding.Code == code {
			return true
		}
	}
	return false
}
