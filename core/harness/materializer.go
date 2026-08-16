package harness

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/materialize"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
)

const (
	MaterializeAdapterAction = "materialize-harness-adapter"
	UpdateAdapterAction      = "update-harness-adapter"
	RollbackAdapterAction    = "rollback-harness-adapter"
	RemoveAdapterAction      = "remove-harness-adapter"
)

type AdapterStepParameters struct {
	Operation               string        `json:"operation"`
	Harness                 string        `json:"harness"`
	TargetRoot              string        `json:"target_root"`
	SourceRoot              string        `json:"source_root,omitempty"`
	CandidateDigest         string        `json:"candidate_digest"`
	PreviousCandidateDigest string        `json:"previous_candidate_digest,omitempty"`
	Files                   []AdapterFile `json:"files"`
	PreviousFiles           []AdapterFile `json:"previous_files,omitempty"`
}

type AdapterMaterializer struct {
	target    string
	candidate AdapterCandidate
	set       *materialize.Set
}

func NewAdapterMaterializer(
	target string,
	candidate AdapterCandidate,
) (*AdapterMaterializer, error) {
	set, err := adapterMaterialization(target, candidate)
	if err != nil {
		return nil, err
	}
	return &AdapterMaterializer{target: target, candidate: candidate, set: set}, nil
}

func AdapterParameters(plan AdapterPlan) map[string]any {
	return map[string]any{
		"harness_adapter": AdapterStepParameters{
			Operation: plan.Operation, Harness: plan.Harness, TargetRoot: plan.TargetRoot,
			SourceRoot: plan.SourceRoot, CandidateDigest: plan.CandidateDigest,
			PreviousCandidateDigest: plan.PreviousCandidateDigest,
			Files:                   plan.Files, PreviousFiles: plan.PreviousFiles,
		},
	}
}

func CandidateFromParameters(parameters AdapterStepParameters) AdapterCandidate {
	contents := make(map[string][]byte, len(parameters.Files))
	for _, file := range parameters.Files {
		contents[file.Path] = nil
	}
	return AdapterCandidate{
		Harness: parameters.Harness, CandidateDigest: parameters.CandidateDigest,
		Files: append([]AdapterFile(nil), parameters.Files...), contents: contents,
	}
}

func PreviousCandidateFromParameters(parameters AdapterStepParameters) AdapterCandidate {
	contents := make(map[string][]byte, len(parameters.PreviousFiles))
	for _, file := range parameters.PreviousFiles {
		contents[file.Path] = nil
	}
	return AdapterCandidate{
		Harness: parameters.Harness, CandidateDigest: parameters.PreviousCandidateDigest,
		Files: append([]AdapterFile(nil), parameters.PreviousFiles...), contents: contents,
	}
}

func DecodeAdapterParameters(step operations.Step) (AdapterStepParameters, error) {
	var parameters AdapterStepParameters
	value, found := step.Parameters["harness_adapter"]
	if !found {
		return parameters, errors.New("adapter step parameters are missing harness_adapter")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return parameters, err
	}
	if err := json.Unmarshal(raw, &parameters); err != nil {
		return parameters, err
	}
	if parameters.Operation == "" || parameters.Harness == "" || parameters.TargetRoot == "" ||
		parameters.CandidateDigest == "" || len(parameters.Files) == 0 {
		return parameters, errors.New("adapter step parameters are incomplete")
	}
	if parameters.Operation != "install" && parameters.Operation != "update" &&
		parameters.Operation != "rollback" && parameters.Operation != "remove" {
		return parameters, fmt.Errorf("unsupported adapter operation %q", parameters.Operation)
	}
	if (parameters.Operation == "update" || parameters.Operation == "rollback") &&
		(parameters.PreviousCandidateDigest == "" || len(parameters.PreviousFiles) == 0) {
		return parameters, errors.New("adapter transition parameters are incomplete")
	}
	if parameters.Operation == "rollback" && parameters.SourceRoot == "" {
		return parameters, errors.New("adapter rollback source root is missing")
	}
	if parameters.Operation != "rollback" && parameters.SourceRoot != "" {
		return parameters, errors.New("adapter source root is only valid for rollback")
	}
	if parameters.Operation != "update" && parameters.Operation != "rollback" &&
		(parameters.PreviousCandidateDigest != "" || len(parameters.PreviousFiles) != 0) {
		return parameters, errors.New("adapter previous candidate is only valid for transitions")
	}
	return parameters, nil
}

