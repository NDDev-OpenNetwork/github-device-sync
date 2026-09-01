package skills

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

const maxPluginSourceBytes = 1 << 20

type PackageFile struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Size   int    `json:"size"`
}

type PackageCandidate struct {
	Plugin         string        `json:"plugin"`
	RegistryDigest string        `json:"registry_digest"`
	PackageDigest  string        `json:"package_digest"`
	Files          []PackageFile `json:"files"`
	contents       map[string][]byte
}

// ReleaseFiles returns an isolated copy of the verified standalone plugin
// package for deterministic release assembly.
func (candidate PackageCandidate) ReleaseFiles() (map[string][]byte, error) {
	if candidate.Plugin == "" || len(candidate.contents) == 0 || len(candidate.Files) == 0 {
		return nil, fmt.Errorf("plugin package candidate is empty")
	}
	result := make(map[string][]byte, len(candidate.contents))
	for _, file := range candidate.Files {
		content, found := candidate.contents[file.Path]
		if !found || len(content) != file.Size || digest(content) != file.Digest {
			return nil, fmt.Errorf("plugin package candidate file %s is invalid", file.Path)
		}
		result[file.Path] = append([]byte(nil), content...)
	}
	return result, nil
}

type packageManifest struct {
	SchemaVersion  int           `json:"schema_version"`
	Plugin         string        `json:"plugin"`
	RegistryDigest string        `json:"registry_digest"`
	PackageDigest  string        `json:"package_digest"`
	Files          []PackageFile `json:"files"`
}

type hookDocument struct {
	Hooks map[string][]hookGroup `json:"hooks"`
}

type hookGroup struct {
	Matcher string        `json:"matcher,omitempty"`
	Hooks   []hookHandler `json:"hooks"`
}

type hookHandler struct {
	Type          string `json:"type"`
	Command       string `json:"command"`
	Timeout       int    `json:"timeout,omitempty"`
	StatusMessage string `json:"statusMessage,omitempty"`
}

// BuildPackage packages one plugin for the authority rooted at root. The
// registry and every skill source belong to the authority; the plugin static
// files (hooks, manifest chrome) ship with the engine distribution and are
// read from staticRoot, which equals root on the engine repository itself.
func BuildPackage(root, staticRoot, pluginID string, schemas *validation.Set) (PackageCandidate, []domain.Finding) {
	outcome := Validate(root, schemas)
	if len(outcome.Findings) != 0 {
		return PackageCandidate{Plugin: pluginID}, outcome.Findings
	}
	plugin, found := pluginByID(outcome.Registry, pluginID)
	if !found {
		return PackageCandidate{Plugin: pluginID}, []domain.Finding{simpleFinding(
			"GDS_PLUGIN_UNKNOWN", "Requested plugin is not registered.", map[string]any{"plugin": pluginID},
		)}
	}

	selected, findings := selectedSkills(outcome.Registry, plugin)
	if len(findings) != 0 {
		return PackageCandidate{Plugin: pluginID}, findings
	}
	contents, staticFindings := pluginStaticFiles(staticRoot, pluginID)
	findings = append(findings, staticFindings...)
	for _, definition := range selected {
		for _, relative := range []string{"SKILL.md", filepath.Join("agents", "openai.yaml")} {
			source := filepath.Join(root, filepath.FromSlash(definition.Path), relative)
			raw, err := readRegularFile(source)
			if err != nil {
				findings = append(findings, finding(
					"GDS_PLUGIN_SKILL_SOURCE_INVALID", "Cannot package a canonical skill file.", source, err,
				))
				continue
			}
			target := filepath.ToSlash(filepath.Join("skills", definition.Name, relative))
			contents[target] = raw
		}
	}
	if len(findings) != 0 {
		sortFindings(findings)
		return PackageCandidate{Plugin: pluginID}, findings
	}

	registryRaw, err := readRegularFile(filepath.Join(root, "skills", "registry.yaml"))
	if err != nil {
		return PackageCandidate{Plugin: pluginID}, []domain.Finding{finding(
			"GDS_PLUGIN_REGISTRY_INVALID", "Cannot hash the canonical skill registry.",
			filepath.Join(root, "skills", "registry.yaml"), err,
		)}
	}
	registryDigest := digest(registryRaw)
	files := packageFiles(contents)
	packageDigest := digestJSON(files)
	manifestRaw, err := json.MarshalIndent(packageManifest{
		SchemaVersion: 1, Plugin: pluginID, RegistryDigest: registryDigest,
		PackageDigest: packageDigest, Files: files,
	}, "", "  ")
	if err != nil {
		return PackageCandidate{Plugin: pluginID}, []domain.Finding{simpleFinding(
			"GDS_PLUGIN_MANIFEST_RENDER_FAILED", "Cannot render the generated plugin package manifest.",
			map[string]any{"plugin": pluginID, "error": err.Error()},
		)}
	}
	manifestRaw = append(manifestRaw, '\n')
	contents["gds-package.json"] = manifestRaw
	allFiles := packageFiles(contents)
	return PackageCandidate{
		Plugin: pluginID, RegistryDigest: registryDigest, PackageDigest: packageDigest,
		Files: allFiles, contents: contents,
	}, nil
}

