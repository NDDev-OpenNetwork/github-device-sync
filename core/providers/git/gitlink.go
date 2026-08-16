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
	if before.GitlinkOID != expectedOldOID || before.GitlinkStage != 0 ||
		before.WorktreeState != "uninitialized" || before.Staged != 0 ||
		before.Unstaged != 0 || before.Untracked != 0 || before.Conflicted != 0 {
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
	if after.GitlinkOID != targetOID || after.GitlinkStage != 0 ||
		after.WorktreeState != "uninitialized" || after.HeadOID != before.HeadOID ||
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
			WorktreeState: submodule.WorktreeState, HeadOID: status.Head.OID,
			Staged: status.Changes.Staged, Unstaged: status.Changes.Unstaged,
			Untracked: status.Changes.Untracked, Conflicted: status.Changes.Conflicted,
		}, nil
	}
	return GitlinkEvidence{}, fmt.Errorf("gitmodules entry %q is missing", gitmodulesName)
}
