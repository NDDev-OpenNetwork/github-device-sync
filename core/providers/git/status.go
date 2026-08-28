package git

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
)

type RepositoryInfo struct {
	WorktreeRoot     string `json:"worktree_root"`
	CommonGitDir     string `json:"common_git_dir"`
	SuperprojectRoot string `json:"superproject_root,omitempty"`
}

type Status struct {
	Repository      RepositoryInfo `json:"repository"`
	Head            HeadState      `json:"head"`
	Branch          BranchState    `json:"branch"`
	Changes         ChangeState    `json:"changes"`
	Submodules      SubmoduleState `json:"submodules"`
	Worktrees       []Worktree     `json:"worktrees"`
	Classification  string         `json:"classification"`
	RemoteFreshness string         `json:"remote_freshness"`
}

type HeadState struct {
	Mode string `json:"mode"`
	OID  string `json:"oid,omitempty"`
}

type BranchState struct {
	Name            string `json:"name,omitempty"`
	Upstream        string `json:"upstream,omitempty"`
	UpstreamState   string `json:"upstream_state"`
	ComparisonState string `json:"comparison_state"`
	Ahead           int    `json:"ahead"`
	Behind          int    `json:"behind"`
	Diverged        bool   `json:"diverged"`
}

type ChangeState struct {
	Staged           int `json:"staged"`
	Unstaged         int `json:"unstaged"`
	Untracked        int `json:"untracked"`
	Conflicted       int `json:"conflicted"`
	SubmoduleChanges int `json:"submodule_changes"`
}

type SubmoduleState struct {
	Total         int `json:"total"`
	Clean         int `json:"clean"`
	Modified      int `json:"modified"`
	Uninitialized int `json:"uninitialized"`
	Conflicted    int `json:"conflicted"`
}

type Worktree struct {
	Path     string `json:"path"`
	Head     string `json:"head,omitempty"`
	Branch   string `json:"branch,omitempty"`
	Bare     bool   `json:"bare"`
	Detached bool   `json:"detached"`
	Locked   bool   `json:"locked"`
	Prunable bool   `json:"prunable"`
}

func (runner *Runner) HeadOID(ctx context.Context, directory string) (string, error) {
	result, err := runner.Run(ctx, directory, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return "", err
	}
	value := strings.TrimSpace(string(result.Stdout))
	if len(value) != 40 && len(value) != 64 {
		return "", fmt.Errorf("unexpected HEAD object id %q", value)
	}
	return value, nil
}

func (runner *Runner) RepositoryInfo(ctx context.Context, directory string) (RepositoryInfo, error) {
	worktreeRoot, err := runner.gitPath(
		ctx, directory, "--path-format=absolute", "--show-toplevel",
	)
	if err != nil {
		return RepositoryInfo{}, err
	}
	commonGitDir, err := runner.gitPath(
		ctx, directory, "--path-format=absolute", "--git-common-dir",
	)
	if err != nil {
		return RepositoryInfo{}, err
	}
	superprojectRoot, err := runner.gitPath(
		ctx, directory, "--path-format=absolute", "--show-superproject-working-tree",
	)
	if err != nil {
		return RepositoryInfo{}, err
	}
	return RepositoryInfo{
		WorktreeRoot:     filepath.Clean(worktreeRoot),
		CommonGitDir:     filepath.Clean(commonGitDir),
		SuperprojectRoot: cleanOptionalPath(superprojectRoot),
	}, nil
}

func (runner *Runner) gitPath(
	ctx context.Context,
	directory string,
	args ...string,
) (string, error) {
	result, err := runner.Run(ctx, directory, append([]string{"rev-parse"}, args...)...)
	if err != nil {
		return "", err
	}
	value := strings.TrimSuffix(string(result.Stdout), "\n")
	if strings.HasPrefix(value, "\"") {
		unquoted, err := strconv.Unquote(value)
		if err != nil {
			return "", fmt.Errorf("decode quoted Git path: %w", err)
		}
		value = unquoted
	}
	return value, nil
}

func cleanOptionalPath(path string) string {
	if path == "" {
		return ""
	}
	return filepath.Clean(path)
}

func (runner *Runner) InspectStatus(ctx context.Context, directory string) (Status, error) {
	info, err := runner.RepositoryInfo(ctx, directory)
	if err != nil {
		return Status{}, err
	}
	statusResult, err := runner.Run(
		ctx, info.WorktreeRoot, "status", "--porcelain=v2", "--branch", "-z",
	)
	if err != nil {
		return Status{}, err
	}
	worktreeResult, err := runner.Run(
		ctx, info.WorktreeRoot, "worktree", "list", "--porcelain", "-z",
	)
	if err != nil {
		return Status{}, err
	}
	submoduleResult, submoduleErr := runner.Run(
		ctx, info.WorktreeRoot, "submodule", "status", "--recursive",
	)
	if submoduleErr != nil && !strings.Contains(submoduleErr.Error(), "no submodule mapping found") {
		return Status{}, submoduleErr
	}

	status, err := parsePorcelainV2(statusResult.Stdout)
	if err != nil {
		return Status{}, err
	}
	status.Repository = info
	status.Worktrees = parseWorktrees(worktreeResult.Stdout)
	status.Submodules = parseSubmodules(submoduleResult.Stdout)
	status.RemoteFreshness = "unknown"
	status.Classification = classify(status)
	return status, nil
}

