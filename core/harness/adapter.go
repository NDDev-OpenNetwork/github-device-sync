package harness

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/materialize"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/skills"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

const maxAdapterSourceBytes = 1 << 20

// Adapter is the canonical lifecycle contract shared by every supported
// harness. Rendering and inspection are deterministic. Mutations are exposed
// only through plans so the application layer can journal them with the GDS
// operation engine.
type Adapter interface {
	ID() string
	Detect(context.Context) (RuntimeObservation, []domain.Finding)
	Render(RenderRequest) (AdapterCandidate, []domain.Finding)
	Inspect(string, RenderRequest) (AdapterInspection, []domain.Finding)
	PlanInstall(string, RenderRequest) (AdapterPlan, []domain.Finding)
	PlanUpdate(string, RenderRequest) (AdapterPlan, []domain.Finding)
	LoadInstalled(string, RenderRequest) (AdapterCandidate, []domain.Finding)
	PlanRollback(string, string, AdapterCandidate) (AdapterPlan, []domain.Finding)
	PlanRemove(string, RenderRequest) (AdapterPlan, []domain.Finding)
	Doctor(string, RenderRequest) (AdapterDoctorReport, []domain.Finding)
}

type RenderRequest struct {
	SkillProfile string `json:"skill_profile"`
	Scope        string `json:"scope"`
}

type AdapterFile struct {
	Path   string `json:"path"`
	Digest string `json:"digest"`
	Size   int    `json:"size"`
}

type AdapterCandidate struct {
	Harness          string        `json:"harness"`
	Capability       string        `json:"capability_version"`
	SkillProfile     string        `json:"skill_profile"`
	Scope            string        `json:"scope"`
	SkillRoot        string        `json:"skill_root"`
	IncludedSkills   []string      `json:"included_skills"`
	ExcludedExplicit []string      `json:"excluded_explicit_only"`
	RegistryDigest   string        `json:"registry_digest"`
	CandidateDigest  string        `json:"candidate_digest"`
	Files            []AdapterFile `json:"files"`
	contents         map[string][]byte
}

type AdapterInspection struct {
	Harness         string                     `json:"harness"`
	TargetRoot      string                     `json:"target_root"`
	CandidateDigest string                     `json:"candidate_digest"`
	Fingerprint     string                     `json:"fingerprint"`
	Files           []materialize.ObservedFile `json:"files"`
	Drift           int                        `json:"drift"`
}

type AdapterPlan struct {
	SchemaVersion           int           `json:"schema_version"`
	Operation               string        `json:"operation"`
	Harness                 string        `json:"harness"`
	TargetRoot              string        `json:"target_root"`
	SourceRoot              string        `json:"source_root,omitempty"`
	CandidateDigest         string        `json:"candidate_digest"`
	PreviousCandidateDigest string        `json:"previous_candidate_digest,omitempty"`
	BeforeFingerprint       string        `json:"before_fingerprint"`
	Files                   []AdapterFile `json:"files"`
	PreviousFiles           []AdapterFile `json:"previous_files,omitempty"`
	RequiresApproval        bool          `json:"requires_approval"`
	PlanDigest              string        `json:"plan_digest"`
	candidate               AdapterCandidate
	previous                AdapterCandidate
}

func (plan AdapterPlan) Candidate() AdapterCandidate {
	return plan.candidate
}

func (plan AdapterPlan) PreviousCandidate() AdapterCandidate {
	return plan.previous
}

type AdapterDoctorReport struct {
	Harness    string             `json:"harness"`
	Profile    Report             `json:"profile"`
	Runtime    RuntimeObservation `json:"runtime"`
	Inspection AdapterInspection  `json:"inspection"`
}

type profileAdapter struct {
	root     string
	profile  CapabilityProfile
	registry RegistryDocument
	schemas  *validation.Set
}

