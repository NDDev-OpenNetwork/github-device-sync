// Package workspace resolves portable device workspace intent into exact local paths.
package workspace

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

type DeviceDescriptor struct {
	SchemaVersion   int                   `json:"schema_version"`
	Device          DeviceIdentity        `json:"device"`
	WorkspaceRoots  map[string]string     `json:"workspace_roots"`
	Materialization MaterializationPolicy `json:"materialization"`
	Harnesses       []string              `json:"harnesses"`
	State           DeviceStatePolicy     `json:"state"`
	// Repositories is the observed checkout inventory. It answers "what is
	// actually on this device" without a live scan, which the declarative
	// materialization block deliberately cannot: that block states which
	// portfolio belongs in which root, not which repositories resolved there.
	// It is evidence and never authority — placement stays decided by
	// materialization, and an entry that disagrees with the filesystem is a
	// finding. Omitted rather than empty on a device nobody has observed.
	Repositories []DeviceRepository `json:"repositories,omitempty"`
}

// DeviceRepository is one observed checkout on a device.
type DeviceRepository struct {
	Provider string `json:"provider"`
	// WorkspaceRoot is empty for an out-of-estate checkout, which by ADR 0025
	// has no declared root.
	WorkspaceRoot string `json:"workspace_root,omitempty"`
	Path          string `json:"path"`
	// Materialization is checkout, git-submodule, or out-of-estate.
	Materialization string `json:"materialization"`
}

type DeviceCandidate struct {
	Descriptor DeviceDescriptor `json:"descriptor"`
	Path       string           `json:"path"`
	Digest     string           `json:"digest"`
}

type DeviceIdentity struct {
	ID           string `json:"id"`
	Name         string `json:"name"`
	OS           string `json:"os"`
	Architecture string `json:"architecture"`
	// Class is optional device-class intent (profile/gui/docker_mode/execution_policy/hardening)
	// mirrored from the macos-ubuntu-bootstrap rldyour-contract.json targets block. It drives
	// the phased bootstrap orchestrator's selection of OS-installer flags. Cross-field rules are
	// enforced by the JSON Schema validator (core/validation deviceClassFindings); this struct
	// only makes the declared facts readable by future placement/audit code. Omitted on devices
	// that do not pin a class.
	Class *DeviceClass `json:"class,omitempty"`
}

// DeviceClass is the optional device-class intent carried by a DeviceIdentity.
type DeviceClass struct {
	Profile         string           `json:"profile,omitempty"`
	GUI             string           `json:"gui,omitempty"`
	DockerMode      string           `json:"docker_mode,omitempty"`
	ExecutionPolicy string           `json:"execution_policy,omitempty"`
	Hardening       *DeviceHardening `json:"hardening,omitempty"`
}

// DeviceHardening carries server-only hardening toggles.
type DeviceHardening struct {
	SSH      bool `json:"ssh,omitempty"`
	UFW      bool `json:"ufw,omitempty"`
	Fail2ban bool `json:"fail2ban,omitempty"`
}

type MaterializationPolicy struct {
	DefaultMode string                      `json:"default_mode"`
	Include     []MaterializationAssignment `json:"include"`
}

type MaterializationAssignment struct {
	Selector      string `json:"selector"`
	WorkspaceRoot string `json:"workspace_root"`
	Mode          string `json:"mode"`
}

type DeviceStatePolicy struct {
	Path string `json:"path"`
}

type Environment struct {
	Home         string
	XDGStateHome string
}

type Placement struct {
	DeviceID      string `json:"device_id"`
	RepositoryID  string `json:"repository_id"`
	Selector      string `json:"selector"`
	Mode          string `json:"mode"`
	WorkspaceRoot string `json:"workspace_root"`
	TargetPath    string `json:"target_path"`
	StateRoot     string `json:"state_root"`
}

func LoadDevice(path string, schemas *validation.Set) (DeviceDescriptor, []domain.Finding) {
	candidate, findings := LoadDeviceCandidate(path, schemas)
	return candidate.Descriptor, findings
}

func LoadDeviceCandidate(path string, schemas *validation.Set) (DeviceCandidate, []domain.Finding) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return DeviceCandidate{}, []domain.Finding{workspaceFinding(
			"GDS_DEVICE_DESCRIPTOR_INVALID", err.Error(), path,
		)}
	}
	info, err := os.Lstat(absolute)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return DeviceCandidate{}, []domain.Finding{workspaceFinding(
			"GDS_DEVICE_DESCRIPTOR_INVALID",
			"Device descriptor must be a regular non-symlink file.", absolute,
		)}
	}
	raw, err := os.ReadFile(absolute)
	if err != nil {
		return DeviceCandidate{}, []domain.Finding{workspaceFinding(
			"GDS_DEVICE_DESCRIPTOR_INVALID", err.Error(), absolute,
		)}
	}
	value, err := serialization.Decode(absolute, raw)
	if err != nil {
		return DeviceCandidate{}, []domain.Finding{workspaceFinding(
			"GDS_DEVICE_DESCRIPTOR_INVALID", err.Error(), absolute,
		)}
	}
	if findings := schemas.Validate("device", value, absolute); len(findings) != 0 {
		return DeviceCandidate{}, findings
	}
	normalized, err := json.Marshal(value)
	if err != nil {
		return DeviceCandidate{}, []domain.Finding{workspaceFinding(
			"GDS_DEVICE_DESCRIPTOR_INVALID", err.Error(), absolute,
		)}
	}
	var descriptor DeviceDescriptor
	if err := json.Unmarshal(normalized, &descriptor); err != nil {
		return DeviceCandidate{}, []domain.Finding{workspaceFinding(
			"GDS_DEVICE_DESCRIPTOR_INVALID", err.Error(), absolute,
		)}
	}
	return DeviceCandidate{
		Descriptor: descriptor, Path: filepath.Clean(absolute),
		Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(raw)),
	}, nil
}

