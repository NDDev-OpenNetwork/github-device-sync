// Package estateregistry owns the device-local locator for the trusted GDS
// control plane. The locator is not estate authority; it binds one physical
// checkout to its stable repository identity and exact repository-anchor
// digest so context resolution can fail closed when the checkout moves or
// changes identity.
package estateregistry

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/materialize"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

const (
	FileName          = "estate-registration.json"
	MaterializeAction = "materialize-estate-registration"
	maxFileBytes      = 64 << 10
)

type EnvironmentLookup func(string) string
type HomeResolver func() (string, error)

type Document struct {
	SchemaVersion int    `json:"schema_version"`
	DeviceID      string `json:"device_id"`
	Estate        Estate `json:"estate"`
}

type Estate struct {
	RepositoryID string `json:"repository_id"`
	Root         string `json:"root"`
	AnchorDigest string `json:"anchor_digest"`
}

type Candidate struct {
	Document Document `json:"document"`
	Raw      []byte   `json:"-"`
	Digest   string   `json:"digest"`
}

type FileObservation struct {
	Path          string `json:"path"`
	State         string `json:"state"`
	ContentDigest string `json:"content_digest,omitempty"`
	Mode          string `json:"mode,omitempty"`
	Digest        string `json:"digest"`
}

type Evidence struct {
	File FileObservation `json:"file"`
}

func DefaultPath(getenv EnvironmentLookup, homeResolver HomeResolver) (string, error) {
	if getenv == nil || homeResolver == nil {
		return "", errors.New("estate registration environment resolvers are required")
	}
	configHome := strings.TrimSpace(getenv("XDG_CONFIG_HOME"))
	if configHome == "" {
		home, err := homeResolver()
		if err != nil || !filepath.IsAbs(home) {
			return "", errors.New("HOME cannot be resolved to an absolute path")
		}
		configHome = filepath.Join(home, ".config")
	}
	if !filepath.IsAbs(configHome) || filepath.Clean(configHome) != configHome {
		return "", errors.New("XDG_CONFIG_HOME must be an absolute clean path")
	}
	return filepath.Join(configHome, "github-device-sync", FileName), nil
}

func ResolvePath(requested string, getenv EnvironmentLookup, homeResolver HomeResolver) (string, error) {
	if strings.TrimSpace(requested) == "" {
		return DefaultPath(getenv, homeResolver)
	}
	if !filepath.IsAbs(requested) || filepath.Clean(requested) != requested {
		return "", errors.New("estate registration path must be absolute and clean")
	}
	return requested, nil
}

func NewCandidate(
	deviceID string,
	repositoryID string,
	root string,
	anchorDigest string,
	schemas *validation.Set,
) (Candidate, []domain.Finding) {
	physical, err := physicalDirectory(root)
	if err != nil {
		return Candidate{}, []domain.Finding{finding(
			"GDS_ESTATE_REGISTRATION_ROOT_INVALID", err.Error(), root,
		)}
	}
	document := Document{
		SchemaVersion: 1,
		DeviceID:      deviceID,
		Estate: Estate{
			RepositoryID: repositoryID,
			Root:         physical,
			AnchorDigest: anchorDigest,
		},
	}
	raw, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return Candidate{}, []domain.Finding{finding(
			"GDS_ESTATE_REGISTRATION_ENCODE_FAILED", err.Error(), root,
		)}
	}
	raw = append(raw, '\n')
	return DecodeCandidate(FileName, raw, schemas)
}