type adapterLock struct {
	SchemaVersion    int           `json:"schema_version"`
	Harness          string        `json:"harness"`
	Capability       string        `json:"capability_version"`
	SkillProfile     string        `json:"skill_profile"`
	Scope            string        `json:"scope"`
	SkillRoot        string        `json:"skill_root"`
	IncludedSkills   []string      `json:"included_skills"`
	ExcludedExplicit []string      `json:"excluded_explicit_only"`
	RegistryDigest   string        `json:"registry_digest"`
	CandidateDigest  string        `json:"candidate_digest"`
	Files            []AdapterFile `json:"files"`
}

func NewAdapter(root, harnessID string, schemas *validation.Set) (Adapter, []domain.Finding) {
	registry, findings := validateRegistry(root, schemas)
	if len(findings) != 0 {
		return nil, findings
	}
	if _, alias := registryAlias(registry, harnessID); alias {
		return nil, []domain.Finding{harnessFinding(
			"GDS_HARNESS_LEGACY_ALIAS_UNSUPPORTED",
			"Migration aliases cannot own adapter projections.",
			map[string]any{"harness": harnessID},
		)}
	}
	if _, found := registryEntry(registry, harnessID); !found {
		return nil, []domain.Finding{harnessFinding(
			"GDS_HARNESS_ID_UNKNOWN", "Harness is not present in the canonical registry.",
			map[string]any{"harness": harnessID, "known": CanonicalIDs},
		)}
	}
	profile, _, profileFindings := validateProfile(root, harnessID, schemas, false, resolveDelegation(root, schemas))
	if len(profileFindings) != 0 {
		return nil, profileFindings
	}
	return &profileAdapter{root: root, profile: profile, registry: registry, schemas: schemas}, nil
}

func (adapter *profileAdapter) ID() string { return adapter.profile.ID }

func (adapter *profileAdapter) Detect(ctx context.Context) (RuntimeObservation, []domain.Finding) {
	return detectProfile(ctx, adapter.profile)
}

