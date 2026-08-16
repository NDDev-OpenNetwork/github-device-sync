// Package source validates and evaluates volatile external source evidence.
package source

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

const registerPath = "docs/source-register/sources.yaml"

type Register struct {
	SchemaVersion int     `json:"schema_version" yaml:"schema_version"`
	Sources       []Entry `json:"sources" yaml:"sources"`
}

type Entry struct {
	ID            string         `json:"id" yaml:"id"`
	Authority     string         `json:"authority" yaml:"authority"`
	URL           string         `json:"url" yaml:"url"`
	Volatility    string         `json:"volatility" yaml:"volatility"`
	VerifiedAt    string         `json:"verified_at" yaml:"verified_at"`
	NextReview    string         `json:"next_review" yaml:"next_review"`
	Status        string         `json:"status" yaml:"status"`
	ContentDigest *string        `json:"content_digest,omitempty" yaml:"content_digest,omitempty"`
	Governs       []string       `json:"governs" yaml:"governs"`
	Observations  map[string]any `json:"observations,omitempty" yaml:"observations,omitempty"`
}

type Item struct {
	ID             string   `json:"id"`
	State          string   `json:"state"`
	Status         string   `json:"status"`
	Volatility     string   `json:"volatility"`
	VerifiedAt     string   `json:"verified_at"`
	NextReview     string   `json:"next_review"`
	ContentDigest  *string  `json:"content_digest"`
	GovernedClaims []string `json:"governed_claims"`
}

type Summary struct {
	Current   int `json:"current"`
	NotProven int `json:"not_proven"`
	Overdue   int `json:"overdue"`
	Blocked   int `json:"blocked"`
}

type Report struct {
	AsOf    string  `json:"as_of"`
	Count   int     `json:"count"`
	Summary Summary `json:"summary"`
	Items   []Item  `json:"items"`
}

func Load(root string, schemas *validation.Set) (Register, []domain.Finding) {
	path := filepath.Join(root, filepath.FromSlash(registerPath))
	value, err := serialization.DecodeFile(path)
	if err != nil {
		return Register{}, []domain.Finding{{
			Code: "GDS_SOURCE_REGISTER_NOT_PROVEN", Severity: domain.SeverityHigh,
			Message:  fmt.Sprintf("Source register cannot be decoded: %v", err),
			Evidence: map[string]any{"path": path},
		}}
	}
	if findings := schemas.Validate("source-register", value, path); len(findings) != 0 {
		return Register{}, findings
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return Register{}, []domain.Finding{{
			Code: "GDS_SOURCE_REGISTER_NOT_PROVEN", Severity: domain.SeverityHigh,
			Message:  fmt.Sprintf("Source register cannot be normalized: %v", err),
			Evidence: map[string]any{"path": path},
		}}
	}
	var register Register
	if err := json.Unmarshal(raw, &register); err != nil {
		return Register{}, []domain.Finding{{
			Code: "GDS_SOURCE_REGISTER_NOT_PROVEN", Severity: domain.SeverityHigh,
			Message:  fmt.Sprintf("Source register cannot be decoded into its typed contract: %v", err),
			Evidence: map[string]any{"path": path},
		}}
	}
	return register, nil
}

func Evaluate(register Register, asOf time.Time) Report {
	date := asOf.UTC().Format(time.DateOnly)
	items := make([]Item, 0, len(register.Sources))
	summary := Summary{}
	for _, entry := range register.Sources {
		state := classify(entry, date)
		switch state {
		case "blocked":
			summary.Blocked++
		case "overdue":
			summary.Overdue++
		case "not-proven":
			summary.NotProven++
		default:
			summary.Current++
		}
		claims := append([]string(nil), entry.Governs...)
		sort.Strings(claims)
		items = append(items, Item{
			ID: entry.ID, State: state, Status: entry.Status,
			Volatility: entry.Volatility, VerifiedAt: entry.VerifiedAt,
			NextReview: entry.NextReview, ContentDigest: entry.ContentDigest,
			GovernedClaims: claims,
		})
	}
	sort.Slice(items, func(left, right int) bool { return items[left].ID < items[right].ID })
	return Report{AsOf: date, Count: len(items), Summary: summary, Items: items}
}

func classify(entry Entry, asOf string) string {
	if strings.Contains(entry.Status, "changed-unreviewed") ||
		strings.Contains(entry.Status, "release-blocking") {
		return "blocked"
	}
	if entry.NextReview < asOf {
		return "overdue"
	}
	// GDS gates a source claim on its supply-chain pin (a content digest) and its
	// review currency, not on runtime proof. Runtime/behavioral proof (statuses
	// carrying a "-not-proven" suffix, e.g. "runtime-not-proven") is owned by the
	// isolated per-harness setup systems, not the control plane, so a docs-verified
	// claim with a pinned digest is release-current here even while its runtime
	// remains unproven. A missing digest is still a genuine control-plane gap.
	if entry.ContentDigest == nil {
		return "not-proven"
	}
	return "current"
}