func (handler *AdapterMaterializer) Apply(
	_ context.Context,
	step operations.Step,
) (operations.ApplyEvidence, error) {
	if err := handler.validateStep(step, MaterializeAdapterAction, "install"); err != nil {
		return operations.ApplyEvidence{}, err
	}
	before, after, err := handler.set.Apply()
	return operations.ApplyEvidence{Before: before, After: after}, err
}

func (handler *AdapterMaterializer) Verify(
	_ context.Context,
	step operations.Step,
	_ json.RawMessage,
) error {
	if err := handler.validateStep(step, MaterializeAdapterAction, "install"); err != nil {
		return err
	}
	return handler.set.Verify()
}

func (handler *AdapterMaterializer) validateStep(
	step operations.Step,
	action string,
	operation string,
) error {
	if step.Action != action {
		return fmt.Errorf("unexpected adapter action %q", step.Action)
	}
	parameters, err := DecodeAdapterParameters(step)
	if err != nil {
		return err
	}
	if parameters.Harness != handler.candidate.Harness ||
		parameters.Operation != operation ||
		parameters.TargetRoot != handler.target ||
		parameters.CandidateDigest != handler.candidate.CandidateDigest ||
		!slices.Equal(parameters.Files, handler.candidate.Files) {
		return errors.New("adapter step does not match the exact rendered candidate")
	}
	return nil
}

type AdapterUpdater struct {
	target             string
	sourceRoot         string
	operation          string
	action             string
	desired            AdapterCandidate
	previous           AdapterCandidate
	set                *materialize.Set
	beforeStaleRemoval func()
}

type adapterBackup struct {
	path    string
	existed bool
	content []byte
}

func NewAdapterUpdater(
	target string,
	sourceRoot string,
	operation string,
	desired AdapterCandidate,
	previous AdapterCandidate,
) (*AdapterUpdater, error) {
	action := UpdateAdapterAction
	if operation == "rollback" {
		action = RollbackAdapterAction
	} else if operation != "update" {
		return nil, fmt.Errorf("unsupported adapter transition %q", operation)
	}
	set, err := adapterMaterialization(target, desired)
	if err != nil {
		return nil, err
	}
	if previous.CandidateDigest == "" || len(previous.Files) == 0 {
		return nil, errors.New("adapter transition requires an exact previous candidate")
	}
	return &AdapterUpdater{
		target: target, sourceRoot: sourceRoot, operation: operation, action: action,
		desired: desired, previous: previous, set: set,
	}, nil
}

func (handler *AdapterUpdater) Apply(
	_ context.Context,
	step operations.Step,
) (operations.ApplyEvidence, error) {
	if err := handler.validateStep(step); err != nil {
		return operations.ApplyEvidence{}, err
	}
	union := adapterFileUnion(handler.desired.Files, handler.previous.Files)
	before, backups, err := captureAdapterSnapshot(handler.target, union)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	if err := verifyPreviousSnapshot(backups, handler.previous.Files); err != nil {
		return operations.ApplyEvidence{Before: before}, err
	}
	if _, _, err := handler.set.Apply(); err != nil {
		return operations.ApplyEvidence{Before: before}, err
	}
	if handler.beforeStaleRemoval != nil {
		handler.beforeStaleRemoval()
	}
	removed, err := removeStaleAdapterFiles(handler.target, handler.desired.Files, handler.previous.Files)
	if err != nil {
		return operations.ApplyEvidence{Before: before}, restoreAdapterTransition(
			handler.target, backups, handler.desired.Files, removed, err,
		)
	}
	if err := handler.verifyState(); err != nil {
		return operations.ApplyEvidence{Before: before}, restoreAdapterTransition(
			handler.target, backups, handler.desired.Files, removed, err,
		)
	}
	after, _, err := captureAdapterSnapshot(handler.target, union)
	if err != nil {
		return operations.ApplyEvidence{Before: before}, restoreAdapterTransition(
			handler.target, backups, handler.desired.Files, removed, err,
		)
	}
	return operations.ApplyEvidence{Before: before, After: after}, nil
}

