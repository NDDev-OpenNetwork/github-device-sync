package projections

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"text/template"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/compiler"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
	gdtemplates "github.com/NDDev-OpenNetwork/github-device-sync/templates"
)

const (
	agentsTemplatePath             = "agents/repository.md.tmpl"
	publicModuleAgentsTemplatePath = "agents/public-module.md.tmpl"
	claudeTemplatePath             = "harnesses/claude.md.tmpl"
	goCITemplatePath               = "github-actions/go.yml.tmpl"
	claudeOutputPath               = ".claude/CLAUDE.md"
	goCIOutputPath                 = ".github/workflows/gds-ci.yml"
	bundleLockPath                 = ".gds/bundle.lock.yaml"
	compiledPolicyPath             = ".gds/compiled-policy.json"
)

var developmentBundleSourcePaths = []string{
	".gds/repository.yaml",
	"core/app",
	"core/compiler",
	"core/domain",
	"core/manifest",
	"core/projections",
	"core/providers/git",
	"core/serialization",
	"core/validation",
	"go.mod",
	"go.sum",
	"policies",
	"schemas/v1/bundle-lock.schema.json",
	"schemas/v1/common.schema.json",
	"schemas/v1/compiled-policy.schema.json",
	"schemas/v1/policy.schema.json",
	"schemas/v1/policy-exception.schema.json",
	"schemas/v1/repository.schema.json",
	"estate/exceptions",
	"templates",
}

type Generator struct {
	schemas   *validation.Set
	templates map[string][]byte
}

type templateData struct {
	Purpose                  string
	Capabilities             []string
	Entrypoints              []domain.ProductEntrypoint
	RepositoryID             string
	Roles                    string
	BundleVersion            string
	ExternalWriteApproval    string
	GeneratedProjectionEdit  string
	PrivateParentPersistence string
	VisibilityContract       string
	DataClassification       string
	SkillProfiles            string
	Commands                 []commandLine
	DefaultBranch            string
	GoVersion                string
	BuildCommand             string
	TestCommand              string
	PRRequiredCommand        string
	TimeoutMinutes           int
	WorkflowRef              string
	Runner                   string
	GitHubWorkflowExpression string
	GitHubRefExpression      string
}

type commandLine struct {
	Label   string
	Command string
}

func New(schemas *validation.Set) (*Generator, error) {
	paths := []string{
		agentsTemplatePath, publicModuleAgentsTemplatePath, claudeTemplatePath, goCITemplatePath,
	}
	sources := make(map[string][]byte, len(paths))
	for _, path := range paths {
		content, err := fs.ReadFile(gdtemplates.Sources, path)
		if err != nil {
			return nil, fmt.Errorf("read projection template %s: %w", path, err)
		}
		sources[path] = content
	}
	return &Generator{schemas: schemas, templates: sources}, nil
}

// DevelopmentBundleSourcePaths returns the committed source boundary used to
// identify one development bundle. Callers receive a copy so the canonical
// contract cannot be mutated process-locally.
func DevelopmentBundleSourcePaths() []string {
	return append([]string(nil), developmentBundleSourcePaths...)
}

// VerifyEmbeddedSources checks template compatibility only: it proves that the
// executing generator embeds the exact templates committed in the claimed
// canonical source checkout, not that all generator logic came from that commit.
func (generator *Generator) VerifyEmbeddedSources(estateRoot string) error {
	for path, embedded := range generator.templates {
		sourcePath := filepath.Join(estateRoot, "templates", filepath.FromSlash(path))
		committed, err := os.ReadFile(sourcePath)
		if err != nil {
			return fmt.Errorf("read canonical projection template %s: %w", path, err)
		}
		if !bytes.Equal(embedded, committed) {
			return fmt.Errorf("executing generator template differs from canonical source: %s", path)
		}
	}
	return nil
}

