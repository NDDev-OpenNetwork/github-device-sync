// Package anchor validates and materializes repository-owned GDS anchors.
package anchor

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v4"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/materialize"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

const (
	Path              = ".gds/repository.yaml"
	MaterializeAction = "materialize-repository-anchor"
	maxAnchorBytes    = 512 << 10
	// The schema bounds a relationship's shape but not how many an anchor may
	// declare, and this function is exported. A caller handing it an unbounded
	// set would size an allocation from a number nothing checked. The estate's
	// largest consumer declares five.
	maxAnchorRelationships = 1024
)

type Candidate struct {
	Anchor domain.RepositoryAnchor `json:"anchor"`
	Raw    []byte                  `json:"-"`
	Digest string                  `json:"digest"`
}

type FileObservation struct {
	State         string `json:"state"`
	ContentDigest string `json:"content_digest,omitempty"`
	Digest        string `json:"digest"`
}

type Evidence struct {
	WorktreeRoot string          `json:"worktree_root"`
	File         FileObservation `json:"file"`
}

func DecodeCandidate(path string, raw []byte, schemas *validation.Set) (Candidate, []domain.Finding) {
	if len(raw) == 0 || len(raw) > maxAnchorBytes {
		return Candidate{}, []domain.Finding{candidateFinding(
			"GDS_ANCHOR_CANDIDATE_SIZE_INVALID", "Repository anchor candidate is empty or too large.",
		)}
	}
	value, err := serialization.Decode(path, raw)
	if err != nil {
		return Candidate{}, []domain.Finding{candidateFinding(
			"GDS_ANCHOR_CANDIDATE_INVALID", err.Error(),
		)}
	}
	if findings := schemas.Validate("repository", value, path); len(findings) != 0 {
		return Candidate{}, findings
	}
	var decoded domain.RepositoryAnchor
	if err := serialization.DecodeInto(path, raw, &decoded); err != nil {
		return Candidate{}, []domain.Finding{candidateFinding(
			"GDS_ANCHOR_CANDIDATE_INVALID", err.Error(),
		)}
	}
	return Candidate{
		Anchor: decoded, Raw: append([]byte(nil), raw...), Digest: digest(raw),
	}, nil
}

func EncodeCandidate(value domain.RepositoryAnchor, schemas *validation.Set) (Candidate, []domain.Finding) {
	raw, err := yaml.Marshal(value)
	if err != nil {
		return Candidate{}, []domain.Finding{candidateFinding(
			"GDS_ANCHOR_CANDIDATE_ENCODE_FAILED", err.Error(),
		)}
	}
	return DecodeCandidate(Path, raw, schemas)
}

func Observe(root string) (Evidence, error) {
	physical, err := physicalRoot(root)
	if err != nil {
		return Evidence{}, err
	}
	path := filepath.Join(physical, filepath.FromSlash(Path))
	info, err := os.Lstat(path)
	observation := FileObservation{}
	switch {
	case errors.Is(err, os.ErrNotExist):
		observation.State = "missing"
	case err != nil:
		return Evidence{}, fmt.Errorf("inspect repository anchor: %w", err)
	case info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular():
		return Evidence{}, errors.New("repository anchor must be a regular non-symlink file")
	case info.Size() > maxAnchorBytes:
		return Evidence{}, errors.New("repository anchor exceeds the size limit")
	default:
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return Evidence{}, fmt.Errorf("read repository anchor: %w", readErr)
		}
		observation.State = "regular"
		observation.ContentDigest = digest(raw)
	}
	observation.Digest, err = canonicaljson.Digest(struct {
		State         string `json:"state"`
		ContentDigest string `json:"content_digest,omitempty"`
	}{observation.State, observation.ContentDigest})
	if err != nil {
		return Evidence{}, err
	}
	return Evidence{WorktreeRoot: physical, File: observation}, nil
}

func Parameters(root string, before FileObservation, candidate Candidate) map[string]any {
	parameters := map[string]any{
		"worktree_root":               root,
		"expected_state":              before.State,
		"expected_observation_digest": before.Digest,
		"repository_id":               candidate.Anchor.Repository.ID,
		"content":                     string(candidate.Raw),
		"content_digest":              candidate.Digest,
	}
	if before.ContentDigest != "" {
		parameters["expected_content_digest"] = before.ContentDigest
	}
	return map[string]any{"repository_anchor": parameters}
}

type Handler struct {
	schemas *validation.Set
}

func NewHandler(schemas *validation.Set) (*Handler, error) {
	if schemas == nil {
		return nil, errors.New("repository anchor handler requires schemas")
	}
	return &Handler{schemas: schemas}, nil
}

