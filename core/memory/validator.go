// Package memory validates provenance-bearing Serena memories.
package memory

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.yaml.in/yaml/v4"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

const maxMemoryBytes = 512 << 10

var memoryNamePattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*\.md$`)

var requiredBodySections = []string{
	"## Purpose",
	"## Invariants",
	"## Sources",
	"## Refresh",
}

type Metadata struct {
	SchemaVersion int    `json:"gds_memory_schema" yaml:"gds_memory_schema"`
	ScopeID       string `json:"scope_id" yaml:"scope_id"`
	Status        string `json:"status" yaml:"status"`
	Visibility    string `json:"visibility" yaml:"visibility"`
	SourceCommit  string `json:"source_commit" yaml:"source_commit"`
	SourceState   string `json:"source_state" yaml:"source_state"`
	SourceDigest  string `json:"source_digest" yaml:"source_digest"`
	// BodyDigest records which body a human actually reviewed. Nothing derives
	// the body from the sources -- Generate carries it through verbatim despite
	// the generated_by field -- so source freshness alone never established that
	// the prose still followed from what it cites. This makes at least the
	// weaker, decidable half provable: that the body under a verified label is
	// the one that was reviewed, not a later edit riding an old approval.
	BodyDigest      string   `json:"body_digest,omitempty" yaml:"body_digest,omitempty"`
	GeneratedBy     string   `json:"generated_by" yaml:"generated_by"`
	BundleVersion   string   `json:"bundle_version" yaml:"bundle_version"`
	VerifiedAt      string   `json:"verified_at" yaml:"verified_at"`
	RefreshTriggers []string `json:"refresh_triggers" yaml:"refresh_triggers"`
	Sources         []string `json:"sources" yaml:"sources"`
}

type Document struct {
	Name     string   `json:"name"`
	Path     string   `json:"path"`
	Metadata Metadata `json:"metadata"`
	Body     string   `json:"body"`
}

type Candidate struct {
	Name         string   `json:"name"`
	Path         string   `json:"path"`
	Metadata     Metadata `json:"metadata"`
	Content      string   `json:"content"`
	OutputDigest string   `json:"output_digest"`
}

type Item struct {
	Name         string `json:"name"`
	Path         string `json:"path"`
	Status       string `json:"status"`
	Visibility   string `json:"visibility"`
	SourceDigest string `json:"source_digest"`
	SourceState  string `json:"source_state"`
}

type Report struct {
	MemoryRoot string         `json:"memory_root"`
	Count      int            `json:"count"`
	Statuses   map[string]int `json:"statuses"`
	Items      []Item         `json:"items"`
}

type sourceRecord struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

// CommitTimeFunc resolves a commit id to its committer time. ok is false when
// the commit cannot be resolved locally (e.g. a shallow clone), in which case
// the temporal ordering check is skipped rather than failing closed.
type CommitTimeFunc func(commit string) (time.Time, bool)

// Validate holds a repository to the strict posture.
//
// The signature is unchanged because most callers -- this control plane's own
// gates among them -- validate a tree that declares Serena enabled and
// provenance required, and threading a posture through every one of them to
// restate the default would obscure the callers that actually vary.
func Validate(root string, schemas *validation.Set, commitTime ...CommitTimeFunc) (Report, []domain.Finding) {
	return ValidateWithPosture(root, schemas, StrictPosture, commitTime...)
}

// ValidateWithPosture holds a repository to what its own anchor declares.
func ValidateWithPosture(
	root string,
	schemas *validation.Set,
	posture Posture,
	commitTime ...CommitTimeFunc,
) (Report, []domain.Finding) {
	if !posture.Enabled {
		return Report{
			MemoryRoot: filepath.ToSlash(filepath.Join(".serena", "memories")),
			Statuses:   map[string]int{},
			Items:      []Item{},
		}, disabledFindings(root)
	}
	report, findings := validateStrict(root, schemas, commitTime...)
	return applyPosture(report, findings, posture)
}

func validateStrict(root string, schemas *validation.Set, commitTime ...CommitTimeFunc) (Report, []domain.Finding) {
	memoryRoot := filepath.Join(root, ".serena", "memories")
	report := Report{
		MemoryRoot: filepath.ToSlash(filepath.Join(".serena", "memories")),
		Statuses:   map[string]int{},
		Items:      []Item{},
	}
	entries, err := os.ReadDir(memoryRoot)
	if err != nil {
		return report, []domain.Finding{memoryFinding(
			"GDS_MEMORY_ROOT_NOT_PROVEN", "Cannot enumerate the Serena memory root.",
			map[string]any{"path": memoryRoot, "error": err.Error()},
		)}
	}
	findings := []domain.Finding{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".md") {
			continue
		}
		relativePath := filepath.ToSlash(filepath.Join(".serena", "memories", entry.Name()))
		if !memoryNamePattern.MatchString(entry.Name()) {
			findings = append(findings, memoryFinding(
				"GDS_MEMORY_NAME_INVALID",
				"Memory filename must be a semantic lowercase kebab-case name.",
				map[string]any{"path": relativePath},
			))
			continue
		}
		metadata, body, itemFindings := validateFile(
			root, filepath.Join(memoryRoot, entry.Name()), relativePath, schemas,
		)
		findings = append(findings, itemFindings...)
		if metadata.ScopeID == "" {
			continue
		}
		if len(commitTime) > 0 && commitTime[0] != nil {
			findings = append(findings, verifiedAtOrderingFindings(relativePath, metadata, commitTime[0])...)
		}
		report.Items = append(report.Items, Item{
			Name: strings.TrimSuffix(entry.Name(), ".md"), Path: relativePath,
			Status: metadata.Status, Visibility: metadata.Visibility,
			SourceDigest: metadata.SourceDigest, SourceState: metadata.SourceState,
		})
		report.Statuses[metadata.Status]++
		_ = body
	}
	sort.Slice(report.Items, func(left, right int) bool {
		return report.Items[left].Name < report.Items[right].Name
	})
	report.Count = len(report.Items)
	if report.Count == 0 {
		findings = append(findings, memoryFinding(
			"GDS_MEMORY_SET_EMPTY", "No valid provenance-bearing Serena memory exists.",
			map[string]any{"path": report.MemoryRoot},
		))
	}
	sortFindings(findings)
	return report, findings
}

// verifiedAtOrderingFindings flags a human-verified memory whose verified_at is
// earlier than the commit it declares as its source — an impossible order that
// means the memory was re-stamped to a newer source commit without a fresh
// verification (RVR-P2-006). Skipped when the commit cannot be resolved.
func verifiedAtOrderingFindings(relativePath string, metadata Metadata, commitTime CommitTimeFunc) []domain.Finding {
	if metadata.Status != "verified" || metadata.SourceCommit == "" || metadata.VerifiedAt == "" {
		return nil
	}
	committed, ok := commitTime(metadata.SourceCommit)
	if !ok {
		return nil
	}
	verified, err := time.Parse(time.RFC3339, metadata.VerifiedAt)
	if err != nil {
		return nil
	}
	if verified.Before(committed) {
		return []domain.Finding{memoryFinding(
			"GDS_MEMORY_VERIFIED_AT_PRECEDES_SOURCE",
			"Memory verified_at precedes its source commit; re-verify and re-stamp after the source commit.",
			map[string]any{
				"path":                relativePath,
				"verified_at":         metadata.VerifiedAt,
				"source_commit":       metadata.SourceCommit,
				"source_committed_at": committed.UTC().Format(time.RFC3339),
			},
		)}
	}
	return nil
}

func validateFile(
	root string,
	path string,
	relativePath string,
	schemas *validation.Set,
) (Metadata, []byte, []domain.Finding) {
	metadata, body, findings := parseFile(path, relativePath, schemas)
	if metadata.ScopeID == "" {
		return metadata, body, findings
	}
	expectedDigest, err := DigestSources(root, metadata.Sources)
	if err != nil {
		findings = append(findings, memoryFinding(
			"GDS_MEMORY_SOURCE_NOT_PROVEN", "Cannot verify one or more memory sources.",
			map[string]any{"path": relativePath, "error": err.Error()},
		))
	}
	// A body that changed after its digest was recorded is a body no review
	// covers, however current its sources are.
	if metadata.BodyDigest != "" && metadata.BodyDigest != DigestBody(body) {
		findings = append(findings, memoryFinding(
			"GDS_MEMORY_BODY_DIGEST_MISMATCH",
			"Memory body differs from the body its status was recorded against.",
			map[string]any{
				"path": relativePath, "expected": DigestBody(body),
				"observed": metadata.BodyDigest,
			},
		))
	}
	if err == nil && metadata.SourceDigest != expectedDigest {
		findings = append(findings, memoryFinding(
			"GDS_MEMORY_SOURCE_DIGEST_MISMATCH",
			"Memory source digest differs from current source files.",
			map[string]any{
				"path": relativePath, "expected": expectedDigest,
				"observed": metadata.SourceDigest,
			},
		))
	}
	sortFindings(findings)
	return metadata, body, findings
}

func parseFile(
	path string,
	relativePath string,
	schemas *validation.Set,
) (Metadata, []byte, []domain.Finding) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() > maxMemoryBytes {
		return Metadata{}, nil, []domain.Finding{memoryFinding(
			"GDS_MEMORY_FILE_INVALID", "Memory must be a bounded regular file.",
			map[string]any{"path": relativePath, "error": errorText(err)},
		)}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return Metadata{}, nil, []domain.Finding{memoryFinding(
			"GDS_MEMORY_FILE_READ_FAILED", "Cannot read Serena memory.",
			map[string]any{"path": relativePath, "error": err.Error()},
		)}
	}
	frontmatter, body, err := splitFrontmatter(raw)
	if err != nil {
		return Metadata{}, nil, []domain.Finding{memoryFinding(
			"GDS_MEMORY_FRONTMATTER_INVALID", "Cannot parse memory frontmatter.",
			map[string]any{"path": relativePath, "error": err.Error()},
		)}
	}
	value, err := serialization.Decode(relativePath+".yaml", frontmatter)
	if err != nil {
		return Metadata{}, body, []domain.Finding{memoryFinding(
			"GDS_MEMORY_FRONTMATTER_INVALID", "Cannot decode memory frontmatter.",
			map[string]any{"path": relativePath, "error": err.Error()},
		)}
	}
	findings := schemas.Validate("memory-metadata", value, relativePath)
	var metadata Metadata
	if err := serialization.DecodeInto(relativePath+".yaml", frontmatter, &metadata); err != nil {
		findings = append(findings, memoryFinding(
			"GDS_MEMORY_FRONTMATTER_INVALID", "Cannot bind memory metadata.",
			map[string]any{"path": relativePath, "error": err.Error()},
		))
		return Metadata{}, body, findings
	}
	if metadata.Status == "verified" && metadata.SourceState != "committed" {
		findings = append(findings, memoryFinding(
			"GDS_MEMORY_STATUS_INVALID",
			"A verified memory must refer to committed source state.",
			map[string]any{"path": relativePath, "source_state": metadata.SourceState},
		))
	}
	for _, section := range requiredBodySections {
		if !bytes.Contains(body, []byte("\n"+section+"\n")) &&
			!bytes.HasPrefix(body, []byte(section+"\n")) {
			findings = append(findings, memoryFinding(
				"GDS_MEMORY_SECTION_MISSING", "Memory is missing a required section.",
				map[string]any{"path": relativePath, "section": section},
			))
		}
	}
	// Verify that the body ## Sources section and the frontmatter sources: list
	// agree as sets. A divergence means a source was added to one but not the
	// other — the digest only covers the frontmatter list, so a body-only entry
	// is not protected by the digest, and a frontmatter-only entry is invisible
	// to a reader of the body (RVR2-P3-003).
	if bodySources := extractBodySources(body); bodySources != nil {
		frontSet := make(map[string]bool, len(metadata.Sources))
		for _, source := range metadata.Sources {
			frontSet[source] = true
		}
		bodySet := make(map[string]bool, len(bodySources))
		for _, source := range bodySources {
			bodySet[source] = true
		}
		var extra, missing []string
		for source := range bodySet {
			if !frontSet[source] {
				extra = append(extra, source)
			}
		}
		for source := range frontSet {
			if !bodySet[source] {
				missing = append(missing, source)
			}
		}
		if len(extra) != 0 || len(missing) != 0 {
			sort.Strings(extra)
			sort.Strings(missing)
			findings = append(findings, memoryFinding(
				"GDS_MEMORY_SOURCE_LIST_DIVERGENCE",
				"Memory body Sources section and frontmatter sources list differ.",
				map[string]any{
					"path": relativePath, "body_only": extra, "frontmatter_only": missing,
				},
			))
		}
	}
	if !bytes.HasPrefix(bytes.TrimSpace(body), []byte("# ")) {
		findings = append(findings, memoryFinding(
			"GDS_MEMORY_TITLE_MISSING", "Memory body must start with one title.",
			map[string]any{"path": relativePath},
		))
	}
	sortFindings(findings)
	return metadata, body, findings
}

func Read(root string, name string, schemas *validation.Set) (Document, []domain.Finding) {
	filename, relativePath, finding := resolveName(name)
	if finding != nil {
		return Document{}, []domain.Finding{*finding}
	}
	metadata, body, findings := validateFile(
		root, filepath.Join(root, ".serena", "memories", filename), relativePath, schemas,
	)
	return Document{
		Name: strings.TrimSuffix(filename, ".md"), Path: relativePath,
		Metadata: metadata, Body: string(body),
	}, findings
}

// Generate builds a deterministic candidate from an existing memory body and
// current committed sources. It never writes the candidate to disk.
// Verify records that the body under name was read against its current sources
// and still holds. It is the only supported way to produce a verified memory.
//
// Without it the sole route is hand-editing status and verified_at in a
// digest-bearing file. That is the shape this package exists to prevent: a
// stale approval riding a body nobody re-read, indistinguishable from an
// honest one. Making the honest action a command keeps provenance out of an
// editor.
//
// It refuses whatever Generate refuses, so verify can never paper over a
// memory that is not already provable -- it only records a decision about one
// that is. It also refuses to date a verification before the source commit it
// claims to have read, which is the same rule the validator enforces.
func Verify(
	root string,
	name string,
	sourceCommit string,
	committedAt time.Time,
	now time.Time,
	schemas *validation.Set,
) (Candidate, []domain.Finding) {
	if !now.After(committedAt) {
		return Candidate{}, []domain.Finding{memoryFinding(
			"GDS_MEMORY_VERIFIED_AT_PRECEDES_SOURCE",
			"Memory verified_at precedes its source commit; re-verify and re-stamp after the source commit.",
			map[string]any{
				"name":                name,
				"verified_at":         now.UTC().Format(time.RFC3339),
				"source_committed_at": committedAt.UTC().Format(time.RFC3339),
			},
		)}
	}
	stamp := now.UTC().Format(time.RFC3339)
	return generate(root, name, sourceCommit, schemas, &stamp)
}

func Generate(
	root string,
	name string,
	sourceCommit string,
	schemas *validation.Set,
) (Candidate, []domain.Finding) {
	return generate(root, name, sourceCommit, schemas, nil)
}

// generate builds the candidate. A non-nil verifiedAt additionally stamps the
// verified label, applied after the freshness demotion below so a verification
// is always recorded against provenance that is already current.
func generate(
	root string,
	name string,
	sourceCommit string,
	schemas *validation.Set,
	verifiedAt *string,
) (Candidate, []domain.Finding) {
	filename, relativePath, finding := resolveName(name)
	if finding != nil {
		return Candidate{}, []domain.Finding{*finding}
	}
	metadata, body, findings := parseFile(
		filepath.Join(root, ".serena", "memories", filename), relativePath, schemas,
	)
	if len(findings) != 0 {
		return Candidate{}, findings
	}
	sourceDigest, err := DigestSources(root, metadata.Sources)
	if err != nil {
		return Candidate{}, []domain.Finding{memoryFinding(
			"GDS_MEMORY_SOURCE_NOT_PROVEN", "Cannot build memory candidate from its sources.",
			map[string]any{"path": relativePath, "error": err.Error()},
		)}
	}
	if metadata.SourceCommit != sourceCommit || metadata.SourceDigest != sourceDigest ||
		metadata.SourceState != "committed" {
		metadata.Status = "generated-unverified"
	}
	metadata.SourceCommit = sourceCommit
	metadata.SourceState = "committed"
	metadata.SourceDigest = sourceDigest
	// Re-stamping provenance does not review prose, so the body digest is only
	// recorded, never advanced past a body someone has not looked at: it binds
	// the label to these exact bytes.
	metadata.BodyDigest = DigestBody(body)
	if verifiedAt != nil {
		metadata.Status = "verified"
		metadata.VerifiedAt = *verifiedAt
	}
	content, err := encode(metadata, body)
	if err != nil {
		return Candidate{}, []domain.Finding{memoryFinding(
			"GDS_MEMORY_CANDIDATE_INVALID", "Cannot encode a deterministic memory candidate.",
			map[string]any{"path": relativePath, "error": err.Error()},
		)}
	}
	frontmatter, _, err := splitFrontmatter(content)
	if err != nil {
		return Candidate{}, []domain.Finding{memoryFinding(
			"GDS_MEMORY_CANDIDATE_INVALID", "Cannot split the generated memory candidate.",
			map[string]any{"path": relativePath, "error": err.Error()},
		)}
	}
	value, err := serialization.Decode(relativePath+".yaml", frontmatter)
	if err != nil {
		return Candidate{}, []domain.Finding{memoryFinding(
			"GDS_MEMORY_CANDIDATE_INVALID", "Cannot decode the generated memory frontmatter.",
			map[string]any{"path": relativePath, "error": err.Error()},
		)}
	}
	if schemaFindings := schemas.Validate("memory-metadata", value, relativePath); len(schemaFindings) != 0 {
		return Candidate{}, schemaFindings
	}
	return Candidate{
		Name: strings.TrimSuffix(filename, ".md"), Path: relativePath,
		Metadata: metadata, Content: string(content),
		OutputDigest: fmt.Sprintf("sha256:%x", sha256.Sum256(content)),
	}, nil
}

func SourcePaths(
	root string,
	name string,
	schemas *validation.Set,
) ([]string, []domain.Finding) {
	filename, relativePath, finding := resolveName(name)
	if finding != nil {
		return nil, []domain.Finding{*finding}
	}
	metadata, _, findings := parseFile(
		filepath.Join(root, ".serena", "memories", filename), relativePath, schemas,
	)
	return append([]string(nil), metadata.Sources...), findings
}

func resolveName(name string) (string, string, *domain.Finding) {
	trimmed := strings.TrimSpace(name)
	if !strings.HasSuffix(trimmed, ".md") {
		trimmed += ".md"
	}
	if !memoryNamePattern.MatchString(trimmed) {
		finding := memoryFinding(
			"GDS_MEMORY_NAME_INVALID", "Memory name must be lowercase kebab-case.",
			map[string]any{"name": name},
		)
		return "", "", &finding
	}
	return trimmed, filepath.ToSlash(filepath.Join(".serena", "memories", trimmed)), nil
}

func encode(metadata Metadata, body []byte) ([]byte, error) {
	var frontmatter bytes.Buffer
	encoder := yaml.NewEncoder(&frontmatter)
	encoder.SetIndent(2)
	if err := encoder.Encode(metadata); err != nil {
		return nil, err
	}
	if err := encoder.Close(); err != nil {
		return nil, err
	}
	content := append([]byte("---\n"), frontmatter.Bytes()...)
	content = append(content, []byte("---\n")...)
	content = append(content, body...)
	return content, nil
}

// DigestBody digests one memory body exactly as stored, so a recorded review
// binds to bytes rather than to a timestamp.
func DigestBody(body []byte) string {
	sum := sha256.Sum256(body)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func DigestSources(root string, sources []string) (string, error) {
	records := make([]sourceRecord, 0, len(sources))
	for _, source := range sources {
		clean := filepath.Clean(filepath.FromSlash(source))
		if clean == "." || filepath.IsAbs(clean) || clean == ".." ||
			strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return "", fmt.Errorf("source path escapes repository: %s", source)
		}
		path := filepath.Join(root, clean)
		info, err := os.Lstat(path)
		if err != nil {
			return "", fmt.Errorf("inspect source %s: %w", source, err)
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("source is not a regular file: %s", source)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("read source %s: %w", source, err)
		}
		records = append(records, sourceRecord{
			Path:   filepath.ToSlash(clean),
			Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(raw)),
		})
	}
	sort.Slice(records, func(left, right int) bool { return records[left].Path < records[right].Path })
	return canonicaljson.Digest(records)
}

func splitFrontmatter(raw []byte) ([]byte, []byte, error) {
	if !bytes.HasPrefix(raw, []byte("---\n")) {
		return nil, nil, fmt.Errorf("opening delimiter is missing")
	}
	end := bytes.Index(raw[4:], []byte("\n---\n"))
	if end < 0 {
		return nil, nil, fmt.Errorf("closing delimiter is missing")
	}
	end += 4
	return raw[4:end], raw[end+5:], nil
}

func memoryFinding(code, message string, evidence map[string]any) domain.Finding {
	return domain.Finding{
		Code: code, Severity: domain.SeverityHigh, Message: message, Evidence: evidence,
	}
}

// extractBodySources parses the `## Sources` section of a memory body and
// returns the backtick-quoted source paths it lists, or nil if the section is
// absent. Only entries enclosed in backticks are recognized, matching the
// convention used across all current memories.
func extractBodySources(body []byte) []string {
	text := string(body)
	headerIndex := strings.Index(text, "\n## Sources\n")
	if headerIndex < 0 {
		headerIndex = strings.Index(text, "\n## Sources ")
	}
	if headerIndex < 0 {
		return nil
	}
	rest := text[headerIndex:]
	nextSection := strings.Index(rest[1:], "\n## ")
	var section string
	if nextSection < 0 {
		section = rest
	} else {
		section = rest[:nextSection+1]
	}
	var sources []string
	for _, line := range strings.Split(section, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		entry := strings.TrimSpace(strings.TrimPrefix(line, "- "))
		if start := strings.Index(entry, "`"); start >= 0 {
			if end := strings.Index(entry[start+1:], "`"); end >= 0 {
				sources = append(sources, entry[start+1:start+1+end])
			}
		}
	}
	return sources
}

func sortFindings(findings []domain.Finding) {
	sort.SliceStable(findings, func(left, right int) bool {
		if findings[left].Code == findings[right].Code {
			return fmt.Sprint(findings[left].Evidence) < fmt.Sprint(findings[right].Evidence)
		}
		return findings[left].Code < findings[right].Code
	})
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
