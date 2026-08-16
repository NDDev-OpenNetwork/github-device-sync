// Command gds-release-builder assembles or verifies one deterministic GDS
// release directory. It is a CI/release primitive, not an estate mutation
// interface.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/releasebuilder"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, arguments []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("gds-release-builder", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	request := releasebuilder.Request{}
	verifyDirectory := ""
	verifyTrustedRoot := ""
	trustPolicy := ""
	extractEvidenceArchive := ""
	extractEvidenceDestination := ""
	flags.StringVar(&request.Root, "root", ".", "exact clean GDS Git worktree root")
	flags.StringVar(&request.OutputDirectory, "output", "", "new release output directory")
	flags.StringVar(&request.Version, "version", "", "release SemVer without a v prefix")
	flags.IntVar(&request.ReleaseSequence, "sequence", 0, "monotonic release sequence")
	flags.StringVar(&request.Channel, "channel", "canary", "canary, stable, or frozen")
	flags.StringVar(&request.MinimumCLIVersion, "minimum-cli-version", "", "minimum compatible CLI SemVer")
	flags.StringVar(&request.SourceRef, "source-ref", "", "exact refs/heads/* or refs/tags/* source ref")
	flags.StringVar(&request.HarnessEvidenceDirectory, "harness-evidence-directory", "", "directory containing manifest.json and isolated active-harness records")
	flags.StringVar(&request.HarnessEvidenceTrustPolicy, "harness-evidence-trust-policy", "", "offline public trust policy for harness evidence")
	flags.StringVar(
		&request.GoBinary, "go-binary", "",
		"absolute Go executable (defaults to the running toolchain GOROOT when available)",
	)
	flags.StringVar(&verifyDirectory, "verify-directory", "", "verify an existing release output directory")
	flags.StringVar(&verifyTrustedRoot, "verify-trusted-root", "", "verify one offline trusted-root.jsonl")
	flags.StringVar(&trustPolicy, "trust-policy", "", "independent local consumer trust policy")
	flags.StringVar(&extractEvidenceArchive, "extract-harness-evidence-archive", "", "bounded harness evidence tar.gz")
	flags.StringVar(&extractEvidenceDestination, "extract-harness-evidence-destination", "", "new private evidence directory")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 {
		return writeFailure(stderr, "GDS_RELEASE_ARGUMENTS_INVALID", "Release builder arguments are invalid.", 4)
	}
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		return writeFailure(stderr, "GDS_RELEASE_SCHEMAS_UNAVAILABLE", "Embedded schemas are unavailable.", 14)
	}
	if extractEvidenceArchive != "" || extractEvidenceDestination != "" {
		if extractEvidenceArchive == "" || extractEvidenceDestination == "" || flags.NArg() != 0 ||
			request.OutputDirectory != "" || verifyDirectory != "" || verifyTrustedRoot != "" || trustPolicy != "" {
			return writeFailure(stderr, "GDS_RELEASE_ARGUMENTS_CONFLICT", "Evidence extraction requires exactly one archive and destination.", 4)
		}
		if err := releasebuilder.MaterializeHarnessEvidenceArchive(extractEvidenceArchive, extractEvidenceDestination); err != nil {
			return writeFailureDetail(stderr, "GDS_HARNESS_EVIDENCE_ARCHIVE_INVALID", "Harness evidence archive was rejected.", err, 2)
		}
		return writeResult(stdout, map[string]any{"status": "materialized", "directory": extractEvidenceDestination})
	}
	if verifyDirectory != "" {
		if request.OutputDirectory != "" || request.Version != "" || request.ReleaseSequence != 0 ||
			verifyTrustedRoot != "" || trustPolicy != "" {
			return writeFailure(stderr, "GDS_RELEASE_ARGUMENTS_CONFLICT", "Verification cannot be combined with build arguments.", 4)
		}
		result, err := releasebuilder.VerifyDirectory(verifyDirectory, schemas)
		if err != nil {
			return writeFailureDetail(
				stderr, "GDS_RELEASE_VERIFICATION_FAILED",
				"Release directory verification failed.", err, 2,
			)
		}
		return writeResult(stdout, result)
	}
	if verifyTrustedRoot != "" || trustPolicy != "" {
		if verifyTrustedRoot == "" || trustPolicy == "" || request.OutputDirectory != "" ||
			request.Version != "" || request.ReleaseSequence != 0 {
			return writeFailure(
				stderr, "GDS_RELEASE_ARGUMENTS_CONFLICT",
				"Trusted-root verification requires exactly one root and one local trust policy.", 4,
			)
		}
		result, err := releasebuilder.VerifyTrustedRoot(verifyTrustedRoot, trustPolicy, schemas)
		if err != nil {
			return writeFailureDetail(
				stderr, "GDS_RELEASE_TRUSTED_ROOT_INVALID",
				"Offline trusted-root verification failed.", err, 12,
			)
		}
		return writeResult(stdout, result)
	}
	if request.MinimumCLIVersion == "" {
		request.MinimumCLIVersion = request.Version
	}
	result, err := releasebuilder.Build(ctx, request, schemas)
	if err != nil {
		return writeFailureDetail(stderr, "GDS_RELEASE_BUILD_FAILED", "Release build failed.", err, 2)
	}
	return writeResult(stdout, result)
}

func writeResult(writer io.Writer, value any) int {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return 14
	}
	return 0
}

func writeFailure(writer io.Writer, code, message string, exit int) int {
	return writeFailureDetail(writer, code, message, nil, exit)
}

func writeFailureDetail(writer io.Writer, code, message string, detail error, exit int) int {
	payload := map[string]any{
		"schema_version": domain.SchemaVersion,
		"result":         "failed",
		"code":           code,
		"message":        message,
		"exit_code":      exit,
	}
	if detail != nil {
		payload["detail"] = detail.Error()
	}
	_ = writeResult(writer, payload)
	return exit
}
