// Package capabilities owns the canonical implemented capability and root
// command surface reported by GDS.
package capabilities

import (
	"fmt"
	"sort"
	"strings"
)

type ID string

const (
	ProviderObservation ID = "provider_observation"
	Mutations           ID = "mutations"
)

type State struct {
	Support string `json:"support"`
	Runtime string `json:"runtime"`
	Policy  string `json:"policy"`
}

type Set struct {
	ProviderObservation State `json:"provider_observation"`
	Mutations           State `json:"mutations"`
}

type Definition struct {
	ID              ID
	State           State
	CommandCarriers []string
}

var rootCommandNames = []string{
	"context",
	"session",
	"sync",
	"handoff",
	"complete",
	"status",
	"discover",
	"inventory",
	"validate",
	"doctor",
	"compile",
	"generate",
	"skill",
	"harness",
	"state",
	"operation",
	"recover",
	"git",
	"repository",
	"workspace",
	"module",
	"fork",
	"portfolio",
	"identity",
	"github",
	"reconcile",
	"report",
	"release",
	"rollout",
	"source",
	"memory",
}

var definitions = []Definition{
	{
		ID: ProviderObservation,
		State: State{
			Support: "implemented",
			Runtime: "configuration-required",
			Policy:  "read-only",
		},
		CommandCarriers: []string{"github", "reconcile", "repository"},
	},
	{
		ID: Mutations,
		State: State{
			Support: "implemented",
			Runtime: "configuration-required",
			Policy:  "explicit-approval",
		},
		CommandCarriers: []string{
			"complete", "fork", "generate", "git", "github", "handoff", "harness",
			"memory", "module", "operation", "portfolio", "recover", "release",
			"repository", "rollout", "session", "source", "state", "sync", "workspace",
		},
	},
}

func RootCommandNames() []string {
	return append([]string(nil), rootCommandNames...)
}

func Definitions() []Definition {
	result := make([]Definition, len(definitions))
	for index, definition := range definitions {
		result[index] = definition
		result[index].CommandCarriers = append([]string(nil), definition.CommandCarriers...)
	}
	return result
}

func ContextSet() Set {
	if err := Validate(); err != nil {
		panic(err)
	}
	states := map[ID]State{}
	for _, definition := range definitions {
		states[definition.ID] = definition.State
	}
	return Set{
		ProviderObservation: states[ProviderObservation],
		Mutations:           states[Mutations],
	}
}

func Validate() error {
	rootCommands := make(map[string]struct{}, len(rootCommandNames))
	for _, name := range rootCommandNames {
		if name == "" {
			return fmt.Errorf("root command name is empty")
		}
		if _, duplicate := rootCommands[name]; duplicate {
			return fmt.Errorf("root command %s is duplicated", name)
		}
		rootCommands[name] = struct{}{}
	}
	seenIDs := map[ID]struct{}{}
	for _, definition := range definitions {
		if _, duplicate := seenIDs[definition.ID]; duplicate {
			return fmt.Errorf("capability %s is duplicated", definition.ID)
		}
		seenIDs[definition.ID] = struct{}{}
		if definition.State.Support == "" || definition.State.Runtime == "" || definition.State.Policy == "" {
			return fmt.Errorf("capability %s has an incomplete state", definition.ID)
		}
		seenCarriers := map[string]struct{}{}
		for _, name := range definition.CommandCarriers {
			if _, found := rootCommands[name]; !found {
				return fmt.Errorf("capability %s references unregistered command %s", definition.ID, name)
			}
			if _, duplicate := seenCarriers[name]; duplicate {
				return fmt.Errorf("capability %s repeats command %s", definition.ID, name)
			}
			seenCarriers[name] = struct{}{}
		}
	}
	for _, required := range []ID{ProviderObservation, Mutations} {
		if _, found := seenIDs[required]; !found {
			return fmt.Errorf("required capability %s is missing", required)
		}
	}
	return nil
}

func ValidateRootCommands(observed []string) error {
	if err := Validate(); err != nil {
		return err
	}
	expected := append([]string(nil), rootCommandNames...)
	actual := append([]string(nil), observed...)
	sort.Strings(expected)
	sort.Strings(actual)
	if strings.Join(actual, "\x00") != strings.Join(expected, "\x00") {
		return fmt.Errorf("registered root commands differ: expected %v, got %v", expected, actual)
	}
	return nil
}

const DocumentationStart = "<!-- gds-capability-registry:start -->"
const DocumentationEnd = "<!-- gds-capability-registry:end -->"

func DocumentationBlock() string {
	var builder strings.Builder
	builder.WriteString(DocumentationStart)
	builder.WriteString("\n| Capability | Support | Runtime | Policy | Registered command carriers |\n")
	builder.WriteString("|---|---|---|---|---|\n")
	for _, definition := range definitions {
		fmt.Fprintf(
			&builder,
			"| `%s` | `%s` | `%s` | `%s` | `%s` |\n",
			definition.ID,
			definition.State.Support,
			definition.State.Runtime,
			definition.State.Policy,
			strings.Join(definition.CommandCarriers, "`, `"),
		)
	}
	builder.WriteString(DocumentationEnd)
	return builder.String()
}
