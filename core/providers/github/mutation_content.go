package github

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const maxMutationContentBytes = 1 << 20

type ContentUpdate struct {
	Path        string
	Message     string
	Content     []byte
	Branch      string
	ExpectedSHA string
}

type ContentResult struct {
	Path      string `json:"path"`
	BlobSHA   string `json:"blob_sha"`
	CommitSHA string `json:"commit_sha"`
}

type contentMutationResponse struct {
	Content struct {
		Path string `json:"path"`
		SHA  string `json:"sha"`
	} `json:"content"`
	Commit struct {
		SHA string `json:"sha"`
	} `json:"commit"`
}

type RefResult struct {
	Ref string `json:"ref"`
	SHA string `json:"sha"`
}

type refMutationResponse struct {
	Ref    string `json:"ref"`
	Object struct {
		SHA string `json:"sha"`
	} `json:"object"`
}

func (mutator *RepositoryMutator) PutContent(
	ctx context.Context,
	update ContentUpdate,
) (ContentResult, MutationMeta, error) {
	escapedPath, err := escapeRepositoryContentPath(update.Path)
	if err != nil || !boundedProviderText(update.Message, 512) ||
		len(update.Content) > maxMutationContentBytes || !safeBranchName(update.Branch) ||
		(update.ExpectedSHA != "" && !validGitOID(update.ExpectedSHA)) {
		return ContentResult{}, MutationMeta{}, fmt.Errorf("GitHub content mutation is invalid")
	}
	operation := MutationContent
	if strings.HasPrefix(update.Path, ".github/workflows/") {
		operation = MutationWorkflowCaller
	}
	target, err := mutator.endpoint("contents/" + escapedPath)
	if err != nil {
		return ContentResult{}, MutationMeta{}, err
	}
	payload := map[string]any{
		"message": update.Message,
		"content": base64.StdEncoding.EncodeToString(update.Content),
		"branch":  update.Branch,
	}
	if update.ExpectedSHA != "" {
		payload["sha"] = update.ExpectedSHA
	}
	response, meta, err := mutator.mutate(ctx, operation, http.MethodPut, target, payload)
	if err != nil {
		return ContentResult{}, meta, err
	}
	var raw contentMutationResponse
	if err := decodeJSON(response.Body, &raw); err != nil || raw.Content.Path != update.Path ||
		!validGitOID(raw.Content.SHA) || !validGitOID(raw.Commit.SHA) {
		return ContentResult{}, meta, invalidMutationResponse(response, err)
	}
	return ContentResult{
		Path: raw.Content.Path, BlobSHA: raw.Content.SHA, CommitSHA: raw.Commit.SHA,
	}, meta, nil
}

func (mutator *RepositoryMutator) CreateBranch(
	ctx context.Context,
	branch string,
	fromSHA string,
) (RefResult, MutationMeta, error) {
	if !safeBranchName(branch) || !validGitOID(fromSHA) {
		return RefResult{}, MutationMeta{}, fmt.Errorf("GitHub branch creation input is invalid")
	}
	target, err := mutator.endpoint("git/refs")
	if err != nil {
		return RefResult{}, MutationMeta{}, err
	}
	return mutator.mutateRef(
		ctx, http.MethodPost, target,
		map[string]any{"ref": "refs/heads/" + branch, "sha": fromSHA},
		"refs/heads/"+branch,
	)
}

func (mutator *RepositoryMutator) FastForwardBranch(
	ctx context.Context,
	branch string,
	toSHA string,
) (RefResult, MutationMeta, error) {
	if !safeBranchName(branch) || !validGitOID(toSHA) {
		return RefResult{}, MutationMeta{}, fmt.Errorf("GitHub branch update input is invalid")
	}
	escaped := strings.Join(escapePathSegments(strings.Split("heads/"+branch, "/")), "/")
	target, err := mutator.endpoint("git/refs/" + escaped)
	if err != nil {
		return RefResult{}, MutationMeta{}, err
	}
	return mutator.mutateRef(
		ctx, http.MethodPatch, target,
		map[string]any{"sha": toSHA, "force": false},
		"refs/heads/"+branch,
	)
}

func (mutator *RepositoryMutator) mutateRef(
	ctx context.Context,
	method string,
	target *url.URL,
	payload any,
	expectedRef string,
) (RefResult, MutationMeta, error) {
	response, meta, err := mutator.mutate(ctx, MutationBranch, method, target, payload)
	if err != nil {
		return RefResult{}, meta, err
	}
	var raw refMutationResponse
	if err := decodeJSON(response.Body, &raw); err != nil || raw.Ref != expectedRef ||
		!validGitOID(raw.Object.SHA) {
		return RefResult{}, meta, invalidMutationResponse(response, err)
	}
	return RefResult{Ref: raw.Ref, SHA: raw.Object.SHA}, meta, nil
}

func escapePathSegments(parts []string) []string {
	result := make([]string, len(parts))
	for index, part := range parts {
		result[index] = url.PathEscape(part)
	}
	return result
}

func validGitOID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