// DevelopmentBundleFromSourceCommit reproduces the pre-content-addressing
// bundle identity for locks which do not yet record source_tree_digest.
func (generator *Generator) DevelopmentBundleFromSourceCommit(
	policy compiler.CompiledPolicyDocument,
	sourceCommit string,
) (Bundle, error) {
	templateDigests := map[string]string{}
	for path, content := range generator.templates {
		templateDigests[path] = digestBytes(content)
	}
	payload := struct {
		Version      string                     `json:"version"`
		SourceCommit string                     `json:"source_commit"`
		Sources      []compiler.PolicySourceRef `json:"sources"`
		Templates    map[string]string          `json:"templates"`
	}{
		Version: compiler.DevelopmentBundleVersion, SourceCommit: sourceCommit,
		Sources: policy.Sources, Templates: templateDigests,
	}
	digest, err := canonicalDigest(payload)
	if err != nil {
		return Bundle{}, err
	}
	return Bundle{
		Version: compiler.DevelopmentBundleVersion, ReleaseSequence: 0,
		Channel: "development", SourceCommit: sourceCommit, Digest: digest,
	}, nil
}

// canonicalDigest normalizes one bundle payload and digests it.
func canonicalDigest(payload any) (string, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	normalized, err := serialization.Decode("development-bundle-digest.json", raw)
	if err != nil {
		return "", err
	}
	canonical, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return digestBytes(canonical), nil
}

// DevelopmentBundle derives one development bundle identity.
//
// The digest covers the canonical source content and never the commit that
// carries it. sourceCommit is recorded as trace metadata only: including it
// made the bundle identity unknowable until after the commit existed, so a
// covered input and its regenerated projection could not be one commit.
func (generator *Generator) DevelopmentBundle(
	policy compiler.CompiledPolicyDocument,
	sourceCommit string,
	sourceTreeDigest string,
) (Bundle, error) {
	if sourceTreeDigest == "" {
		return Bundle{}, fmt.Errorf("development bundle requires a canonical source tree digest")
	}
	templateDigests := map[string]string{}
	for path, content := range generator.templates {
		templateDigests[path] = digestBytes(content)
	}
	payload := struct {
		Version          string                     `json:"version"`
		SourceTreeDigest string                     `json:"source_tree_digest"`
		Sources          []compiler.PolicySourceRef `json:"sources"`
		Templates        map[string]string          `json:"templates"`
	}{
		Version: compiler.DevelopmentBundleVersion, SourceTreeDigest: sourceTreeDigest,
		Sources: policy.Sources, Templates: templateDigests,
	}
	digest, err := canonicalDigest(payload)
	if err != nil {
		return Bundle{}, err
	}
	return Bundle{
		Version: compiler.DevelopmentBundleVersion, ReleaseSequence: 0,
		Channel: "development", SourceCommit: sourceCommit,
		SourceTreeDigest: sourceTreeDigest, Digest: digest,
	}, nil
}

