package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
)

type CompletionEvidence struct {
	WorktreeRoot     string `json:"worktree_root"`
	CurrentBranchRef string `json:"current_branch_ref"`
	HeadOID          string `json:"head_oid"`
	DefaultBranchRef string `json:"default_branch_ref"`
	LocalDefaultOID  string `json:"local_default_oid"`
	RemoteDefaultOID string `json:"remote_default_oid"`
	TaskBranchRef    string `json:"task_branch_ref"`
	LocalTaskOID     string `json:"local_task_oid"`
	RemoteTaskOID    string `json:"remote_task_oid"`
	Clean            bool   `json:"clean"`
}

type CompletionReport struct {
	Before CompletionEvidence `json:"before"`
	After  CompletionEvidence `json:"after"`
}

func (runner *MutationRunner) ObserveLocalBranch(
	ctx context.Context,
	directory string,
	branchRef string,
) (string, bool, error) {
	if err := validateLocalBranchRef(branchRef); err != nil {
		return "", false, err
	}
	root, err := validateMutationRoot(directory)
	if err != nil {
		return "", false, err
	}
	head, err := runner.readRunner().HeadOID(ctx, root)
	if err != nil {
		return "", false, err
	}
	oid, err := runner.observeOptionalLocalRef(ctx, root, branchRef, len(head))
	if err != nil {
		return "", false, err
	}
	if oid == zeroOID(len(head)) {
		return "", false, nil
	}
	return oid, true, nil
}