func (candidate PackageCandidate) WriteNew(destination string) error {
	if candidate.Plugin == "" || len(candidate.contents) == 0 {
		return fmt.Errorf("plugin package candidate is empty")
	}
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("destination already exists: %s", destination)
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := os.Mkdir(destination, 0o755); err != nil {
		return err
	}
	root, err := os.OpenRoot(destination)
	if err != nil {
		return err
	}
	defer root.Close()
	for _, file := range candidate.Files {
		clean := filepath.Clean(filepath.FromSlash(file.Path))
		if clean == "." || filepath.IsAbs(clean) || clean == ".." ||
			strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return fmt.Errorf("generated plugin path escapes destination: %s", file.Path)
		}
		if err := root.MkdirAll(filepath.Dir(clean), 0o755); err != nil {
			return err
		}
		output, err := root.OpenFile(clean, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
		if err != nil {
			return err
		}
		if _, err := output.Write(candidate.contents[file.Path]); err != nil {
			_ = output.Close()
			return err
		}
		if err := output.Close(); err != nil {
			return err
		}
	}
	return nil
}

func pluginByID(registry Registry, id string) (PluginProfile, bool) {
	for _, plugin := range registry.Plugins {
		if plugin.ID == id {
			return plugin, true
		}
	}
	return PluginProfile{}, false
}

func selectedSkills(registry Registry, plugin PluginProfile) ([]Definition, []domain.Finding) {
	profiles := map[string]Profile{}
	for _, profile := range registry.Profiles {
		profiles[profile.ID] = profile
	}
	definitions := map[string]Definition{}
	for _, definition := range registry.Skills {
		definitions[definition.Name] = definition
	}
	selectedNames := map[string]struct{}{}
	findings := []domain.Finding{}
	for _, profileID := range plugin.Profiles {
		profile, found := profiles[profileID]
		if !found {
			findings = append(findings, simpleFinding(
				"GDS_PLUGIN_PROFILE_UNKNOWN", "Plugin profile cannot be resolved.",
				map[string]any{"plugin": plugin.ID, "profile": profileID},
			))
			continue
		}
		for _, name := range profile.Skills {
			selectedNames[name] = struct{}{}
		}
	}
	selected := make([]Definition, 0, len(selectedNames))
	for name := range selectedNames {
		definition, found := definitions[name]
		if !found {
			findings = append(findings, simpleFinding(
				"GDS_PLUGIN_SKILL_UNKNOWN", "Plugin profile references an unknown skill.",
				map[string]any{"plugin": plugin.ID, "name": name},
			))
			continue
		}
		selected = append(selected, definition)
	}
	sort.Slice(selected, func(left, right int) bool { return selected[left].Name < selected[right].Name })
	return selected, findings
}

