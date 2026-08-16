package git

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const submoduleConfigPattern = `^submodule\..*\.(path|url|branch)$`

const remoteConfigPattern = `^remote\..*\.(url|pushurl)$`

var scpGitHubURL = regexp.MustCompile(`^(?:git@)?github\.com:([^/]+)/([^/]+)$`)

type Topology struct {
	Repository RepositoryInfo `json:"repository"`
	Remotes    []Remote       `json:"remotes"`
	Submodules []Submodule    `json:"submodules"`
}

type Remote struct {
	Name      string      `json:"name"`
	FetchURLs []RemoteURL `json:"fetch_urls"`
	PushURLs  []RemoteURL `json:"push_urls"`
}

type RemoteURL struct {
	Value               string `json:"value"`
	CredentialsRedacted bool   `json:"credentials_redacted"`
}

type Submodule struct {
	Name          string `json:"name,omitempty"`
	Path          string `json:"path"`
	URL           string `json:"url,omitempty"`
	URLRedacted   bool   `json:"url_redacted"`
	Branch        string `json:"branch,omitempty"`
	GitlinkOID    string `json:"gitlink_oid,omitempty"`
	GitlinkStage  int    `json:"gitlink_stage"`
	CurrentOID    string `json:"current_oid,omitempty"`
	WorktreeState string `json:"worktree_state"`
}

type GitHubRepository struct {
	Host  string `json:"host"`
	Owner string `json:"owner"`
	Name  string `json:"name"`
}

type RefComparison struct {
	LeftRef   string `json:"left_ref"`
	RightRef  string `json:"right_ref"`
	LeftOID   string `json:"left_oid,omitempty"`
	RightOID  string `json:"right_oid,omitempty"`
	LeftOnly  int    `json:"left_only"`
	RightOnly int    `json:"right_only"`
	Available bool   `json:"available"`
	Freshness string `json:"freshness"`
}

type configuredSubmodule struct {
	name   string
	path   string
	url    string
	branch string
}

type gitlink struct {
	oid   string
	stage int
}

func (runner *Runner) InspectTopology(ctx context.Context, directory string) (Topology, error) {
	info, err := runner.RepositoryInfo(ctx, directory)
	if err != nil {
		return Topology{}, err
	}
	remotes, err := runner.inspectRemotes(ctx, info.WorktreeRoot)
	if err != nil {
		return Topology{}, err
	}
	configured, err := runner.readConfiguredSubmodules(ctx, info.WorktreeRoot)
	if err != nil {
		return Topology{}, err
	}
	links, err := runner.readGitlinks(ctx, info.WorktreeRoot)
	if err != nil {
		return Topology{}, err
	}

	byPath := map[string]Submodule{}
	for _, item := range configured {
		sanitizedURL, redacted := sanitizeRepositoryURL(item.url)
		byPath[item.path] = Submodule{
			Name: item.name, Path: item.path, URL: sanitizedURL, URLRedacted: redacted,
			Branch:        item.branch,
			WorktreeState: "uninitialized",
		}
	}
	for gitlinkPath, link := range links {
		item := byPath[gitlinkPath]
		item.Path = gitlinkPath
		item.GitlinkOID = link.oid
		item.GitlinkStage = link.stage
		if item.WorktreeState == "" {
			item.WorktreeState = "unconfigured"
		}
		byPath[gitlinkPath] = item
	}
	paths := make([]string, 0, len(byPath))
	for itemPath := range byPath {
		paths = append(paths, itemPath)
	}
	sort.Strings(paths)
	submodules := make([]Submodule, 0, len(paths))
	for _, itemPath := range paths {
		item := byPath[itemPath]
		if item.Name != "" {
			item.CurrentOID, item.WorktreeState = runner.inspectSubmoduleWorktree(
				ctx, info.WorktreeRoot, item,
			)
		}
		submodules = append(submodules, item)
	}
	return Topology{Repository: info, Remotes: remotes, Submodules: submodules}, nil
}