func (runner *MutationRunner) CompleteTaskBranch(
	ctx context.Context,
	directory string,
	defaultBranchRef string,
	taskBranchRef string,
	expectedDefaultOID string,
	expectedTaskOID string,
) (CompletionReport, error) {
	if err := validateCompletionInput(
		defaultBranchRef, taskBranchRef, expectedDefaultOID, expectedTaskOID,
	); err != nil {
		return CompletionReport{}, err
	}
	root, err := validateMutationRoot(directory)
	if err != nil {
		return CompletionReport{}, err
	}
	if err := runner.validateWorktreeMutationConfiguration(ctx, root); err != nil {
		return CompletionReport{}, err
	}
	remoteURL, err := runner.validatedPushURL(ctx, root, "origin")
	if err != nil {
		return CompletionReport{}, err
	}
	before, err := runner.ObserveCompletion(ctx, root, defaultBranchRef, taskBranchRef)
	if err != nil {
		return CompletionReport{}, err
	}
	report := CompletionReport{Before: before}
	if !before.Clean || before.CurrentBranchRef != taskBranchRef ||
		before.HeadOID != expectedTaskOID || before.LocalTaskOID != expectedTaskOID ||
		before.RemoteTaskOID != expectedTaskOID ||
		before.LocalDefaultOID != expectedDefaultOID ||
		before.RemoteDefaultOID != expectedDefaultOID {
		return report, errors.New("completion Git precondition changed")
	}
	ancestor, err := runner.isAncestor(ctx, root, expectedDefaultOID, expectedTaskOID)
	if err != nil || !ancestor || expectedDefaultOID == expectedTaskOID {
		return report, errors.New("task branch is not a strict fast-forward of the default branch")
	}
	if err := runner.validateCompletionWorktrees(ctx, root, defaultBranchRef, taskBranchRef); err != nil {
		return report, err
	}
	hooks, err := os.MkdirTemp("", "gds-disabled-hooks-")
	if err != nil {
		return report, fmt.Errorf("create isolated hooks directory: %w", err)
	}
	defer os.RemoveAll(hooks)
	environment := []string{"GIT_MERGE_AUTOEDIT=no"}
	base := []string{
		"-c", "core.hooksPath=" + hooks,
		"-c", "commit.gpgSign=false",
		"-c", "submodule.recurse=false",
	}
	pushDefault := []string{
		"-c", "protocol.allow=never", "-c", "protocol.file.allow=always",
		"push", "--porcelain", "--no-verify",
		"--force-with-lease=" + defaultBranchRef + ":" + expectedDefaultOID,
		remoteURL, expectedTaskOID + ":" + defaultBranchRef,
	}
	if _, err := runner.runWithEnvironment(
		ctx, root, map[int]struct{}{0: {}}, nil, pushDefault...,
	); err != nil {
		return report, err
	}
	remoteDefault, err := runner.observeRemoteRef(
		ctx, root, remoteURL, defaultBranchRef, len(expectedTaskOID),
	)
	if err != nil || remoteDefault != expectedTaskOID {
		return report, errors.New("remote default branch integration verification failed")
	}
	defaultTracking := "refs/remotes/origin/" + strings.TrimPrefix(defaultBranchRef, "refs/heads/")
	if _, err := runner.run(
		ctx, root, map[int]struct{}{0: {}},
		"update-ref", "--no-deref", "-m", "gds complete default",
		defaultTracking, expectedTaskOID, expectedDefaultOID,
	); err != nil {
		return report, err
	}
	defaultName := strings.TrimPrefix(defaultBranchRef, "refs/heads/")
	switchArguments := append(append([]string{}, base...), "switch", "--no-guess", "--", defaultName)
	if _, err := runner.runWithEnvironment(
		ctx, root, map[int]struct{}{0: {}}, environment, switchArguments...,
	); err != nil {
		return report, err
	}
	mergeArguments := append(append([]string{}, base...),
		"merge", "--ff-only", "--no-edit", "--no-stat", expectedTaskOID,
	)
	if _, err := runner.runWithEnvironment(
		ctx, root, map[int]struct{}{0: {}}, environment, mergeArguments...,
	); err != nil {
		return report, err
	}
	taskTracking := "refs/remotes/origin/" + strings.TrimPrefix(taskBranchRef, "refs/heads/")
	deleteRemote := []string{
		"-c", "protocol.allow=never", "-c", "protocol.file.allow=always",
		"push", "--porcelain", "--no-verify",
		"--force-with-lease=" + taskBranchRef + ":" + expectedTaskOID,
		remoteURL, ":" + taskBranchRef,
	}
	if _, err := runner.runWithEnvironment(
		ctx, root, map[int]struct{}{0: {}}, nil, deleteRemote...,
	); err != nil {
		return report, err
	}
	remoteTask, err := runner.observeRemoteRef(
		ctx, root, remoteURL, taskBranchRef, len(expectedTaskOID),
	)
	if err != nil || remoteTask != zeroOID(len(expectedTaskOID)) {
		return report, errors.New("remote task branch cleanup verification failed")
	}
	if _, err := runner.run(
		ctx, root, map[int]struct{}{0: {}},
		"update-ref", "--no-deref", "-d", taskTracking, expectedTaskOID,
	); err != nil {
		return report, err
	}
	taskName := strings.TrimPrefix(taskBranchRef, "refs/heads/")
	branchArguments := append(append([]string{}, base...), "branch", "-d", "--", taskName)
	if _, err := runner.runWithEnvironment(
		ctx, root, map[int]struct{}{0: {}}, environment, branchArguments...,
	); err != nil {
		return report, err
	}
	after, err := runner.ObserveCompletion(ctx, root, defaultBranchRef, taskBranchRef)
	if err != nil {
		return report, err
	}
	report.After = after
	if !after.Clean || after.CurrentBranchRef != defaultBranchRef ||
		after.HeadOID != expectedTaskOID || after.LocalDefaultOID != expectedTaskOID ||
		after.RemoteDefaultOID != expectedTaskOID ||
		after.LocalTaskOID != zeroOID(len(expectedTaskOID)) ||
		after.RemoteTaskOID != zeroOID(len(expectedTaskOID)) {
		return report, errors.New("completion postcondition failed")
	}
	return report, nil
}