func (adapter *profileAdapter) Render(request RenderRequest) (AdapterCandidate, []domain.Finding) {
	if request.Scope != "project" {
		return AdapterCandidate{Harness: adapter.ID()}, []domain.Finding{harnessFinding(
			"GDS_HARNESS_SCOPE_UNSUPPORTED",
			"Only repository-contained project projections are currently portable and deterministic.",
			map[string]any{"harness": adapter.ID(), "scope": request.Scope},
		)}
	}
	skillRoot, found := projectSkillRoot(adapter.profile)
	if !found {
		return AdapterCandidate{Harness: adapter.ID()}, []domain.Finding{harnessFinding(
			"GDS_HARNESS_PROJECT_SKILLS_NOT_PROVEN",
			"The profile has no verified project-local skill root.",
			map[string]any{"harness": adapter.ID()},
		)}
	}
	outcome := skills.Validate(adapter.root, adapter.schemas)
	if len(outcome.Findings) != 0 {
		return AdapterCandidate{Harness: adapter.ID()}, outcome.Findings
	}
	selected, found := selectedProfile(outcome.Registry, request.SkillProfile)
	if !found {
		return AdapterCandidate{Harness: adapter.ID()}, []domain.Finding{harnessFinding(
			"GDS_HARNESS_SKILL_PROFILE_UNKNOWN", "Requested skill profile is not registered.",
			map[string]any{"harness": adapter.ID(), "profile": request.SkillProfile},
		)}
	}
	definitions := map[string]skills.Definition{}
	for _, definition := range outcome.Registry.Skills {
		definitions[definition.Name] = definition
	}
	contents := map[string][]byte{}
	included := []string{}
	excluded := []string{}
	findings := []domain.Finding{}
	for _, name := range selected.Skills {
		definition, known := definitions[name]
		if !known {
			findings = append(findings, harnessFinding(
				"GDS_HARNESS_SKILL_UNKNOWN", "Skill profile references an unknown skill.",
				map[string]any{"harness": adapter.ID(), "profile": request.SkillProfile, "skill": name},
			))
			continue
		}
		if definition.Invocation == "explicit-only" &&
			adapter.profile.Skills.ExplicitOnly.Mechanism == "profile-exclusion" {
			excluded = append(excluded, name)
			continue
		}
		sourceRoot := filepath.Join(adapter.root, filepath.FromSlash(definition.Path))
		targetRoot := filepath.ToSlash(filepath.Join(skillRoot, name))
		copyFindings := collectSkillFiles(sourceRoot, targetRoot, adapter.ID() == "codex", contents)
		findings = append(findings, copyFindings...)
		included = append(included, name)
	}
	if len(findings) != 0 {
		sortFindings(findings)
		return AdapterCandidate{Harness: adapter.ID()}, findings
	}
	sort.Strings(included)
	sort.Strings(excluded)
	registryRaw, err := os.ReadFile(filepath.Join(adapter.root, "skills", "registry.yaml"))
	if err != nil {
		return AdapterCandidate{Harness: adapter.ID()}, []domain.Finding{harnessFinding(
			"GDS_HARNESS_SKILL_REGISTRY_READ_FAILED", "Cannot hash the canonical skill registry.",
			map[string]any{"error": err.Error()},
		)}
	}
	registryDigest := bytesDigest(registryRaw)
	files := adapterFiles(contents)
	candidateDigest, err := canonicaljson.Digest(map[string]any{
		"harness": adapter.ID(), "capability_version": adapter.profile.CapabilityVersion,
		"skill_profile": request.SkillProfile, "scope": request.Scope,
		"skill_root": skillRoot, "included_skills": included,
		"excluded_explicit_only": excluded, "registry_digest": registryDigest,
		"files": files,
	})
	if err != nil {
		return AdapterCandidate{Harness: adapter.ID()}, []domain.Finding{harnessFinding(
			"GDS_HARNESS_CANDIDATE_DIGEST_FAILED", "Cannot digest the adapter candidate.",
			map[string]any{"error": err.Error()},
		)}
	}
	lockPath := filepath.ToSlash(filepath.Join(
		".gds", "harness", adapter.ID()+"-"+request.SkillProfile+".lock.json",
	))
	lockRaw, err := json.MarshalIndent(adapterLock{
		SchemaVersion: 1, Harness: adapter.ID(), Capability: adapter.profile.CapabilityVersion,
		SkillProfile: request.SkillProfile, Scope: request.Scope, SkillRoot: skillRoot,
		IncludedSkills: included, ExcludedExplicit: excluded, RegistryDigest: registryDigest,
		CandidateDigest: candidateDigest, Files: files,
	}, "", "  ")
	if err != nil {
		return AdapterCandidate{Harness: adapter.ID()}, []domain.Finding{harnessFinding(
			"GDS_HARNESS_LOCK_RENDER_FAILED", "Cannot render the adapter lock.",
			map[string]any{"error": err.Error()},
		)}
	}
	contents[lockPath] = append(lockRaw, '\n')
	return AdapterCandidate{
		Harness: adapter.ID(), Capability: adapter.profile.CapabilityVersion,
		SkillProfile: request.SkillProfile, Scope: request.Scope, SkillRoot: skillRoot,
		IncludedSkills: included, ExcludedExplicit: excluded, RegistryDigest: registryDigest,
		CandidateDigest: candidateDigest, Files: adapterFiles(contents), contents: contents,
	}, nil
}

func (adapter *profileAdapter) Inspect(
	targetRoot string,
	request RenderRequest,
) (AdapterInspection, []domain.Finding) {
	candidate, findings := adapter.Render(request)
	if len(findings) != 0 {
		return AdapterInspection{Harness: adapter.ID(), TargetRoot: targetRoot}, findings
	}
	set, err := adapterMaterialization(targetRoot, candidate)
	if err != nil {
		return AdapterInspection{Harness: adapter.ID(), TargetRoot: targetRoot}, []domain.Finding{harnessFinding(
			"GDS_HARNESS_TARGET_INVALID", "Cannot inspect the adapter target.",
			map[string]any{"target_root": targetRoot, "error": err.Error()},
		)}
	}
	observed, err := set.Observe()
	if err != nil {
		return AdapterInspection{Harness: adapter.ID(), TargetRoot: targetRoot}, []domain.Finding{harnessFinding(
			"GDS_HARNESS_TARGET_INSPECTION_FAILED", "Cannot inspect managed adapter files.",
			map[string]any{"target_root": targetRoot, "error": err.Error()},
		)}
	}
	fingerprint, err := set.Fingerprint(candidate.CandidateDigest)
	if err != nil {
		return AdapterInspection{Harness: adapter.ID(), TargetRoot: targetRoot}, []domain.Finding{harnessFinding(
			"GDS_HARNESS_TARGET_FINGERPRINT_FAILED", "Cannot fingerprint the adapter target.",
			map[string]any{"target_root": targetRoot, "error": err.Error()},
		)}
	}
	expected := map[string]string{}
	for _, file := range candidate.Files {
		expected[file.Path] = file.Digest
	}
	drift := 0
	for _, file := range observed {
		if file.State != "regular" || file.Digest != expected[file.Path] {
			drift++
		}
	}
	return AdapterInspection{
		Harness: adapter.ID(), TargetRoot: targetRoot, CandidateDigest: candidate.CandidateDigest,
		Fingerprint: fingerprint, Files: observed, Drift: drift,
	}, nil
}