func (runner *Runner) inspectRemotes(ctx context.Context, root string) ([]Remote, error) {
	result, err := runner.Run(ctx, root, "remote")
	if err != nil {
		return nil, err
	}
	names := []string{}
	for _, name := range strings.Split(strings.TrimSpace(string(result.Stdout)), "\n") {
		if name == "" {
			continue
		}
		if !utf8.ValidString(name) || strings.ContainsAny(name, "\x00\r") {
			return nil, fmt.Errorf("remote name is not safe UTF-8")
		}
		names = append(names, name)
	}
	sort.Strings(names)
	configured, err := runner.Run(
		ctx, root, "config", "--local", "--no-includes", "--null", "--get-regexp", remoteConfigPattern,
	)
	if err != nil && len(names) != 0 {
		return nil, err
	}
	urls, err := parseRemoteConfig(configured.Stdout)
	if err != nil {
		return nil, err
	}
	remotes := make([]Remote, 0, len(names))
	for _, name := range names {
		fetch := urls[name+"\x00url"]
		if len(fetch) == 0 {
			return nil, fmt.Errorf("remote %q has no repository URL", name)
		}
		push := urls[name+"\x00pushurl"]
		if len(push) == 0 {
			push = fetch
		}
		remotes = append(remotes, Remote{
			Name: name, FetchURLs: sanitizeRemoteURLs(fetch), PushURLs: sanitizeRemoteURLs(push),
		})
	}
	return remotes, nil
}

func parseRemoteConfig(raw []byte) (map[string][]string, error) {
	result := map[string][]string{}
	for _, record := range bytes.Split(raw, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		keyRaw, valueRaw, found := bytes.Cut(record, []byte{'\n'})
		key := string(keyRaw)
		value := string(valueRaw)
		if !found || !utf8.ValidString(key) || !utf8.ValidString(value) ||
			strings.ContainsAny(key+value, "\x00\r\n") || !strings.HasPrefix(key, "remote.") {
			return nil, errors.New("remote configuration is not safe UTF-8")
		}
		kind := ""
		switch {
		case strings.HasSuffix(key, ".pushurl"):
			kind = "pushurl"
			key = strings.TrimSuffix(strings.TrimPrefix(key, "remote."), ".pushurl")
		case strings.HasSuffix(key, ".url"):
			kind = "url"
			key = strings.TrimSuffix(strings.TrimPrefix(key, "remote."), ".url")
		default:
			return nil, errors.New("remote configuration key is unsupported")
		}
		if key == "" || value == "" {
			return nil, errors.New("remote configuration is incomplete")
		}
		result[key+"\x00"+kind] = append(result[key+"\x00"+kind], value)
	}
	return result, nil
}

func (runner *Runner) readConfiguredSubmodules(
	ctx context.Context,
	root string,
) ([]configuredSubmodule, error) {
	moduleFile := filepath.Join(root, ".gitmodules")
	info, err := os.Lstat(moduleFile)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("inspect .gitmodules: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf(".gitmodules must be a regular non-symlink file")
	}
	result, err := runner.run(
		ctx, root, map[int]struct{}{0: {}, 1: {}},
		"config", "--file", ".gitmodules", "--no-includes", "--null",
		"--get-regexp", submoduleConfigPattern,
	)
	if err != nil {
		return nil, err
	}
	if result.ExitCode == 1 {
		return nil, nil
	}
	return parseConfiguredSubmodules(result.Stdout)
}

