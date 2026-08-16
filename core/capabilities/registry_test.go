package capabilities

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistryAccessorsReturnIndependentCopies(t *testing.T) {
	if err := Validate(); err != nil {
		t.Fatal(err)
	}
	commands := RootCommandNames()
	commands[0] = "changed"
	if RootCommandNames()[0] == "changed" {
		t.Fatal("root command names share mutable storage")
	}
	registry := Definitions()
	registry[0].CommandCarriers[0] = "changed"
	if Definitions()[0].CommandCarriers[0] == "changed" {
		t.Fatal("capability definitions share mutable storage")
	}
}

func TestOperationsContractContainsCanonicalCapabilityBlock(t *testing.T) {
	contractPath := filepath.Join("..", "..", "docs", "contracts", "operations-v1.md")
	content, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	if count := strings.Count(string(content), DocumentationBlock()); count != 1 {
		t.Fatalf("canonical capability documentation block count = %d", count)
	}
}
