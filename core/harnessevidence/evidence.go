// Package harnessevidence verifies isolated harness proofs and their aggregate manifest.
package harnessevidence

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/trust"
)

var ActiveHarnesses = []string{
	"antigravity", "claude-code", "codex", "cursor", "grok-build", "opencode", "pi",
}

var immutableCommit = regexp.MustCompile(`^[0-9a-f]{40}$`)
var executableVersion = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z._+-]{0,127}$`)

// AnchoredIdentity returns producer and module pins only when the independently
// supplied trust policy contains the exact immutable active-seven mapping.
func AnchoredIdentity(policy trust.Policy) (string, map[string]string, error) {
	anchored := policy.HarnessEvidence
	if anchored == nil || anchored.Producer.Repository == "" ||
		(!strings.HasPrefix(anchored.Producer.Ref, "refs/heads/") &&
			!strings.HasPrefix(anchored.Producer.Ref, "refs/tags/")) ||
		!immutableCommit.MatchString(anchored.Producer.Commit) ||
		len(anchored.Modules) != len(ActiveHarnesses) {
		return "", nil, errors.New("harness evidence trust lacks an immutable producer and exact module identity")
	}
	for _, id := range ActiveHarnesses {
		if !immutableCommit.MatchString(anchored.Modules[id]) {
			return "", nil, fmt.Errorf("harness evidence trust lacks an immutable module identity for %s", id)
		}
	}
	return anchored.Producer.Commit, anchored.Modules, nil
}

type Platform struct {
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	DeviceClass  string `json:"device_class"`
}

type Payload struct {
	SchemaVersion     int       `json:"schema_version"`
	EvidenceID        string    `json:"evidence_id"`
	HarnessID         string    `json:"harness_id"`
	HarnessRootSHA    string    `json:"harness_root_sha"`
	ModuleSHA         string    `json:"module_sha"`
	ProfileDigest     string    `json:"profile_digest"`
	BridgeDigest      string    `json:"bridge_digest"`
	ExecutableVersion string    `json:"executable_version"`
	Platform          Platform  `json:"platform"`
	SuiteVersion      string    `json:"suite_version"`
	SuiteCasesDigest  string    `json:"suite_cases_digest"`
	Result            string    `json:"result"`
	GeneratedAt       time.Time `json:"generated_at"`
	ExpiresAt         time.Time `json:"expires_at"`
	ActorID           string    `json:"actor_id"`
}

type Record struct {
	Payload        Payload         `json:"payload"`
	EvidenceDigest string          `json:"evidence_digest"`
	Signature      trust.Signature `json:"signature"`
}

type ManifestEntry struct {
	HarnessID      string `json:"harness_id"`
	EvidenceDigest string `json:"evidence_digest"`
}

type ManifestPayload struct {
	SchemaVersion  int             `json:"schema_version"`
	ManifestID     string          `json:"manifest_id"`
	HarnessRootSHA string          `json:"harness_root_sha"`
	Channel        string          `json:"channel"`
	GeneratedAt    time.Time       `json:"generated_at"`
	ExpiresAt      time.Time       `json:"expires_at"`
	ActorID        string          `json:"actor_id"`
	Evidence       []ManifestEntry `json:"evidence"`
}

type Manifest struct {
	Payload        ManifestPayload `json:"payload"`
	ManifestDigest string          `json:"manifest_digest"`
	Signature      trust.Signature `json:"signature"`
}

type Expectation struct {
	Channel            string
	HarnessRootSHA     string
	ModuleSHAs         map[string]string
	ExecutableVersions map[string]string
	ProfileDigests     map[string]string
	BridgeDigests      map[string]string
	Now                time.Time
}

type Verifier struct{ Trust trust.Verifier }

type GateResult struct {
	Channel     string   `json:"channel"`
	Status      string   `json:"status"`
	Provisional bool     `json:"provisional"`
	Missing     []string `json:"missing"`
	AutoPromote bool     `json:"auto_promote"`
}

func (verifier Verifier) Verify(record Record, expected Expectation) error {
	p := record.Payload
	digest, err := canonicaljson.Digest(p)
	if err != nil || digest != record.EvidenceDigest {
		return errors.New("harness evidence digest mismatch")
	}
	if p.SchemaVersion != 1 || !slices.Contains(ActiveHarnesses, p.HarnessID) ||
		p.HarnessRootSHA != expected.HarnessRootSHA || p.ModuleSHA != expected.ModuleSHAs[p.HarnessID] ||
		p.ProfileDigest != expected.ProfileDigests[p.HarnessID] ||
		p.BridgeDigest != expected.BridgeDigests[p.HarnessID] ||
		p.ExecutableVersion != expected.ExecutableVersions[p.HarnessID] ||
		!executableVersion.MatchString(p.ExecutableVersion) ||
		p.Result != "pass" || p.GeneratedAt.After(expected.Now) || !expected.Now.Before(p.ExpiresAt) ||
		p.ExpiresAt.Sub(p.GeneratedAt) > 72*time.Hour || p.Platform.OS == "" ||
		p.Platform.Architecture == "" || p.SuiteVersion == "" || p.SuiteCasesDigest == "" {
		return errors.New("harness evidence does not match the exact release expectation")
	}
	return verifier.Trust.Verify("gds-harness-runtime-evidence/v1", p.ActorID, "harness-evidence", p.GeneratedAt, p, record.Signature)
}

func (verifier Verifier) VerifyManifest(manifest Manifest, records []Record, expected Expectation) error {
	p := manifest.Payload
	digest, err := canonicaljson.Digest(p)
	if err != nil || digest != manifest.ManifestDigest {
		return errors.New("harness evidence manifest digest mismatch")
	}
	if p.SchemaVersion != 1 || p.HarnessRootSHA != expected.HarnessRootSHA || p.Channel != expected.Channel ||
		p.GeneratedAt.After(expected.Now) || !expected.Now.Before(p.ExpiresAt) ||
		!p.ExpiresAt.After(p.GeneratedAt) || p.ExpiresAt.Sub(p.GeneratedAt) > 72*time.Hour ||
		(p.Channel != "canary" && p.Channel != "stable" && p.Channel != "frozen") {
		return errors.New("harness evidence manifest identity is invalid")
	}
	if err := verifier.Trust.Verify("gds-harness-runtime-manifest/v1", p.ActorID, "harness-evidence-aggregate", p.GeneratedAt, p, manifest.Signature); err != nil {
		return err
	}
	entries := append([]ManifestEntry(nil), p.Evidence...)
	sort.Slice(entries, func(i, j int) bool { return entries[i].HarnessID < entries[j].HarnessID })
	if len(entries) != len(ActiveHarnesses) {
		return errors.New("harness evidence manifest does not contain the exact active set")
	}
	byDigest := make(map[string]Record, len(records))
	for _, record := range records {
		byDigest[record.EvidenceDigest] = record
	}
	for index, id := range ActiveHarnesses {
		entry := entries[index]
		if entry.HarnessID != id {
			return fmt.Errorf("harness evidence manifest missing active harness %s", id)
		}
		record, ok := byDigest[entry.EvidenceDigest]
		if !ok || record.Payload.HarnessID != id {
			return fmt.Errorf("isolated evidence for %s is missing", id)
		}
		if err := verifier.Verify(record, expected); err != nil {
			return fmt.Errorf("verify %s evidence: %w", id, err)
		}
	}
	return nil
}

// EvaluateChannel applies release-channel semantics. Canary may carry an
// explicitly provisional subset, but it can never auto-promote. Stable and
// frozen require the exact signed active-seven closure.
func (verifier Verifier) EvaluateChannel(manifest Manifest, records []Record, expected Expectation) (GateResult, error) {
	if expected.Channel == "stable" || expected.Channel == "frozen" {
		if err := verifier.VerifyManifest(manifest, records, expected); err != nil {
			return GateResult{}, err
		}
		return GateResult{Channel: expected.Channel, Status: "pass"}, nil
	}
	if expected.Channel != "canary" || manifest.Payload.Channel != "canary" {
		return GateResult{}, errors.New("release channel is invalid")
	}
	// Prove the aggregate identity even when its evidence list is incomplete.
	p := manifest.Payload
	digest, err := canonicaljson.Digest(p)
	if err != nil || digest != manifest.ManifestDigest || p.HarnessRootSHA != expected.HarnessRootSHA ||
		p.GeneratedAt.After(expected.Now) || !expected.Now.Before(p.ExpiresAt) ||
		!p.ExpiresAt.After(p.GeneratedAt) || p.ExpiresAt.Sub(p.GeneratedAt) > 72*time.Hour {
		return GateResult{}, errors.New("canary harness manifest identity is invalid")
	}
	if err := verifier.Trust.Verify("gds-harness-runtime-manifest/v1", p.ActorID, "harness-evidence-aggregate", p.GeneratedAt, p, manifest.Signature); err != nil {
		return GateResult{}, err
	}
	byID := map[string]Record{}
	for _, record := range records {
		if _, duplicate := byID[record.Payload.HarnessID]; duplicate {
			return GateResult{}, errors.New("canary repeats isolated harness evidence")
		}
		byID[record.Payload.HarnessID] = record
	}
	listed := map[string]string{}
	for _, entry := range p.Evidence {
		if !slices.Contains(ActiveHarnesses, entry.HarnessID) || listed[entry.HarnessID] != "" {
			return GateResult{}, errors.New("canary manifest contains duplicate or non-active harness identity")
		}
		listed[entry.HarnessID] = entry.EvidenceDigest
	}
	missing := []string{}
	for _, id := range ActiveHarnesses {
		record, found := byID[id]
		if !found || listed[id] == "" {
			missing = append(missing, id)
			continue
		}
		if listed[id] != record.EvidenceDigest {
			return GateResult{}, fmt.Errorf("canary manifest digest differs for %s", id)
		}
		if err := verifier.Verify(record, expected); err != nil {
			return GateResult{}, fmt.Errorf("verify canary %s evidence: %w", id, err)
		}
	}
	return GateResult{Channel: "canary", Status: "provisional", Provisional: true, Missing: missing, AutoPromote: false}, nil
}
