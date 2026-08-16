package github

import (
	"context"
	"fmt"
	"net/url"
)

// The gate on a branch is not the contents of one ruleset document. Rules reach
// a branch from repository rulesets and from organization rulesets alike, and
// the two are fetched from different endpoints -- an organization ruleset id is
// a 404 on the repository endpoint. Reconstructing the effective set by hand
// would mean re-implementing GitHub's own condition matching, including the
// `repository_name` conditions an organization ruleset carries.
//
// `repos/{owner}/{repo}/rules/branches/{branch}` answers the question directly:
// which rules apply to this branch, from whichever ruleset they came. What it
// does not carry is enforcement, so an `evaluate`-mode ruleset is
// indistinguishable here from an active one. The caller pairs this with the
// ruleset list, which does carry enforcement, and keeps only the rules whose
// ruleset actually blocks.

const maxBranchRules = 200

// BranchRule is one rule that reaches a branch, with the ruleset it came from.
type BranchRule struct {
	Type              string `json:"type"`
	RulesetID         int64  `json:"ruleset_id"`
	RulesetSource     string `json:"ruleset_source"`
	RulesetSourceKind string `json:"ruleset_source_type"`
	Parameters        struct {
		RequiredStatusChecks []RequiredStatusCheck `json:"required_status_checks"`
	} `json:"parameters"`
}

// ListBranchRules reads the rules effective on one branch.
func (client *Client) ListBranchRules(
	ctx context.Context,
	owner string,
	name string,
	branch string,
) ([]BranchRule, ResponseMeta, error) {
	if !safePathSegment(owner) || !safePathSegment(name) || !safeBranchName(branch) {
		return nil, ResponseMeta{}, fmt.Errorf("invalid GitHub branch rule identity")
	}
	target, err := client.endpoint(
		"repos/"+url.PathEscape(owner)+"/"+url.PathEscape(name)+
			"/rules/branches/"+url.PathEscape(branch),
		url.Values{"per_page": {"100"}, "page": {"1"}},
	)
	if err != nil {
		return nil, ResponseMeta{}, err
	}
	response, err := client.get(ctx, target, "")
	if err != nil {
		return nil, response.Meta, err
	}
	var rules []BranchRule
	if err := decodeJSON(response.Body, &rules); err != nil {
		return nil, response.Meta, invalidGovernanceResponse(response, err)
	}
	if len(rules) > maxBranchRules {
		return nil, response.Meta, fmt.Errorf(
			"GitHub branch rule count exceeds the %d-item bound", maxBranchRules,
		)
	}
	// A truncated page would silently understate the gate, which is the exact
	// error this whole comparison exists to catch, so it is refused rather than
	// accepted as a partial answer.
	next, err := client.nextPage(response.Header, target)
	if err != nil {
		return nil, response.Meta, err
	}
	if next != nil {
		return nil, response.Meta, fmt.Errorf(
			"GitHub branch rule count exceeds the %d-item bound", maxBranchRules,
		)
	}
	for _, rule := range rules {
		if rule.Type == "" || rule.RulesetID <= 0 {
			return nil, response.Meta, invalidGovernanceResponse(response, nil)
		}
		for _, check := range rule.Parameters.RequiredStatusChecks {
			if !boundedProviderText(check.Context, 256) {
				return nil, response.Meta, invalidGovernanceResponse(response, nil)
			}
		}
	}
	return rules, response.Meta, nil
}
