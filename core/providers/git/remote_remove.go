package git

import (
	"context"
	"errors"
	"strings"
)

type RemoteRemovalEvidence struct {
	WorktreeRoot string `json:"worktree_root"`
	Remote       string `json:"remote"`
	ExpectedURL  string `json:"expected_url"`
	State        string `json:"state"`
}

type RemoteRemovalReport struct {
	Before RemoteRemovalEvidence `json:"before"`
	After  RemoteRemovalEvidence `json:"after"`
}

func (runner *MutationRunner) ObserveRemoteRemoval(
	ctx context.Context,
	directory string,
	remote string,
	expectedURL string,
) (RemoteRemovalEvidence, error) {
	if !safeRemoteName(remote) || strings.TrimSpace(expectedURL) == "" {
		return RemoteRemovalEvidence{}, errors.New("remote removal input is invalid")
	}
	root, err := validateMutationRoot(directory)
	if err != nil {
		return RemoteRemovalEvidence{}, err
	}
	if err := runner.validateWorktreeMutationConfiguration(ctx, root); err != nil {
		return RemoteRemovalEvidence{}, err
	}
	fetchURLs, pushURLs, present, err := runner.storedRemoteURLs(ctx, root, remote)
	if err != nil {
		return RemoteRemovalEvidence{}, err
	}
	if !present {
		return RemoteRemovalEvidence{
			WorktreeRoot: root, Remote: remote, ExpectedURL: expectedURL, State: "missing",
		}, nil
	}
	if len(fetchURLs) != 1 || len(pushURLs) != 1 ||
		fetchURLs[0] != expectedURL || pushURLs[0] != expectedURL {
		return RemoteRemovalEvidence{}, errors.New("remote URLs differ from the exact removal plan")
	}
	return RemoteRemovalEvidence{
		WorktreeRoot: root, Remote: remote, ExpectedURL: expectedURL, State: "present",
	}, nil
}

func (runner *MutationRunner) RemoveRemote(
	ctx context.Context,
	directory string,
	remote string,
	expectedURL string,
) (RemoteRemovalReport, error) {
	before, err := runner.ObserveRemoteRemoval(ctx, directory, remote, expectedURL)
	if err != nil {
		return RemoteRemovalReport{}, err
	}
	report := RemoteRemovalReport{Before: before}
	if before.State != "present" {
		return report, errors.New("remote is already absent")
	}
	if _, err := runner.run(
		ctx, before.WorktreeRoot, map[int]struct{}{0: {}}, "remote", "remove", remote,
	); err != nil {
		return report, err
	}
	after, err := runner.ObserveRemoteRemoval(ctx, before.WorktreeRoot, remote, expectedURL)
	if err != nil {
		return report, err
	}
	report.After = after
	if after.State != "missing" {
		return report, errors.New("remote removal postcondition failed")
	}
	return report, nil
}
