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
)

type RemoteRef struct {
	Reference string `json:"reference"`
	OID       string `json:"oid"`
}

type RefChange struct {
	Reference string `json:"reference"`
	BeforeOID string `json:"before_oid,omitempty"`
	AfterOID  string `json:"after_oid,omitempty"`
	Kind      string `json:"kind"`
}

type FetchReport struct {
	Remote  string      `json:"remote"`
	Before  []RemoteRef `json:"before"`
	After   []RemoteRef `json:"after"`
	Changes []RefChange `json:"changes"`
}

func (runner *MutationRunner) FetchRemote(
	ctx context.Context,
	directory string,
	remote string,
) (FetchReport, error) {
	root, remoteURL, err := runner.validatedRemoteURL(ctx, directory, remote)
	if err != nil {
		return FetchReport{}, err
	}
	before, err := runner.remoteTrackingRefs(ctx, root, remote)
	if err != nil {
		return FetchReport{}, err
	}
	refspec := "+refs/heads/*:refs/remotes/" + remote + "/*"
	args := []string{
		"-c", "protocol.allow=never",
		"-c", "protocol.file.allow=always",
		"-c", "protocol.https.allow=always",
		"-c", "protocol.ssh.allow=always",
		"fetch", "--no-tags", "--no-write-fetch-head", "--no-recurse-submodules",
		"--no-auto-gc", remoteURL, refspec,
	}
	if _, err := runner.runWithEnvironment(
		ctx, root, map[int]struct{}{0: {}}, nil, args...,
	); err != nil {
		return FetchReport{}, err
	}
	after, err := runner.remoteTrackingRefs(ctx, root, remote)
	if err != nil {
		return FetchReport{}, err
	}
	changes, err := runner.classifyRefChanges(ctx, root, before, after)
	if err != nil {
		return FetchReport{}, err
	}
	return FetchReport{Remote: remote, Before: before, After: after, Changes: changes}, nil
}

// ObserveRemoteBranch reads one exact remote branch OID without updating any
// local ref, object, index, worktree, or FETCH_HEAD state.
func (runner *MutationRunner) ObserveRemoteBranch(
	ctx context.Context,
	directory string,
	remote string,
	branchRef string,
) (string, error) {
	oid, found, err := runner.ObserveRemoteBranchOptional(ctx, directory, remote, branchRef)
	if err != nil {
		return "", err
	}
	if !found {
		return "", errors.New("remote branch does not exist")
	}
	return oid, nil
}

func (runner *MutationRunner) ObserveRemoteBranchOptional(
	ctx context.Context,
	directory string,
	remote string,
	branchRef string,
) (string, bool, error) {
	if err := validateLocalBranchRef(branchRef); err != nil {
		return "", false, err
	}
	root, remoteURL, err := runner.validatedRemoteURL(ctx, directory, remote)
	if err != nil {
		return "", false, err
	}
	result, err := runner.runWithEnvironment(
		ctx, root, map[int]struct{}{0: {}}, nil,
		"-c", "protocol.allow=never",
		"-c", "protocol.file.allow=always",
		"-c", "protocol.https.allow=always",
		"-c", "protocol.ssh.allow=always",
		"ls-remote", "--refs", remoteURL, branchRef,
	)
	if err != nil {
		return "", false, err
	}
	lines := nonEmptyLines(result.Stdout)
	if len(lines) == 0 {
		return "", false, nil
	}
	if len(lines) != 1 {
		return "", false, fmt.Errorf("remote branch lookup returned %d refs", len(lines))
	}
	oid, reference, found := strings.Cut(lines[0], "\t")
	if !found || reference != branchRef || validateOID(oid, false) != nil {
		return "", false, errors.New("remote branch lookup returned invalid output")
	}
	return oid, true, nil
}

func (runner *MutationRunner) validatedRemoteURL(
	ctx context.Context,
	directory string,
	remote string,
) (string, string, error) {
	if !safeRemoteName(remote) {
		return "", "", fmt.Errorf("unsafe remote name")
	}
	root, err := validateMutationRoot(directory)
	if err != nil {
		return "", "", err
	}
	if err := runner.validateFetchConfiguration(ctx, root, remote); err != nil {
		return "", "", err
	}
	readRunner := runner.readRunner()
	urlResult, err := readRunner.Run(ctx, root, "remote", "get-url", "--all", remote)
	if err != nil {
		return "", "", fmt.Errorf("resolve remote URL: %w", err)
	}
	urls := nonEmptyLines(urlResult.Stdout)
	if len(urls) != 1 {
		return "", "", fmt.Errorf("remote must have exactly one URL")
	}
	if err := validateFetchURL(root, urls[0]); err != nil {
		return "", "", err
	}
	return root, urls[0], nil
}

func (runner *MutationRunner) remoteTrackingRefs(
	ctx context.Context,
	root string,
	remote string,
) ([]RemoteRef, error) {
	prefix := "refs/remotes/" + remote + "/"
	result, err := runner.run(
		ctx, root, map[int]struct{}{0: {}},
		"for-each-ref", "--format=%(refname)%09%(objectname)", prefix,
	)
	if err != nil {
		return nil, err
	}
	refs := []RemoteRef{}
	for _, line := range nonEmptyLines(result.Stdout) {
		reference, oid, found := strings.Cut(line, "\t")
		if !found || !strings.HasPrefix(reference, prefix) || validateOID(oid, false) != nil {
			return nil, fmt.Errorf("invalid remote-tracking ref output")
		}
		refs = append(refs, RemoteRef{Reference: reference, OID: oid})
	}
	sort.Slice(refs, func(left, right int) bool { return refs[left].Reference < refs[right].Reference })
	return refs, nil
}