func (generator *Generator) Generate(
	anchor domain.RepositoryAnchor,
	policy compiler.CompiledPolicyDocument,
	bundle Bundle,
) (Candidate, []domain.Finding) {
	if anchor.Repository.ID != policy.CompiledPolicy.RepositoryID {
		return Candidate{}, []domain.Finding{{
			Code: "GDS_PROJECTION_IDENTITY_MISMATCH", Severity: domain.SeverityHigh,
			Message: "Repository anchor and compiled policy identities differ.",
		}}
	}
	if bundle.Version != policy.CompiledPolicy.BundleVersion {
		return Candidate{}, []domain.Finding{{
			Code: "GDS_PROJECTION_BUNDLE_MISMATCH", Severity: domain.SeverityHigh,
			Message: "Bundle and compiled policy versions differ.",
		}}
	}
	for _, source := range policy.Sources {
		if !projectionDistributionAllowed(
			source.Distribution, anchor.Classification.VisibilityContract,
		) {
			return Candidate{}, []domain.Finding{{
				Code:     "GDS_PROJECTION_VISIBILITY_VIOLATION",
				Severity: domain.SeverityCritical,
				Message: fmt.Sprintf(
					"Policy source %q with %s distribution cannot enter a %s projection.",
					source.ID, source.Distribution, anchor.Classification.VisibilityContract,
				),
			}}
		}
	}

	inputDigest, err := generator.inputDigest(anchor, policy, bundle)
	if err != nil {
		return Candidate{}, []domain.Finding{projectionError(
			"GDS_PROJECTION_INPUT_DIGEST_FAILED", "Cannot compute projection input digest", err,
		)}
	}
	data := projectionTemplateData(anchor, policy, bundle)
	editSources := projectionEditSources(policy)

	compiledJSON, err := policy.CanonicalJSON()
	if err != nil {
		return Candidate{}, []domain.Finding{projectionError(
			"GDS_PROJECTION_RENDER_FAILED", "Cannot render compiled policy", err,
		)}
	}

	managed := []File{newFile(compiledPolicyPath, compiledJSON)}
	if anchor.Agent.GeneratedAgents {
		agents, claude, renderErr := generator.renderAgentInstructions(
			anchor, data, bundle, inputDigest, editSources,
		)
		if renderErr != nil {
			return Candidate{}, []domain.Finding{projectionError(
				"GDS_PROJECTION_RENDER_FAILED", "Cannot render agent instructions", renderErr,
			)}
		}
		managed = append(managed, newFile("AGENTS.md", agents), newFile(claudeOutputPath, claude))
	}
	if anchor.CI != nil {
		workflow, renderErr := generator.renderYAML(
			goCITemplatePath, data, bundle, inputDigest, editSources,
		)
		if renderErr != nil {
			return Candidate{}, []domain.Finding{projectionError(
				"GDS_PROJECTION_RENDER_FAILED", "Cannot render GitHub Actions caller", renderErr,
			)}
		}
		if validationErr := validateGoWorkflowCaller(workflow, anchor); validationErr != nil {
			return Candidate{}, []domain.Finding{projectionError(
				"GDS_WORKFLOW_CALLER_INVALID", "Generated GitHub Actions caller is unsafe", validationErr,
			)}
		}
		managed = append(managed, newFile(goCIOutputPath, workflow))
	}
	sort.Slice(managed, func(left, right int) bool { return managed[left].Path < managed[right].Path })
	outputDigest, err := aggregateDigest(managed)
	if err != nil {
		return Candidate{}, []domain.Finding{projectionError(
			"GDS_PROJECTION_OUTPUT_DIGEST_FAILED", "Cannot compute projection output digest", err,
		)}
	}
	lock, findings := generator.renderLock(bundle, inputDigest, outputDigest, managed)
	if len(findings) != 0 {
		return Candidate{}, findings
	}
	files := append(append([]File(nil), managed...), newFile(bundleLockPath, lock))
	sort.Slice(files, func(left, right int) bool { return files[left].Path < files[right].Path })
	return Candidate{InputDigest: inputDigest, OutputDigest: outputDigest, Files: files}, nil
}

func (generator *Generator) renderAgentInstructions(
	anchor domain.RepositoryAnchor,
	data templateData,
	bundle Bundle,
	inputDigest string,
	editSources []string,
) ([]byte, []byte, error) {
	if anchor.Module != nil &&
		anchor.Classification.VisibilityContract == "public" {
		agents, err := generator.renderTemplate(publicModuleAgentsTemplatePath, data)
		if err != nil {
			return nil, nil, err
		}
		return agents, []byte("@../AGENTS.md\n"), nil
	}
	agents, err := generator.renderMarkdown(
		agentsTemplatePath, data, bundle, inputDigest, editSources,
	)
	if err != nil {
		return nil, nil, err
	}
	claude, err := generator.renderMarkdown(
		claudeTemplatePath, data, bundle, inputDigest, editSources,
	)
	if err != nil {
		return nil, nil, err
	}
	return agents, claude, nil
}

