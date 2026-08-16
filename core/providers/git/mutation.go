package git

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/gitauthority"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/redaction"
)

const recoveryRefPrefix = "refs/gds/recovery/"

type MutationRunner struct {
	authority    *gitauthority.Authority
	authorityErr error
	maxOutput    int
}

func NewMutationRunner() (*MutationRunner, error) {
	authority, err := gitauthority.Discover()
	if err != nil {
		return nil, fmt.Errorf("locate git executable: %w", err)
	}
	return &MutationRunner{authority: authority, maxOutput: defaultOutputLimit}, nil
}

func NewMutationRunnerForPath(path string, maxOutput int) *MutationRunner {
	if maxOutput <= 0 {
		maxOutput = defaultOutputLimit
	}
	authority, err := gitauthority.Open(path)
	return &MutationRunner{authority: authority, authorityErr: err, maxOutput: maxOutput}
}

func (runner *MutationRunner) ObserveRecoveryRef(
	ctx context.Context,
	directory string,
	reference string,
) (string, error) {
	if err := validateRecoveryRef(reference); err != nil {
		return "", err
	}
	root, err := validateMutationRoot(directory)
	if err != nil {
		return "", err
	}
	result, err := runner.run(
		ctx, root, map[int]struct{}{0: {}, 1: {}},
		"rev-parse", "--verify", "--quiet", reference,
	)
	if err != nil {
		return "", err
	}
	if result.ExitCode == 1 {
		return zeroOID(40), nil
	}
	oid := strings.TrimSpace(string(result.Stdout))
	if err := validateOID(oid, false); err != nil {
		return "", fmt.Errorf("observe recovery ref %s: %w", reference, err)
	}
	return oid, nil
}

func (runner *MutationRunner) UpdateRecoveryRef(
	ctx context.Context,
	directory string,
	reference string,
	newOID string,
	expectedOldOID string,
) error {
	if err := validateRecoveryRef(reference); err != nil {
		return err
	}
	if err := validateOID(newOID, false); err != nil {
		return fmt.Errorf("invalid new recovery ref OID: %w", err)
	}
	if err := validateOID(expectedOldOID, true); err != nil {
		return fmt.Errorf("invalid expected recovery ref OID: %w", err)
	}
	if len(newOID) != len(expectedOldOID) {
		return errors.New("new and expected recovery ref OIDs use different hash formats")
	}
	root, err := validateMutationRoot(directory)
	if err != nil {
		return err
	}
	_, err = runner.run(
		ctx, root, map[int]struct{}{0: {}},
		"update-ref", "--no-deref", "-m", "gds recovery reference",
		reference, newOID, expectedOldOID,
	)
	return err
}

func (runner *MutationRunner) run(
	ctx context.Context,
	directory string,
	allowedExitCodes map[int]struct{},
	args ...string,
) (CommandResult, error) {
	return runner.runWithEnvironment(ctx, directory, allowedExitCodes, nil, args...)
}

func (runner *MutationRunner) runWithEnvironment(
	ctx context.Context,
	directory string,
	allowedExitCodes map[int]struct{},
	extraEnvironment []string,
	args ...string,
) (CommandResult, error) {
	if runner == nil || runner.authority == nil {
		if runner != nil && runner.authorityErr != nil {
			return CommandResult{}, fmt.Errorf("git mutation executable is unavailable: %w", runner.authorityErr)
		}
		return CommandResult{}, errors.New("git mutation executable is unavailable")
	}
	stdout := newLimitedBuffer(runner.maxOutput)
	stderr := newLimitedBuffer(runner.maxOutput)
	run, err := runner.authority.Run(ctx, gitauthority.RunRequest{
		Directory: directory, Arguments: args, ExtraEnvironment: extraEnvironment,
		Stdout: stdout, Stderr: stderr, Configure: configureCancellation,
	})
	exitCode := run.ExitCode
	result := CommandResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), ExitCode: exitCode}
	if stdout.Truncated() || stderr.Truncated() {
		return result, fmt.Errorf("git mutation output exceeded %d bytes", runner.maxOutput)
	}
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return result, fmt.Errorf("git mutation %s: %w", args[0], ctxErr)
		}
		if _, allowed := allowedExitCodes[exitCode]; allowed {
			return result, nil
		}
		message := redaction.String(strings.TrimSpace(string(result.Stderr)))
		if message == "" {
			message = "command failed"
		}
		return result, fmt.Errorf("git mutation %s failed: %s", args[0], message)
	}
	return result, nil
}

func (runner *MutationRunner) readRunner() *Runner {
	if runner == nil {
		return &Runner{authorityErr: errors.New("git mutation executable is unavailable"), maxOutput: defaultOutputLimit}
	}
	return &Runner{
		authority: runner.authority, authorityErr: runner.authorityErr, maxOutput: runner.maxOutput,
	}
}

func validateMutationRoot(directory string) (string, error) {
	absolute, err := filepath.Abs(directory)
	if err != nil {
		return "", fmt.Errorf("resolve Git mutation root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve Git mutation root components: %w", err)
	}
	absolute = filepath.Clean(resolved)
	info, err := os.Lstat(absolute)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("Git mutation root must be a real directory: %s", absolute)
	}
	return absolute, nil
}

func validateRecoveryRef(reference string) error {
	if !strings.HasPrefix(reference, recoveryRefPrefix) {
		return fmt.Errorf("Git ref %q is outside %s", reference, recoveryRefPrefix)
	}
	remainder := strings.TrimPrefix(reference, recoveryRefPrefix)
	if remainder == "" || strings.HasPrefix(remainder, "/") || strings.HasSuffix(remainder, "/") ||
		strings.Contains(remainder, "//") || strings.Contains(remainder, "..") ||
		strings.Contains(remainder, "@{") || strings.ContainsAny(remainder, " ~^:?*[\\") ||
		strings.HasSuffix(remainder, ".") {
		return fmt.Errorf("Git recovery ref %q is invalid", reference)
	}
	for _, component := range strings.Split(remainder, "/") {
		if component == "" || strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return fmt.Errorf("Git recovery ref %q is invalid", reference)
		}
	}
	for _, character := range reference {
		if character < 0x21 || character == 0x7f {
			return fmt.Errorf("Git recovery ref %q contains a control character", reference)
		}
	}
	return nil
}

func validateOID(value string, allowZero bool) error {
	if len(value) != 40 && len(value) != 64 {
		return fmt.Errorf("object id has %d characters", len(value))
	}
	allZero := true
	for _, character := range value {
		if character != '0' {
			allZero = false
		}
		if !((character >= '0' && character <= '9') || (character >= 'a' && character <= 'f')) {
			return errors.New("object id is not lowercase hexadecimal")
		}
	}
	if allZero && !allowZero {
		return errors.New("zero object id is not allowed")
	}
	return nil
}

func zeroOID(length int) string { return strings.Repeat("0", length) }
