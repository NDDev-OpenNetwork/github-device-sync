// Command gds-performance-evidence creates immutable relative baselines and
// variance-backed absolute policies from existing clean assurance reports.
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/assurance"
)

type paths []string

func (value *paths) String() string { return fmt.Sprint([]string(*value)) }
func (value *paths) Set(item string) error {
	if item == "" {
		return errors.New("empty report path")
	}
	*value = append(*value, item)
	return nil
}

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(arguments []string, stdout, stderr io.Writer) int {
	if len(arguments) == 0 {
		fmt.Fprintln(stderr, "mode baseline|calibrate is required")
		return 4
	}
	flags := flag.NewFlagSet(arguments[0], flag.ContinueOnError)
	flags.SetOutput(stderr)
	runner, output, id := "", "", ""
	var reports paths
	flags.StringVar(&runner, "runner-digest", "", "exact runner sha256 digest")
	flags.StringVar(&output, "output", "", "new mode-0600 evidence path")
	flags.StringVar(&id, "id", "", "stable baseline or policy identity")
	flags.Var(&reports, "report", "bounded assurance report JSON; repeat for calibration")
	if err := flags.Parse(arguments[1:]); err != nil || flags.NArg() != 0 || runner == "" || output == "" || id == "" {
		return 4
	}
	loaded := make([]assurance.Report, 0, len(reports))
	for _, path := range reports {
		var report assurance.Report
		if readBounded(path, &report) != nil {
			return 2
		}
		loaded = append(loaded, report)
	}
	var result any
	var err error
	switch arguments[0] {
	case "baseline":
		if len(loaded) != 1 {
			return 4
		}
		result, err = assurance.NewBaseline(id, runner, time.Now().UTC(), loaded[0])
	case "calibrate":
		result, err = assurance.Calibrate(loaded, id, runner, time.Now().UTC())
	default:
		return 4
	}
	if err != nil || writeExclusive(output, result) != nil {
		return 2
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(result)
	return 0
}

func readBounded(path string, target any) error {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 2 || info.Size() > 8<<20 {
		return errors.New("report is not a bounded regular file")
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
		return errors.New("trailing report JSON")
	}
	return nil
}

func writeExclusive(path string, value any) error {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil {
		return err
	}
	file, err := os.OpenFile(filepath.Join(parent, filepath.Base(absolute)), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(file)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	writeErr := encoder.Encode(value)
	closeErr := file.Close()
	return errors.Join(writeErr, closeErr)
}