func (adapter *profileAdapter) PlanInstall(
	targetRoot string,
	request RenderRequest,
) (AdapterPlan, []domain.Finding) {
	candidate, findings := adapter.Render(request)
	if len(findings) != 0 {
		return AdapterPlan{Harness: adapter.ID()}, findings
	}
	inspection, findings := adapter.inspectCandidate(targetRoot, candidate)
	if len(findings) != 0 {
		return AdapterPlan{Harness: adapter.ID()}, findings
	}
	for _, file := range inspection.Files {
		if file.State != "missing" {
			return AdapterPlan{Harness: adapter.ID()}, []domain.Finding{harnessFinding(
				"GDS_HARNESS_INSTALL_COLLISION",
				"Install refuses to replace an existing managed-path candidate; use update or resolve the collision.",
				map[string]any{"harness": adapter.ID(), "path": file.Path, "state": file.State},
			)}
		}
	}
	return adapter.buildPlan("install", targetRoot, "", candidate, AdapterCandidate{}, inspection.Fingerprint)
}

func (adapter *profileAdapter) PlanUpdate(
	targetRoot string,
	request RenderRequest,
) (AdapterPlan, []domain.Finding) {
	desired, findings := adapter.Render(request)
	if len(findings) != 0 {
		return AdapterPlan{Harness: adapter.ID()}, findings
	}
	previous, findings := adapter.installedCandidate(targetRoot, request)
	if len(findings) != 0 {
		return AdapterPlan{Harness: adapter.ID()}, findings
	}
	return adapter.planTransition("update", targetRoot, "", desired, previous)
}

func (adapter *profileAdapter) LoadInstalled(
	targetRoot string,
	request RenderRequest,
) (AdapterCandidate, []domain.Finding) {
	return adapter.installedCandidate(targetRoot, request)
}

func (adapter *profileAdapter) PlanRollback(
	targetRoot string,
	sourceRoot string,
	prior AdapterCandidate,
) (AdapterPlan, []domain.Finding) {
	if prior.Harness != adapter.ID() || prior.CandidateDigest == "" || len(prior.contents) == 0 {
		return AdapterPlan{Harness: adapter.ID()}, []domain.Finding{harnessFinding(
			"GDS_HARNESS_ROLLBACK_ARTIFACT_REQUIRED",
			"Rollback requires an exact previously verified adapter candidate.",
			map[string]any{"harness": adapter.ID()},
		)}
	}
	current, findings := adapter.installedCandidate(targetRoot, RenderRequest{
		SkillProfile: prior.SkillProfile, Scope: prior.Scope,
	})
	if len(findings) != 0 {
		return AdapterPlan{Harness: adapter.ID()}, findings
	}
	return adapter.planTransition("rollback", targetRoot, sourceRoot, prior, current)
}

func (adapter *profileAdapter) PlanRemove(
	targetRoot string,
	request RenderRequest,
) (AdapterPlan, []domain.Finding) {
	candidate, findings := adapter.installedCandidate(targetRoot, request)
	if len(findings) != 0 {
		return AdapterPlan{Harness: adapter.ID()}, findings
	}
	inspection, findings := adapter.inspectCandidate(targetRoot, candidate)
	if len(findings) != 0 {
		return AdapterPlan{Harness: adapter.ID()}, findings
	}
	return adapter.buildPlan("remove", targetRoot, "", candidate, AdapterCandidate{}, inspection.Fingerprint)
}

