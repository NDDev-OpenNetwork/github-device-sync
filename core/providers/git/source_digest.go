package git

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
)

// SourceTreeDigest returns a content digest of the declared canonical source
// paths, taken from the index rather than from history.
//
// This is the identity a generated projection is bound to. Using a commit for
// that job forces a self-reference: a commit cannot contain its own SHA, so
// changing a covered input and regenerating could never be one atomic commit,
// and every dependency bump needed a follow-up re-pin whose only purpose was to
// teach the first commit its own identity. It also cost the 128-level
// merge-collapse walk in CommittedSourceOID, which exists only to stabilize a
// commit identity that content addressing gives for free.
//
// The digest is built from the blob object ids git already records for staged
// content, so it is content-addressed by construction and never reads a working
// tree file. Binding to the index rather than to HEAD is what allows a source
// edit and its regenerated projection to land in one commit: stage the edit,
// generate, stage the output, commit once.
func (runner *Runner) SourceTreeDigest(
	ctx context.Context,
	directory string,
	paths []string,
) (string, error) {
	if len(paths) == 0 {
		return "", fmt.Errorf("canonical source path set is empty")
	}
	for _, path := range paths {
		if !safeRepositoryPath(path) {
			return "", fmt.Errorf("canonical source path %q is invalid", path)
		}
	}

	// The runner allows exactly `git ls-files --stage -z`, with no pathspec, so
	// the declared boundary is applied here instead of by git.
	listed, err := runner.Run(ctx, directory, "ls-files", "--stage", "-z")
	if err != nil {
		return "", fmt.Errorf("enumerate canonical source files: %w", err)
	}

	entries := map[string]string{}
	for _, record := range strings.Split(string(listed.Stdout), "\x00") {
		if record == "" {
			continue
		}
		// `<mode> <object> <stage>\t<path>`
		metadata, relative, found := strings.Cut(record, "\t")
		if !found {
			return "", fmt.Errorf("unexpected canonical source index record")
		}
		fields := strings.Fields(metadata)
		if len(fields) != 3 {
			return "", fmt.Errorf("unexpected canonical source index metadata")
		}
		if fields[2] != "0" {
			// A non-zero stage means an unresolved merge conflict; the source
			// content is not yet a single decidable state.
			return "", fmt.Errorf("canonical source %q has an unmerged index entry", relative)
		}
		if !safeRepositoryPath(relative) {
			return "", fmt.Errorf("canonical source file %q is invalid", relative)
		}
		if !withinDeclaredSourcePaths(relative, paths) {
			continue
		}
		entries[relative] = fields[1]
	}

	ordered := make([]string, 0, len(entries))
	for relative := range entries {
		ordered = append(ordered, relative)
	}
	sort.Strings(ordered)

	manifest := sha256.New()
	for _, relative := range ordered {
		// Length-prefix the path so no two different (path, object) pairs can
		// produce the same manifest bytes.
		fmt.Fprintf(manifest, "%d:%s\x00%s\n", len(relative), relative, entries[relative])
	}
	return "sha256:" + hex.EncodeToString(manifest.Sum(nil)), nil
}

// withinDeclaredSourcePaths reports whether one repository-relative file lies
// under a declared canonical path. A declared path is either that exact file or
// a directory prefix; a shared name prefix such as `core/apply` against a
// declared `core/app` is not a match.
func withinDeclaredSourcePaths(relative string, paths []string) bool {
	for _, path := range paths {
		if relative == path || strings.HasPrefix(relative, path+"/") {
			return true
		}
	}
	return false
}