func (generator *Generator) renderTemplate(path string, data templateData) ([]byte, error) {
	parsed, err := template.New(path).Option("missingkey=error").Parse(string(generator.templates[path]))
	if err != nil {
		return nil, err
	}
	var output bytes.Buffer
	if err := parsed.Execute(&output, data); err != nil {
		return nil, err
	}
	content := output.Bytes()
	if len(content) == 0 || content[len(content)-1] != '\n' {
		content = append(content, '\n')
	}
	return content, nil
}

func (generator *Generator) renderYAML(
	path string,
	data templateData,
	bundle Bundle,
	inputDigest string,
	editSources []string,
) ([]byte, error) {
	parsed, err := template.New(path).Option("missingkey=error").Funcs(template.FuncMap{
		"yamlQuote": strconv.Quote,
	}).Parse(string(generator.templates[path]))
	if err != nil {
		return nil, err
	}
	var body bytes.Buffer
	if err := parsed.Execute(&body, data); err != nil {
		return nil, err
	}
	bodyBytes := body.Bytes()
	if len(bodyBytes) == 0 || bodyBytes[len(bodyBytes)-1] != '\n' {
		bodyBytes = append(bodyBytes, '\n')
	}
	var output bytes.Buffer
	output.WriteString("# GENERATED FILE - DO NOT EDIT DIRECTLY\n")
	output.WriteString("# generator: gds\n")
	output.WriteString("# bundle: ")
	output.WriteString(bundle.Version)
	output.WriteByte('\n')
	// The header names the source content, not the commit that carried it. A
	// commit here changed these bytes on every re-commit of identical sources,
	// which made the projection stale against its own lock.
	output.WriteString("# source-tree-digest: ")
	output.WriteString(bundle.SourceTreeDigest)
	output.WriteByte('\n')
	output.WriteString("# input-digest: ")
	output.WriteString(inputDigest)
	output.WriteByte('\n')
	output.WriteString("# output-digest: ")
	output.WriteString(digestBytes(bodyBytes))
	output.WriteByte('\n')
	output.WriteString("# edit-source:\n")
	for _, source := range editSources {
		output.WriteString("#   - ")
		output.WriteString(source)
		output.WriteByte('\n')
	}
	output.Write(bodyBytes)
	return output.Bytes(), nil
}

func (generator *Generator) inputDigest(
	anchor domain.RepositoryAnchor,
	policy compiler.CompiledPolicyDocument,
	bundle Bundle,
) (string, error) {
	templateDigests := map[string]string{}
	for path, content := range generator.templates {
		templateDigests[path] = digestBytes(content)
	}
	// Embedding the whole Bundle here would carry SourceCommit into the
	// projection identity and reintroduce the self-reference one layer up: the
	// projection would change when the commit changed, even though nothing it
	// renders did. Only the bundle's identity-bearing fields participate.
	payload := struct {
		SchemaVersion int                     `json:"schema_version"`
		Bundle        BundleIdentity          `json:"bundle"`
		Repository    domain.RepositoryAnchor `json:"repository"`
		PolicyDigest  string                  `json:"policy_digest"`
		Templates     map[string]string       `json:"templates"`
	}{
		SchemaVersion: domain.SchemaVersion, Bundle: bundle.Identity(), Repository: anchor,
		PolicyDigest: policy.CompiledPolicy.Digest, Templates: templateDigests,
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), nil
}