func parseConfiguredSubmodules(raw []byte) ([]configuredSubmodule, error) {
	byName := map[string]*configuredSubmodule{}
	for _, record := range strings.Split(string(raw), "\x00") {
		if record == "" {
			continue
		}
		key, value, found := strings.Cut(record, "\n")
		if !found || !strings.HasPrefix(key, "submodule.") {
			return nil, fmt.Errorf("invalid .gitmodules config record")
		}
		remainder := strings.TrimPrefix(key, "submodule.")
		lastDot := strings.LastIndexByte(remainder, '.')
		if lastDot <= 0 || lastDot == len(remainder)-1 {
			return nil, fmt.Errorf("invalid submodule config key %q", key)
		}
		name, field := remainder[:lastDot], remainder[lastDot+1:]
		item := byName[name]
		if item == nil {
			item = &configuredSubmodule{name: name}
			byName[name] = item
		}
		switch field {
		case "path":
			if item.path != "" {
				return nil, fmt.Errorf("duplicate path for submodule %q", name)
			}
			if err := validateSubmodulePath(value); err != nil {
				return nil, fmt.Errorf("submodule %q: %w", name, err)
			}
			item.path = value
		case "url":
			if item.url != "" {
				return nil, fmt.Errorf("duplicate URL for submodule %q", name)
			}
			item.url = value
		case "branch":
			if item.branch != "" {
				return nil, fmt.Errorf("duplicate branch for submodule %q", name)
			}
			item.branch = value
		default:
			return nil, fmt.Errorf("unexpected submodule field %q", field)
		}
	}
	names := make([]string, 0, len(byName))
	paths := map[string]string{}
	for name, item := range byName {
		if item.path == "" || item.url == "" {
			return nil, fmt.Errorf("submodule %q requires path and URL", name)
		}
		if other, duplicate := paths[item.path]; duplicate {
			return nil, fmt.Errorf("submodules %q and %q share path %q", other, name, item.path)
		}
		paths[item.path] = name
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]configuredSubmodule, 0, len(names))
	for _, name := range names {
		result = append(result, *byName[name])
	}
	return result, nil
}

func (runner *Runner) readGitlinks(ctx context.Context, root string) (map[string]gitlink, error) {
	result, err := runner.Run(ctx, root, "ls-files", "--stage", "-z")
	if err != nil {
		return nil, err
	}
	links := map[string]gitlink{}
	for _, record := range strings.Split(string(result.Stdout), "\x00") {
		if record == "" {
			continue
		}
		header, itemPath, found := strings.Cut(record, "\t")
		fields := strings.Fields(header)
		if !found || len(fields) != 3 {
			return nil, fmt.Errorf("invalid git ls-files stage record")
		}
		if fields[0] != "160000" {
			continue
		}
		if !utf8.ValidString(itemPath) {
			return nil, fmt.Errorf("gitlink path is not valid UTF-8")
		}
		if err := validateSubmodulePath(itemPath); err != nil {
			return nil, err
		}
		stage, err := strconv.Atoi(fields[2])
		if err != nil || stage < 0 || stage > 3 {
			return nil, fmt.Errorf("invalid gitlink stage %q", fields[2])
		}
		if _, duplicate := links[itemPath]; duplicate {
			return nil, fmt.Errorf("multiple gitlinks exist for path %q", itemPath)
		}
		links[itemPath] = gitlink{oid: fields[1], stage: stage}
	}
	return links, nil
}

func (runner *Runner) inspectSubmoduleWorktree(
	ctx context.Context,
	root string,
	item Submodule,
) (string, string) {
	modulePath := filepath.Join(root, filepath.FromSlash(item.Path))
	info, err := os.Lstat(modulePath)
	if os.IsNotExist(err) {
		return "", "uninitialized"
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", "unsafe"
	}
	marker := filepath.Join(modulePath, ".git")
	markerInfo, markerErr := os.Lstat(marker)
	if os.IsNotExist(markerErr) {
		entries, readErr := os.ReadDir(modulePath)
		if readErr == nil && len(entries) == 0 {
			return "", "uninitialized"
		}
		return "", "unavailable"
	}
	if markerErr != nil || markerInfo.Mode()&os.ModeSymlink != 0 ||
		(!markerInfo.IsDir() && !markerInfo.Mode().IsRegular()) {
		return "", "unsafe"
	}
	oid, err := runner.HeadOID(ctx, modulePath)
	if err != nil {
		return "", "unavailable"
	}
	if item.GitlinkOID == "" {
		return oid, "untracked-gitlink"
	}
	if oid == item.GitlinkOID {
		return oid, "at-gitlink"
	}
	return oid, "off-gitlink"
}

