package git

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

var ErrNetworkMutationDisabled = errors.New("network Git mutation is disabled before the live provider stage")

type CommitIdentity struct {
	Name  string `json:"name"`
	Email string `json:"email"`
}

type HandoffEvidence struct {
	WorktreeRoot string   `json:"worktree_root"`
	BranchRef    string   `json:"branch_ref"`
	HeadOID      string   `json:"head_oid"`
	RemoteRef    string   `json:"remote_ref"`
	RemoteOID    string   `json:"remote_oid"`
	Files        []string `json:"files"`
}

type HandoffReport struct {
	Before HandoffEvidence `json:"before"`
	After  HandoffEvidence `json:"after"`
}

func (runner *MutationRunner) LocalPushSupported(
	ctx context.Context,
	directory string,
) error {
	root, err := validateMutationRoot(directory)
	if err != nil {
		return err
	}
	_, err = runner.validatedPushURL(ctx, root, "origin")
	return err
}

func (runner *MutationRunner) CommitAndPushHandoff(
	ctx context.Context,
	directory string,
	branchRef string,
	expectedHeadOID string,
	remoteRef string,
	expectedRemoteOID string,
	files []string,
	message string,
	identity CommitIdentity,
	commitTime time.Time,
) (HandoffReport, error) {
	if err := validateHandoffInput(
		branchRef, expectedHeadOID, remoteRef, expectedRemoteOID,
		files, message, identity, commitTime,
	); err != nil {
		return HandoffReport{}, err
	}
	root, err := validateMutationRoot(directory)
	if err != nil {
		return HandoffReport{}, err
	}
	if err := runner.validateWorktreeMutationConfiguration(ctx, root); err != nil {
		return HandoffReport{}, err
	}
	remoteURL, err := runner.validatedPushURL(ctx, root, "origin")
	if err != nil {
		return HandoffReport{}, err
	}
	before, err := runner.ObserveHandoff(ctx, root, branchRef, remoteRef, files)
	if err != nil {
		return HandoffReport{}, err
	}
	report := HandoffReport{Before: before}
	if before.HeadOID != expectedHeadOID || before.RemoteOID != expectedRemoteOID {
		return report, errors.New("handoff Git precondition changed")
	}
	hooks, err := os.MkdirTemp("", "gds-disabled-hooks-")
	if err != nil {
		return report, fmt.Errorf("create isolated hooks directory: %w", err)
	}
	defer os.RemoveAll(hooks)
	if info, inspectErr := os.Lstat(hooks); inspectErr != nil || !info.IsDir() ||
		info.Mode()&os.ModeSymlink != 0 {
		return report, errors.New("isolated hooks directory is unsafe")
	}
	timestamp := commitTime.UTC().Format(time.RFC3339)
	environment := []string{
		"GIT_AUTHOR_NAME=" + identity.Name,
		"GIT_AUTHOR_EMAIL=" + identity.Email,
		"GIT_AUTHOR_DATE=" + timestamp,
		"GIT_COMMITTER_NAME=" + identity.Name,
		"GIT_COMMITTER_EMAIL=" + identity.Email,
		"GIT_COMMITTER_DATE=" + timestamp,
	}
	base := []string{
		"-c", "core.hooksPath=" + hooks,
		"-c", "commit.gpgSign=false",
		"-c", "tag.gpgSign=false",
		"-c", "submodule.recurse=false",
	}
	addArguments := append(append([]string{}, base...), "add", "-A", "--")
	addArguments = append(addArguments, files...)
	if _, err := runner.runWithEnvironment(
		ctx, root, map[int]struct{}{0: {}}, environment, addArguments...,
	); err != nil {
		return report, err
	}
	commitArguments := append(append([]string{}, base...),
		"commit", "--only", "--no-verify", "--no-gpg-sign", "--cleanup=verbatim",
		"-m", message, "--",
	)
	commitArguments = append(commitArguments, files...)
	if _, err := runner.runWithEnvironment(
		ctx, root, map[int]struct{}{0: {}}, environment, commitArguments...,
	); err != nil {
		after, _ := runner.ObserveHandoff(context.WithoutCancel(ctx), root, branchRef, remoteRef, files)
		report.After = after
		return report, err
	}
	newHead, err := runner.observeBranchHead(ctx, root, branchRef)
	if err != nil {
		return report, err
	}
	parent, err := runner.commitParent(ctx, root, newHead)
	if err != nil || parent != expectedHeadOID {
		return report, errors.New("handoff commit is not one exact child of the planned HEAD")
	}
	changedFiles, err := runner.commitChangedFiles(ctx, root, newHead)
	if err != nil || !equalFileSets(changedFiles, files) {
		return report, errors.New("handoff commit file set differs from the exact plan")
	}
	if expectedRemoteOID != zeroOID(len(expectedHeadOID)) {
		ancestor, ancestorErr := runner.isAncestor(ctx, root, expectedRemoteOID, newHead)
		if ancestorErr != nil || !ancestor {
			return report, errors.New("handoff commit would not fast-forward the planned remote ref")
		}
	}
	leaseOID := expectedRemoteOID
	if leaseOID == zeroOID(len(expectedHeadOID)) {
		leaseOID = ""
	}
	pushArguments := []string{
		"-c", "protocol.allow=never",
		"-c", "protocol.file.allow=always",
		"push", "--porcelain", "--no-verify",
		"--force-with-lease=" + remoteRef + ":" + leaseOID,
		remoteURL, newHead + ":" + remoteRef,
	}
	if _, err := runner.runWithEnvironment(
		ctx, root, map[int]struct{}{0: {}}, nil, pushArguments...,
	); err != nil {
		after, _ := runner.ObserveHandoff(context.WithoutCancel(ctx), root, branchRef, remoteRef, files)
		report.After = after
		return report, err
	}
	remoteOID, err := runner.observeRemoteRef(ctx, root, remoteURL, remoteRef, len(newHead))
	if err != nil || remoteOID != newHead {
		return report, errors.New("handoff remote OID verification failed")
	}
	if err := runner.updateRemoteTrackingAndUpstream(
		ctx, root, branchRef, remoteRef, newHead, expectedRemoteOID,
	); err != nil {
		return report, err
	}
	after, err := runner.ObserveHandoff(ctx, root, branchRef, remoteRef, files)
	if err != nil {
		return report, err
	}
	report.After = after
	if after.HeadOID != newHead || after.RemoteOID != newHead {
		return report, errors.New("handoff postcondition failed")
	}
	return report, nil
}

