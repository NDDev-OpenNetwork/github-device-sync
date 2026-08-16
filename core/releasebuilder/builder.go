package releasebuilder

import (
	"bytes"
	"context"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/bundle"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/gitauthority"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/semver"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/skills"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

const maxCommandOutput = 8 << 20

type commandResult struct {
	stdout []byte
	stderr []byte
}

func Build(ctx context.Context, request Request, schemas *validation.Set) (result Result, returnErr error) {
	if schemas == nil {
		return Result{}, errors.New("release schema set is unavailable")
	}
	root, output, err := validateRequest(request)
	if err != nil {
		return Result{}, err
	}
	harnessDigest, harnessProvisional, err := verifyHarnessEvidence(request, root)
	if err != nil {
		return Result{}, err
	}
	gitAuthority, err := gitauthority.Discover()
	if err != nil {
		return Result{}, err
	}
	gitIdentity, err := gitAuthority.InspectVersion(ctx)
	if err != nil {
		return Result{}, err
	}
	source, err := inspectSourceWithAuthority(ctx, gitAuthority, root, request.SourceRef)
	if err != nil {
		return Result{}, err
	}
	if err := validateReleaseRef(source.Ref, request.Version, request.Channel); err != nil {
		return Result{}, err
	}
	goBinary := request.GoBinary
	goBinary, err = resolveGoBinary(goBinary)
	if err != nil {
		return Result{}, err
	}
	temporary, err := os.MkdirTemp("", "gds-release-build-")
	if err != nil {
		return Result{}, err
	}
	defer func() {
		if err := cleanupReleaseTemporary(temporary); err != nil {
			result = Result{}
			returnErr = errors.Join(returnErr, fmt.Errorf("clean release build workspace: %w", err))
		}
	}()
	home := filepath.Join(temporary, "home")
	if err := os.Mkdir(home, 0o700); err != nil {
		return Result{}, err
	}
	snapshotRoot, trackedSources, err := materializeSourceSnapshotWithAuthority(
		ctx, gitAuthority, root, temporary, source.Commit, home, productionSourceSnapshotLimits(),
	)
	if err != nil {
		return Result{}, err
	}
	root = snapshotRoot
	goVersion, err := inspectGoVersion(ctx, root, goBinary, home)
	if err != nil {
		return Result{}, err
	}
	binaries, err := buildBinaries(ctx, root, temporary, goBinary, request.Version)
	if err != nil {
		return Result{}, err
	}
	modules, err := inspectModules(ctx, root, goBinary, request.Version, home)
	if err != nil {
		return Result{}, err
	}
	sbom, err := buildSBOM(request.Version, source, modules, binaries, gitIdentity)
	if err != nil {
		return Result{}, err
	}
	trust, err := bundle.LoadTrust(root, schemas)
	if err != nil {
		return Result{}, err
	}
	if err := validateVerifierTargetCoverage(trust); err != nil {
		return Result{}, err
	}
	if len(trust.Source.AllowedWorkflows) != 1 {
		return Result{}, errors.New("release trust policy must declare one exact workflow")
	}
	trustRaw, err := readRegular(filepath.Join(root, "requirements", "bundle-trust.yaml"), 1<<20)
	if err != nil {
		return Result{}, err
	}
	additional, err := releaseAdditionalFiles(root, binaries, sbom, trustRaw, schemas)
	if err != nil {
		return Result{}, err
	}
	options := bundle.BuildOptions{
		BundleVersion: request.Version, ReleaseSequence: request.ReleaseSequence,
		Channel: request.Channel, SourceCommit: source.Commit,
		MinimumCLIVersion: request.MinimumCLIVersion,
		Workflow:          trust.Source.AllowedWorkflows[0], SourceRef: source.Ref,
		TrackedSources: trackedSources, AdditionalFiles: additional,
		HarnessEvidenceManifestDigest: harnessDigest,
		HarnessEvidenceProvisional:    harnessProvisional,
	}
	first, findings := bundle.Build(root, options, trust, schemas)
	if len(findings) != 0 {
		return Result{}, findingError("build release bundle", findings)
	}
	if err := bundle.ValidateReleaseExecutableMatrix(first.Manifest); err != nil {
		return Result{}, err
	}
	second, findings := bundle.Build(root, options, trust, schemas)
	if len(findings) != 0 || !bytes.Equal(first.Artifact, second.Artifact) || first.Envelope != second.Envelope {
		return Result{}, errors.New("independent release bundle assembly is not byte-reproducible")
	}
	if _, findings := bundle.VerifyReleaseUnit(first.Artifact, first.Envelope, schemas); len(findings) != 0 {
		return Result{}, findingError("verify release bundle", findings)
	}
	result, files, err := releaseOutputFiles(
		request, source, goVersion, gitIdentity, first, sbom, trustRaw,
	)
	if err != nil {
		return Result{}, err
	}
	if err := writeOutput(output, files); err != nil {
		return Result{}, err
	}
	verified, err := VerifyDirectory(output, schemas)
	if err != nil {
		return Result{}, err
	}
	if verified.ArtifactDigest != first.Envelope.ArtifactDigest || !verified.Reproducible {
		return Result{}, errors.New("materialized release output differs from the verified candidate")
	}
	result.OutputDirectory = output
	result.Files = verified.Files
	return result, nil
}

func validateVerifierTargetCoverage(trust bundle.TrustPolicy) error {
	targets := defaultTargets()
	executables := make(map[Target]struct{}, len(trust.Verification.Verifier.Executables))
	for _, executable := range trust.Verification.Verifier.Executables {
		target := Target{OS: executable.OS, Arch: executable.Arch}
		if _, duplicate := executables[target]; duplicate {
			return fmt.Errorf("release verifier trust contains duplicate target %s/%s", target.OS, target.Arch)
		}
		executables[target] = struct{}{}
	}
	if len(executables) != len(targets) {
		return fmt.Errorf(
			"release verifier trust covers %d targets, want %d", len(executables), len(targets),
		)
	}
	for _, target := range targets {
		if _, found := executables[target]; !found {
			return fmt.Errorf("release verifier trust is missing target %s/%s", target.OS, target.Arch)
		}
	}
	return nil
}

func validateRequest(request Request) (string, string, error) {
	if !semver.Valid(request.Version) || request.ReleaseSequence < 1 ||
		(request.Channel != "canary" && request.Channel != "stable" && request.Channel != "frozen") ||
		!semver.Valid(request.MinimumCLIVersion) ||
		((request.HarnessEvidenceDirectory == "") != (request.HarnessEvidenceTrustPolicy == "")) ||
		((request.Channel == "stable" || request.Channel == "frozen") && request.HarnessEvidenceDirectory == "") {
		return "", "", errors.New("release request version, sequence, channel, or CLI floor is invalid")
	}
	root, err := filepath.Abs(request.Root)
	if err != nil {
		return "", "", err
	}
	output, err := filepath.Abs(request.OutputDirectory)
	if err != nil || output == root || strings.HasPrefix(output, root+string(filepath.Separator)) {
		return "", "", errors.New("release output directory is invalid")
	}
	if info, err := os.Lstat(output); err == nil || !os.IsNotExist(err) {
		if err == nil {
			return "", "", fmt.Errorf("release output already exists (%s)", info.Mode().Type())
		}
		return "", "", err
	}
	parent := filepath.Dir(output)
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", "", errors.New("release output parent must be an existing real directory")
	}
	return root, output, nil
}

