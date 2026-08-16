package github

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const maxCheckRuns = 100

// CheckRun is the bounded provider evidence for one check execution on an
// exact commit. Callers decide which names and conclusions satisfy policy.
type CheckRun struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	HeadSHA     string    `json:"head_sha"`
	Status      string    `json:"status"`
	Conclusion  string    `json:"conclusion"`
	CompletedAt time.Time `json:"completed_at"`
	DetailsURL  string    `json:"details_url"`
	ExternalID  string    `json:"external_id,omitempty"`
	AppID       int64     `json:"app_id"`
	AppSlug     string    `json:"app_slug"`
	RunID       int64     `json:"run_id,omitempty"`
	JobID       int64     `json:"job_id,omitempty"`
}

type checkRunsResponse struct {
	TotalCount int `json:"total_count"`
	CheckRuns  []struct {
		ID          int64  `json:"id"`
		Name        string `json:"name"`
		HeadSHA     string `json:"head_sha"`
		Status      string `json:"status"`
		Conclusion  string `json:"conclusion"`
		CompletedAt string `json:"completed_at"`
		DetailsURL  string `json:"details_url"`
		ExternalID  string `json:"external_id"`
		App         struct {
			ID   int64  `json:"id"`
			Slug string `json:"slug"`
		} `json:"app"`
	} `json:"check_runs"`
}

// ListCheckRuns reads the latest provider check runs for one exact commit.
// Pagination is deliberately fail-closed: release planning cannot claim a
// complete check set when the provider returns more than the bounded page.
func (client *Client) ListCheckRuns(
	ctx context.Context,
	owner string,
	name string,
	commitOID string,
) ([]CheckRun, ResponseMeta, error) {
	if !safePathSegment(owner) || !safePathSegment(name) || !validGitOID(commitOID) {
		return nil, ResponseMeta{}, fmt.Errorf("invalid GitHub check-run identity")
	}
	target, err := client.endpoint(
		"repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+
			"/commits/"+url.PathEscape(commitOID)+"/check-runs",
		url.Values{"filter": {"latest"}, "per_page": {"100"}, "page": {"1"}},
	)
	if err != nil {
		return nil, ResponseMeta{}, err
	}
	response, err := client.get(ctx, target, "")
	if err != nil {
		return nil, response.Meta, err
	}
	var raw checkRunsResponse
	if err := decodeJSON(response.Body, &raw); err != nil || raw.TotalCount < 0 ||
		raw.TotalCount > maxCheckRuns || raw.TotalCount != len(raw.CheckRuns) {
		return nil, response.Meta, invalidGovernanceResponse(response, err)
	}
	result := make([]CheckRun, len(raw.CheckRuns))
	for index, item := range raw.CheckRuns {
		completedAt, timeErr := time.Parse(time.RFC3339, item.CompletedAt)
		if item.ID < 1 || !boundedCheckName(item.Name) || item.HeadSHA != commitOID ||
			!validCheckStatus(item.Status) || !validCheckConclusion(item.Conclusion) ||
			timeErr != nil || !completedAt.Equal(completedAt.UTC()) ||
			!validCheckURL(item.DetailsURL) || len(item.ExternalID) > 512 ||
			strings.ContainsRune(item.ExternalID, '\x00') || item.App.ID < 1 ||
			!safePathSegment(item.App.Slug) {
			return nil, response.Meta, invalidGovernanceResponse(response, timeErr)
		}
		result[index] = CheckRun{
			ID: item.ID, Name: item.Name, HeadSHA: item.HeadSHA,
			Status: item.Status, Conclusion: item.Conclusion, CompletedAt: completedAt,
			DetailsURL: item.DetailsURL, ExternalID: item.ExternalID,
			AppID: item.App.ID, AppSlug: item.App.Slug,
		}
		result[index].RunID, result[index].JobID = actionsRunAndJobIDs(
			item.DetailsURL, owner, name,
		)
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Name != result[right].Name {
			return result[left].Name < result[right].Name
		}
		if result[left].AppID != result[right].AppID {
			return result[left].AppID < result[right].AppID
		}
		return result[left].ID < result[right].ID
	})
	return result, response.Meta, nil
}

func actionsRunAndJobIDs(detailsURL string, owner string, name string) (int64, int64) {
	parsed, err := url.Parse(detailsURL)
	if err != nil || parsed.Scheme != "https" || !strings.EqualFold(parsed.Host, "github.com") ||
		parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return 0, 0
	}
	prefix := "/" + owner + "/" + name + "/actions/runs/"
	if !strings.HasPrefix(parsed.Path, prefix) {
		return 0, 0
	}
	parts := strings.Split(strings.TrimPrefix(parsed.Path, prefix), "/")
	if len(parts) != 3 || parts[1] != "job" {
		return 0, 0
	}
	runID, runErr := strconv.ParseInt(parts[0], 10, 64)
	jobID, jobErr := strconv.ParseInt(parts[2], 10, 64)
	if runErr != nil || jobErr != nil || runID < 1 || jobID < 1 {
		return 0, 0
	}
	return runID, jobID
}

func boundedCheckName(value string) bool {
	return value != "" && len(value) <= 256 && !strings.ContainsAny(value, "\x00\r\n")
}

func validCheckStatus(value string) bool {
	switch value {
	case "queued", "in_progress", "completed", "waiting", "requested", "pending":
		return true
	default:
		return false
	}
}

func validCheckConclusion(value string) bool {
	switch value {
	case "", "action_required", "cancelled", "failure", "neutral", "skipped", "stale", "startup_failure", "success", "timed_out":
		return true
	default:
		return false
	}
}

func validCheckURL(value string) bool {
	if value == "" || len(value) > 2048 || strings.ContainsAny(value, "\x00\r\n") {
		return false
	}
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme == "https" && parsed.Host != "" && parsed.User == nil
}
