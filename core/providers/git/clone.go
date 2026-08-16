package git

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type LocalCloneSource struct {
	Path      string `json:"path"`
	BranchRef string `json:"branch_ref"`
	HeadOID   string `json:"head_oid"`
}

type CheckoutMaterializationEvidence struct {
	WorkspaceRoot string                 `json:"workspace_root"`
	TargetPath    string                 `json:"target_path"`
	SourcePath    string                 `json:"source_path"`
	BranchRef     string                 `json:"branch_ref"`
	HeadOID       string                 `json:"head_oid"`
	Filter        string                 `json:"filter"`
	TargetState   string                 `json:"target_state"`
	VerifiedFiles []ExpectedCheckoutFile `json:"verified_files"`
}

type ExpectedCheckoutFile struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
}

type CheckoutMaterializationReport struct {
	Before CheckoutMaterializationEvidence `json:"before"`
	After  CheckoutMaterializationEvidence `json:"after"`
}

func (runner *MutationRunner) ObserveLocalCloneSource(
	ctx context.Context,
	source string,
	branchRef string,
) (LocalCloneSource, error) {
	if err := validateLocalBranchRef(branchRef); err != nil {
		return LocalCloneSource{}, err
	}
	physical, err := physicalGitDirectory(source)
	if err != nil {
		return LocalCloneSource{}, err
	}
	bare, err := runner.run(ctx, physical, map[int]struct{}{0: {}}, "rev-parse", "--is-bare-repository")
	if err != nil || strings.TrimSpace(string(bare.Stdout)) != "true" {
		return LocalCloneSource{}, errors.New("local clone source must be a bare Git repository")
	}
	lookup, err := runner.runWithEnvironment(
		ctx, physical, map[int]struct{}{0: {}}, nil, "-c", "protocol.allow=never",
		"-c", "protocol.file.allow=always", "ls-remote", "--refs", physical, branchRef,
	)
	if err != nil {
		return LocalCloneSource{}, errors.New("local clone source branch is unavailable")
	}
	lines := nonEmptyLines(lookup.Stdout)
	if len(lines) != 1 {
		return LocalCloneSource{}, errors.New("local clone source branch is unavailable")
	}
	oid, reference, found := strings.Cut(lines[0], "\t")
	if !found || reference != branchRef || validateOID(oid, false) != nil {
		return LocalCloneSource{}, errors.New("local clone source branch is unavailable")
	}
	return LocalCloneSource{Path: physical, BranchRef: branchRef, HeadOID: oid}, nil
}

func (runner *MutationRunner) ObserveLocalCloneFileDigest(
	ctx context.Context,
	source LocalCloneSource,
	relativePath string,
) (string, error) {
	if !safeRepositoryPath(relativePath) || validateOID(source.HeadOID, false) != nil {
		return "", errors.New("local clone file observation input is invalid")
	}
	observed, err := runner.ObserveLocalCloneSource(ctx, source.Path, source.BranchRef)
	if err != nil || observed != source {
		return "", errors.New("local clone source changed before file observation")
	}
	result, err := runner.run(
		ctx, source.Path, map[int]struct{}{0: {}}, "show", source.HeadOID+":"+relativePath,
	)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(result.Stdout)), nil
}

