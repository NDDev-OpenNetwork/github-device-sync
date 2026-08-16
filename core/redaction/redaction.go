// Package redaction removes high-confidence credential forms from bounded
// process and provider error text before it reaches agent output or journals.
package redaction

import "regexp"

var replacements = []struct {
	pattern     *regexp.Regexp
	replacement string
}{
	{
		regexp.MustCompile(`(?i)\b(?:gh[pousr]|github_pat)_[A-Za-z0-9_]{8,}\b`),
		`[redacted-github-token]`,
	},
	{
		regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
		`[redacted-aws-key]`,
	},
	{
		regexp.MustCompile(`\bglpat-[A-Za-z0-9_-]{20,}\b`),
		`[redacted-gitlab-token]`,
	},
	{
		regexp.MustCompile(`\bxox[baprs]-[A-Za-z0-9-]{10,}\b`),
		`[redacted-slack-token]`,
	},
	{
		regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\b`),
		`[redacted-jwt]`,
	},
	{
		regexp.MustCompile(`(?i)(authorization\s*:\s*(?:bearer|basic)\s+)[^\s,;]+`),
		`${1}[redacted]`,
	},
	{
		regexp.MustCompile(`(?i)(https?://)[^/@\s]+@`),
		`${1}[redacted]@`,
	},
	{
		regexp.MustCompile(`(?i)((?:access[_-]?token|token|password|secret)=)[^&\s]+`),
		`${1}[redacted]`,
	},
	{
		regexp.MustCompile(`(?i)("(?:access[_-]?token|token|password|secret)"\s*:\s*")[^"]+("?)`),
		`${1}[redacted]${2}`,
	},
	{
		regexp.MustCompile(`-----BEGIN (?:[A-Z0-9 ]+ )?PRIVATE KEY-----`),
		`[redacted-private-key]`,
	},
	{
		regexp.MustCompile(`(?i)(x-access-token:)[A-Za-z0-9._~+/=-]{12,}`),
		`${1}[redacted]`,
	},
}

func String(value string) string {
	for _, item := range replacements {
		value = item.pattern.ReplaceAllString(value, item.replacement)
	}
	return value
}