func inspectSource(ctx context.Context, root string, requestedRef string) (Source, error) {
	authority, err := gitauthority.Discover()
	if err != nil {
		return Source{}, err
	}
	return inspectSourceWithAuthority(ctx, authority, root, requestedRef)
}

func inspectSourceWithAuthority(
	ctx context.Context,
	authority *gitauthority.Authority,
	root string,
	requestedRef string,
) (Source, error) {
	resolved, err := runGit(ctx, authority, root, nil, "rev-parse", "--show-toplevel")
	if err != nil || filepath.Clean(strings.TrimSpace(string(resolved.stdout))) != root {
		return Source{}, errors.New("release root is not the exact Git worktree root")
	}
	status, err := runGit(ctx, authority, root, nil, "status", "--porcelain=v2", "-z", "--untracked-files=all")
	if err != nil || len(status.stdout) != 0 {
		return Source{}, errors.New("release source must be fully tracked and clean")
	}
	headResult, err := runGit(ctx, authority, root, nil, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return Source{}, err
	}
	commit := strings.TrimSpace(string(headResult.stdout))
	if !gitOID(commit) {
		return Source{}, errors.New("release source commit is invalid")
	}
	ref := strings.TrimSpace(requestedRef)
	if ref == "" {
		branch, err := runGit(ctx, authority, root, nil, "symbolic-ref", "--quiet", "HEAD")
		if err != nil {
			return Source{}, errors.New("detached release source requires an exact --source-ref")
		}
		ref = strings.TrimSpace(string(branch.stdout))
	}
	if !strings.HasPrefix(ref, "refs/heads/") && !strings.HasPrefix(ref, "refs/tags/") {
		return Source{}, errors.New("release source ref is invalid")
	}
	refCommit, err := runGit(ctx, authority, root, nil, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil || strings.TrimSpace(string(refCommit.stdout)) != commit {
		return Source{}, errors.New("release source ref does not resolve to HEAD")
	}
	timestampResult, err := runGit(
		ctx, authority, root, nil, "--no-replace-objects", "show", "-s", "--format=%cI", commit,
	)
	if err != nil {
		return Source{}, err
	}
	timestamp, err := time.Parse(time.RFC3339, strings.TrimSpace(string(timestampResult.stdout)))
	if err != nil {
		return Source{}, errors.New("release source commit timestamp is invalid")
	}
	return Source{Commit: commit, Ref: ref, Timestamp: timestamp.UTC()}, nil
}

func runGit(
	ctx context.Context,
	authority *gitauthority.Authority,
	directory string,
	environment []string,
	arguments ...string,
) (commandResult, error) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	_, err := authority.Run(ctx, gitauthority.RunRequest{
		Directory: directory, Arguments: arguments, ExtraEnvironment: environment,
		Stdout: &limitedWriter{writer: &stdout, remaining: maxCommandOutput},
		Stderr: &limitedWriter{writer: &stderr, remaining: maxCommandOutput},
	})
	result := commandResult{stdout: stdout.Bytes(), stderr: stderr.Bytes()}
	if err != nil {
		return result, fmt.Errorf("release Git command failed (%T): %w", err, err)
	}
	return result, nil
}