func DecodeCandidate(path string, raw []byte, schemas *validation.Set) (Candidate, []domain.Finding) {
	if schemas == nil {
		return Candidate{}, []domain.Finding{finding(
			"GDS_ESTATE_REGISTRATION_SCHEMA_UNAVAILABLE", "Schema set is unavailable.", path,
		)}
	}
	if len(raw) == 0 || len(raw) > maxFileBytes {
		return Candidate{}, []domain.Finding{finding(
			"GDS_ESTATE_REGISTRATION_SIZE_INVALID", "Estate registration is empty or too large.", path,
		)}
	}
	value, err := serialization.Decode(path, raw)
	if err != nil {
		return Candidate{}, []domain.Finding{finding(
			"GDS_ESTATE_REGISTRATION_INVALID", err.Error(), path,
		)}
	}
	if findings := schemas.Validate("estate-registration", value, path); len(findings) != 0 {
		return Candidate{}, findings
	}
	var document Document
	if err := serialization.DecodeInto(path, raw, &document); err != nil {
		return Candidate{}, []domain.Finding{finding(
			"GDS_ESTATE_REGISTRATION_INVALID", err.Error(), path,
		)}
	}
	physical, err := physicalDirectory(document.Estate.Root)
	if err != nil || physical != document.Estate.Root {
		message := "Estate root is not a canonical physical directory."
		if err != nil {
			message = err.Error()
		}
		return Candidate{}, []domain.Finding{finding(
			"GDS_ESTATE_REGISTRATION_ROOT_INVALID", message, document.Estate.Root,
		)}
	}
	return Candidate{
		Document: document,
		Raw:      append([]byte(nil), raw...),
		Digest:   digest(raw),
	}, nil
}

func Load(path string, schemas *validation.Set) (Candidate, []domain.Finding) {
	evidence, err := Observe(path)
	if err != nil {
		return Candidate{}, []domain.Finding{finding(
			"GDS_ESTATE_REGISTRATION_UNREADABLE", err.Error(), path,
		)}
	}
	if evidence.File.State == "missing" {
		return Candidate{}, []domain.Finding{finding(
			"GDS_ESTATE_REGISTRATION_MISSING", "No device-local estate registration exists.", path,
		)}
	}
	raw, err := readStableRegular(path)
	if err != nil {
		return Candidate{}, []domain.Finding{finding(
			"GDS_ESTATE_REGISTRATION_UNREADABLE", err.Error(), path,
		)}
	}
	candidate, findings := DecodeCandidate(path, raw, schemas)
	if len(findings) == 0 && candidate.Digest != evidence.File.ContentDigest {
		return Candidate{}, []domain.Finding{finding(
			"GDS_ESTATE_REGISTRATION_CHANGED", "Estate registration changed while it was inspected.", path,
		)}
	}
	return candidate, findings
}

func Observe(path string) (Evidence, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return Evidence{}, errors.New("estate registration path must be absolute and clean")
	}
	if _, _, err := materializationLocation(path); err != nil {
		return Evidence{}, err
	}
	info, err := os.Lstat(path)
	observation := FileObservation{Path: path}
	switch {
	case errors.Is(err, os.ErrNotExist):
		observation.State = "missing"
	case err != nil:
		return Evidence{}, fmt.Errorf("inspect estate registration: %w", err)
	case info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular():
		return Evidence{}, errors.New("estate registration must be a regular non-symlink file")
	case info.Size() > maxFileBytes:
		return Evidence{}, errors.New("estate registration exceeds the size limit")
	default:
		raw, readErr := readStableRegular(path)
		if readErr != nil {
			return Evidence{}, readErr
		}
		observation.State = "regular"
		observation.ContentDigest = digest(raw)
		observation.Mode = info.Mode().Perm().String()
	}
	observation.Digest, err = canonicaljson.Digest(struct {
		Path          string `json:"path"`
		State         string `json:"state"`
		ContentDigest string `json:"content_digest,omitempty"`
		Mode          string `json:"mode,omitempty"`
	}{observation.Path, observation.State, observation.ContentDigest, observation.Mode})
	if err != nil {
		return Evidence{}, err
	}
	return Evidence{File: observation}, nil
}

func Parameters(path string, before FileObservation, candidate Candidate) (map[string]any, error) {
	root, relative, err := materializationLocation(path)
	if err != nil {
		return nil, err
	}
	values := map[string]any{
		"path":                        path,
		"materialization_root":        root,
		"relative_path":               relative,
		"expected_state":              before.State,
		"expected_observation_digest": before.Digest,
		"repository_id":               candidate.Document.Estate.RepositoryID,
		"content":                     string(candidate.Raw),
		"content_digest":              candidate.Digest,
	}
	if before.ContentDigest != "" {
		values["expected_content_digest"] = before.ContentDigest
	}
	return map[string]any{"estate_registration": values}, nil
}

