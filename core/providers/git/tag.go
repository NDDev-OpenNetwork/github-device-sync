package git

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

const semverPattern = `(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-(?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*))*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?`

var safeVersionTagRef = regexp.MustCompile(`^refs/tags/v?` + semverPattern + `$`)

type TagEvidence struct {
	WorktreeRoot string `json:"worktree_root"`
	TagRef       string `json:"tag_ref"`
	CommitOID    string `json:"commit_oid"`
	LocalOID     string `json:"local_oid"`
	RemoteOID    string `json:"remote_oid"`
}

type TagReport struct {
	Before TagEvidence `json:"before"`
	After  TagEvidence `json:"after"`
}

func (runner *MutationRunner) PublishVersionTag(
	ctx context.Context,
	directory string,
	tagRef string,
	expectedCommitOID string,
) (TagReport, error) {
	if !safeVersionTagRef.MatchString(tagRef) || validateOID(expectedCommitOID, false) != nil {
		return TagReport{}, errors.New("version tag input is invalid")
	}
	root, err := validateMutationRoot(directory)
	if err != nil {
		return TagReport{}, err
	}
	if err := runner.validateWorktreeMutationConfiguration(ctx, root); err != nil {
		return TagReport{}, err
	}
	remoteURL, err := runner.validatedPushURL(ctx, root, "origin")
	if err != nil {
		return TagReport{}, err
	}
	before, err := runner.ObserveVersionTag(ctx, root, tagRef, expectedCommitOID)
	if err != nil {
		return TagReport{}, err
	}
	report := TagReport{Before: before}
	zero := zeroOID(len(expectedCommitOID))
	if before.LocalOID != zero || before.RemoteOID != zero {
		return report, errors.New("version tag already exists")
	}
	push := []string{
		"-c", "protocol.allow=never", "-c", "protocol.file.allow=always",
		"push", "--porcelain", "--no-verify",
		"--force-with-lease=" + tagRef + ":" + zero,
		remoteURL, expectedCommitOID + ":" + tagRef,
	}
	if _, err := runner.runWithEnvironment(
		ctx, root, map[int]struct{}{0: {}}, nil, push...,
	); err != nil {
		return report, err
	}
	remoteOID, err := runner.observeRemoteRef(ctx, root, remoteURL, tagRef, len(expectedCommitOID))
	if err != nil || remoteOID != expectedCommitOID {
		return report, errors.New("remote version tag verification failed")
	}
	if _, err := runner.run(
		ctx, root, map[int]struct{}{0: {}},
		"update-ref", "--no-deref", "-m", "gds module release",
		tagRef, expectedCommitOID, zero,
	); err != nil {
		return report, err
	}
	after, err := runner.ObserveVersionTag(ctx, root, tagRef, expectedCommitOID)
	if err != nil {
		return report, err
	}
	report.After = after
	if after.LocalOID != expectedCommitOID || after.RemoteOID != expectedCommitOID {
		return report, errors.New("version tag publication postcondition failed")
	}
	return report, nil
}

func (runner *MutationRunner) ObserveVersionTag(
	ctx context.Context,
	directory string,
	tagRef string,
	expectedCommitOID string,
) (TagEvidence, error) {
	if !safeVersionTagRef.MatchString(tagRef) || validateOID(expectedCommitOID, false) != nil {
		return TagEvidence{}, errors.New("version tag observation input is invalid")
	}
	root, err := validateMutationRoot(directory)
	if err != nil {
		return TagEvidence{}, err
	}
	localOID, err := runner.observeOptionalLocalRef(ctx, root, tagRef, len(expectedCommitOID))
	if err != nil {
		return TagEvidence{}, err
	}
	remoteURL, err := runner.validatedPushURL(ctx, root, "origin")
	if err != nil {
		return TagEvidence{}, err
	}
	remoteOID, err := runner.observeRemoteRef(ctx, root, remoteURL, tagRef, len(expectedCommitOID))
	if err != nil {
		return TagEvidence{}, err
	}
	return TagEvidence{
		WorktreeRoot: root, TagRef: tagRef, CommitOID: strings.ToLower(expectedCommitOID),
		LocalOID: localOID, RemoteOID: remoteOID,
	}, nil
}

func VersionTagRef(version string) (string, error) {
	return VersionTagRefWithStyle(version, "v-semver")
}

func VersionTagRefWithStyle(version string, style string) (string, error) {
	prefix := "v"
	switch style {
	case "", "v-semver":
	case "semver":
		prefix = ""
	default:
		return "", fmt.Errorf("version tag style %q is unsupported", style)
	}
	reference := "refs/tags/" + prefix + version
	if !safeVersionTagRef.MatchString(reference) {
		return "", fmt.Errorf("version %q is not canonical SemVer", version)
	}
	return reference, nil
}
