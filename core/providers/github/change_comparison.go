package github

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
)

const maxComparedFiles = 300

type ComparisonFile struct {
	Path         string `json:"path"`
	PreviousPath string `json:"previous_path,omitempty"`
	Status       string `json:"status"`
	BlobSHA      string `json:"blob_sha"`
}

type BranchComparison struct {
	Status       string           `json:"status"`
	AheadBy      int              `json:"ahead_by"`
	BehindBy     int              `json:"behind_by"`
	TotalCommits int              `json:"total_commits"`
	BaseSHA      string           `json:"base_sha"`
	HeadSHA      string           `json:"head_sha"`
	MergeBaseSHA string           `json:"merge_base_sha"`
	Files        []ComparisonFile `json:"files"`
}

type comparisonResponse struct {
	Status       string `json:"status"`
	AheadBy      int    `json:"ahead_by"`
	BehindBy     int    `json:"behind_by"`
	TotalCommits int    `json:"total_commits"`
	BaseCommit   struct {
		SHA string `json:"sha"`
	} `json:"base_commit"`
	HeadCommit struct {
		SHA string `json:"sha"`
	} `json:"head_commit"`
	MergeBaseCommit struct {
		SHA string `json:"sha"`
	} `json:"merge_base_commit"`
	Files []struct {
		SHA              string `json:"sha"`
		Filename         string `json:"filename"`
		Status           string `json:"status"`
		PreviousFilename string `json:"previous_filename"`
	} `json:"files"`
}

func (client *Client) CompareBranches(
	ctx context.Context,
	owner string,
	name string,
	base string,
	head string,
) (BranchComparison, ResponseMeta, error) {
	if !safePathSegment(owner) || !safePathSegment(name) ||
		!safeBranchName(base) || !safeBranchName(head) || base == head {
		return BranchComparison{}, ResponseMeta{}, fmt.Errorf("invalid GitHub comparison identity")
	}
	comparisonPath := strings.Join(escapePathSegments(strings.Split(base, "/")), "/") + "..." +
		strings.Join(escapePathSegments(strings.Split(head, "/")), "/")
	target, err := client.endpoint(
		"repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+"/compare/"+comparisonPath, nil,
	)
	if err != nil {
		return BranchComparison{}, ResponseMeta{}, err
	}
	response, err := client.get(ctx, target, "")
	if err != nil {
		return BranchComparison{}, response.Meta, err
	}
	var raw comparisonResponse
	if err := decodeJSON(response.Body, &raw); err != nil {
		return BranchComparison{}, response.Meta, invalidGovernanceResponse(response, err)
	}
	result, err := normalizeBranchComparison(raw)
	if err != nil {
		return BranchComparison{}, response.Meta, invalidGovernanceResponse(response, err)
	}
	return result, response.Meta, nil
}

func normalizeBranchComparison(raw comparisonResponse) (BranchComparison, error) {
	if !comparisonStatus(raw.Status) || raw.AheadBy < 0 || raw.BehindBy < 0 ||
		raw.TotalCommits < 0 || !validGitOID(raw.BaseCommit.SHA) ||
		!validGitOID(raw.HeadCommit.SHA) || !validGitOID(raw.MergeBaseCommit.SHA) ||
		len(raw.Files) > maxComparedFiles {
		return BranchComparison{}, fmt.Errorf("GitHub branch comparison response is invalid")
	}
	files := make([]ComparisonFile, len(raw.Files))
	seen := make(map[string]struct{}, len(raw.Files))
	for index, file := range raw.Files {
		if _, err := escapeRepositoryContentPath(file.Filename); err != nil ||
			!comparisonFileStatus(file.Status) || !validGitOID(file.SHA) {
			return BranchComparison{}, fmt.Errorf("GitHub comparison file is invalid")
		}
		if file.Status == "renamed" {
			if _, err := escapeRepositoryContentPath(file.PreviousFilename); err != nil {
				return BranchComparison{}, fmt.Errorf("GitHub comparison rename source is invalid")
			}
		} else if file.PreviousFilename != "" {
			return BranchComparison{}, fmt.Errorf("GitHub comparison previous path is unexpected")
		}
		if _, duplicate := seen[file.Filename]; duplicate {
			return BranchComparison{}, fmt.Errorf("GitHub comparison contains duplicate files")
		}
		seen[file.Filename] = struct{}{}
		files[index] = ComparisonFile{
			Path: file.Filename, PreviousPath: file.PreviousFilename,
			Status: file.Status, BlobSHA: file.SHA,
		}
	}
	sort.Slice(files, func(left, right int) bool { return files[left].Path < files[right].Path })
	return BranchComparison{
		Status: raw.Status, AheadBy: raw.AheadBy, BehindBy: raw.BehindBy,
		TotalCommits: raw.TotalCommits, BaseSHA: raw.BaseCommit.SHA,
		HeadSHA: raw.HeadCommit.SHA, MergeBaseSHA: raw.MergeBaseCommit.SHA,
		Files: files,
	}, nil
}

func comparisonStatus(value string) bool {
	return value == "ahead" || value == "behind" || value == "diverged" || value == "identical"
}

func comparisonFileStatus(value string) bool {
	switch value {
	case "added", "removed", "modified", "renamed", "copied", "changed", "unchanged":
		return true
	default:
		return false
	}
}