func (adapter *profileAdapter) planTransition(
	operation string,
	targetRoot string,
	sourceRoot string,
	desired AdapterCandidate,
	previous AdapterCandidate,
) (AdapterPlan, []domain.Finding) {
	transition, err := transitionCandidate(desired, previous)
	if err != nil {
		return AdapterPlan{Harness: adapter.ID()}, []domain.Finding{harnessFinding(
			"GDS_HARNESS_TRANSITION_INVALID", "Cannot build the exact adapter transition set.",
			map[string]any{"harness": adapter.ID(), "error": err.Error()},
		)}
	}
	inspection, findings := adapter.inspectCandidate(targetRoot, transition)
	if len(findings) != 0 {
		return AdapterPlan{Harness: adapter.ID()}, findings
	}
	previousPaths := adapterFileMap(previous.Files)
	desiredPaths := adapterFileMap(desired.Files)
	for _, file := range inspection.Files {
		if _, wasManaged := previousPaths[file.Path]; wasManaged {
			continue
		}
		desiredFile, willManage := desiredPaths[file.Path]
		if willManage && file.State != "missing" &&
			(file.State != "regular" || file.Digest != desiredFile.Digest) {
			return AdapterPlan{Harness: adapter.ID()}, []domain.Finding{harnessFinding(
				"GDS_HARNESS_UPDATE_COLLISION",
				"Transition refuses to overwrite a path that was not managed by the installed adapter.",
				map[string]any{"harness": adapter.ID(), "path": file.Path, "state": file.State},
			)}
		}
	}
	return adapter.buildPlan(
		operation, targetRoot, sourceRoot, desired, previous, inspection.Fingerprint,
	)
}

func (adapter *profileAdapter) buildPlan(
	operation string,
	targetRoot string,
	sourceRoot string,
	candidate AdapterCandidate,
	previous AdapterCandidate,
	beforeFingerprint string,
) (AdapterPlan, []domain.Finding) {
	plan := AdapterPlan{
		SchemaVersion: 1, Operation: operation, Harness: adapter.ID(), TargetRoot: targetRoot,
		SourceRoot: sourceRoot, CandidateDigest: candidate.CandidateDigest,
		BeforeFingerprint: beforeFingerprint, Files: candidate.Files,
		RequiresApproval: true, candidate: candidate, previous: previous,
	}
	if previous.CandidateDigest != "" {
		plan.PreviousCandidateDigest = previous.CandidateDigest
		plan.PreviousFiles = previous.Files
	}
	digestInput := plan
	digestInput.PlanDigest = ""
	digestInput.candidate = AdapterCandidate{}
	digestInput.previous = AdapterCandidate{}
	digest, err := canonicaljson.Digest(digestInput)
	if err != nil {
		return AdapterPlan{Harness: adapter.ID()}, []domain.Finding{harnessFinding(
			"GDS_HARNESS_PLAN_DIGEST_FAILED", "Cannot digest the adapter lifecycle plan.",
			map[string]any{"error": err.Error()},
		)}
	}
	plan.PlanDigest = digest
	return plan, nil
}

func transitionCandidate(desired, previous AdapterCandidate) (AdapterCandidate, error) {
	files := adapterFileMap(previous.Files)
	contents := make(map[string][]byte, len(previous.contents)+len(desired.contents))
	for path, content := range previous.contents {
		contents[path] = append([]byte(nil), content...)
	}
	for _, file := range desired.Files {
		files[file.Path] = file
	}
	for path, content := range desired.contents {
		contents[path] = append([]byte(nil), content...)
	}
	union := make([]AdapterFile, 0, len(files))
	for _, file := range files {
		union = append(union, file)
	}
	sort.Slice(union, func(left, right int) bool { return union[left].Path < union[right].Path })
	digest, err := canonicaljson.Digest(map[string]any{
		"desired": desired.CandidateDigest, "previous": previous.CandidateDigest, "files": union,
	})
	if err != nil {
		return AdapterCandidate{}, err
	}
	return AdapterCandidate{
		Harness: desired.Harness, CandidateDigest: digest, Files: union, contents: contents,
	}, nil
}

