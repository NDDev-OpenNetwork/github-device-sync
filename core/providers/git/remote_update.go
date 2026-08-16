package git

import (
	"context"
	"errors"
	"strings"
)

type RemoteUpdateEvidence struct {
	WorktreeRoot string `json:"worktree_root"`
	Remote       string `json:"remote"`
	ExpectedURL  string `json:"expected_url"`
	TargetURL    string `json:"target_url"`
	State        string `json:"state"`
}

type RemoteUpdateReport struct {
	Before RemoteUpdateEvidence `json:"before"`
	After  RemoteUpdateEvidence `json:"after"`
}

func (runner *MutationRunner) ObserveRemoteUpdate(
	ctx context.Context,
	directory string,
	remote string,
	expectedURL string,
	targetURL string,
) (RemoteUpdateEvidence, error) {
	if !safeRemoteName(remote) || expectedURL == "" || targetURL == "" || expectedURL == targetURL ||
		strings.ContainsAny(expectedURL+targetURL, "\x00\r\n") {
		return RemoteUpdateEvidence{}, errors.New("remote update input is invalid")
	}
	root, err := validateMutationRoot(directory)
	if err != nil {
		return RemoteUpdateEvidence{}, err
	}
	if err := runner.validateWorktreeMutationConfiguration(ctx, root); err != nil {
		return RemoteUpdateEvidence{}, err
	}
	fetchURLs, pushURLs, present, err := runner.storedRemoteURLs(ctx, root, remote)
	if err != nil || !present {
		return RemoteUpdateEvidence{}, errors.Join(errors.New("remote URL is unavailable"), err)
	}
	if len(fetchURLs) != 1 || len(pushURLs) != 1 || fetchURLs[0] != pushURLs[0] {
		return RemoteUpdateEvidence{}, errors.New("remote update requires one identical fetch and push URL")
	}
	state := ""
	switch fetchURLs[0] {
	case expectedURL:
		state = "expected"
	case targetURL:
		state = "target"
	default:
		return RemoteUpdateEvidence{}, errors.New("remote URL differs from both exact plan states")
	}
	return RemoteUpdateEvidence{
		WorktreeRoot: root, Remote: remote, ExpectedURL: expectedURL,
		TargetURL: targetURL, State: state,
	}, nil
}

func (runner *MutationRunner) storedRemoteURLs(
	ctx context.Context,
	root string,
	remote string,
) ([]string, []string, bool, error) {
	configured, err := runner.run(
		ctx, root, map[int]struct{}{0: {}, 1: {}},
		"config", "--local", "--no-includes", "--null", "--get-regexp", remoteConfigPattern,
	)
	if err != nil {
		return nil, nil, false, err
	}
	if configured.ExitCode == 1 {
		return nil, nil, false, nil
	}
	urls, err := parseRemoteConfig(configured.Stdout)
	if err != nil {
		return nil, nil, false, err
	}
	fetch := urls[remote+"\x00url"]
	push := urls[remote+"\x00pushurl"]
	if len(push) == 0 {
		push = fetch
	}
	return fetch, push, len(fetch) != 0, nil
}

func (runner *MutationRunner) UpdateRemote(
	ctx context.Context,
	directory string,
	remote string,
	expectedURL string,
	targetURL string,
) (RemoteUpdateReport, error) {
	before, err := runner.ObserveRemoteUpdate(ctx, directory, remote, expectedURL, targetURL)
	if err != nil {
		return RemoteUpdateReport{}, err
	}
	report := RemoteUpdateReport{Before: before}
	if before.State == "target" {
		report.After = before
		return report, nil
	}
	if _, err := runner.run(ctx, before.WorktreeRoot, map[int]struct{}{0: {}}, "remote", "set-url", remote, targetURL); err != nil {
		return report, err
	}
	if _, err := runner.run(ctx, before.WorktreeRoot, map[int]struct{}{0: {}}, "remote", "set-url", "--push", remote, targetURL); err != nil {
		_, rollbackErr := runner.run(context.WithoutCancel(ctx), before.WorktreeRoot, map[int]struct{}{0: {}}, "remote", "set-url", remote, expectedURL)
		return report, errors.Join(err, rollbackErr)
	}
	after, err := runner.ObserveRemoteUpdate(ctx, before.WorktreeRoot, remote, expectedURL, targetURL)
	if err != nil || after.State != "target" {
		_, fetchErr := runner.run(context.WithoutCancel(ctx), before.WorktreeRoot, map[int]struct{}{0: {}}, "remote", "set-url", remote, expectedURL)
		_, pushErr := runner.run(context.WithoutCancel(ctx), before.WorktreeRoot, map[int]struct{}{0: {}}, "remote", "set-url", "--push", remote, expectedURL)
		return report, errors.Join(err, fetchErr, pushErr)
	}
	report.After = after
	return report, nil
}
