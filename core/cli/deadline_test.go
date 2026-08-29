package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
)

// A deadline is not a changed repository. `module update-pin --apply` used to
// report GDS_STALE_PLAN when its own context expired mid-observation, which
// sends the reader looking for a concurrent writer that does not exist.
func TestExpiredDeadlineIsNamedAsADeadline(t *testing.T) {
	exitCode, envelope, _ := executeJSON(t, "--json", "--timeout", "1ns", "doctor")
	if exitCode == 0 {
		t.Fatalf("an expired deadline must not succeed: %#v", envelope)
	}
	if !containsFinding(envelope.Findings, "GDS_COMMAND_DEADLINE_EXCEEDED") {
		t.Fatalf("expired deadline was not named as one: %#v", envelope.Findings)
	}
	for _, finding := range envelope.Findings {
		if finding.Code != "GDS_COMMAND_DEADLINE_EXCEEDED" {
			continue
		}
		if !strings.Contains(finding.Message, "not a changed repository") {
			t.Fatalf("deadline finding does not say what it is not: %q", finding.Message)
		}
		if finding.Evidence["flag"] != "--timeout" {
			t.Fatalf("deadline finding does not name the flag: %#v", finding.Evidence)
		}
	}
}

// A successful command must not collect the finding just because it finished
// near its deadline.
func TestSuccessfulCommandIsNotLabelledADeadline(t *testing.T) {
	_, envelope, _ := executeJSON(t, "--json", "identity", "new", "device")
	if containsFinding(envelope.Findings, "GDS_COMMAND_DEADLINE_EXCEEDED") {
		t.Fatalf("a successful command must not report a deadline: %#v", envelope.Findings)
	}
}

func deadlineFor(t *testing.T, lanes bool, flagSet bool) time.Duration {
	t.Helper()
	runner := &executor{options: options{cwd: ".", timeout: 2 * time.Minute}}
	command := &cobra.Command{Use: "probe"}
	command.Flags().DurationVar(&runner.options.timeout, "timeout", 2*time.Minute, "command deadline")
	if flagSet {
		if err := command.Flags().Set("timeout", "90s"); err != nil {
			t.Fatal(err)
		}
	}
	command.SetContext(context.Background())
	var observed time.Duration
	operation := func(ctx context.Context) domain.Envelope {
		if deadline, ok := ctx.Deadline(); ok {
			observed = time.Until(deadline).Round(time.Second)
		}
		return domain.Success("probe", nil)
	}
	var err error
	if lanes {
		err = runner.runLanes(command, operation)
	} else {
		err = runner.run(command, operation)
	}
	if err != nil {
		t.Fatal(err)
	}
	return observed
}

// Commands that execute another repository's verification lanes need more than
// the deadline sized for a read -- update-pin runs them twice, once to plan and
// once to re-observe before mutating -- but an explicit --timeout always wins.
func TestLaneCommandsGetALongerDefaultUnlessTheCallerChose(t *testing.T) {
	if got := deadlineFor(t, false, false); got > 3*time.Minute {
		t.Fatalf("ordinary command deadline = %s, want the 2m read default", got)
	}
	if got := deadlineFor(t, true, false); got < 15*time.Minute {
		t.Fatalf("lane command deadline = %s, want the longer default", got)
	}
	if got := deadlineFor(t, true, true); got > 2*time.Minute {
		t.Fatalf("explicit --timeout was overridden: %s", got)
	}
}