func (runner *MutationRunner) MaterializeLocalCheckout(
	ctx context.Context,
	workspaceRoot string,
	targetPath string,
	source LocalCloneSource,
	expectedHeadOID string,
	filter string,
	expectedFiles []ExpectedCheckoutFile,
) (CheckoutMaterializationReport, error) {
	root, target, err := validateCheckoutTarget(workspaceRoot, targetPath)
	if err != nil {
		return CheckoutMaterializationReport{}, err
	}
	if validateOID(expectedHeadOID, false) != nil || source.HeadOID != expectedHeadOID {
		return CheckoutMaterializationReport{}, errors.New("checkout materialization expected OID is invalid")
	}
	if filter != "full" && filter != "blob-none" {
		return CheckoutMaterializationReport{}, errors.New("checkout materialization filter is invalid")
	}
	expectedFiles, err = normalizeExpectedCheckoutFiles(expectedFiles)
	if err != nil {
		return CheckoutMaterializationReport{}, err
	}
	observedSource, err := runner.ObserveLocalCloneSource(ctx, source.Path, source.BranchRef)
	if err != nil || observedSource != source {
		return CheckoutMaterializationReport{}, errors.New("local clone source changed before materialization")
	}
	before := CheckoutMaterializationEvidence{
		WorkspaceRoot: root, TargetPath: target, SourcePath: source.Path,
		BranchRef: source.BranchRef, HeadOID: expectedHeadOID, Filter: filter,
		TargetState: "missing", VerifiedFiles: expectedFiles,
	}
	temporary, err := os.MkdirTemp(root, ".gds-materialize-")
	if err != nil {
		return CheckoutMaterializationReport{Before: before}, err
	}
	if err := os.Remove(temporary); err != nil {
		return CheckoutMaterializationReport{Before: before}, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(temporary)
		}
	}()
	args := []string{
		"-c", "protocol.allow=never", "-c", "protocol.file.allow=always",
		"-c", "core.hooksPath=/dev/null", "clone", "--no-local", "--no-tags",
		"--no-recurse-submodules", "--single-branch", "--branch",
		strings.TrimPrefix(source.BranchRef, "refs/heads/"),
	}
	if filter == "blob-none" {
		args = append(args, "--filter=blob:none")
	}
	args = append(args, source.Path, temporary)
	if _, err := runner.runWithEnvironment(
		ctx, root, map[int]struct{}{0: {}}, nil, args...,
	); err != nil {
		return CheckoutMaterializationReport{Before: before}, err
	}
	if _, err := runner.observeMaterializedCheckout(
		ctx, temporary, source, expectedHeadOID, filter, expectedFiles,
	); err != nil {
		return CheckoutMaterializationReport{Before: before}, err
	}
	if err := os.Rename(temporary, target); err != nil {
		return CheckoutMaterializationReport{Before: before}, fmt.Errorf("publish materialized checkout: %w", err)
	}
	cleanup = false
	after, err := runner.observeMaterializedCheckout(
		ctx, target, source, expectedHeadOID, filter, expectedFiles,
	)
	if err != nil {
		return CheckoutMaterializationReport{Before: before}, err
	}
	after.WorkspaceRoot = root
	after.TargetPath = target
	return CheckoutMaterializationReport{Before: before, After: after}, nil
}

func (runner *MutationRunner) ObserveMaterializedCheckout(
	ctx context.Context,
	workspaceRoot string,
	targetPath string,
	source LocalCloneSource,
	expectedHeadOID string,
	filter string,
	expectedFiles []ExpectedCheckoutFile,
) (CheckoutMaterializationEvidence, error) {
	root, target, err := validateCheckoutTargetPresent(workspaceRoot, targetPath)
	if err != nil {
		return CheckoutMaterializationEvidence{}, err
	}
	expectedFiles, err = normalizeExpectedCheckoutFiles(expectedFiles)
	if err != nil {
		return CheckoutMaterializationEvidence{}, err
	}
	evidence, err := runner.observeMaterializedCheckout(
		ctx, target, source, expectedHeadOID, filter, expectedFiles,
	)
	if err != nil {
		return CheckoutMaterializationEvidence{}, err
	}
	evidence.WorkspaceRoot = root
	evidence.TargetPath = target
	return evidence, nil
}

func (runner *MutationRunner) observeMaterializedCheckout(
	ctx context.Context,
	target string,
	source LocalCloneSource,
	expectedHeadOID string,
	filter string,
	expectedFiles []ExpectedCheckoutFile,
) (CheckoutMaterializationEvidence, error) {
	head, err := runner.run(ctx, target, map[int]struct{}{0: {}}, "rev-parse", "--verify", "HEAD")
	if err != nil || strings.TrimSpace(string(head.Stdout)) != expectedHeadOID {
		return CheckoutMaterializationEvidence{}, errors.New("materialized checkout HEAD differs from plan")
	}
	branch, err := runner.run(ctx, target, map[int]struct{}{0: {}}, "symbolic-ref", "--quiet", "HEAD")
	if err != nil || strings.TrimSpace(string(branch.Stdout)) != source.BranchRef {
		return CheckoutMaterializationEvidence{}, errors.New("materialized checkout branch differs from plan")
	}
	remote, err := runner.run(ctx, target, map[int]struct{}{0: {}}, "remote", "get-url", "origin")
	if err != nil {
		return CheckoutMaterializationEvidence{}, err
	}
	remotePath, err := physicalGitDirectory(strings.TrimSpace(string(remote.Stdout)))
	if err != nil || remotePath != source.Path {
		return CheckoutMaterializationEvidence{}, errors.New("materialized checkout origin differs from plan")
	}
	status, err := runner.run(ctx, target, map[int]struct{}{0: {}}, "status", "--porcelain=v2", "-z")
	if err != nil || len(status.Stdout) != 0 {
		return CheckoutMaterializationEvidence{}, errors.New("materialized checkout is not clean")
	}
	if err := verifyCheckoutFiles(target, expectedFiles); err != nil {
		return CheckoutMaterializationEvidence{}, err
	}
	return CheckoutMaterializationEvidence{
		TargetPath: target, SourcePath: source.Path, BranchRef: source.BranchRef,
		HeadOID: expectedHeadOID, Filter: filter, TargetState: "present",
		VerifiedFiles: expectedFiles,
	}, nil
}

