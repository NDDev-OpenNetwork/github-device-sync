package memory

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
)

// A repository anchor declares `agent.serena.enabled` and
// `agent.serena.provenance_required`, and `schemas/v1/repository.schema.json`
// makes both required booleans -- so writing them is an explicit contract
// statement, not an omission. Nothing read them. The validator demanded a
// non-empty provenance-bearing memory set from every repository, which meant a
// module could declare Serena off and still be told its memory set was empty,
// and a module could declare provenance optional and still be held to it.
//
// A required field nothing consults is worse than an absent one: it reads as a
// control, so the reader stops looking for the real one.

// Posture is what an anchor says about Serena state in its own tree.
type Posture struct {
	Enabled            bool
	ProvenanceRequired bool
}

// StrictPosture is what every repository was held to before an anchor could say
// otherwise. It remains the fallback when the anchor cannot be read, because the
// alternative -- assuming an unreadable anchor opted out -- would let an
// unparseable file silently disable a gate.
var StrictPosture = Posture{Enabled: true, ProvenanceRequired: true}

// provenanceCodes are the findings that only mean something to a repository that
// asked for provenance. A memory whose body no longer matches its recorded
// digest is a defect exactly when the anchor says digests are load-bearing;
// where they are not, reporting it asserts a contract the repository declined.
var provenanceCodes = map[string]struct{}{
	"GDS_MEMORY_SOURCE_DIGEST_MISMATCH":      {},
	"GDS_MEMORY_BODY_DIGEST_MISMATCH":        {},
	"GDS_MEMORY_SOURCE_NOT_PROVEN":           {},
	"GDS_MEMORY_SOURCE_LIST_DIVERGENCE":      {},
	"GDS_MEMORY_VERIFIED_AT_PRECEDES_SOURCE": {},
}

// disabledFindings reports Serena state living in a tree whose anchor disclaims
// it.
//
// The direction matters. Under a disabled declaration the absence of memories is
// the expected state and says nothing; their presence is the surprise, because
// some writer put agent state into a repository that said it keeps none, and
// whoever reads that tree next has no contract telling them what it is or who
// maintains it.
func disabledFindings(root string) []domain.Finding {
	memoryRoot := filepath.Join(root, ".serena", "memories")
	entries, err := os.ReadDir(memoryRoot)
	if err != nil {
		// Absent is the declared state. An unreadable directory that exists is
		// not distinguished here on purpose: either way the anchor claims no
		// memory set, and there is none to validate.
		return nil
	}
	present := []string{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		present = append(present, filepath.ToSlash(
			filepath.Join(".serena", "memories", entry.Name()),
		))
	}
	if len(present) == 0 {
		return nil
	}
	sort.Strings(present)
	return []domain.Finding{memoryFinding(
		"GDS_MEMORY_DISABLED_STATE_PRESENT",
		"The anchor declares agent.serena.enabled: false and the tree carries Serena memories.",
		map[string]any{
			"path":     filepath.ToSlash(filepath.Join(".serena", "memories")),
			"memories": present, "count": len(present),
		},
	)}
}

// applyPosture narrows a strict result to what the anchor actually asked for.
func applyPosture(report Report, findings []domain.Finding, posture Posture) (Report, []domain.Finding) {
	if posture.ProvenanceRequired {
		return report, findings
	}
	kept := make([]domain.Finding, 0, len(findings))
	for _, finding := range findings {
		if _, provenance := provenanceCodes[finding.Code]; provenance {
			continue
		}
		// An empty set is a claim of missing assurance only where assurance was
		// claimed. A repository that keeps memories without binding them to
		// sources has not promised to keep any.
		if finding.Code == "GDS_MEMORY_SET_EMPTY" || finding.Code == "GDS_MEMORY_ROOT_NOT_PROVEN" {
			continue
		}
		kept = append(kept, finding)
	}
	return report, kept
}