func (generator *Generator) renderMarkdown(
	path string,
	data templateData,
	bundle Bundle,
	inputDigest string,
	editSources []string,
) ([]byte, error) {
	parsed, err := template.New(path).Option("missingkey=error").Parse(string(generator.templates[path]))
	if err != nil {
		return nil, err
	}
	var body bytes.Buffer
	if err := parsed.Execute(&body, data); err != nil {
		return nil, err
	}
	bodyBytes := body.Bytes()
	if len(bodyBytes) == 0 || bodyBytes[len(bodyBytes)-1] != '\n' {
		bodyBytes = append(bodyBytes, '\n')
	}
	bodyDigest := digestBytes(bodyBytes)
	var output bytes.Buffer
	output.WriteString("<!--\n")
	output.WriteString("GENERATED FILE - DO NOT EDIT DIRECTLY\n")
	output.WriteString("generator: gds\n")
	output.WriteString("bundle: ")
	output.WriteString(bundle.Version)
	output.WriteByte('\n')
	output.WriteString("source-tree-digest: ")
	output.WriteString(bundle.SourceTreeDigest)
	output.WriteByte('\n')
	output.WriteString("input-digest: ")
	output.WriteString(inputDigest)
	output.WriteByte('\n')
	output.WriteString("output-digest: ")
	output.WriteString(bodyDigest)
	output.WriteByte('\n')
	output.WriteString("edit-source:\n")
	for _, source := range editSources {
		output.WriteString("  - ")
		output.WriteString(source)
		output.WriteByte('\n')
	}
	output.WriteString("-->\n")
	output.Write(bodyBytes)
	return output.Bytes(), nil
}

func (generator *Generator) renderLock(
	bundle Bundle,
	inputDigest string,
	outputDigest string,
	files []File,
) ([]byte, []domain.Finding) {
	lockFiles := make([]lockFile, 0, len(files))
	for _, file := range files {
		lockFiles = append(lockFiles, lockFile{Path: file.Path, Digest: file.Digest})
	}
	document := lockDocument{
		SchemaVersion: domain.SchemaVersion, Bundle: bundle,
		Projection: lockProjection{
			InputDigest: inputDigest, OutputDigest: outputDigest, Files: lockFiles,
		},
	}
	content := renderLockYAML(document)
	value, err := serialization.Decode("bundle.lock.yaml", content)
	if err != nil {
		return nil, []domain.Finding{projectionError(
			"GDS_PROJECTION_LOCK_INVALID", "Cannot decode generated bundle lock", err,
		)}
	}
	if findings := generator.schemas.Validate("bundle-lock", value, "bundle.lock.yaml"); len(findings) != 0 {
		return nil, findings
	}
	return content, nil
}

func renderLockYAML(document lockDocument) []byte {
	var output strings.Builder
	output.WriteString("# GENERATED FILE - DO NOT EDIT DIRECTLY\n")
	output.WriteString("schema_version: 1\n\n")
	output.WriteString("bundle:\n")
	writeYAMLString(&output, 2, "version", document.Bundle.Version)
	output.WriteString(fmt.Sprintf("  release_sequence: %d\n", document.Bundle.ReleaseSequence))
	writeYAMLString(&output, 2, "channel", document.Bundle.Channel)
	// A content-addressed lock does not record the commit at all. Writing it
	// made the lock its own moving target: re-rendering after the commit that
	// carried it produced different bytes, so the lock went stale against
	// itself. A v1 lock still carries the field and is still identified by it,
	// which is why the reader accepts both shapes.
	if document.Bundle.SourceTreeDigest == "" && document.Bundle.SourceCommit != "" {
		writeYAMLString(&output, 2, "source_commit", document.Bundle.SourceCommit)
	}
	// Emitted only when present, so a lock written before the content contract
	// stays byte-identical and keeps verifying by commit until it is regenerated.
	if document.Bundle.SourceTreeDigest != "" {
		writeYAMLString(&output, 2, "source_tree_digest", document.Bundle.SourceTreeDigest)
	}
	writeYAMLString(&output, 2, "digest", document.Bundle.Digest)
	if document.Bundle.AttestationIdentityDigest != "" {
		writeYAMLString(
			&output, 2, "attestation_identity_digest", document.Bundle.AttestationIdentityDigest,
		)
	}
	output.WriteString("\nprojection:\n")
	writeYAMLString(&output, 2, "input_digest", document.Projection.InputDigest)
	writeYAMLString(&output, 2, "output_digest", document.Projection.OutputDigest)
	output.WriteString("  files:\n")
	for _, file := range document.Projection.Files {
		output.WriteString("    - path: ")
		output.WriteString(strconv.Quote(file.Path))
		output.WriteByte('\n')
		output.WriteString("      digest: ")
		output.WriteString(strconv.Quote(file.Digest))
		output.WriteByte('\n')
	}
	return []byte(output.String())
}