func (handler *AdapterUpdater) Verify(
	_ context.Context,
	step operations.Step,
	_ json.RawMessage,
) error {
	if err := handler.validateStep(step); err != nil {
		return err
	}
	return handler.verifyState()
}

func (handler *AdapterUpdater) validateStep(step operations.Step) error {
	if step.Action != handler.action {
		return fmt.Errorf("unexpected adapter action %q", step.Action)
	}
	parameters, err := DecodeAdapterParameters(step)
	if err != nil {
		return err
	}
	if parameters.Operation != handler.operation || parameters.Harness != handler.desired.Harness ||
		parameters.TargetRoot != handler.target || parameters.SourceRoot != handler.sourceRoot ||
		parameters.CandidateDigest != handler.desired.CandidateDigest ||
		parameters.PreviousCandidateDigest != handler.previous.CandidateDigest ||
		!slices.Equal(parameters.Files, handler.desired.Files) ||
		!slices.Equal(parameters.PreviousFiles, handler.previous.Files) {
		return errors.New("adapter transition step does not match the exact old and new candidates")
	}
	return nil
}

func (handler *AdapterUpdater) verifyState() error {
	if err := handler.set.Verify(); err != nil {
		return err
	}
	return verifyStaleAdapterFilesAbsent(handler.target, handler.desired.Files, handler.previous.Files)
}

func adapterFileUnion(left, right []AdapterFile) []AdapterFile {
	files := adapterFileMap(right)
	for _, file := range left {
		files[file.Path] = file
	}
	result := make([]AdapterFile, 0, len(files))
	for _, file := range files {
		result = append(result, file)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Path < result[j].Path })
	return result
}

func captureAdapterSnapshot(
	target string,
	files []AdapterFile,
) ([]materialize.ObservedFile, []adapterBackup, error) {
	root, err := os.OpenRoot(target)
	if err != nil {
		return nil, nil, err
	}
	defer root.Close()
	observed := make([]materialize.ObservedFile, 0, len(files))
	backups := make([]adapterBackup, 0, len(files))
	for _, file := range files {
		path := filepath.FromSlash(file.Path)
		info, err := root.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			observed = append(observed, materialize.ObservedFile{Path: file.Path, State: "missing"})
			backups = append(backups, adapterBackup{path: file.Path})
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
			info.Size() > maxAdapterSourceBytes {
			return nil, nil, fmt.Errorf("adapter transition path is not a bounded regular file: %s", file.Path)
		}
		content, err := root.ReadFile(path)
		if err != nil {
			return nil, nil, err
		}
		digest := bytesDigest(content)
		observed = append(observed, materialize.ObservedFile{
			Path: file.Path, State: "regular", Digest: digest, Mode: info.Mode().String(),
		})
		backups = append(backups, adapterBackup{path: file.Path, existed: true, content: content})
	}
	return observed, backups, nil
}

func verifyPreviousSnapshot(backups []adapterBackup, previous []AdapterFile) error {
	byPath := make(map[string]adapterBackup, len(backups))
	for _, backup := range backups {
		byPath[backup.path] = backup
	}
	for _, file := range previous {
		backup, found := byPath[file.Path]
		if !found || !backup.existed || bytesDigest(backup.content) != file.Digest {
			return fmt.Errorf("installed adapter file has drift: %s", file.Path)
		}
	}
	return nil
}