func (runner *MutationRunner) ObserveCompletion(
	ctx context.Context,
	directory string,
	defaultBranchRef string,
	taskBranchRef string,
) (CompletionEvidence, error) {
	if err := validateLocalBranchRef(defaultBranchRef); err != nil {
		return CompletionEvidence{}, err
	}
	if err := validateLocalBranchRef(taskBranchRef); err != nil || taskBranchRef == defaultBranchRef {
		return CompletionEvidence{}, errors.New("task branch ref is invalid")
	}
	root, err := validateMutationRoot(directory)
	if err != nil {
		return CompletionEvidence{}, err
	}
	readRunner := runner.readRunner()
	status, err := readRunner.InspectStatus(ctx, root)
	if err != nil || status.Head.Mode != "branch" || status.Head.OID == "" {
		return CompletionEvidence{}, errors.New("completion checkout HEAD is not an attached branch")
	}
	currentBranchRef := "refs/heads/" + status.Branch.Name
	if err := validateLocalBranchRef(currentBranchRef); err != nil {
		return CompletionEvidence{}, err
	}
	defaultOID, err := runner.observeOptionalLocalRef(ctx, root, defaultBranchRef, len(status.Head.OID))
	if err != nil {
		return CompletionEvidence{}, err
	}
	taskOID, err := runner.observeOptionalLocalRef(ctx, root, taskBranchRef, len(status.Head.OID))
	if err != nil {
		return CompletionEvidence{}, err
	}
	remoteURL, err := runner.validatedPushURL(ctx, root, "origin")
	if err != nil {
		return CompletionEvidence{}, err
	}
	remoteDefault, err := runner.observeRemoteRef(
		ctx, root, remoteURL, defaultBranchRef, len(status.Head.OID),
	)
	if err != nil {
		return CompletionEvidence{}, err
	}
	remoteTask, err := runner.observeRemoteRef(
		ctx, root, remoteURL, taskBranchRef, len(status.Head.OID),
	)
	if err != nil {
		return CompletionEvidence{}, err
	}
	return CompletionEvidence{
		WorktreeRoot: root, CurrentBranchRef: currentBranchRef, HeadOID: status.Head.OID,
		DefaultBranchRef: defaultBranchRef, LocalDefaultOID: defaultOID,
		RemoteDefaultOID: remoteDefault, TaskBranchRef: taskBranchRef,
		LocalTaskOID: taskOID, RemoteTaskOID: remoteTask,
		Clean: checkoutStatusClean(status),
	}, nil
}

func (runner *MutationRunner) observeOptionalLocalRef(
	ctx context.Context,
	root string,
	reference string,
	oidLength int,
) (string, error) {
	result, err := runner.run(
		ctx, root, map[int]struct{}{0: {}, 1: {}},
		"rev-parse", "--verify", "--quiet", reference,
	)
	if err != nil {
		return "", err
	}
	if result.ExitCode == 1 {
		return zeroOID(oidLength), nil
	}
	oid := strings.TrimSpace(string(result.Stdout))
	if err := validateOID(oid, false); err != nil {
		return "", err
	}
	return oid, nil
}

func (runner *MutationRunner) validateCompletionWorktrees(
	ctx context.Context,
	root string,
	defaultBranchRef string,
	taskBranchRef string,
) error {
	readRunner := runner.readRunner()
	status, err := readRunner.InspectStatus(ctx, root)
	if err != nil {
		return err
	}
	for _, worktree := range status.Worktrees {
		if worktree.Path == root {
			continue
		}
		branchRef := ""
		if worktree.Branch != "" {
			branchRef = "refs/heads/" + worktree.Branch
		}
		if branchRef == defaultBranchRef || branchRef == taskBranchRef {
			return errors.New("default or task branch is active in another worktree")
		}
	}
	return nil
}

func validateCompletionInput(
	defaultBranchRef string,
	taskBranchRef string,
	expectedDefaultOID string,
	expectedTaskOID string,
) error {
	if err := validateLocalBranchRef(defaultBranchRef); err != nil {
		return err
	}
	if err := validateLocalBranchRef(taskBranchRef); err != nil || taskBranchRef == defaultBranchRef {
		return errors.New("completion task branch is invalid")
	}
	if validateOID(expectedDefaultOID, false) != nil || validateOID(expectedTaskOID, false) != nil ||
		len(expectedDefaultOID) != len(expectedTaskOID) {
		return errors.New("completion OIDs are invalid")
	}
	return nil
}