func writeYAMLString(output *strings.Builder, indent int, key, value string) {
	output.WriteString(strings.Repeat(" ", indent))
	output.WriteString(key)
	output.WriteString(": ")
	output.WriteString(strconv.Quote(value))
	output.WriteByte('\n')
}

func projectionTemplateData(
	anchor domain.RepositoryAnchor,
	policy compiler.CompiledPolicyDocument,
	bundle Bundle,
) templateData {
	product := anchor.Product
	if product == nil {
		product = &domain.ProductFacts{}
	}
	return templateData{
		Purpose:      product.Purpose,
		Capabilities: product.Capabilities,
		Entrypoints:  product.Entrypoints,
		RepositoryID: anchor.Repository.ID,
		Roles:        strings.Join(anchor.Repository.Roles, ", "), BundleVersion: bundle.Version,
		ExternalWriteApproval: effectiveString(
			policy.Effective, "security", "external_write_requires_approval",
		),
		GeneratedProjectionEdit: effectiveString(
			policy.Effective, "agent", "generated_projection_edit",
		),
		PrivateParentPersistence: effectiveString(
			policy.Effective, "context", "private_parent_persistence",
		),
		VisibilityContract:       anchor.Classification.VisibilityContract,
		DataClassification:       anchor.Classification.DataClassification,
		SkillProfiles:            strings.Join(effectiveProfiles(policy.Effective), ", "),
		Commands:                 verificationCommands(anchor.Verification.Commands),
		DefaultBranch:            anchor.Git.DefaultBranch,
		GoVersion:                ciString(anchor.CI, func(value *domain.CIPolicy) string { return value.GoVersion }),
		BuildCommand:             ciString(anchor.CI, func(value *domain.CIPolicy) string { return value.BuildCommand }),
		TestCommand:              ciString(anchor.CI, func(value *domain.CIPolicy) string { return value.TestCommand }),
		// The pull-request tier runs what the anchor declares, not a fixed
		// script name. A repository that does not ship a tier runner still
		// has to be able to state its own gate.
		PRRequiredCommand:        strings.Join(anchor.Verification.Commands.PRRequired, " && "),
		TimeoutMinutes:           ciInt(anchor.CI, func(value *domain.CIPolicy) int { return value.TimeoutMinutes }),
		WorkflowRef:              ciString(anchor.CI, func(value *domain.CIPolicy) string { return value.WorkflowRef }),
		Runner:                   ciRunner(anchor.CI),
		GitHubWorkflowExpression: "${{ github.workflow }}",
		GitHubRefExpression:      "${{ github.ref }}",
	}
}

func ciString(value *domain.CIPolicy, selectValue func(*domain.CIPolicy) string) string {
	if value == nil {
		return ""
	}
	return selectValue(value)
}

func ciInt(value *domain.CIPolicy, selectValue func(*domain.CIPolicy) int) int {
	if value == nil {
		return 0
	}
	return selectValue(value)
}

