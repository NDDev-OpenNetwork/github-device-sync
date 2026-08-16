package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	MutationBranch              = "branch"
	MutationContent             = "content"
	MutationCustomProperties    = "custom-properties"
	MutationPullRequest         = "pull-request"
	MutationRepositoryDelete    = "repository-delete"
	MutationRepositoryLifecycle = "repository-lifecycle"
	MutationRepositoryRelease   = "repository-release"
	MutationRepositoryRuleset   = "repository-ruleset"
	MutationRepositorySettings  = "repository-settings"
	MutationWorkflowCaller      = "workflow-caller"
)

var supportedMutationOperations = map[string]struct{}{
	MutationBranch: {}, MutationContent: {}, MutationCustomProperties: {},
	MutationPullRequest: {}, MutationRepositoryDelete: {},
	MutationRepositoryLifecycle: {}, MutationRepositoryRelease: {},
	MutationRepositoryRuleset: {}, MutationRepositorySettings: {},
	MutationWorkflowCaller: {},
}

type MutationWait func(context.Context, time.Duration) error

type MutatorConfig struct {
	Client         Config
	Operations     []string
	MinimumSpacing time.Duration
	Wait           MutationWait
}

type Mutator struct {
	client         *Client
	operations     map[string]struct{}
	minimumSpacing time.Duration
	wait           MutationWait
	mu             sync.Mutex
	lastMutation   time.Time
}

type RepositoryMutationScope struct {
	RepositoryID int64    `json:"repository_id"`
	Owner        string   `json:"owner"`
	Name         string   `json:"name"`
	Operations   []string `json:"operations"`
}

type RepositoryMutator struct {
	factory    *Mutator
	scope      RepositoryMutationScope
	operations map[string]struct{}
}

type MutationMeta struct {
	RepositoryID int64  `json:"repository_id"`
	StatusCode   int    `json:"status_code"`
	RequestID    string `json:"request_id,omitempty"`
	Rate         Rate   `json:"rate"`
}

func NewMutator(config MutatorConfig) (*Mutator, error) {
	if config.Client.PermissionContract.RepositorySelection != "selected" {
		return nil, fmt.Errorf("GitHub mutation App must use selected repository installation scope")
	}
	writePermission := false
	for _, level := range config.Client.PermissionContract.Permissions {
		if level == "write" {
			writePermission = true
		}
	}
	if !writePermission {
		return nil, fmt.Errorf("GitHub mutation App requires at least one exact write permission")
	}
	if config.MinimumSpacing < time.Second || config.MinimumSpacing > time.Minute {
		return nil, fmt.Errorf("GitHub mutation spacing must be between one second and one minute")
	}
	operations := make(map[string]struct{}, len(config.Operations))
	for _, operation := range config.Operations {
		if _, supported := supportedMutationOperations[operation]; !supported {
			return nil, fmt.Errorf("unsupported GitHub mutation operation %q", operation)
		}
		if _, duplicate := operations[operation]; duplicate {
			return nil, fmt.Errorf("duplicate GitHub mutation operation %q", operation)
		}
		operations[operation] = struct{}{}
	}
	if len(operations) == 0 {
		return nil, fmt.Errorf("GitHub mutation capability has no operations")
	}
	client, err := NewClient(config.Client)
	if err != nil {
		return nil, err
	}
	wait := config.Wait
	if wait == nil {
		wait = func(ctx context.Context, delay time.Duration) error {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-timer.C:
				return nil
			}
		}
	}
	return &Mutator{
		client: client, operations: operations,
		minimumSpacing: config.MinimumSpacing, wait: wait,
	}, nil
}

func (mutator *Mutator) BindRepository(scope RepositoryMutationScope) (*RepositoryMutator, error) {
	if scope.RepositoryID <= 0 || !githubOwnerPattern.MatchString(scope.Owner) ||
		!githubNamePattern.MatchString(scope.Name) {
		return nil, fmt.Errorf("GitHub mutation repository scope is invalid")
	}
	operations := make(map[string]struct{}, len(scope.Operations))
	for _, operation := range scope.Operations {
		if _, allowed := mutator.operations[operation]; !allowed {
			return nil, fmt.Errorf("GitHub mutation operation %q is outside capability scope", operation)
		}
		if _, duplicate := operations[operation]; duplicate {
			return nil, fmt.Errorf("GitHub mutation repository scope repeats operation %q", operation)
		}
		operations[operation] = struct{}{}
	}
	if len(operations) == 0 {
		return nil, fmt.Errorf("GitHub mutation repository scope has no operations")
	}
	scope.Operations = sortedMutationOperations(operations)
	return &RepositoryMutator{factory: mutator, scope: scope, operations: operations}, nil
}

func (mutator *RepositoryMutator) Scope() RepositoryMutationScope {
	result := mutator.scope
	result.Operations = append([]string(nil), result.Operations...)
	return result
}

func (mutator *RepositoryMutator) endpoint(suffix string) (*url.URL, error) {
	base := "repos/" + url.PathEscape(mutator.scope.Owner) + "/" + url.PathEscape(mutator.scope.Name)
	if suffix != "" {
		base += "/" + suffix
	}
	return mutator.factory.client.endpoint(base, nil)
}

func (mutator *RepositoryMutator) mutate(
	ctx context.Context,
	operation string,
	method string,
	target *url.URL,
	payload any,
) (getResult, MutationMeta, error) {
	if _, allowed := mutator.operations[operation]; !allowed {
		return getResult{}, MutationMeta{}, fmt.Errorf(
			"GitHub mutation operation %q is outside bound repository scope", operation,
		)
	}
	if method != http.MethodPost && method != http.MethodPut &&
		method != http.MethodPatch && method != http.MethodDelete {
		return getResult{}, MutationMeta{}, fmt.Errorf("unsupported GitHub mutation method")
	}
	body := []byte(nil)
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return getResult{}, MutationMeta{}, fmt.Errorf("encode GitHub mutation: %w", err)
		}
	}
	if err := mutator.factory.waitForMutationSlot(ctx); err != nil {
		return getResult{}, MutationMeta{}, err
	}
	response, err := mutator.factory.client.request(ctx, method, target, body, "")
	meta := MutationMeta{
		RepositoryID: mutator.scope.RepositoryID,
		StatusCode:   response.StatusCode, RequestID: response.Meta.RequestID,
		Rate: response.Meta.Rate,
	}
	return response, meta, err
}

func (mutator *Mutator) waitForMutationSlot(ctx context.Context) error {
	mutator.mu.Lock()
	defer mutator.mu.Unlock()
	now := mutator.client.now()
	if !mutator.lastMutation.IsZero() {
		delay := mutator.lastMutation.Add(mutator.minimumSpacing).Sub(now)
		if delay > 0 {
			if err := mutator.wait(ctx, delay); err != nil {
				return err
			}
		}
	}
	mutator.lastMutation = mutator.client.now()
	return nil
}

func sortedMutationOperations(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func escapeRepositoryContentPath(path string) (string, error) {
	if path == "" || strings.HasPrefix(path, "/") || strings.HasSuffix(path, "/") ||
		strings.Contains(path, "//") {
		return "", fmt.Errorf("repository content path is invalid")
	}
	parts := strings.Split(path, "/")
	for index, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\\x00\r\n") {
			return "", fmt.Errorf("repository content path is invalid")
		}
		parts[index] = url.PathEscape(part)
	}
	return strings.Join(parts, "/"), nil
}