func adapterFileMap(files []AdapterFile) map[string]AdapterFile {
	result := make(map[string]AdapterFile, len(files))
	for _, file := range files {
		result[file.Path] = file
	}
	return result
}

func (adapter *profileAdapter) inspectCandidate(
	targetRoot string,
	candidate AdapterCandidate,
) (AdapterInspection, []domain.Finding) {
	set, err := adapterMaterialization(targetRoot, candidate)
	if err != nil {
		return AdapterInspection{Harness: adapter.ID(), TargetRoot: targetRoot}, []domain.Finding{harnessFinding(
			"GDS_HARNESS_TARGET_INVALID", "Cannot inspect the adapter target.",
			map[string]any{"target_root": targetRoot, "error": err.Error()},
		)}
	}
	observed, err := set.Observe()
	if err != nil {
		return AdapterInspection{Harness: adapter.ID(), TargetRoot: targetRoot}, []domain.Finding{harnessFinding(
			"GDS_HARNESS_TARGET_INSPECTION_FAILED", "Cannot inspect managed adapter files.",
			map[string]any{"target_root": targetRoot, "error": err.Error()},
		)}
	}
	fingerprint, err := set.Fingerprint(candidate.CandidateDigest)
	if err != nil {
		return AdapterInspection{Harness: adapter.ID(), TargetRoot: targetRoot}, []domain.Finding{harnessFinding(
			"GDS_HARNESS_TARGET_FINGERPRINT_FAILED", "Cannot fingerprint the adapter target.",
			map[string]any{"target_root": targetRoot, "error": err.Error()},
		)}
	}
	return AdapterInspection{
		Harness: adapter.ID(), TargetRoot: targetRoot, CandidateDigest: candidate.CandidateDigest,
		Fingerprint: fingerprint, Files: observed,
	}, nil
}

func (adapter *profileAdapter) Doctor(
	targetRoot string,
	request RenderRequest,
) (AdapterDoctorReport, []domain.Finding) {
	_, profileReport, profileFindings := validateProfile(
		adapter.root, adapter.ID(), adapter.schemas, false,
		resolveDelegation(adapter.root, adapter.schemas),
	)
	runtime, runtimeFindings := adapter.Detect(context.Background())
	inspection, inspectionFindings := adapter.Inspect(targetRoot, request)
	findings := append(profileFindings, runtimeFindings...)
	findings = append(findings, inspectionFindings...)
	sortFindings(findings)
	return AdapterDoctorReport{
		Harness: adapter.ID(), Profile: profileReport, Runtime: runtime, Inspection: inspection,
	}, findings
}

func selectedProfile(registry skills.Registry, profileID string) (skills.Profile, bool) {
	for _, profile := range registry.Profiles {
		if profile.ID == profileID {
			return profile, true
		}
	}
	return skills.Profile{}, false
}

func projectSkillRoot(profile CapabilityProfile) (string, bool) {
	for _, candidate := range profile.Skills.NativePaths {
		if candidate == "" || strings.HasPrefix(candidate, "~") || filepath.IsAbs(candidate) {
			continue
		}
		clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(candidate)))
		if clean == candidate && clean != "." && clean != ".." && !strings.HasPrefix(clean, "../") {
			return clean, true
		}
	}
	return "", false
}