type Handler struct {
	schemas *validation.Set
}

func NewHandler(schemas *validation.Set) (*Handler, error) {
	if schemas == nil {
		return nil, errors.New("estate registration handler requires schemas")
	}
	return &Handler{schemas: schemas}, nil
}

func (handler *Handler) Apply(_ context.Context, step operations.Step) (operations.ApplyEvidence, error) {
	parameters, err := parameters(step)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	if _, findings := DecodeCandidate(parameters.Path, []byte(parameters.Content), handler.schemas); len(findings) != 0 {
		return operations.ApplyEvidence{}, errors.New("estate registration content failed semantic validation")
	}
	before, err := Observe(parameters.Path)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	if before.File.State != parameters.ExpectedState ||
		before.File.ContentDigest != parameters.ExpectedContentDigest ||
		before.File.Digest != parameters.ExpectedObservationDigest {
		return operations.ApplyEvidence{Before: before}, errors.New("estate registration precondition changed")
	}
	set, err := materialize.NewSet(parameters.MaterializationRoot, []materialize.File{{
		Path: parameters.RelativePath, Content: []byte(parameters.Content), Digest: parameters.ContentDigest,
	}})
	if err != nil {
		return operations.ApplyEvidence{Before: before}, err
	}
	if _, _, err := set.Apply(); err != nil {
		return operations.ApplyEvidence{Before: before}, err
	}
	after, err := Observe(parameters.Path)
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
	var expected Evidence
	if len(afterRaw) == 0 || json.Unmarshal(afterRaw, &expected) != nil {
		return errors.New("estate registration after evidence is missing or invalid")
	}
	observed, err := Observe(parameters.Path)
	if err != nil {
		return err
	}
	if expected.File != observed.File || observed.File.State != "regular" ||
		observed.File.ContentDigest != parameters.ContentDigest {
		return errors.New("estate registration no longer matches recorded evidence")
	}
	return nil
}

func StepCandidate(step operations.Step, schemas *validation.Set) (string, Candidate, error) {
	parameters, err := parameters(step)
	if err != nil {
		return "", Candidate{}, err
	}
	candidate, findings := DecodeCandidate(parameters.Path, []byte(parameters.Content), schemas)
	if len(findings) != 0 || candidate.Document.Estate.RepositoryID != step.RepositoryID {
		return "", Candidate{}, errors.New("estate registration step candidate is invalid")
	}
	return parameters.Path, candidate, nil
}

type stepParameters struct {
	Path                      string
	MaterializationRoot       string
	RelativePath              string
	ExpectedState             string
	ExpectedContentDigest     string
	ExpectedObservationDigest string
	RepositoryID              string
	Content                   string
	ContentDigest             string
}