func verificationCommands(commands domain.VerificationCommands) []commandLine {
	groups := []struct {
		label  string
		values []string
	}{
		{label: "Bootstrap", values: commands.Bootstrap},
		{label: "Lint", values: commands.Lint},
		{label: "Typecheck", values: commands.Typecheck},
		{label: "Test", values: commands.Test},
		{label: "Build", values: commands.Build},
		{label: "Compatibility", values: commands.Compatibility},
		{label: "Package", values: commands.Package},
		{label: "Fast", values: commands.Fast},
		{label: "PR required", values: commands.PRRequired},
		{label: "Full", values: commands.Full},
		{label: "Release", values: commands.Release},
	}
	result := []commandLine{}
	for _, group := range groups {
		for _, command := range group.values {
			result = append(result, commandLine{
				Label: group.label, Command: markdownCodeSpan(command),
			})
		}
	}
	return result
}

func markdownCodeSpan(value string) string {
	longest := 0
	current := 0
	for _, character := range value {
		if character == '`' {
			current++
			if current > longest {
				longest = current
			}
			continue
		}
		current = 0
	}
	fence := strings.Repeat("`", longest+1)
	padding := ""
	if strings.HasPrefix(value, "`") || strings.HasSuffix(value, "`") {
		padding = " "
	}
	return fence + padding + value + padding + fence
}

func effectiveString(effective map[string]any, section, key string) string {
	object, ok := effective[section].(map[string]any)
	if !ok {
		return "not-proven"
	}
	value, found := object[key]
	if !found {
		return "not-proven"
	}
	return fmt.Sprint(value)
}

func effectiveProfiles(effective map[string]any) []string {
	agent, ok := effective["agent"].(map[string]any)
	if !ok {
		return []string{}
	}
	if typed, ok := agent["profiles"].([]string); ok {
		return append([]string(nil), typed...)
	}
	raw, ok := agent["profiles"].([]any)
	if !ok {
		return []string{}
	}
	result := make([]string, 0, len(raw))
	for _, item := range raw {
		if value, ok := item.(string); ok {
			result = append(result, value)
		}
	}
	return result
}

func projectionEditSources(policy compiler.CompiledPolicyDocument) []string {
	sources := []string{".gds/repository.yaml"}
	seen := map[string]struct{}{sources[0]: {}}
	for _, source := range policy.Sources {
		if _, exists := seen[source.Path]; !exists {
			sources = append(sources, source.Path)
			seen[source.Path] = struct{}{}
		}
	}
	for _, provenance := range policy.Provenance {
		if provenance.Operation != "exception" {
			continue
		}
		if _, exists := seen[provenance.File]; !exists {
			sources = append(sources, provenance.File)
			seen[provenance.File] = struct{}{}
		}
	}
	sources = append(sources,
		"templates/"+agentsTemplatePath,
		"templates/"+claudeTemplatePath,
		"templates/"+goCITemplatePath,
	)
	sort.Strings(sources)
	return sources
}

func aggregateDigest(files []File) (string, error) {
	values := make([]map[string]any, 0, len(files))
	for _, file := range files {
		values = append(values, map[string]any{"path": file.Path, "digest": file.Digest})
	}
	raw, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	return digestBytes(raw), nil
}

func newFile(path string, content []byte) File {
	copyOfContent := append([]byte(nil), content...)
	return File{Path: path, Content: copyOfContent, Digest: digestBytes(copyOfContent)}
}

func projectionError(code, message string, err error) domain.Finding {
	return domain.Finding{
		Code: code, Severity: domain.SeverityHigh, Message: message + ": " + err.Error(),
	}
}

func projectionDistributionAllowed(distribution, visibility string) bool {
	rank := map[string]int{"public": 0, "internal": 1, "private": 2}
	distributionRank, distributionFound := rank[distribution]
	visibilityRank, visibilityFound := rank[visibility]
	return distributionFound && visibilityFound && distributionRank <= visibilityRank
}

// ciRunner defaults an undeclared runner to the GitHub-hosted label. Absence
// must mean hosted: a repository that never thought about runner placement gets
// the safe one, and reaching estate hardware stays an explicit declaration.
func ciRunner(value *domain.CIPolicy) string {
	if value == nil || value.Runner == "" {
		return "ubuntu-latest"
	}
	return value.Runner
}