func (runner *MutationRunner) ObserveHandoff(
	ctx context.Context,
	root string,
	branchRef string,
	remoteRef string,
	files []string,
) (HandoffEvidence, error) {
	head, err := runner.observeBranchHead(ctx, root, branchRef)
	if err != nil {
		return HandoffEvidence{}, err
	}
	remoteURL, err := runner.validatedPushURL(ctx, root, "origin")
	if err != nil {
		return HandoffEvidence{}, err
	}
	remoteOID, err := runner.observeRemoteRef(ctx, root, remoteURL, remoteRef, len(head))
	if err != nil {
		return HandoffEvidence{}, err
	}
	return HandoffEvidence{
		WorktreeRoot: root, BranchRef: branchRef, HeadOID: head,
		RemoteRef: remoteRef, RemoteOID: remoteOID,
		Files: append([]string(nil), files...),
	}, nil
}

func (runner *MutationRunner) observeBranchHead(
	ctx context.Context,
	root string,
	branchRef string,
) (string, error) {
	result, err := runner.run(
		ctx, root, map[int]struct{}{0: {}}, "rev-parse", "--verify", branchRef,
	)
	if err != nil {
		return "", err
	}
	oid := strings.TrimSpace(string(result.Stdout))
	if err := validateOID(oid, false); err != nil {
		return "", err
	}
	readRunner := runner.readRunner()
	status, err := readRunner.InspectStatus(ctx, root)
	if err != nil || status.Head.OID != oid || "refs/heads/"+status.Branch.Name != branchRef {
		return "", errors.New("planned branch is not the attached checkout HEAD")
	}
	if status.Changes.Conflicted != 0 || status.Submodules.Conflicted != 0 {
		return "", errors.New("conflicted checkout cannot be handed off")
	}
	return oid, nil
}

func (runner *MutationRunner) commitParent(
	ctx context.Context,
	root string,
	commitOID string,
) (string, error) {
	result, err := runner.run(
		ctx, root, map[int]struct{}{0: {}},
		"rev-parse", "--verify", commitOID+"^",
	)
	if err != nil {
		return "", err
	}
	parent := strings.TrimSpace(string(result.Stdout))
	if err := validateOID(parent, false); err != nil {
		return "", err
	}
	return parent, nil
}

func (runner *MutationRunner) commitChangedFiles(
	ctx context.Context,
	root string,
	commitOID string,
) ([]string, error) {
	result, err := runner.run(
		ctx, root, map[int]struct{}{0: {}},
		"diff-tree", "--no-commit-id", "--name-only", "-r", "-z", commitOID,
	)
	if err != nil {
		return nil, err
	}
	files := []string{}
	for _, path := range strings.Split(string(result.Stdout), "\x00") {
		if path == "" {
			continue
		}
		if !safeRepositoryPath(path) {
			return nil, errors.New("handoff commit returned an unsafe path")
		}
		files = append(files, path)
	}
	sort.Strings(files)
	return files, nil
}