func (runner *Runner) CompareCachedRemoteRefs(
	ctx context.Context,
	directory string,
	leftRemote string,
	rightRemote string,
	branch string,
) (RefComparison, error) {
	left := "refs/remotes/" + leftRemote + "/" + branch
	right := "refs/remotes/" + rightRemote + "/" + branch
	if !safeRemoteTrackingRef(left) || !safeRemoteTrackingRef(right) {
		return RefComparison{}, fmt.Errorf("remote or branch cannot form a safe tracking ref")
	}
	refs, err := runner.Run(
		ctx, directory, "for-each-ref", "--format=%(refname)%09%(objectname)", left, right,
	)
	if err != nil {
		return RefComparison{}, err
	}
	oids := map[string]string{}
	for _, line := range nonEmptyLines(refs.Stdout) {
		name, oid, found := strings.Cut(line, "\t")
		if !found {
			return RefComparison{}, fmt.Errorf("invalid for-each-ref output")
		}
		oids[name] = oid
	}
	comparison := RefComparison{
		LeftRef: left, RightRef: right, LeftOID: oids[left], RightOID: oids[right],
		Freshness: "cached-unknown",
	}
	if comparison.LeftOID == "" || comparison.RightOID == "" {
		return comparison, nil
	}
	counts, err := runner.Run(
		ctx, directory, "rev-list", "--left-right", "--count", left+"..."+right,
	)
	if err != nil {
		return RefComparison{}, err
	}
	fields := strings.Fields(string(counts.Stdout))
	if len(fields) != 2 {
		return RefComparison{}, fmt.Errorf("invalid rev-list count output")
	}
	comparison.LeftOnly, err = strconv.Atoi(fields[0])
	if err == nil {
		comparison.RightOnly, err = strconv.Atoi(fields[1])
	}
	if err != nil {
		return RefComparison{}, fmt.Errorf("decode rev-list counts: %w", err)
	}
	comparison.Available = true
	return comparison, nil
}

func ParseGitHubRepository(raw string) (GitHubRepository, error) {
	value := strings.TrimSpace(raw)
	if match := scpGitHubURL.FindStringSubmatch(value); match != nil {
		return githubRepository("github.com", match[1], match[2])
	}
	parsed, err := url.Parse(value)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return GitHubRepository{}, fmt.Errorf("unsupported GitHub repository URL")
	}
	if parsed.Scheme != "https" && parsed.Scheme != "ssh" && parsed.Scheme != "git" {
		return GitHubRepository{}, fmt.Errorf("unsupported GitHub repository URL scheme %q", parsed.Scheme)
	}
	if err := validateCredentialFreeNetworkURL(parsed); err != nil {
		return GitHubRepository{}, err
	}
	if parsed.RawPath != "" || strings.Contains(parsed.EscapedPath(), "%") {
		return GitHubRepository{}, fmt.Errorf("encoded GitHub repository paths are forbidden")
	}
	host := parsed.Hostname()
	if !strings.EqualFold(host, "github.com") {
		return GitHubRepository{}, fmt.Errorf("repository host %q is not github.com", host)
	}
	parts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(parts) != 2 {
		return GitHubRepository{}, fmt.Errorf("GitHub repository URL must contain owner and name")
	}
	return githubRepository("github.com", parts[0], parts[1])
}

func validateCredentialFreeNetworkURL(parsed *url.URL) error {
	if parsed == nil {
		return fmt.Errorf("repository URL is invalid")
	}
	if parsed.User == nil {
		return nil
	}
	_, hasPassword := parsed.User.Password()
	if parsed.Scheme == "ssh" && parsed.User.Username() == "git" && !hasPassword {
		return nil
	}
	return fmt.Errorf("repository URL must not contain credentials")
}