func pluginStaticFiles(root, pluginID string) (map[string][]byte, []domain.Finding) {
	sourceRoot := filepath.Join(root, "plugins", pluginID)
	contents := map[string][]byte{}
	findings := []domain.Finding{}
	rootInfo, rootErr := os.Lstat(sourceRoot)
	if rootErr != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return contents, []domain.Finding{finding(
			"GDS_PLUGIN_SOURCE_INVALID", "Canonical plugin source must be a real directory.",
			sourceRoot, rootErr,
		)}
	}
	err := filepath.WalkDir(sourceRoot, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(sourceRoot, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if ignoredPluginSourcePath(relative, entry.IsDir()) {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink is forbidden in plugin source: %s", path)
		}
		if entry.IsDir() {
			if relative == "skills" {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > maxPluginSourceBytes {
			return fmt.Errorf("invalid plugin source file: %s", path)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		target := filepath.ToSlash(relative)
		if target == "gds-package.json" || strings.HasPrefix(target, "skills/") {
			return fmt.Errorf("generated plugin path is present in canonical plugin source: %s", target)
		}
		contents[target] = raw
		return nil
	})
	if err != nil {
		findings = append(findings, finding(
			"GDS_PLUGIN_SOURCE_INVALID", "Cannot enumerate canonical plugin source.", sourceRoot, err,
		))
		return contents, findings
	}
	manifestPath := ".codex-plugin/plugin.json"
	manifestRaw, found := contents[manifestPath]
	if !found {
		findings = append(findings, simpleFinding(
			"GDS_PLUGIN_MANIFEST_MISSING", "Plugin source has no manifest.",
			map[string]any{"plugin": pluginID, "path": filepath.Join(sourceRoot, manifestPath)},
		))
		return contents, findings
	}
	manifest, err := serialization.Decode(manifestPath, manifestRaw)
	if err != nil {
		findings = append(findings, finding(
			"GDS_PLUGIN_MANIFEST_INVALID", "Cannot decode plugin manifest.",
			filepath.Join(sourceRoot, manifestPath), err,
		))
		return contents, findings
	}
	object, ok := manifest.(map[string]any)
	if !ok || object["name"] != pluginID || object["skills"] != "./skills/" {
		findings = append(findings, simpleFinding(
			"GDS_PLUGIN_MANIFEST_INVALID", "Plugin manifest identity or skills path is invalid.",
			map[string]any{"plugin": pluginID, "path": filepath.Join(sourceRoot, manifestPath)},
		))
	}
	findings = append(findings, validateHookSources(pluginID, sourceRoot, contents)...)
	return contents, findings
}

func ignoredPluginSourcePath(relative string, directory bool) bool {
	base := filepath.Base(relative)
	if base == ".DS_Store" || base == "Thumbs.db" || base == "desktop.ini" ||
		strings.HasPrefix(base, "._") {
		return true
	}
	if directory {
		switch base {
		case "__pycache__", ".pytest_cache", ".mypy_cache", ".ruff_cache", ".cache":
			return true
		}
		return false
	}
	return strings.HasSuffix(base, ".pyc") || strings.HasSuffix(base, ".pyo") ||
		strings.HasSuffix(base, ".swp") || strings.HasSuffix(base, "~")
}

func validateHookSources(pluginID, sourceRoot string, contents map[string][]byte) []domain.Finding {
	raw, hasHooks := contents["hooks/hooks.json"]
	if pluginID != "gds-core" {
		if hasHooks {
			return []domain.Finding{simpleFinding(
				"GDS_HOOK_DUPLICATE_LIFECYCLE_SOURCE",
				"Only gds-core may own shared lifecycle hooks.",
				map[string]any{"plugin": pluginID},
			)}
		}
		return nil
	}
	if !hasHooks {
		return []domain.Finding{simpleFinding(
			"GDS_HOOK_CONFIG_MISSING", "gds-core has no lifecycle hook configuration.",
			map[string]any{"plugin": pluginID},
		)}
	}
	var document hookDocument
	if err := serialization.DecodeInto("hooks.json", raw, &document); err != nil {
		return []domain.Finding{finding(
			"GDS_HOOK_CONFIG_INVALID", "Cannot decode Codex hook configuration.",
			filepath.Join(sourceRoot, "hooks", "hooks.json"), err,
		)}
	}
	findings := []domain.Finding{}
	required := map[string]bool{"SessionStart": false, "PreToolUse": false, "Stop": false}
	for event, groups := range document.Hooks {
		if _, supported := required[event]; !supported {
			findings = append(findings, simpleFinding(
				"GDS_HOOK_EVENT_UNEXPECTED", "The core plugin declares an unowned lifecycle event.",
				map[string]any{"event": event},
			))
			continue
		}
		required[event] = true
		if len(groups) == 0 {
			findings = append(findings, simpleFinding(
				"GDS_HOOK_GROUP_MISSING", "A required hook event has no matcher group.",
				map[string]any{"event": event},
			))
		}
		for groupIndex, group := range groups {
			if len(group.Hooks) == 0 {
				findings = append(findings, simpleFinding(
					"GDS_HOOK_HANDLER_MISSING", "A hook matcher group has no handler.",
					map[string]any{"event": event, "group": groupIndex},
				))
			}
			for handlerIndex, handler := range group.Hooks {
				if handler.Type != "command" || !strings.Contains(handler.Command, "$PLUGIN_ROOT/") ||
					handler.Timeout < 1 || handler.Timeout > 60 {
					findings = append(findings, simpleFinding(
						"GDS_HOOK_HANDLER_INVALID",
						"Hook handlers must be bounded plugin-root command handlers.",
						map[string]any{"event": event, "group": groupIndex, "handler": handlerIndex},
					))
				}
			}
		}
	}
	for event, found := range required {
		if !found {
			findings = append(findings, simpleFinding(
				"GDS_HOOK_EVENT_MISSING", "gds-core is missing a required lifecycle hook event.",
				map[string]any{"event": event},
			))
		}
	}
	if script, found := contents["hooks/gds_hook.py"]; !found || bytes.Contains(script, []byte("[TODO:")) {
		findings = append(findings, simpleFinding(
			"GDS_HOOK_SCRIPT_INVALID", "gds-core hook implementation is missing or incomplete.",
			map[string]any{"plugin": pluginID},
		))
	}
	return findings
}

func packageFiles(contents map[string][]byte) []PackageFile {
	paths := make([]string, 0, len(contents))
	for path := range contents {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	files := make([]PackageFile, 0, len(paths))
	for _, path := range paths {
		files = append(files, PackageFile{Path: path, Digest: digest(contents[path]), Size: len(contents[path])})
	}
	return files
}

func digest(raw []byte) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
}

func digestJSON(value any) string {
	raw, _ := json.Marshal(value)
	return digest(raw)
}