func equalFileSets(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func (runner *MutationRunner) validatedPushURL(
	ctx context.Context,
	root string,
	remote string,
) (string, error) {
	if !safeRemoteName(remote) {
		return "", errors.New("unsafe push remote name")
	}
	if err := runner.validateFetchConfiguration(ctx, root, remote); err != nil {
		return "", err
	}
	readRunner := runner.readRunner()
	result, err := readRunner.Run(ctx, root, "remote", "get-url", "--push", "--all", remote)
	if err != nil {
		return "", err
	}
	urls := nonEmptyLines(result.Stdout)
	if len(urls) != 1 {
		return "", errors.New("push remote must have exactly one URL")
	}
	if err := validateFetchURL(root, urls[0]); err != nil {
		return "", err
	}
	local, err := localRemoteURL(root, urls[0])
	if err != nil {
		return "", err
	}
	if !local {
		return "", ErrNetworkMutationDisabled
	}
	return urls[0], nil
}

func localRemoteURL(root string, raw string) (bool, error) {
	if !strings.Contains(raw, "://") && strings.Contains(raw, ":") && !filepath.IsAbs(raw) {
		return false, nil
	}
	parsed, err := url.Parse(raw)
	if err == nil && parsed.Scheme != "" {
		switch parsed.Scheme {
		case "https", "ssh":
			return false, nil
		case "file":
			return true, nil
		default:
			return false, fmt.Errorf("push URL scheme %q is forbidden", parsed.Scheme)
		}
	}
	path := raw
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return false, err
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, errors.New("local push URL must identify a real directory")
	}
	return true, nil
}

func (runner *MutationRunner) observeRemoteRef(
	ctx context.Context,
	root string,
	remoteURL string,
	remoteRef string,
	oidLength int,
) (string, error) {
	result, err := runner.runWithEnvironment(
		ctx, root, map[int]struct{}{0: {}}, nil,
		"-c", "protocol.allow=never", "-c", "protocol.file.allow=always",
		"ls-remote", "--refs", remoteURL, remoteRef,
	)
	if err != nil {
		return "", err
	}
	lines := nonEmptyLines(result.Stdout)
	if len(lines) == 0 {
		return zeroOID(oidLength), nil
	}
	if len(lines) != 1 {
		return "", errors.New("remote ref lookup is ambiguous")
	}
	oid, reference, found := strings.Cut(lines[0], "\t")
	if !found || reference != remoteRef || validateOID(oid, false) != nil {
		return "", errors.New("remote ref lookup returned invalid output")
	}
	return oid, nil
}

func (runner *MutationRunner) updateRemoteTrackingAndUpstream(
	ctx context.Context,
	root string,
	branchRef string,
	remoteRef string,
	newOID string,
	expectedRemoteOID string,
) error {
	trackingRef := "refs/remotes/origin/" + strings.TrimPrefix(remoteRef, "refs/heads/")
	if !safeRemoteTrackingRef(trackingRef) {
		return errors.New("derived remote-tracking ref is unsafe")
	}
	if _, err := runner.run(
		ctx, root, map[int]struct{}{0: {}},
		"update-ref", "--no-deref", "-m", "gds handoff push",
		trackingRef, newOID, expectedRemoteOID,
	); err != nil {
		return err
	}
	branch := strings.TrimPrefix(branchRef, "refs/heads/")
	if _, err := runner.run(
		ctx, root, map[int]struct{}{0: {}},
		"config", "--local", "branch."+branch+".remote", "origin",
	); err != nil {
		return err
	}
	_, err := runner.run(
		ctx, root, map[int]struct{}{0: {}},
		"config", "--local", "branch."+branch+".merge", remoteRef,
	)
	return err
}

func validateHandoffInput(
	branchRef string,
	expectedHeadOID string,
	remoteRef string,
	expectedRemoteOID string,
	files []string,
	message string,
	identity CommitIdentity,
	commitTime time.Time,
) error {
	trackingRef := "refs/remotes/origin/" + strings.TrimPrefix(remoteRef, "refs/heads/")
	if err := ValidateFastForwardRefs(branchRef, trackingRef); err != nil {
		return err
	}
	if err := validateOID(expectedHeadOID, false); err != nil {
		return err
	}
	if err := validateOID(expectedRemoteOID, true); err != nil ||
		len(expectedRemoteOID) != len(expectedHeadOID) {
		return errors.New("expected handoff remote OID is invalid")
	}
	if len(files) == 0 || len(files) > 4096 {
		return errors.New("handoff requires a bounded explicit file set")
	}
	seen := map[string]struct{}{}
	ordered := append([]string(nil), files...)
	sort.Strings(ordered)
	for _, path := range files {
		if !safeRepositoryPath(path) || path == ".git" || strings.HasPrefix(path, ".git/") ||
			strings.ContainsAny(path, "\x00\r\n") {
			return fmt.Errorf("handoff file path %q is unsafe", path)
		}
		if _, duplicate := seen[path]; duplicate {
			return errors.New("handoff file paths must be unique")
		}
		seen[path] = struct{}{}
	}
	for index := range files {
		if files[index] != ordered[index] {
			return errors.New("handoff file paths must be sorted")
		}
	}
	if strings.TrimSpace(message) == "" || len(message) > 4096 || strings.ContainsRune(message, '\x00') {
		return errors.New("handoff commit message is invalid")
	}
	if strings.TrimSpace(identity.Name) == "" || strings.TrimSpace(identity.Email) == "" ||
		len(identity.Name) > 256 || len(identity.Email) > 320 ||
		strings.ContainsAny(identity.Name+identity.Email, "\x00\r\n<>") ||
		!strings.Contains(identity.Email, "@") || commitTime.IsZero() {
		return errors.New("handoff commit identity or timestamp is invalid")
	}
	return nil
}