func collectSkillFiles(
	sourceRoot string,
	targetRoot string,
	includeOpenAI bool,
	contents map[string][]byte,
) []domain.Finding {
	rootInfo, err := os.Lstat(sourceRoot)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return []domain.Finding{harnessFinding(
			"GDS_HARNESS_SKILL_SOURCE_INVALID", "Canonical skill source must be a real directory.",
			map[string]any{"path": sourceRoot, "error": errorText(err)},
		)}
	}
	findings := []domain.Finding{}
	err = filepath.WalkDir(sourceRoot, func(path string, entry fs.DirEntry, walkErr error) error {
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
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink is forbidden in canonical skill source: %s", path)
		}
		if entry.IsDir() {
			if !includeOpenAI && filepath.ToSlash(relative) == "agents" {
				return filepath.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > maxAdapterSourceBytes {
			return fmt.Errorf("invalid canonical skill source file: %s", path)
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		target := filepath.ToSlash(filepath.Join(targetRoot, relative))
		if _, duplicate := contents[target]; duplicate {
			return fmt.Errorf("duplicate adapter projection path: %s", target)
		}
		contents[target] = raw
		return nil
	})
	if err != nil {
		findings = append(findings, harnessFinding(
			"GDS_HARNESS_SKILL_SOURCE_INVALID", "Cannot enumerate canonical skill source.",
			map[string]any{"path": sourceRoot, "error": err.Error()},
		))
	}
	return findings
}

func adapterFiles(contents map[string][]byte) []AdapterFile {
	paths := make([]string, 0, len(contents))
	for path := range contents {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	files := make([]AdapterFile, 0, len(paths))
	for _, path := range paths {
		files = append(files, AdapterFile{
			Path: path, Digest: bytesDigest(contents[path]), Size: len(contents[path]),
		})
	}
	return files
}

func adapterMaterialization(root string, candidate AdapterCandidate) (*materialize.Set, error) {
	files := make([]materialize.File, 0, len(candidate.Files))
	for _, file := range candidate.Files {
		content, found := candidate.contents[file.Path]
		if !found {
			return nil, fmt.Errorf("adapter candidate content is missing: %s", file.Path)
		}
		files = append(files, materialize.File{Path: file.Path, Content: content, Digest: file.Digest})
	}
	return materialize.NewSet(root, files)
}

func bytesDigest(raw []byte) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
}

func (adapter *profileAdapter) installedCandidate(
	targetRoot string,
	request RenderRequest,
) (AdapterCandidate, []domain.Finding) {
	lockPath := filepath.ToSlash(filepath.Join(
		".gds", "harness", adapter.ID()+"-"+request.SkillProfile+".lock.json",
	))
	root, err := os.OpenRoot(targetRoot)
	if err != nil {
		return AdapterCandidate{Harness: adapter.ID()}, []domain.Finding{harnessFinding(
			"GDS_HARNESS_INSTALLED_ROOT_INVALID",
			"Cannot open the installed adapter root safely.",
			map[string]any{"path": targetRoot, "error": err.Error()},
		)}
	}
	defer root.Close()
	info, err := root.Lstat(filepath.FromSlash(lockPath))
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() > maxAdapterSourceBytes {
		return AdapterCandidate{Harness: adapter.ID()}, []domain.Finding{harnessFinding(
			"GDS_HARNESS_INSTALLED_LOCK_INVALID",
			"Remove and rollback require one bounded regular installed adapter lock.",
			map[string]any{"path": lockPath, "error": errorText(err)},
		)}
	}
	raw, err := root.ReadFile(filepath.FromSlash(lockPath))
	if err != nil {
		return AdapterCandidate{Harness: adapter.ID()}, []domain.Finding{harnessFinding(
			"GDS_HARNESS_INSTALLED_LOCK_INVALID", "Cannot read the installed adapter lock.",
			map[string]any{"path": lockPath, "error": err.Error()},
		)}
	}
	var lock adapterLock
	if err := json.Unmarshal(raw, &lock); err != nil {
		return AdapterCandidate{Harness: adapter.ID()}, []domain.Finding{harnessFinding(
			"GDS_HARNESS_INSTALLED_LOCK_INVALID", "Cannot decode the installed adapter lock.",
			map[string]any{"path": lockPath, "error": err.Error()},
		)}
	}
	if lock.SchemaVersion != 1 || lock.Harness != adapter.ID() ||
		lock.SkillProfile != request.SkillProfile || lock.Scope != request.Scope ||
		lock.SkillRoot == "" || lock.CandidateDigest == "" || lock.RegistryDigest == "" {
		return AdapterCandidate{Harness: adapter.ID()}, []domain.Finding{harnessFinding(
			"GDS_HARNESS_INSTALLED_LOCK_IDENTITY_INVALID",
			"Installed adapter lock identity differs from the requested adapter.",
			map[string]any{"path": lockPath, "harness": adapter.ID(), "profile": request.SkillProfile},
		)}
	}
	expectedCandidateDigest, err := canonicaljson.Digest(map[string]any{
		"harness": lock.Harness, "capability_version": lock.Capability,
		"skill_profile": lock.SkillProfile, "scope": lock.Scope,
		"skill_root": lock.SkillRoot, "included_skills": lock.IncludedSkills,
		"excluded_explicit_only": lock.ExcludedExplicit,
		"registry_digest":        lock.RegistryDigest, "files": lock.Files,
	})
	if err != nil || expectedCandidateDigest != lock.CandidateDigest {
		return AdapterCandidate{Harness: adapter.ID()}, []domain.Finding{harnessFinding(
			"GDS_HARNESS_INSTALLED_LOCK_DIGEST_INVALID",
			"Installed adapter lock does not reproduce its candidate digest.",
			map[string]any{"path": lockPath, "error": errorText(err)},
		)}
	}
	seen := map[string]struct{}{lockPath: {}}
	files := make([]AdapterFile, 0, len(lock.Files)+1)
	contents := map[string][]byte{lockPath: raw}
	for _, file := range lock.Files {
		clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(file.Path)))
		if clean != file.Path || clean == "." || clean == ".." ||
			strings.HasPrefix(clean, "../") || filepath.IsAbs(clean) || file.Digest == "" ||
			file.Size < 0 || file.Size > maxAdapterSourceBytes {
			return AdapterCandidate{Harness: adapter.ID()}, []domain.Finding{harnessFinding(
				"GDS_HARNESS_INSTALLED_LOCK_PATH_INVALID",
				"Installed adapter lock contains an unsafe managed path.",
				map[string]any{"path": file.Path},
			)}
		}
		if _, duplicate := seen[file.Path]; duplicate {
			return AdapterCandidate{Harness: adapter.ID()}, []domain.Finding{harnessFinding(
				"GDS_HARNESS_INSTALLED_LOCK_PATH_DUPLICATE",
				"Installed adapter lock contains a duplicate managed path.",
				map[string]any{"path": file.Path},
			)}
		}
		seen[file.Path] = struct{}{}
		relativeFile := filepath.FromSlash(file.Path)
		fileInfo, err := root.Lstat(relativeFile)
		if err != nil || !fileInfo.Mode().IsRegular() || fileInfo.Mode()&os.ModeSymlink != 0 ||
			fileInfo.Size() != int64(file.Size) {
			return AdapterCandidate{Harness: adapter.ID()}, []domain.Finding{harnessFinding(
				"GDS_HARNESS_INSTALLED_FILE_INVALID",
				"An installed managed file is missing, changed type, or changed size.",
				map[string]any{"path": file.Path, "error": errorText(err)},
			)}
		}
		content, err := root.ReadFile(relativeFile)
		if err != nil || bytesDigest(content) != file.Digest {
			return AdapterCandidate{Harness: adapter.ID()}, []domain.Finding{harnessFinding(
				"GDS_HARNESS_INSTALLED_FILE_DRIFT",
				"An installed managed file differs from the exact digest in its lock.",
				map[string]any{"path": file.Path, "error": errorText(err)},
			)}
		}
		files = append(files, file)
		contents[file.Path] = content
	}
	files = append(files, AdapterFile{Path: lockPath, Digest: bytesDigest(raw), Size: len(raw)})
	sort.Slice(files, func(left, right int) bool { return files[left].Path < files[right].Path })
	return AdapterCandidate{
		Harness: adapter.ID(), Capability: lock.Capability, SkillProfile: lock.SkillProfile,
		Scope: lock.Scope, SkillRoot: lock.SkillRoot, IncludedSkills: lock.IncludedSkills,
		ExcludedExplicit: lock.ExcludedExplicit, RegistryDigest: lock.RegistryDigest,
		CandidateDigest: lock.CandidateDigest, Files: files, contents: contents,
	}, nil
}