func parsePorcelainV2(raw []byte) (Status, error) {
	status := Status{Head: HeadState{Mode: "unknown"}}
	records := strings.Split(string(raw), "\x00")
	for index := 0; index < len(records); index++ {
		record := records[index]
		if record == "" {
			continue
		}
		switch {
		case strings.HasPrefix(record, "# branch.oid "):
			value := strings.TrimPrefix(record, "# branch.oid ")
			if value == "(initial)" {
				status.Head.Mode = "unborn"
			} else {
				status.Head.OID = value
			}
		case strings.HasPrefix(record, "# branch.head "):
			value := strings.TrimPrefix(record, "# branch.head ")
			if value == "(detached)" {
				status.Head.Mode = "detached"
			} else {
				if status.Head.Mode != "unborn" {
					status.Head.Mode = "branch"
				}
				status.Branch.Name = value
			}
		case strings.HasPrefix(record, "# branch.upstream "):
			status.Branch.Upstream = strings.TrimPrefix(record, "# branch.upstream ")
		case strings.HasPrefix(record, "# branch.ab "):
			fields := strings.Fields(strings.TrimPrefix(record, "# branch.ab "))
			if len(fields) != 2 {
				return Status{}, fmt.Errorf("invalid branch.ab record %q", record)
			}
			status.Branch.Ahead, _ = strconv.Atoi(strings.TrimPrefix(fields[0], "+"))
			status.Branch.Behind, _ = strconv.Atoi(strings.TrimPrefix(fields[1], "-"))
			status.Branch.Diverged = status.Branch.Ahead > 0 && status.Branch.Behind > 0
			status.Branch.ComparisonState = "available"
		case strings.HasPrefix(record, "1 "), strings.HasPrefix(record, "2 "):
			fields := strings.Fields(record)
			if len(fields) < 3 || len(fields[1]) != 2 {
				return Status{}, fmt.Errorf("invalid changed-entry record %q", record)
			}
			countXY(&status.Changes, fields[1])
			if fields[2] != "N..." {
				status.Changes.SubmoduleChanges++
			}
			if strings.HasPrefix(record, "2 ") && index+1 < len(records) {
				index++ // the original path follows a type-2 record as a second NUL field
			}
		case strings.HasPrefix(record, "u "):
			status.Changes.Conflicted++
		case strings.HasPrefix(record, "? "):
			status.Changes.Untracked++
		default:
			return Status{}, fmt.Errorf("unknown porcelain v2 record %q", record)
		}
	}
	if status.Head.Mode == "branch" {
		if status.Branch.Upstream == "" {
			status.Branch.UpstreamState = "missing"
			status.Branch.ComparisonState = "unavailable"
		} else {
			status.Branch.UpstreamState = "present"
		}
	} else {
		status.Branch.UpstreamState = "not-applicable"
		status.Branch.ComparisonState = "not-applicable"
	}
	return status, nil
}

func countXY(changes *ChangeState, xy string) {
	if xy[0] != '.' {
		changes.Staged++
	}
	if xy[1] != '.' {
		changes.Unstaged++
	}
	if strings.ContainsAny(xy, "U") || xy == "AA" || xy == "DD" {
		changes.Conflicted++
	}
}

func parseWorktrees(raw []byte) []Worktree {
	records := strings.Split(string(raw), "\x00")
	worktrees := []Worktree{}
	var current *Worktree
	for _, record := range records {
		if record == "" {
			if current != nil {
				worktrees = append(worktrees, *current)
				current = nil
			}
			continue
		}
		key, value, _ := strings.Cut(record, " ")
		if key == "worktree" {
			if current != nil {
				worktrees = append(worktrees, *current)
			}
			current = &Worktree{Path: filepath.Clean(value)}
			continue
		}
		if current == nil {
			continue
		}
		switch key {
		case "HEAD":
			current.Head = value
		case "branch":
			current.Branch = strings.TrimPrefix(value, "refs/heads/")
		case "bare":
			current.Bare = true
		case "detached":
			current.Detached = true
		case "locked":
			current.Locked = true
		case "prunable":
			current.Prunable = true
		}
	}
	if current != nil {
		worktrees = append(worktrees, *current)
	}
	return worktrees
}

func parseSubmodules(raw []byte) SubmoduleState {
	state := SubmoduleState{}
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\r\n"), "\n") {
		if line == "" {
			continue
		}
		state.Total++
		switch line[0] {
		case ' ':
			state.Clean++
		case '+':
			state.Modified++
		case '-':
			state.Uninitialized++
		case 'U':
			state.Conflicted++
		default:
			state.Modified++
		}
	}
	return state
}

func classify(status Status) string {
	if status.Changes.Conflicted > 0 || status.Submodules.Conflicted > 0 {
		return "conflicted"
	}
	if status.Head.Mode == "unborn" {
		return "unborn"
	}
	if status.Head.Mode == "detached" {
		return "detached"
	}
	if status.Changes.Staged > 0 || status.Changes.Unstaged > 0 ||
		status.Changes.Untracked > 0 || status.Changes.SubmoduleChanges > 0 {
		return "dirty"
	}
	if status.Branch.Diverged {
		return "diverged-cached"
	}
	if status.Branch.Ahead > 0 {
		return "ahead-cached"
	}
	if status.Branch.Behind > 0 {
		return "behind-cached"
	}
	if status.Head.Mode == "branch" && status.Branch.UpstreamState == "missing" {
		return "no-upstream"
	}
	return "clean-cached"
}
