package git

import (
	"context"
	"errors"
	"strings"
)

// VersionArtifact binds both the tag object and its peeled commit. Retagging an
// annotated tag at the same commit still changes the artifact's identity.
type VersionArtifact struct {
	TagRef    string `json:"tag_ref"`
	TagOID    string `json:"tag_oid"`
	CommitOID string `json:"commit_oid"`
}

// ObserveVersionArtifact reads origin and the fetched local tag without moving
// refs or requiring push access. The caller must materialize the selected tag.
func (runner *MutationRunner) ObserveVersionArtifact(ctx context.Context, directory, tagRef string) (VersionArtifact, error) {
	if !safeVersionTagRef.MatchString(tagRef) {
		return VersionArtifact{}, errors.New("invalid version artifact tag")
	}
	root, remoteURL, err := runner.validatedRemoteURL(ctx, directory, "origin")
	if err != nil {
		return VersionArtifact{}, err
	}
	result, err := runner.runWithEnvironment(ctx, root, map[int]struct{}{0: {}}, nil,
		"-c", "protocol.allow=never", "-c", "protocol.file.allow=always",
		"-c", "protocol.https.allow=always", "-c", "protocol.ssh.allow=always",
		"ls-remote", remoteURL, tagRef, tagRef+"^{}")
	if err != nil {
		return VersionArtifact{}, err
	}
	refs := map[string]string{}
	for _, line := range nonEmptyLines(result.Stdout) {
		oid, ref, ok := strings.Cut(line, "\t")
		if !ok || (ref != tagRef && ref != tagRef+"^{}") || validateOID(oid, false) != nil || refs[ref] != "" {
			return VersionArtifact{}, errors.New("ambiguous version artifact response")
		}
		refs[ref] = oid
	}
	if refs[tagRef] == "" {
		return VersionArtifact{}, errors.New("version tag is not published")
	}
	commit := refs[tagRef+"^{}"]
	if commit == "" {
		commit = refs[tagRef]
	}
	for ref, want := range map[string]string{tagRef: refs[tagRef], tagRef + "^{commit}": commit} {
		local, readErr := runner.run(ctx, root, map[int]struct{}{0: {}}, "rev-parse", "--verify", ref)
		if readErr != nil || strings.TrimSpace(string(local.Stdout)) != want {
			return VersionArtifact{}, errors.New("local version tag does not match published artifact")
		}
	}
	return VersionArtifact{TagRef: tagRef, TagOID: refs[tagRef], CommitOID: commit}, nil
}