func inspectGoVersion(ctx context.Context, root string, goBinary string, home string) (string, error) {
	result, err := run(ctx, root, releaseEnvironment("", "", "", home), goBinary, "version")
	if err != nil {
		return "", err
	}
	fields := strings.Fields(string(result.stdout))
	if len(fields) < 3 || fields[0] != "go" || fields[1] != "version" || fields[2] != ExpectedGoVersion {
		return "", fmt.Errorf("release builder requires %s", ExpectedGoVersion)
	}
	return fields[2], nil
}

func validateReleaseRef(ref, version, channel string) error {
	expectedTag := "refs/tags/gds-v" + version
	if strings.HasPrefix(ref, "refs/tags/") && ref != expectedTag {
		return errors.New("release tag does not match the requested bundle version")
	}
	if channel == "stable" || channel == "frozen" {
		if ref != expectedTag {
			return errors.New("stable and frozen releases require the exact version tag")
		}
		return nil
	}
	if channel == "canary" && ref != "refs/heads/main" && ref != expectedTag {
		return errors.New("canary releases require main or the exact version tag")
	}
	return nil
}

func buildBinaries(
	ctx context.Context,
	root string,
	temporary string,
	goBinary string,
	version string,
) ([]binaryRecord, error) {
	commandsByName := map[string]struct {
		name         string
		pkg          string
		expectedPath string
		ldflags      string
	}{
		"gds":                      {name: "gds", pkg: "./core/cmd/gds", expectedPath: "github.com/NDDev-OpenNetwork/github-device-sync/core/cmd/gds", ldflags: "-s -w -buildid= -X github.com/NDDev-OpenNetwork/github-device-sync/core/cli.Version=" + version},
		"gds-controller":           {name: "gds-controller", pkg: "./core/cmd/gds-controller", expectedPath: "github.com/NDDev-OpenNetwork/github-device-sync/core/cmd/gds-controller", ldflags: "-s -w -buildid= -X main.version=" + version},
		"gds-codex-runtime-driver": {name: "gds-codex-runtime-driver", pkg: "./core/cmd/gds-codex-runtime-driver", expectedPath: "github.com/NDDev-OpenNetwork/github-device-sync/core/cmd/gds-codex-runtime-driver", ldflags: "-s -w -buildid="},
	}
	commands := make([]struct {
		name         string
		pkg          string
		expectedPath string
		ldflags      string
	}, 0, len(commandsByName))
	for _, name := range bundle.RequiredReleaseExecutables() {
		command, found := commandsByName[name]
		if !found {
			return nil, fmt.Errorf("release builder has no implementation for required executable %s", name)
		}
		commands = append(commands, command)
	}
	targets := defaultTargets()
	result := make([]binaryRecord, 0, len(targets)*len(commands))
	for _, target := range targets {
		for _, command := range commands {
			paths := []string{
				filepath.Join(temporary, "first", target.OS, target.Arch, command.name),
				filepath.Join(temporary, "second", target.OS, target.Arch, command.name),
			}
			for buildIndex, output := range paths {
				if err := os.MkdirAll(filepath.Dir(output), 0o700); err != nil {
					return nil, err
				}
				environment := releaseEnvironment(
					target.OS, target.Arch,
					filepath.Join(temporary, "cache", fmt.Sprintf("build-%d", buildIndex+1)),
					filepath.Join(temporary, "home"),
				)
				_, err := run(
					ctx, root, environment, goBinary, "build", "-mod=readonly", "-trimpath",
					"-buildvcs=false", "-ldflags", command.ldflags, "-o", output, command.pkg,
				)
				if err != nil {
					return nil, err
				}
			}
			first, err := readRegular(paths[0], 128<<20)
			if err != nil {
				return nil, err
			}
			second, err := readRegular(paths[1], 128<<20)
			if err != nil || !bytes.Equal(first, second) {
				return nil, fmt.Errorf("%s/%s %s binary is not byte-reproducible", target.OS, target.Arch, command.name)
			}
			if err := validateBuiltGoBinary(first, command.expectedPath, target, root, temporary); err != nil {
				return nil, fmt.Errorf("%s/%s %s binary is invalid: %w", target.OS, target.Arch, command.name, err)
			}
			path := filepath.ToSlash(filepath.Join("bin", target.OS, target.Arch, command.name))
			result = append(result, binaryRecord{Path: path, Digest: digestBytes(first), Content: first})
		}
	}
	sort.Slice(result, func(left, right int) bool { return result[left].Path < result[right].Path })
	return result, nil
}