func (handler *Handler) Apply(_ context.Context, step operations.Step) (operations.ApplyEvidence, error) {
	parameters, err := parameters(step)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	if _, findings := DecodeCandidate(Path, []byte(parameters.Content), handler.schemas); len(findings) != 0 {
		return operations.ApplyEvidence{}, errors.New("repository anchor content failed semantic validation")
	}
	before, err := Observe(parameters.WorktreeRoot)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	if before.File.State != parameters.ExpectedState ||
		before.File.ContentDigest != parameters.ExpectedContentDigest ||
		before.File.Digest != parameters.ExpectedObservationDigest {
		return operations.ApplyEvidence{Before: before}, errors.New("repository anchor precondition changed")
	}
	set, err := materialize.NewSet(parameters.WorktreeRoot, []materialize.File{{
		Path: Path, Content: []byte(parameters.Content), Digest: parameters.ContentDigest,
	}})
	if err != nil {
		return operations.ApplyEvidence{Before: before}, err
	}
	if _, _, err := set.Apply(); err != nil {
		return operations.ApplyEvidence{Before: before}, err
	}
	after, err := Observe(parameters.WorktreeRoot)
	if err != nil {
		return operations.ApplyEvidence{Before: before}, err
	}
	return operations.ApplyEvidence{Before: before, After: after}, nil
}

func (handler *Handler) Verify(_ context.Context, step operations.Step, afterRaw json.RawMessage) error {
	parameters, err := parameters(step)
	if err != nil {
		return err
	}
	if _, findings := DecodeCandidate(Path, []byte(parameters.Content), handler.schemas); len(findings) != 0 {
		return errors.New("repository anchor content failed semantic validation")
	}
	var expected Evidence
	if len(afterRaw) == 0 || json.Unmarshal(afterRaw, &expected) != nil {
		return errors.New("repository anchor after evidence is missing or invalid")
	}
	observed, err := Observe(parameters.WorktreeRoot)
	if err != nil {
		return err
	}
	if expected != observed || observed.File.State != "regular" ||
		observed.File.ContentDigest != parameters.ContentDigest {
		return errors.New("repository anchor no longer matches recorded evidence")
	}
	return nil
}

func StepCandidate(step operations.Step, schemas *validation.Set) (string, Candidate, error) {
	parameters, err := parameters(step)
	if err != nil {
		return "", Candidate{}, err
	}
	candidate, findings := DecodeCandidate(Path, []byte(parameters.Content), schemas)
	if len(findings) != 0 || candidate.Anchor.Repository.ID != step.RepositoryID {
		return "", Candidate{}, errors.New("repository anchor step candidate is invalid")
	}
	return parameters.WorktreeRoot, candidate, nil
}

type stepParameters struct {
	WorktreeRoot              string
	ExpectedState             string
	ExpectedContentDigest     string
	ExpectedObservationDigest string
	RepositoryID              string
	Content                   string
	ContentDigest             string
}

func parameters(step operations.Step) (stepParameters, error) {
	if step.Action != MaterializeAction {
		return stepParameters{}, fmt.Errorf("unexpected repository anchor action %q", step.Action)
	}
	raw, ok := step.Parameters["repository_anchor"].(map[string]any)
	if !ok {
		return stepParameters{}, errors.New("repository anchor parameters are missing")
	}
	result := stepParameters{}
	result.WorktreeRoot, _ = raw["worktree_root"].(string)
	result.ExpectedState, _ = raw["expected_state"].(string)
	result.ExpectedContentDigest, _ = raw["expected_content_digest"].(string)
	result.ExpectedObservationDigest, _ = raw["expected_observation_digest"].(string)
	result.RepositoryID, _ = raw["repository_id"].(string)
	result.Content, _ = raw["content"].(string)
	result.ContentDigest, _ = raw["content_digest"].(string)
	if result.WorktreeRoot == "" || !filepath.IsAbs(result.WorktreeRoot) ||
		filepath.Clean(result.WorktreeRoot) != result.WorktreeRoot ||
		(result.ExpectedState != "missing" && result.ExpectedState != "regular") ||
		result.ExpectedObservationDigest == "" || result.RepositoryID != step.RepositoryID ||
		result.Content == "" || len(result.Content) > maxAnchorBytes ||
		result.ContentDigest != digest([]byte(result.Content)) {
		return stepParameters{}, errors.New("repository anchor parameters are invalid")
	}
	if result.ExpectedState == "regular" && result.ExpectedContentDigest == "" {
		return stepParameters{}, errors.New("regular repository anchor requires its exact digest")
	}
	if result.ExpectedState == "missing" && result.ExpectedContentDigest != "" {
		return stepParameters{}, errors.New("missing repository anchor cannot have a content digest")
	}
	return result, nil
}

func physicalRoot(root string) (string, error) {
	absolute, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(resolved)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("repository anchor root must be a real directory")
	}
	return filepath.Clean(resolved), nil
}

func digest(raw []byte) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
}

func candidateFinding(code string, message string) domain.Finding {
	return domain.Finding{Code: code, Severity: domain.SeverityHigh, Message: message}
}