// RemoteTrackingRefs returns a bounded, sorted snapshot without contacting the
// remote. Callers must compare it with durable refresh evidence before use.
func (runner *MutationRunner) RemoteTrackingRefs(
	ctx context.Context,
	directory string,
	remote string,
) ([]RemoteRef, error) {
	if !safeRemoteName(remote) {
		return nil, fmt.Errorf("unsafe remote name")
	}
	root, err := validateMutationRoot(directory)
	if err != nil {
		return nil, err
	}
	return runner.remoteTrackingRefs(ctx, root, remote)
}

func (runner *MutationRunner) classifyRefChanges(
	ctx context.Context,
	root string,
	before []RemoteRef,
	after []RemoteRef,
) ([]RefChange, error) {
	previous := map[string]string{}
	current := map[string]string{}
	for _, item := range before {
		previous[item.Reference] = item.OID
	}
	for _, item := range after {
		current[item.Reference] = item.OID
	}
	names := map[string]struct{}{}
	for name := range previous {
		names[name] = struct{}{}
	}
	for name := range current {
		names[name] = struct{}{}
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)
	changes := []RefChange{}
	for _, name := range ordered {
		oldOID, oldFound := previous[name]
		newOID, newFound := current[name]
		if oldFound && newFound && oldOID == newOID {
			continue
		}
		change := RefChange{Reference: name, BeforeOID: oldOID, AfterOID: newOID}
		switch {
		case !oldFound:
			change.Kind = "created"
		case !newFound:
			change.Kind = "deleted"
		default:
			ancestor, err := runner.isAncestor(ctx, root, oldOID, newOID)
			if err != nil {
				return nil, err
			}
			if ancestor {
				change.Kind = "fast-forward"
			} else {
				change.Kind = "forced-update"
			}
		}
		changes = append(changes, change)
	}
	return changes, nil
}

func (runner *MutationRunner) isAncestor(
	ctx context.Context,
	root string,
	ancestor string,
	descendant string,
) (bool, error) {
	if validateOID(ancestor, false) != nil || validateOID(descendant, false) != nil {
		return false, fmt.Errorf("invalid ancestry object id")
	}
	result, err := runner.run(
		ctx, root, map[int]struct{}{0: {}, 1: {}},
		"merge-base", "--is-ancestor", ancestor, descendant,
	)
	if err != nil {
		return false, err
	}
	return result.ExitCode == 0, nil
}

func (runner *MutationRunner) IsAncestor(
	ctx context.Context,
	directory string,
	ancestor string,
	descendant string,
) (bool, error) {
	root, err := validateMutationRoot(directory)
	if err != nil {
		return false, err
	}
	return runner.isAncestor(ctx, root, ancestor, descendant)
}

func (runner *MutationRunner) validateFetchConfiguration(
	ctx context.Context,
	root string,
	remote string,
) error {
	result, err := runner.run(
		ctx, root, map[int]struct{}{0: {}, 1: {}},
		"config", "--local", "--no-includes", "--null", "--name-only", "--list",
	)
	if err != nil {
		return err
	}
	if result.ExitCode == 1 {
		return nil
	}
	remotePrefix := "remote." + strings.ToLower(remote) + "."
	for _, raw := range strings.Split(string(result.Stdout), "\x00") {
		key := strings.ToLower(strings.TrimSpace(raw))
		if key == "" {
			continue
		}
		unsafe := strings.HasPrefix(key, "include.") || strings.HasPrefix(key, "includeif.") ||
			strings.HasPrefix(key, "url.") || strings.HasPrefix(key, "credential.") ||
			strings.HasPrefix(key, "filter.") || key == "core.sshcommand" ||
			key == "core.hookspath" || strings.HasPrefix(key, "http.")
		if strings.HasPrefix(key, remotePrefix) {
			suffix := strings.TrimPrefix(key, remotePrefix)
			unsafe = unsafe || suffix == "uploadpack" || suffix == "vcs" || suffix == "proxy"
		}
		if unsafe {
			return fmt.Errorf("fetch blocked by unsafe local Git config key %q", key)
		}
	}
	return nil
}

func validateFetchURL(root string, raw string) error {
	if raw == "" || strings.ContainsAny(raw, "\x00\r\n") || strings.HasPrefix(raw, "-") ||
		strings.Contains(raw, "::") {
		return errors.New("fetch URL is unsafe")
	}
	if !strings.Contains(raw, "://") && strings.Contains(raw, ":") && !filepath.IsAbs(raw) {
		identity, path, found := strings.Cut(raw, ":")
		if !found || identity == "" || path == "" || strings.ContainsAny(identity, "/\\ ") ||
			strings.ContainsAny(path, "\r\n") {
			return errors.New("SSH fetch URL is unsafe")
		}
		if strings.Contains(identity, "@") {
			user, host, found := strings.Cut(identity, "@")
			if !found || user != "git" || host == "" || strings.Contains(host, "@") {
				return errors.New("credential-bearing SSH fetch URL is forbidden")
			}
		}
		return nil
	}
	parsed, err := url.Parse(raw)
	if err == nil && parsed.Scheme != "" {
		switch parsed.Scheme {
		case "https", "ssh":
			if parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
				return errors.New("network fetch URL is incomplete or contains query material")
			}
			if err := validateCredentialFreeNetworkURL(parsed); err != nil {
				return fmt.Errorf("credential-bearing fetch URL is forbidden: %w", err)
			}
			return nil
		case "file":
			if parsed.Host != "" && parsed.Host != "localhost" {
				return errors.New("file fetch URL host is forbidden")
			}
			raw = parsed.Path
		default:
			return fmt.Errorf("fetch URL scheme %q is forbidden", parsed.Scheme)
		}
	}
	path := raw
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return errors.New("local fetch URL cannot be resolved")
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("local fetch URL must identify a real directory")
	}
	return nil
}
