package harness

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

const (
	versionCommandTimeout = 5 * time.Second
	versionOutputLimit    = 4096
	versionValueLimit     = 512
)

type RuntimeObservation struct {
	Harness           string `json:"harness"`
	Result            string `json:"result"`
	Command           string `json:"command,omitempty"`
	Executable        string `json:"executable,omitempty"`
	Version           string `json:"version,omitempty"`
	CapabilityVersion string `json:"capability_version"`
}

type RuntimeDetectionReport struct {
	Harnesses []RuntimeObservation `json:"harnesses"`
}

func Detect(
	ctx context.Context,
	root string,
	harnessID string,
	schemas *validation.Set,
) (RuntimeObservation, []domain.Finding) {
	registry, findings := validateRegistry(root, schemas)
	if len(findings) != 0 {
		return RuntimeObservation{Harness: harnessID, Result: "not-proven"}, findings
	}
	if canonical, found := registryAlias(registry, harnessID); found {
		return RuntimeObservation{Harness: harnessID, Result: "legacy-alias"},
			[]domain.Finding{harnessFinding(
				"GDS_HARNESS_LEGACY_ALIAS_UNSUPPORTED",
				fmt.Sprintf("Harness %q is a migration-only alias; detect %q instead.", harnessID, canonical),
				map[string]any{"alias": harnessID, "canonical_harness": canonical},
			)}
	}
	if _, found := registryEntry(registry, harnessID); !found {
		return RuntimeObservation{Harness: harnessID, Result: "not-proven"},
			[]domain.Finding{harnessFinding(
				"GDS_HARNESS_ID_UNKNOWN", "Harness is not present in the canonical registry.",
				map[string]any{"harness": harnessID, "known": CanonicalIDs},
			)}
	}
	profile, _, findings := validateProfile(root, harnessID, schemas, false, resolveDelegation(root, schemas))
	if len(findings) != 0 {
		return RuntimeObservation{Harness: harnessID, Result: "not-proven"}, findings
	}
	return detectProfile(ctx, profile)
}

func DetectAll(
	ctx context.Context,
	root string,
	schemas *validation.Set,
) (RuntimeDetectionReport, []domain.Finding) {
	registry, findings := validateRegistry(root, schemas)
	report := RuntimeDetectionReport{Harnesses: []RuntimeObservation{}}
	if len(findings) != 0 {
		return report, findings
	}
	for _, entry := range registry.Harnesses {
		profile, _, profileFindings := validateProfile(root, entry.ID, schemas, false, resolveDelegation(root, schemas))
		if len(profileFindings) != 0 {
			findings = append(findings, profileFindings...)
			report.Harnesses = append(report.Harnesses, RuntimeObservation{
				Harness: entry.ID, Result: "not-proven",
			})
			continue
		}
		observation, detectionFindings := detectProfile(ctx, profile)
		report.Harnesses = append(report.Harnesses, observation)
		findings = append(findings, detectionFindings...)
	}
	sort.Slice(report.Harnesses, func(left, right int) bool {
		return report.Harnesses[left].Harness < report.Harnesses[right].Harness
	})
	sortFindings(findings)
	return report, findings
}

func detectProfile(
	ctx context.Context,
	profile CapabilityProfile,
) (RuntimeObservation, []domain.Finding) {
	observation := RuntimeObservation{
		Harness: profile.ID, Result: "not-proven",
		CapabilityVersion: profile.CapabilityVersion,
	}
	for _, candidate := range profile.Detection.CommandCandidates {
		path, err := exec.LookPath(candidate)
		if err != nil {
			continue
		}
		observation.Command = candidate
		observation.Executable = filepath.Clean(path)
		commandContext, cancel := context.WithTimeout(ctx, versionCommandTimeout)
		command := exec.CommandContext(
			commandContext,
			path,
			profile.Detection.VersionArguments...,
		)
		output := &cappedBuffer{remaining: versionOutputLimit}
		command.Stdout = output
		command.Stderr = output
		err = command.Run()
		cancel()
		version := normalizeVersion(output.String())
		if err != nil {
			return observation, []domain.Finding{harnessFinding(
				"GDS_HARNESS_VERSION_NOT_PROVEN",
				"Harness binary was found but its bounded version command failed.",
				map[string]any{
					"harness": profile.ID,
					"command": candidate,
					"error":   err.Error(),
					"output":  version,
				},
			)}
		}
		if version == "" {
			return observation, []domain.Finding{harnessFinding(
				"GDS_HARNESS_VERSION_NOT_PROVEN",
				"Harness version command succeeded without a usable version value.",
				map[string]any{"harness": profile.ID, "command": candidate},
			)}
		}
		observation.Result = "observed"
		observation.Version = version
		return observation, nil
	}
	return observation, []domain.Finding{harnessFinding(
		"GDS_HARNESS_BINARY_NOT_PROVEN",
		"No documented harness command candidate is available on PATH.",
		map[string]any{
			"harness":            profile.ID,
			"command_candidates": profile.Detection.CommandCandidates,
		},
	)}
}

type cappedBuffer struct {
	buffer    bytes.Buffer
	remaining int
}

func (buffer *cappedBuffer) Write(value []byte) (int, error) {
	original := len(value)
	if buffer.remaining > 0 {
		portion := value
		if len(portion) > buffer.remaining {
			portion = portion[:buffer.remaining]
		}
		_, _ = buffer.buffer.Write(portion)
		buffer.remaining -= len(portion)
	}
	return original, nil
}

func (buffer *cappedBuffer) String() string {
	return buffer.buffer.String()
}

var _ io.Writer = (*cappedBuffer)(nil)

func normalizeVersion(value string) string {
	value = strings.Map(func(character rune) rune {
		if unicode.IsControl(character) {
			return ' '
		}
		return character
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	if len(value) > versionValueLimit {
		value = value[:versionValueLimit]
	}
	return value
}