func parameters(step operations.Step) (stepParameters, error) {
	if step.Action != MaterializeAction {
		return stepParameters{}, fmt.Errorf("unexpected estate registration action %q", step.Action)
	}
	raw, ok := step.Parameters["estate_registration"].(map[string]any)
	if !ok {
		return stepParameters{}, errors.New("estate registration parameters are missing")
	}
	result := stepParameters{}
	result.Path, _ = raw["path"].(string)
	result.MaterializationRoot, _ = raw["materialization_root"].(string)
	result.RelativePath, _ = raw["relative_path"].(string)
	result.ExpectedState, _ = raw["expected_state"].(string)
	result.ExpectedContentDigest, _ = raw["expected_content_digest"].(string)
	result.ExpectedObservationDigest, _ = raw["expected_observation_digest"].(string)
	result.RepositoryID, _ = raw["repository_id"].(string)
	result.Content, _ = raw["content"].(string)
	result.ContentDigest, _ = raw["content_digest"].(string)
	root, relative, locationErr := materializationLocation(result.Path)
	storedTarget := filepath.Clean(filepath.Join(
		result.MaterializationRoot, filepath.FromSlash(result.RelativePath),
	))
	observedTarget := filepath.Clean(filepath.Join(root, filepath.FromSlash(relative)))
	if result.Path == "" || locationErr != nil || !validMaterializationRoot(result.MaterializationRoot) ||
		!validRelativePath(result.RelativePath) || storedTarget != observedTarget ||
		(result.ExpectedState != "missing" && result.ExpectedState != "regular") ||
		result.ExpectedObservationDigest == "" || result.RepositoryID != step.RepositoryID ||
		result.Content == "" || len(result.Content) > maxFileBytes ||
		result.ContentDigest != digest([]byte(result.Content)) {
		return stepParameters{}, errors.New("estate registration parameters are invalid")
	}
	if result.ExpectedState == "regular" && result.ExpectedContentDigest == "" {
		return stepParameters{}, errors.New("regular estate registration requires its exact digest")
	}
	if result.ExpectedState == "missing" && result.ExpectedContentDigest != "" {
		return stepParameters{}, errors.New("missing estate registration cannot have a content digest")
	}
	return result, nil
}

func validMaterializationRoot(root string) bool {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return false
	}
	info, err := os.Lstat(root)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false
	}
	physical, err := filepath.EvalSymlinks(root)
	return err == nil && filepath.Clean(physical) == root
}

func validRelativePath(relative string) bool {
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(relative)))
	return clean == relative && clean != "." && !filepath.IsAbs(clean) && clean != ".." &&
		!strings.HasPrefix(clean, "../")
}

func materializationLocation(path string) (string, string, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", "", errors.New("estate registration path must be absolute and clean")
	}
	current := filepath.Dir(path)
	for {
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
				return "", "", errors.New("estate registration ancestor must be a real directory")
			}
			physical, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", "", errors.New("estate registration ancestor cannot be resolved")
			}
			physicalInfo, err := os.Lstat(physical)
			if err != nil || physicalInfo.Mode()&os.ModeSymlink != 0 || !physicalInfo.IsDir() {
				return "", "", errors.New("resolved estate registration ancestor must be a real directory")
			}
			relative, err := filepath.Rel(current, path)
			if err != nil || relative == "." || filepath.IsAbs(relative) || relative == ".." ||
				strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return "", "", errors.New("estate registration path escapes its materialization root")
			}
			return filepath.Clean(physical), filepath.ToSlash(relative), nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", "", fmt.Errorf("inspect estate registration ancestor: %w", err)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", "", errors.New("no safe estate registration ancestor exists")
		}
		current = parent
	}
}

func physicalDirectory(path string) (string, error) {
	if !filepath.IsAbs(path) {
		return "", errors.New("estate root must be absolute")
	}
	physical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", fmt.Errorf("resolve estate root: %w", err)
	}
	info, err := os.Lstat(physical)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", errors.New("estate root must be a real directory")
	}
	return filepath.Clean(physical), nil
}

func readStableRegular(path string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() {
		return nil, errors.New("estate registration is not a readable regular file")
	}
	if before.Size() > maxFileBytes {
		return nil, errors.New("estate registration exceeds the size limit")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, errors.New("estate registration changed during open")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxFileBytes+1))
	if err != nil || len(raw) > maxFileBytes {
		return nil, errors.New("estate registration cannot be read within its size limit")
	}
	after, err := file.Stat()
	if err != nil || !os.SameFile(opened, after) || opened.Size() != after.Size() ||
		!opened.ModTime().Equal(after.ModTime()) {
		return nil, errors.New("estate registration changed while it was read")
	}
	return raw, nil
}

func digest(raw []byte) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
}

func finding(code string, message string, path string) domain.Finding {
	return domain.Finding{
		Code: code, Severity: domain.SeverityHigh, Message: message,
		Evidence: map[string]any{"path": path},
	}
}
