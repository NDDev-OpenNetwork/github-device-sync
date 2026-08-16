package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/harness"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

const maximumRequestBytes = 1 << 20

func main() {
	if len(os.Args) != 1 {
		fmt.Fprintln(os.Stderr, "gds-codex-runtime-driver accepts one JSON request on stdin and no arguments")
		os.Exit(64)
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, maximumRequestBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > maximumRequestBytes {
		fmt.Fprintln(os.Stderr, "runtime driver request is missing or exceeds 1 MiB")
		os.Exit(4)
	}
	request, err := decodeRuntimeDriverRequest(raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, "runtime driver request is invalid:", err)
		os.Exit(4)
	}
	schemas, err := validation.NewSchemaSet()
	if err != nil {
		fmt.Fprintln(os.Stderr, "load GDS schemas:", err)
		os.Exit(14)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	evidence, err := harness.RunCodexEvidenceDriver(
		ctx, request, schemas, harness.CodexEvidenceDriverOptions{Concurrency: 2},
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, "Codex runtime evidence failed:", err)
		os.Exit(2)
	}
	encoded, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "encode Codex runtime evidence:", err)
		os.Exit(14)
	}
	encoded = append(encoded, '\n')
	if _, err := io.Copy(os.Stdout, bytes.NewReader(encoded)); err != nil {
		fmt.Fprintln(os.Stderr, "write Codex runtime evidence:", err)
		os.Exit(14)
	}
}

func decodeRuntimeDriverRequest(raw []byte) (harness.RuntimeDriverRequest, error) {
	var request harness.RuntimeDriverRequest
	if err := serialization.DecodeInto("stdin.json", raw, &request); err != nil {
		return harness.RuntimeDriverRequest{}, err
	}
	return request, nil
}