func validateBuiltGoBinary(content []byte, expectedPath string, target Target, forbiddenPaths ...string) error {
	info, err := buildinfo.Read(bytes.NewReader(content))
	if err != nil {
		return errors.New("Go build information is unavailable")
	}
	if info.GoVersion != ExpectedGoVersion || info.Path != expectedPath ||
		info.Main.Path != "github.com/NDDev-OpenNetwork/github-device-sync" {
		return errors.New("Go build identity does not match the release contract")
	}
	settings := map[string]string{}
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
		if strings.HasPrefix(setting.Key, "vcs.") {
			return errors.New("VCS metadata is embedded despite the release contract")
		}
	}
	if settings["-trimpath"] != "true" || settings["CGO_ENABLED"] != "0" ||
		settings["GOOS"] != target.OS || settings["GOARCH"] != target.Arch {
		return errors.New("Go build settings do not match the release contract")
	}
	if target.Arch == "amd64" && settings["GOAMD64"] != "v1" {
		return errors.New("Go AMD64 baseline is not portable")
	}
	if target.Arch == "arm64" && settings["GOARM64"] != "v8.0" {
		return errors.New("Go ARM64 baseline is not portable")
	}
	for _, forbidden := range forbiddenPaths {
		if forbidden != "" && bytes.Contains(content, []byte(filepath.Clean(forbidden))) {
			return errors.New("Go binary contains a local build path")
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" &&
		bytes.Contains(content, []byte(filepath.Clean(home))) {
		return errors.New("Go binary contains the builder home path")
	}
	return nil
}

func inspectModules(
	ctx context.Context,
	root string,
	goBinary string,
	version string,
	home string,
) ([]moduleRecord, error) {
	result, err := run(
		ctx, root, releaseEnvironment("", "", "", home), goBinary,
		"list", "-mod=readonly", "-m", "-json", "all",
	)
	if err != nil {
		return nil, err
	}
	type module struct {
		Path    string
		Version string
		Sum     string
		Main    bool
		Replace *module
	}
	decoder := json.NewDecoder(bytes.NewReader(result.stdout))
	modules := []moduleRecord{}
	for {
		var value module
		if err := decoder.Decode(&value); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, err
		}
		if value.Main {
			modules = append(modules, moduleRecord{Path: value.Path, Version: version})
			continue
		}
		if value.Replace != nil {
			if value.Replace.Version == "" {
				return nil, fmt.Errorf("local Go module replacement is forbidden in release: %s", value.Path)
			}
			value = *value.Replace
		}
		modules = append(modules, moduleRecord{Path: value.Path, Version: value.Version, Sum: value.Sum})
	}
	if len(modules) == 0 {
		return nil, errors.New("release Go module graph is empty")
	}
	return modules, nil
}

