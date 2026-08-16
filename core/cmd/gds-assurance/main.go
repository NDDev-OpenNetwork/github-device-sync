// Command gds-assurance runs the bounded, offline C10 scale and recovery gate.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/assurance"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	os.Exit(run(ctx, os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, arguments []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("gds-assurance", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	root := "."
	output := ""
	timeout := 3 * time.Minute
	performanceMode := string(assurance.DeterministicRequired)
	baselineReportPath := ""
	baselineEvidencePath := ""
	calibratedPolicyPath := ""
	runnerDigest := ""
	maximumRegression := 0.10
	flags.StringVar(&root, "root", ".", "exact GDS control-plane repository root")
	flags.StringVar(&output, "output", "", "new path for the validated JSON report")
	flags.DurationVar(&timeout, "timeout", 3*time.Minute, "bounded assurance timeout")
	flags.StringVar(&performanceMode, "performance-mode", performanceMode, "deterministic-required|relative-required|absolute-calibrated|informational")
	flags.StringVar(&baselineReportPath, "baseline-report", "", "immutable baseline assurance report JSON")
	flags.StringVar(&baselineEvidencePath, "baseline-evidence", "", "immutable baseline binding JSON")
	flags.StringVar(&calibratedPolicyPath, "calibrated-policy", "", "calibrated absolute policy JSON")
	flags.StringVar(&runnerDigest, "runner-digest", "", "exact calibrated/comparable runner sha256 digest")
	flags.Float64Var(&maximumRegression, "maximum-regression", maximumRegression, "relative regression fraction from 0 through 1")
	if err := flags.Parse(arguments); err != nil || flags.NArg() != 0 ||
		timeout < time.Minute || timeout > 10*time.Minute || maximumRegression < 0 || maximumRegression > 1 ||
		!assurance.ValidPerformanceMode(assurance.PerformanceMode(performanceMode)) {
		return writeFailure(stderr, "GDS_ASSURANCE_ARGUMENTS_INVALID", "Assurance arguments are invalid.", 4)
	}
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		return writeFailure(stderr, "GDS_ASSURANCE_SCHEMAS_UNAVAILABLE", "Embedded schemas are unavailable.", 14)
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	report, err := assurance.Run(ctx, assurance.Options{
		Root: root, RequireCleanWorktree: true,
	}, schemas)
	if err != nil {
		return writeFailureDetail(
			stderr, "GDS_ASSURANCE_EXECUTION_FAILED",
			"Integrated assurance did not produce accepted evidence.", err, 2,
		)
	}
	if output != "" {
		if err := writeExclusiveReport(output, report); err != nil {
			return writeFailureDetail(
				stderr, "GDS_ASSURANCE_OUTPUT_FAILED",
				"Assurance report could not be written safely.", err, 14,
			)
		}
	}
	if err := writeJSON(stdout, report); err != nil {
		return 14
	}
	// The report's Status field remains honest whenever any metric fails. The
	// explicit performance mode determines gate semantics; ambient CI variables
	// never relax a required contract.
	mode := assurance.PerformanceMode(performanceMode)
	if !assurance.ReportAcceptable(report, mode) {
		return 2
	}
	switch mode {
	case assurance.RelativeRequired:
		var baselineReport assurance.Report
		var baseline assurance.Baseline
		if runnerDigest == "" || loadBoundedJSON(baselineReportPath, &baselineReport) != nil ||
			loadBoundedJSON(baselineEvidencePath, &baseline) != nil ||
			assurance.ComparePinnedRelative(report, baselineReport, baseline, runnerDigest, maximumRegression) != nil {
			return writeFailure(stderr, "GDS_ASSURANCE_RELATIVE_BASELINE_INVALID", "Relative-required mode needs immutable comparable baseline evidence.", 2)
		}
	case assurance.AbsoluteCalibrated:
		var policy assurance.CalibratedPolicy
		if runnerDigest == "" || loadBoundedJSON(calibratedPolicyPath, &policy) != nil ||
			assurance.EvaluateCalibrated(report, policy, runnerDigest) != nil {
			return writeFailure(stderr, "GDS_ASSURANCE_CALIBRATION_INVALID", "Absolute-calibrated mode needs comparable variance-backed policy evidence.", 2)
		}
	}
	return 0
}

func loadBoundedJSON(path string, target any) error {
	if path == "" {
		return fmt.Errorf("evidence path is required")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 2 || info.Size() > 8<<20 {
		return fmt.Errorf("evidence must be a bounded regular file")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("evidence contains trailing JSON")
	}
	return nil
}

func writeExclusiveReport(path string, report assurance.Report) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return err
	}
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		return fmt.Errorf("report parent is not a directory")
	}
	file, err := os.OpenFile(
		filepath.Join(parent, filepath.Base(absolute)),
		os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600,
	)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	writeErr := encoder.Encode(report)
	closeErr := file.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func writeFailure(writer io.Writer, code, message string, exit int) int {
	return writeFailureDetail(writer, code, message, nil, exit)
}

func writeFailureDetail(writer io.Writer, code, message string, detail error, exit int) int {
	payload := map[string]any{
		"schema_version": domain.SchemaVersion,
		"result":         "failed", "code": code, "message": message, "exit_code": exit,
	}
	if detail != nil {
		payload["detail"] = detail.Error()
	}
	_ = writeJSON(writer, payload)
	return exit
}