func ResolvePlacement(
	descriptor DeviceDescriptor,
	anchor domain.RepositoryAnchor,
	environment Environment,
) (Placement, []domain.Finding) {
	matches := make([]MaterializationAssignment, 0, 1)
	for _, assignment := range descriptor.Materialization.Include {
		if contains(anchor.Classification.Portfolios, assignment.Selector) {
			matches = append(matches, assignment)
		}
	}
	if len(matches) == 0 {
		return Placement{
				DeviceID: descriptor.Device.ID, RepositoryID: anchor.Repository.ID,
				Mode: descriptor.Materialization.DefaultMode,
			}, []domain.Finding{workspaceFinding(
				"GDS_WORKSPACE_PLACEMENT_NOT_SELECTED",
				"Repository does not match a device materialization assignment.", anchor.Repository.ID,
			)}
	}
	if len(matches) != 1 {
		return Placement{}, []domain.Finding{workspaceFinding(
			"GDS_WORKSPACE_PLACEMENT_AMBIGUOUS",
			"Repository matches more than one device materialization assignment.", anchor.Repository.ID,
		)}
	}
	assignment := matches[0]
	portableRoot, found := descriptor.WorkspaceRoots[assignment.WorkspaceRoot]
	if !found {
		return Placement{}, []domain.Finding{workspaceFinding(
			"GDS_WORKSPACE_ROOT_UNKNOWN", "Materialization assignment references an unknown workspace root.",
			assignment.WorkspaceRoot,
		)}
	}
	workspaceRoot, err := expandPortablePath(portableRoot, environment)
	if err != nil {
		return Placement{}, []domain.Finding{workspaceFinding(
			"GDS_WORKSPACE_ROOT_INVALID", err.Error(), portableRoot,
		)}
	}
	stateRoot, err := expandPortablePath(descriptor.State.Path, environment)
	if err != nil {
		return Placement{}, []domain.Finding{workspaceFinding(
			"GDS_WORKSPACE_STATE_ROOT_INVALID", err.Error(), descriptor.State.Path,
		)}
	}
	if workspaceRoot == string(filepath.Separator) || anchor.Provider.Name == "." || anchor.Provider.Name == ".." {
		return Placement{}, []domain.Finding{workspaceFinding(
			"GDS_WORKSPACE_TARGET_UNSAFE", "Workspace target path is unsafe.", workspaceRoot,
		)}
	}
	target := filepath.Join(workspaceRoot, anchor.Provider.Name)
	if filepath.Dir(target) != workspaceRoot {
		return Placement{}, []domain.Finding{workspaceFinding(
			"GDS_WORKSPACE_TARGET_UNSAFE", "Repository name escapes the selected workspace root.", target,
		)}
	}
	return Placement{
		DeviceID: descriptor.Device.ID, RepositoryID: anchor.Repository.ID,
		Selector: assignment.Selector, Mode: assignment.Mode,
		WorkspaceRoot: workspaceRoot, TargetPath: target, StateRoot: stateRoot,
	}, nil
}

func CurrentEnvironment() (Environment, error) {
	home, err := os.UserHomeDir()
	if err != nil || !filepath.IsAbs(home) {
		return Environment{}, fmt.Errorf("HOME cannot be resolved to an absolute path")
	}
	xdg := strings.TrimSpace(os.Getenv("XDG_STATE_HOME"))
	if xdg == "" {
		xdg = filepath.Join(home, ".local", "state")
	}
	if !filepath.IsAbs(xdg) {
		return Environment{}, fmt.Errorf("XDG_STATE_HOME must be absolute")
	}
	return Environment{Home: filepath.Clean(home), XDGStateHome: filepath.Clean(xdg)}, nil
}

// ExpandPortablePath resolves one portable device path against the environment.
func ExpandPortablePath(value string, environment Environment) (string, error) {
	return expandPortablePath(value, environment)
}

func expandPortablePath(value string, environment Environment) (string, error) {
	var expanded string
	switch {
	case strings.HasPrefix(value, "~/"):
		expanded = filepath.Join(environment.Home, strings.TrimPrefix(value, "~/"))
	case value == "${HOME}":
		expanded = environment.Home
	case strings.HasPrefix(value, "${HOME}/"):
		expanded = filepath.Join(environment.Home, strings.TrimPrefix(value, "${HOME}/"))
	case value == "${XDG_STATE_HOME}":
		expanded = environment.XDGStateHome
	case strings.HasPrefix(value, "${XDG_STATE_HOME}/"):
		expanded = filepath.Join(environment.XDGStateHome, strings.TrimPrefix(value, "${XDG_STATE_HOME}/"))
	default:
		return "", fmt.Errorf("portable path uses an unsupported variable")
	}
	if !filepath.IsAbs(expanded) {
		return "", fmt.Errorf("portable path did not resolve to an absolute path")
	}
	return filepath.Clean(expanded), nil
}

func contains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func workspaceFinding(code string, message string, value string) domain.Finding {
	return domain.Finding{
		Code: code, Severity: domain.SeverityHigh, Message: message,
		Evidence: map[string]any{"value": value},
	}
}