func releaseAdditionalFiles(
	root string,
	binaries []binaryRecord,
	sbom []byte,
	trust []byte,
	schemas *validation.Set,
) ([]bundle.AdditionalFile, error) {
	files := make([]bundle.AdditionalFile, 0, len(binaries)+64)
	for _, binary := range binaries {
		files = append(files, bundle.AdditionalFile{
			Path: binary.Path, Content: append([]byte(nil), binary.Content...), Mode: "0755",
			ContentKind: bundle.AdditionalContentOpaqueExecutable,
		})
	}
	for _, plugin := range []string{"gds-core", "gds-estate-admin", "gds-module"} {
		candidate, findings := skills.BuildPackage(root, plugin, schemas)
		if len(findings) != 0 {
			return nil, findingError("build plugin "+plugin, findings)
		}
		contents, err := candidate.ReleaseFiles()
		if err != nil {
			return nil, err
		}
		paths := make([]string, 0, len(contents))
		for path := range contents {
			paths = append(paths, path)
		}
		sort.Strings(paths)
		for _, path := range paths {
			files = append(files, bundle.AdditionalFile{
				Path:    filepath.ToSlash(filepath.Join("packages", "codex", plugin, path)),
				Content: contents[path], Mode: "0644",
			})
		}
	}
	files = append(files,
		bundle.AdditionalFile{Path: "sbom/gds.spdx.json", Content: sbom, Mode: "0644"},
		bundle.AdditionalFile{Path: "trust/bundle-trust.yaml", Content: trust, Mode: "0644"},
	)
	return files, nil
}