func normalizeExpectedCheckoutFiles(values []ExpectedCheckoutFile) ([]ExpectedCheckoutFile, error) {
	result := append([]ExpectedCheckoutFile(nil), values...)
	sort.Slice(result, func(left, right int) bool { return result[left].Path < result[right].Path })
	for index, value := range result {
		if !safeRepositoryPath(value.Path) || !strings.HasPrefix(value.Digest, "sha256:") || len(value.Digest) != 71 {
			return nil, errors.New("expected checkout file is invalid")
		}
		if index > 0 && result[index-1].Path == value.Path {
			return nil, errors.New("expected checkout file paths must be unique")
		}
	}
	return result, nil
}

func verifyCheckoutFiles(root string, values []ExpectedCheckoutFile) error {
	for _, value := range values {
		path := filepath.Join(root, filepath.FromSlash(value.Path))
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() > 4<<20 {
			return fmt.Errorf("expected checkout file %s is unavailable", value.Path)
		}
		raw, err := os.ReadFile(path)
		if err != nil || fmt.Sprintf("sha256:%x", sha256.Sum256(raw)) != value.Digest {
			return fmt.Errorf("expected checkout file %s differs from plan", value.Path)
		}
	}
	return nil
}

func physicalGitDirectory(path string) (string, error) {
	if strings.TrimSpace(path) == "" || !filepath.IsAbs(path) {
		return "", errors.New("local Git path must be absolute")
	}
	physical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}
	info, err := os.Lstat(physical)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("local Git path must be a real directory")
	}
	return filepath.Clean(physical), nil
}

func validateCheckoutTarget(workspaceRoot string, targetPath string) (string, string, error) {
	root, target, err := checkoutTargetPaths(workspaceRoot, targetPath)
	if err != nil {
		return "", "", err
	}
	if _, err := os.Lstat(target); !errors.Is(err, os.ErrNotExist) {
		return "", "", errors.New("checkout target must be absent")
	}
	return root, target, nil
}

func validateCheckoutTargetPresent(workspaceRoot string, targetPath string) (string, string, error) {
	root, target, err := checkoutTargetPaths(workspaceRoot, targetPath)
	if err != nil {
		return "", "", err
	}
	info, err := os.Lstat(target)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", "", errors.New("checkout target must be a real directory")
	}
	return root, target, nil
}

func checkoutTargetPaths(workspaceRoot string, targetPath string) (string, string, error) {
	absoluteRoot, err := filepath.Abs(workspaceRoot)
	if err != nil {
		return "", "", errors.New("workspace root cannot be resolved")
	}
	absoluteRoot = filepath.Clean(absoluteRoot)
	rootInfo, err := os.Lstat(absoluteRoot)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", "", errors.New("workspace root must be a real directory")
	}
	root, err := validateMutationRoot(workspaceRoot)
	if err != nil || root == string(filepath.Separator) {
		return "", "", errors.New("workspace root must be a safe real directory")
	}
	if !filepath.IsAbs(targetPath) || filepath.Clean(targetPath) != targetPath ||
		filepath.Dir(targetPath) != absoluteRoot || filepath.Base(targetPath) == "." || filepath.Base(targetPath) == ".." {
		return "", "", errors.New("checkout target must be one direct child of the workspace root")
	}
	return root, filepath.Join(root, filepath.Base(targetPath)), nil
}