func RewriteGitHubRepositoryURL(raw string, owner string, name string) (string, error) {
	if _, err := githubRepository("github.com", owner, name); err != nil {
		return "", err
	}
	value := strings.TrimSpace(raw)
	if match := scpGitHubURL.FindStringSubmatch(value); match != nil {
		prefix, _, _ := strings.Cut(value, ":")
		suffix := ""
		if strings.HasSuffix(match[2], ".git") {
			suffix = ".git"
		}
		return prefix + ":" + owner + "/" + name + suffix, nil
	}
	if _, err := ParseGitHubRepository(value); err != nil {
		return "", err
	}
	parsed, err := url.Parse(value)
	if err != nil {
		return "", err
	}
	suffix := ""
	if strings.HasSuffix(strings.TrimSuffix(parsed.Path, "/"), ".git") {
		suffix = ".git"
	}
	parsed.Path = "/" + owner + "/" + name + suffix
	parsed.RawPath = ""
	return parsed.String(), nil
}

func githubRepository(host, owner, name string) (GitHubRepository, error) {
	name = strings.TrimSuffix(name, ".git")
	if owner == "" || name == "" || strings.ContainsAny(owner+name, "\x00\r\n") ||
		strings.Contains(owner, "/") || strings.Contains(name, "/") {
		return GitHubRepository{}, fmt.Errorf("invalid GitHub owner or repository name")
	}
	return GitHubRepository{Host: host, Owner: owner, Name: name}, nil
}

func sanitizeRemoteURLs(values []string) []RemoteURL {
	result := make([]RemoteURL, 0, len(values))
	for _, value := range values {
		sanitized, redacted := sanitizeRepositoryURL(value)
		result = append(result, RemoteURL{Value: sanitized, CredentialsRedacted: redacted})
	}
	return result
}

func sanitizeRepositoryURL(raw string) (string, bool) {
	value := strings.TrimSpace(raw)
	if scpGitHubURL.MatchString(value) {
		return value, false
	}
	parsed, err := url.Parse(value)
	if err != nil || strings.ContainsAny(value, "\x00\r\n") {
		return "<redacted-invalid-url>", true
	}
	redacted := false
	if parsed.Scheme != "" && parsed.Host != "" {
		if parsed.User != nil {
			_, hasPassword := parsed.User.Password()
			if parsed.Scheme == "ssh" && parsed.User.Username() == "git" && !hasPassword {
				parsed.User = url.User("git")
			} else {
				parsed.User = nil
				redacted = true
			}
		}
		if parsed.RawQuery != "" || parsed.Fragment != "" {
			parsed.RawQuery = ""
			parsed.ForceQuery = false
			parsed.Fragment = ""
			redacted = true
		}
		return parsed.String(), redacted
	}
	if parsed.RawQuery != "" || parsed.Fragment != "" {
		parsed.RawQuery = ""
		parsed.ForceQuery = false
		parsed.Fragment = ""
		value = parsed.String()
		redacted = true
	}
	if strings.Contains(value, "@") && !strings.HasPrefix(value, "git@") {
		_, suffix, found := strings.Cut(value, "@")
		if found {
			return suffix, true
		}
	}
	return value, redacted
}

func validateSubmodulePath(value string) error {
	if value == "" || path.IsAbs(value) || path.Clean(value) != value || value == "." ||
		value == ".." || strings.HasPrefix(value, "../") || strings.Contains(value, "\\") ||
		strings.ContainsAny(value, "\x00\r\n") {
		return fmt.Errorf("submodule path %q is not a normalized repository-relative path", value)
	}
	return nil
}

func nonEmptyLines(raw []byte) []string {
	result := []string{}
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\r\n"), "\n") {
		if line != "" {
			result = append(result, line)
		}
	}
	return result
}