func removeStaleAdapterFiles(
	target string,
	desired []AdapterFile,
	previous []AdapterFile,
) ([]string, error) {
	desiredPaths := adapterFileMap(desired)
	root, err := os.OpenRoot(target)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	removed := []string{}
	for _, file := range previous {
		if _, retained := desiredPaths[file.Path]; retained {
			continue
		}
		path := filepath.FromSlash(file.Path)
		info, err := root.Lstat(path)
		if err != nil {
			return removed, fmt.Errorf("inspect stale adapter file %s: %w", file.Path, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
			info.Size() > maxAdapterSourceBytes {
			return removed, fmt.Errorf("cannot safely remove stale adapter file %s", file.Path)
		}
		content, err := root.ReadFile(path)
		if err != nil || bytesDigest(content) != file.Digest {
			return removed, fmt.Errorf("stale adapter file has drift: %s", file.Path)
		}
		if err := root.Remove(path); err != nil {
			return removed, err
		}
		removed = append(removed, file.Path)
	}
	return removed, nil
}

func verifyStaleAdapterFilesAbsent(target string, desired, previous []AdapterFile) error {
	desiredPaths := adapterFileMap(desired)
	root, err := os.OpenRoot(target)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, file := range previous {
		if _, retained := desiredPaths[file.Path]; retained {
			continue
		}
		if _, err := root.Lstat(filepath.FromSlash(file.Path)); !errors.Is(err, os.ErrNotExist) {
			if err == nil {
				return fmt.Errorf("stale adapter file still exists: %s", file.Path)
			}
			return err
		}
	}
	return nil
}

func restoreAdapterTransition(
	target string,
	backups []adapterBackup,
	desired []AdapterFile,
	removed []string,
	cause error,
) error {
	backupByPath := make(map[string]adapterBackup, len(backups))
	for _, backup := range backups {
		backupByPath[backup.path] = backup
	}
	removedSet := make(map[string]struct{}, len(removed))
	for _, path := range removed {
		removedSet[path] = struct{}{}
	}
	root, err := os.OpenRoot(target)
	if err != nil {
		return fmt.Errorf("%v; transition rollback failed: %w", cause, err)
	}
	filesToRestore := []materialize.File{}
	pathsToRemove := []string{}
	for _, file := range desired {
		path := filepath.FromSlash(file.Path)
		info, inspectErr := root.Lstat(path)
		if inspectErr != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
			info.Size() > maxAdapterSourceBytes {
			root.Close()
			return fmt.Errorf("%v; transition rollback blocked by concurrent drift at %s", cause, file.Path)
		}
		content, readErr := root.ReadFile(path)
		if readErr != nil || bytesDigest(content) != file.Digest {
			root.Close()
			return fmt.Errorf("%v; transition rollback blocked by concurrent drift at %s", cause, file.Path)
		}
		backup := backupByPath[file.Path]
		if backup.existed {
			filesToRestore = append(filesToRestore, materialize.File{
				Path: backup.path, Content: backup.content, Digest: bytesDigest(backup.content),
			})
		} else {
			pathsToRemove = append(pathsToRemove, backup.path)
		}
	}
	for path := range removedSet {
		if _, inspectErr := root.Lstat(filepath.FromSlash(path)); !errors.Is(inspectErr, os.ErrNotExist) {
			root.Close()
			return fmt.Errorf("%v; transition rollback blocked by concurrent recreation at %s", cause, path)
		}
		backup := backupByPath[path]
		filesToRestore = append(filesToRestore, materialize.File{
			Path: backup.path, Content: backup.content, Digest: bytesDigest(backup.content),
		})
	}
	if err := root.Close(); err != nil {
		return fmt.Errorf("%v; transition rollback failed: %w", cause, err)
	}
	if len(filesToRestore) != 0 {
		set, restoreErr := materialize.NewSet(target, filesToRestore)
		if restoreErr == nil {
			_, _, restoreErr = set.Apply()
		}
		if restoreErr != nil {
			return fmt.Errorf("%v; transition rollback failed: %w", cause, restoreErr)
		}
	}
	if len(pathsToRemove) != 0 {
		root, err = os.OpenRoot(target)
		if err != nil {
			return fmt.Errorf("%v; transition rollback failed: %w", cause, err)
		}
		defer root.Close()
		for _, path := range pathsToRemove {
			if err := root.Remove(filepath.FromSlash(path)); err != nil {
				return fmt.Errorf("%v; transition rollback failed: %w", cause, err)
			}
		}
	}
	return cause
}

type AdapterRemover struct {
	target    string
	candidate AdapterCandidate
}

type removedFile struct {
	path    string
	content []byte
	mode    os.FileMode
}

func NewAdapterRemover(target string, candidate AdapterCandidate) (*AdapterRemover, error) {
	if _, err := adapterMaterialization(target, candidate); err != nil {
		return nil, err
	}
	return &AdapterRemover{target: target, candidate: candidate}, nil
}

func (handler *AdapterRemover) Apply(
	_ context.Context,
	step operations.Step,
) (operations.ApplyEvidence, error) {
	if err := handler.validateStep(step); err != nil {
		return operations.ApplyEvidence{}, err
	}
	root, err := os.OpenRoot(handler.target)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	defer root.Close()
	removed := []removedFile{}
	before := []materialize.ObservedFile{}
	for _, file := range handler.candidate.Files {
		path := filepath.FromSlash(file.Path)
		info, err := root.Lstat(path)
		if err != nil {
			return operations.ApplyEvidence{Before: before}, rollbackRemoved(root, removed, err)
		}
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Size() > maxAdapterSourceBytes {
			return operations.ApplyEvidence{Before: before}, rollbackRemoved(
				root, removed, fmt.Errorf("managed adapter file is not a bounded regular file: %s", file.Path),
			)
		}
		content, err := root.ReadFile(path)
		if err != nil {
			return operations.ApplyEvidence{Before: before}, rollbackRemoved(root, removed, err)
		}
		digest := fmt.Sprintf("sha256:%x", sha256.Sum256(content))
		before = append(before, materialize.ObservedFile{
			Path: file.Path, State: "regular", Digest: digest, Mode: info.Mode().String(),
		})
		if digest != file.Digest {
			return operations.ApplyEvidence{Before: before}, rollbackRemoved(
				root, removed, fmt.Errorf("managed adapter file has drift: %s", file.Path),
			)
		}
		if err := root.Remove(path); err != nil {
			return operations.ApplyEvidence{Before: before}, rollbackRemoved(root, removed, err)
		}
		removed = append(removed, removedFile{path: file.Path, content: content, mode: info.Mode().Perm()})
	}
	after := make([]materialize.ObservedFile, 0, len(handler.candidate.Files))
	for _, file := range handler.candidate.Files {
		after = append(after, materialize.ObservedFile{Path: file.Path, State: "missing"})
	}
	return operations.ApplyEvidence{Before: before, After: after}, nil
}

func (handler *AdapterRemover) Verify(
	_ context.Context,
	step operations.Step,
	_ json.RawMessage,
) error {
	if err := handler.validateStep(step); err != nil {
		return err
	}
	root, err := os.OpenRoot(handler.target)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, file := range handler.candidate.Files {
		if _, err := root.Lstat(filepath.FromSlash(file.Path)); !errors.Is(err, os.ErrNotExist) {
			if err == nil {
				return fmt.Errorf("managed adapter file still exists: %s", file.Path)
			}
			return err
		}
	}
	return nil
}

func (handler *AdapterRemover) validateStep(step operations.Step) error {
	materializer := AdapterMaterializer{target: handler.target, candidate: handler.candidate}
	return materializer.validateStep(step, RemoveAdapterAction, "remove")
}

func rollbackRemoved(root *os.Root, removed []removedFile, cause error) error {
	if len(removed) == 0 {
		return cause
	}
	files := make([]materialize.File, 0, len(removed))
	for _, file := range removed {
		files = append(files, materialize.File{
			Path: file.path, Content: file.content,
			Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(file.content)),
		})
	}
	sort.Slice(files, func(left, right int) bool { return files[left].Path < files[right].Path })
	set, err := materialize.NewSet(root.Name(), files)
	if err == nil {
		_, _, err = set.Apply()
	}
	if err != nil {
		return fmt.Errorf("%v; rollback failed: %w", cause, err)
	}
	return cause
}
