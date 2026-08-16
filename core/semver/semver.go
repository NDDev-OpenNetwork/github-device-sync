// Package semver validates and compares Semantic Versioning 2.0.0 values
// without accepting a leading v or non-canonical numeric identifiers.
package semver

import (
	"regexp"
	"strings"
)

var pattern = regexp.MustCompile(
	`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)` +
		`(?:-((?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)` +
		`(?:\.(?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*))*))?` +
		`(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`,
)

type Version struct {
	major      string
	minor      string
	patch      string
	prerelease []string
}

func Parse(value string) (Version, bool) {
	match := pattern.FindStringSubmatch(value)
	if match == nil {
		return Version{}, false
	}
	version := Version{major: match[1], minor: match[2], patch: match[3]}
	if match[4] != "" {
		version.prerelease = strings.Split(match[4], ".")
	}
	return version, true
}

func Valid(value string) bool {
	_, valid := Parse(value)
	return valid
}

// Compare returns -1, 0, or 1 when left has lower, equal, or higher SemVer
// precedence than right. Build metadata does not affect precedence.
func Compare(left, right string) (int, bool) {
	leftVersion, leftValid := Parse(left)
	rightVersion, rightValid := Parse(right)
	if !leftValid || !rightValid {
		return 0, false
	}
	for _, values := range [][2]string{
		{leftVersion.major, rightVersion.major},
		{leftVersion.minor, rightVersion.minor},
		{leftVersion.patch, rightVersion.patch},
	} {
		if compared := compareNumeric(values[0], values[1]); compared != 0 {
			return compared, true
		}
	}
	return comparePrerelease(leftVersion.prerelease, rightVersion.prerelease), true
}

func comparePrerelease(left, right []string) int {
	if len(left) == 0 && len(right) == 0 {
		return 0
	}
	if len(left) == 0 {
		return 1
	}
	if len(right) == 0 {
		return -1
	}
	limit := min(len(left), len(right))
	for index := 0; index < limit; index++ {
		leftNumeric := numeric(left[index])
		rightNumeric := numeric(right[index])
		switch {
		case leftNumeric && rightNumeric:
			if compared := compareNumeric(left[index], right[index]); compared != 0 {
				return compared
			}
		case leftNumeric:
			return -1
		case rightNumeric:
			return 1
		case left[index] < right[index]:
			return -1
		case left[index] > right[index]:
			return 1
		}
	}
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	return 0
}

func compareNumeric(left, right string) int {
	if len(left) < len(right) {
		return -1
	}
	if len(left) > len(right) {
		return 1
	}
	if left < right {
		return -1
	}
	if left > right {
		return 1
	}
	return 0
}

func numeric(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}
