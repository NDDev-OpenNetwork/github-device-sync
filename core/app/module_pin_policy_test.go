package app

import (
	"testing"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
)

func TestModulePinManagementDefaultsToGenericGitlinkTransaction(t *testing.T) {
	for _, mode := range []string{"", "gitlink-only"} {
		finding := modulePinManagementFinding(domain.Relationship{
			Type: "git-submodule-consumer", PinManagement: mode,
		})
		if finding != nil {
			t.Fatalf("mode %q unexpectedly blocked: %#v", mode, finding)
		}
	}
}

func TestModulePinManagementBlocksGenericMutationForConsumerTransaction(t *testing.T) {
	finding := modulePinManagementFinding(domain.Relationship{
		Type: "git-submodule-consumer", PinManagement: "consumer-transaction",
	})
	if finding == nil || finding.Code != "GDS_MODULE_PIN_CONSUMER_TRANSACTION_REQUIRED" ||
		finding.Severity != domain.SeverityHigh {
		t.Fatalf("unexpected finding: %#v", finding)
	}
}
