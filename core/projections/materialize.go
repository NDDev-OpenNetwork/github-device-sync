package projections

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/materialize"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
)

const MaterializeAction = "materialize-projections"

type PlannedFile struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

type MaterializationSpec struct {
	InputDigest  string        `json:"input_digest"`
	OutputDigest string        `json:"output_digest"`
	Files        []PlannedFile `json:"files"`
}

type ObservedFile = materialize.ObservedFile

type Materializer struct {
	candidate Candidate
	files     *materialize.Set
}

func NewMaterializer(root string, candidate Candidate) (*Materializer, error) {
	files := make([]materialize.File, 0, len(candidate.Files))
	for _, file := range candidate.Files {
		files = append(files, materialize.File{
			Path: file.Path, Content: file.Content, Digest: file.Digest,
		})
	}
	set, err := materialize.NewSet(root, files)
	if err != nil {
		return nil, err
	}
	return &Materializer{candidate: candidate, files: set}, nil
}

func Spec(candidate Candidate) MaterializationSpec {
	files := make([]PlannedFile, 0, len(candidate.Files))
	for _, file := range candidate.Files {
		files = append(files, PlannedFile{Path: file.Path, Digest: file.Digest})
	}
	sort.Slice(files, func(left, right int) bool { return files[left].Path < files[right].Path })
	return MaterializationSpec{
		InputDigest: candidate.InputDigest, OutputDigest: candidate.OutputDigest, Files: files,
	}
}

func Parameters(candidate Candidate) map[string]any {
	raw, _ := json.Marshal(Spec(candidate))
	var value map[string]any
	_ = json.Unmarshal(raw, &value)
	return map[string]any{"projection": value}
}

func Fingerprint(root string, candidate Candidate) (string, error) {
	materializer, err := NewMaterializer(root, candidate)
	if err != nil {
		return "", err
	}
	return materializer.files.Fingerprint(candidate.InputDigest)
}

func (materializer *Materializer) Apply(
	_ context.Context,
	step operations.Step,
) (operations.ApplyEvidence, error) {
	if step.Action != MaterializeAction {
		return operations.ApplyEvidence{}, fmt.Errorf("unsupported projection action %q", step.Action)
	}
	if err := matchParameters(step.Parameters, materializer.candidate); err != nil {
		return operations.ApplyEvidence{}, err
	}
	before, after, err := materializer.files.Apply()
	return operations.ApplyEvidence{Before: before, After: after}, err
}

func (materializer *Materializer) Verify(
	_ context.Context,
	step operations.Step,
	_ json.RawMessage,
) error {
	if err := matchParameters(step.Parameters, materializer.candidate); err != nil {
		return err
	}
	return materializer.files.Verify()
}

func matchParameters(parameters map[string]any, candidate Candidate) error {
	rawProjection, found := parameters["projection"]
	if !found || len(parameters) != 1 {
		return errors.New("projection step parameters are missing or contain unknown fields")
	}
	raw, err := json.Marshal(rawProjection)
	if err != nil {
		return fmt.Errorf("encode projection step parameters: %w", err)
	}
	var observed MaterializationSpec
	if err := json.Unmarshal(raw, &observed); err != nil {
		return fmt.Errorf("decode projection step parameters: %w", err)
	}
	expected := Spec(candidate)
	expectedRaw, _ := json.Marshal(expected)
	observedRaw, _ := json.Marshal(observed)
	if string(expectedRaw) != string(observedRaw) {
		return errors.New("projection candidate differs from the approved step parameters")
	}
	return nil
}