func releaseEnvironment(goos string, goarch string, cache string, home string) []string {
	filtered := []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin"}
	for _, key := range []string{"SSL_CERT_DIR", "SSL_CERT_FILE", "TMPDIR"} {
		if value := os.Getenv(key); value != "" {
			filtered = append(filtered, key+"="+value)
		}
	}
	if cache == "" {
		cache = filepath.Join(home, ".cache", "go-build")
	}
	filtered = append(filtered,
		"CGO_ENABLED=0", "GOAMD64=v1", "GOARM64=v8.0", "GOENV=off",
		"GOCACHE="+cache, "GOMODCACHE="+filepath.Join(home, "go", "pkg", "mod"),
		"GOPROXY=https://proxy.golang.org,direct", "GOSUMDB=sum.golang.org",
		"GOTELEMETRY=off", "GOTOOLCHAIN=go1.26.5", "GOWORK=off",
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1", "GIT_TERMINAL_PROMPT=0",
		"HOME="+home, "LANG=C", "LC_ALL=C", "TZ=UTC",
	)
	if goos != "" {
		filtered = append(filtered, "GOOS="+goos)
	}
	if goarch != "" {
		filtered = append(filtered, "GOARCH="+goarch)
	}
	if cache != "" {
		filtered = append(filtered, "GOCACHE="+cache)
	}
	sort.Strings(filtered)
	return filtered
}

func resolveGoBinary(requested string) (string, error) {
	requested = strings.TrimSpace(requested)
	if requested == "" || requested == "go" {
		goroot := strings.TrimSpace(runtime.GOROOT())
		if goroot == "" {
			return "", errors.New(
				"release Go executable requires an absolute --go-binary when runtime GOROOT is unavailable",
			)
		}
		requested = filepath.Join(goroot, "bin", "go")
	}
	if !filepath.IsAbs(requested) {
		return "", errors.New("release Go executable must use an absolute path")
	}
	resolved, err := filepath.EvalSymlinks(filepath.Clean(requested))
	if err != nil {
		return "", fmt.Errorf("resolve release Go executable: %w", err)
	}
	info, err := os.Lstat(resolved)
	if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
		info.Mode().Perm()&0o111 == 0 {
		return "", errors.New("release Go executable must be an executable regular file")
	}
	return resolved, nil
}

func run(
	ctx context.Context,
	directory string,
	environment []string,
	executable string,
	arguments ...string,
) (commandResult, error) {
	command := exec.CommandContext(ctx, executable, arguments...)
	command.Dir = directory
	if environment != nil {
		command.Env = environment
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &limitedWriter{writer: &stdout, remaining: maxCommandOutput}
	command.Stderr = &limitedWriter{writer: &stderr, remaining: maxCommandOutput}
	err := command.Run()
	result := commandResult{stdout: stdout.Bytes(), stderr: stderr.Bytes()}
	if err != nil {
		return result, fmt.Errorf("release command %s failed (%T)", filepath.Base(executable), err)
	}
	return result, nil
}

type limitedWriter struct {
	writer    *bytes.Buffer
	remaining int
}

func (writer *limitedWriter) Write(value []byte) (int, error) {
	if len(value) > writer.remaining {
		return 0, errors.New("release command output exceeds bound")
	}
	written, err := writer.writer.Write(value)
	writer.remaining -= written
	return written, err
}

func readRegular(path string, maximum int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() < 0 || info.Size() > maximum {
		return nil, fmt.Errorf("release input is not a bounded regular file: %s", path)
	}
	return os.ReadFile(path)
}

func findingError(phase string, findings []domain.Finding) error {
	if len(findings) == 0 {
		return nil
	}
	return fmt.Errorf("%s failed with %s: %s", phase, findings[0].Code, findings[0].Message)
}

func gitOID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