// SpliceRelationships rewrites only the `relationships` block of an authored
// anchor and leaves every other byte exactly as written.
//
// EncodeCandidate marshals the whole domain struct, which cannot carry
// comments, so a relationship change through it silently deleted every comment
// in the file. Onboarding the fourth module cost fourteen lines that way,
// including the complete record of why this repository routes CI to a
// self-hosted fleet — the argument an open decision issue was still weighing.
// A canonical source that loses its reasoning on an unrelated edit stops being
// worth writing reasoning into.
//
// The parser locates the block and text replaces it, so comments, blank lines,
// key order and quoting style outside the block survive untouched. The result
// is decoded and schema-validated, and its relationships are compared against
// the intended set, so a splice that lands wrong fails instead of shipping.
func SpliceRelationships(
	raw []byte,
	updated domain.RepositoryAnchor,
	schemas *validation.Set,
) (Candidate, []domain.Finding) {
	if len(raw) == 0 || len(raw) > maxAnchorBytes {
		return Candidate{}, []domain.Finding{candidateFinding(
			"GDS_ANCHOR_CANDIDATE_SIZE_INVALID", "Repository anchor candidate is empty or too large.",
		)}
	}
	var document yaml.Node
	if err := yaml.Unmarshal(raw, &document); err != nil {
		return Candidate{}, []domain.Finding{candidateFinding(
			"GDS_ANCHOR_CANDIDATE_INVALID", err.Error(),
		)}
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return Candidate{}, []domain.Finding{candidateFinding(
			"GDS_ANCHOR_CANDIDATE_INVALID", "Repository anchor is not a single YAML mapping.",
		)}
	}
	if len(updated.Relationships) > maxAnchorRelationships {
		return Candidate{}, []domain.Finding{candidateFinding(
			"GDS_ANCHOR_RELATIONSHIP_COUNT_INVALID",
			"Repository anchor declares more relationships than the block may carry.",
		)}
	}
	lines := strings.Split(string(raw), "\n")
	start, end, found := relationshipBlockSpan(document.Content[0], len(lines))
	if !found {
		start, end = len(lines), len(lines)
	}
	replacement := renderRelationships(updated.Relationships)
	// Sized from the document alone. Adding the replacement length would compute
	// an allocation size from two independently bounded inputs, and append grows
	// the tail for free.
	spliced := make([]string, 0, len(lines))
	spliced = append(spliced, lines[:start]...)
	if !found && len(replacement) != 0 && start > 0 && strings.TrimSpace(lines[start-1]) != "" {
		spliced = append(spliced, "")
	}
	spliced = append(spliced, replacement...)
	spliced = append(spliced, lines[end:]...)

	candidate, findings := DecodeCandidate(Path, []byte(strings.Join(spliced, "\n")), schemas)
	if len(findings) != 0 {
		return Candidate{}, findings
	}
	// The whole anchor is compared, not just the block that moved. A splice is
	// only correct if it changed exactly what it claimed to change, and the
	// caller's intended anchor is the only thing that can say so.
	intended, err := canonicaljson.Digest(updated)
	produced, producedErr := canonicaljson.Digest(candidate.Anchor)
	if err != nil || producedErr != nil || intended != produced {
		return Candidate{}, []domain.Finding{candidateFinding(
			"GDS_ANCHOR_RELATIONSHIP_SPLICE_FAILED",
			"Rewriting the relationships block did not reproduce the intended anchor.",
		)}
	}
	return candidate, nil
}

// relationshipBlockSpan returns the half-open line range the `relationships`
// key and its value occupy. The end is the last line any node of the value
// touches rather than the line before the next key, so a comment written above
// the following key stays outside the replacement and survives.
func relationshipBlockSpan(mapping *yaml.Node, total int) (int, int, bool) {
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		key := mapping.Content[index]
		if key.Value != "relationships" {
			continue
		}
		start := key.Line - 1
		if key.HeadComment != "" {
			start -= strings.Count(key.HeadComment, "\n") + 1
		}
		end := maxNodeLine(mapping.Content[index+1])
		if end < key.Line {
			end = key.Line
		}
		if start < 0 {
			start = 0
		}
		if end > total {
			end = total
		}
		return start, end, true
	}
	return 0, 0, false
}

func maxNodeLine(node *yaml.Node) int {
	if node == nil {
		return 0
	}
	highest := node.Line
	for _, child := range node.Content {
		if line := maxNodeLine(child); line > highest {
			highest = line
		}
	}
	return highest
}

// renderRelationships writes the block in the estate's authored style: two
// space indentation and quoted scalars, matching every anchor in the tree.
func renderRelationships(relationships []domain.Relationship) []string {
	if len(relationships) == 0 {
		return nil
	}
	lines := []string{"relationships:"}
	for _, relationship := range relationships {
		lines = append(lines, "  - type: "+quoteScalar(relationship.Type))
		lines = append(lines, "    target: "+quoteScalar(relationship.Target))
		for _, optional := range []struct{ key, value string }{
			{"gitmodules_name", relationship.GitmodulesName},
			{"pin_management", relationship.PinManagement},
			{"materialization", relationship.Materialization},
		} {
			if optional.value != "" {
				lines = append(lines, "    "+optional.key+": "+quoteScalar(optional.value))
			}
		}
	}
	return lines
}

func quoteScalar(value string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value) + `"`
}
