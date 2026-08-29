package git

import (
	"context"
	"errors"
	"fmt"
)

type GitlinkEvidence struct {
	WorktreeRoot  string `json:"worktree_root"`
	Name          string `json:"name"`
	Path          string `json:"path"`
	GitlinkOID    string `json:"gitlink_oid"`
	GitlinkStage  int    `json:"gitlink_stage"`
	WorktreeState string `json:"worktree_state"`
	CurrentOID    string `json:"current_oid,omitempty"`
	HeadOID       string `json:"head_oid"`
	Staged        int    `json:"staged"`
	Unstaged      int    `json:"unstaged"`
	Untracked     int    `json:"untracked"`
	Conflicted    int    `json:"conflicted"`
}

type GitlinkReport struct {
	Before GitlinkEvidence `json:"before"`
	After  GitlinkEvidence `json:"after"`
}

func (runner *MutationRunner) UpdateGitlink(
	ctx context.Context,
	directory string,
	gitmodulesName string,
	expectedOldOID string,
	targetOID string,
) (GitlinkReport, error) {
	if validateOID(expectedOldOID, false) != nil || validateOID(targetOID, false) != nil ||
		len(expectedOldOID) != len(targetOID) || expectedOldOID == targetOID {
		return GitlinkReport{}, errors.New("gitlink update OIDs are invalid or unchanged")
	}
	root, err := validateMutationRoot(directory)
	if err != nil {
		return GitlinkReport{}, err
	}
	if err := runner.validateWorktreeMutationConfiguration(ctx, root); err != nil {
		return GitlinkReport{}, err
	}
	before, err := runner.ObserveGitlink(ctx, root, gitmodulesName)
	if err != nil {
		return GitlinkReport{}, err
	}
	report := GitlinkReport{Before: before}
	// Two shapes are writable, and they are the two an agent actually arrives in.
	// An absent checkout leaves the tree clean. A checkout already sitting at the
	// target commit reports that one gitlink as unstaged -- which is the whole
	// change this operation is about to record, and stronger evidence than an
	// absent directory, since the consumer holds the commit. Requiring only the
	// first meant a pin could not be advanced without `git submodule deinit`.
	// Everything else is unchanged: any staged, untracked or conflicted path, or
	// a second unstaged one, still refuses.
	if before.GitlinkOID != expectedOldOID || before.GitlinkStage != 0 ||
		before.Staged != 0 || before.Untracked != 0 || before.Conflicted != 0 {
		return report, errors.New("gitlink update precondition changed")
	}
	switch {
	case before.WorktreeState == "uninitialized" && before.Unstaged == 0:
	case before.WorktreeState == "off-gitlink" && before.CurrentOID == targetOID &&
		before.Unstaged == 1:
	default:
		return report, errors.New("gitlink update precondition changed")
	}
	specification := fmt.Sprintf("160000,%s,%s", targetOID, before.Path)
	if _, err := runner.run(
		ctx, root, map[int]struct{}{0: {}},
		"update-index", "--cacheinfo", specification,
	); err != nil {
		return report, err
	}
	after, err := runner.ObserveGitlink(ctx, root, gitmodulesName)
	if err != nil {
		return report, err
	}
	report.After = after
	// An absent checkout stays absent; a checkout that was off its gitlink is now
	// exactly at it, because the index moved to the commit the worktree already
	// held. Both leave the single staged gitlink this operation writes.
	if after.GitlinkOID != targetOID || after.GitlinkStage != 0 ||
		(after.WorktreeState != "uninitialized" && after.WorktreeState != "at-gitlink") ||
		after.HeadOID != before.HeadOID ||
		after.Staged != 1 || after.Unstaged != 0 || after.Untracked != 0 ||
		after.Conflicted != 0 {
		return report, errors.New("gitlink update postcondition failed")
	}
	return report, nil
}

func (runner *MutationRunner) ObserveGitlink(
	ctx context.Context,
	directory string,
	gitmodulesName string,
) (GitlinkEvidence, error) {
	root, err := validateMutationRoot(directory)
	if err != nil {
		return GitlinkEvidence{}, err
	}
	readRunner := runner.readRunner()
	topology, err := readRunner.InspectTopology(ctx, root)
	if err != nil {
		return GitlinkEvidence{}, err
	}
	status, err := readRunner.InspectStatus(ctx, root)
	if err != nil {
		return GitlinkEvidence{}, err
	}
	for _, submodule := range topology.Submodules {
		if submodule.Name != gitmodulesName {
			continue
		}
		return GitlinkEvidence{
			WorktreeRoot: root, Name: submodule.Name, Path: submodule.Path,
			GitlinkOID: submodule.GitlinkOID, GitlinkStage: submodule.GitlinkStage,
			WorktreeState: submodule.WorktreeState, CurrentOID: submodule.CurrentOID,
			HeadOID: status.Head.OID,
			Staged:  status.Changes.Staged, Unstaged: status.Changes.Unstaged,
			Untracked: status.Changes.Untracked, Conflicted: status.Changes.Conflicted,
		}, nil
	}
	return GitlinkEvidence{}, fmt.Errorf("gitmodules entry %q is missing", gitmodulesName)
}
