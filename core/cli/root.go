// Package cli provides the Cobra adapter and owns process-facing rendering.
package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/app"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/capabilities"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/harness"
)

var Version = "0.7.0"

type options struct {
	json    bool
	cwd     string
	timeout time.Duration
}

type executor struct {
	services *app.Services
	options  options
	result   *domain.Envelope
	stdin    io.Reader
	stdout   io.Writer
	stderr   io.Writer
}

func Execute(
	ctx context.Context,
	args []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
) (exitCode int) {
	wantsJSON := containsJSONFlag(args)
	defer func() {
		if recovered := recover(); recovered != nil {
			envelope := domain.InternalError("gds", fmt.Errorf("panic: %v", recovered))
			_ = render(envelope, wantsJSON, stdout, stderr)
			exitCode = envelope.ExitCode
		}
	}()
	services, err := app.NewServices(app.DefaultClock)
	if err != nil {
		envelope := domain.InternalError("gds", err)
		_ = render(envelope, wantsJSON, stdout, stderr)
		return envelope.ExitCode
	}
	return executeWithServices(ctx, args, stdin, stdout, stderr, services)
}

// executeWithServices is the single dispatch path after dependency assembly.
// Production always enters through Execute; tests may provide bounded fake
// provider clients without reading a developer's private runtime configuration.
func executeWithServices(
	ctx context.Context,
	args []string,
	stdin io.Reader,
	stdout io.Writer,
	stderr io.Writer,
	services *app.Services,
) (exitCode int) {
	wantsJSON := containsJSONFlag(args)
	defer func() {
		if recovered := recover(); recovered != nil {
			envelope := domain.InternalError(
				"gds", fmt.Errorf("panic: %v", recovered),
			)
			_ = render(envelope, wantsJSON, stdout, stderr)
			exitCode = envelope.ExitCode
		}
	}()

	runner := &executor{
		services: services, stdin: stdin, stdout: stdout, stderr: stderr,
		options: options{cwd: ".", timeout: 2 * time.Minute},
	}
	root := runner.rootCommand()
	root.SetArgs(args)
	root.SetIn(stdin)
	root.SetOut(stdout)
	root.SetErr(stderr)
	root.SetContext(ctx)
	if err := root.ExecuteContext(ctx); err != nil {
		envelope := domain.NewEnvelope("gds", domain.ExitInput, nil, domain.Finding{
			Code:     "GDS_CLI_INPUT_INVALID",
			Severity: domain.SeverityHigh,
			Message:  err.Error(),
		})
		if renderErr := render(envelope, runner.options.json || wantsJSON, stdout, stderr); renderErr != nil {
			return domain.InternalError("gds", renderErr).ExitCode
		}
		return envelope.ExitCode
	}
	if runner.result == nil {
		return 0 // Cobra rendered explicit help or version output.
	}
	if err := render(*runner.result, runner.options.json, stdout, stderr); err != nil {
		envelope := domain.InternalError("gds", err)
		_ = render(envelope, runner.options.json, stdout, stderr)
		return envelope.ExitCode
	}
	return runner.result.ExitCode
}

func (executor *executor) rootCommand() *cobra.Command {
	root := &cobra.Command{
		Use:           "gds",
		Short:         "Agent-first repository estate control plane",
		Version:       Version,
		SilenceErrors: true,
		SilenceUsage:  true,
		Args:          cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return command.Help()
		},
	}
	root.CompletionOptions.DisableDefaultCmd = true
	root.PersistentFlags().BoolVar(
		&executor.options.json, "json", false, "emit a versioned JSON result envelope",
	)
	root.PersistentFlags().StringVar(
		&executor.options.cwd, "cwd", ".", "directory used to resolve the current scope",
	)
	root.PersistentFlags().DurationVar(
		&executor.options.timeout, "timeout", 2*time.Minute, "command deadline",
	)
	commands := map[string]*cobra.Command{
		"context": executor.contextCommand(), "session": executor.sessionCommand(),
		"sync": executor.syncCommand(), "handoff": executor.handoffCommand(),
		"complete": executor.completeCommand(), "status": executor.statusCommand(),
		"discover": executor.discoverCommand(), "inventory": executor.inventoryCommand(),
		"validate": executor.validateCommand(), "doctor": executor.doctorCommand(),
		"compile": executor.compileCommand(), "generate": executor.generateCommand(),
		"skill": executor.skillCommand(), "harness": executor.harnessCommand(),
		"state": executor.stateCommand(), "operation": executor.operationCommand(),
		"recover": executor.recoverCommand(), "git": executor.gitCommand(),
		"repository": executor.repositoryCommand(), "workspace": executor.workspaceCommand(),
		"module": executor.moduleCommand(), "fork": executor.forkCommand(),
		"portfolio": executor.portfolioCommand(), "identity": executor.identityCommand(),
		"github": executor.githubCommand(), "reconcile": executor.reconcileCommand(),
		"report": executor.reportCommand(), "release": executor.releaseCommand(),
		"rollout": executor.rolloutCommand(), "source": executor.sourceCommand(),
		"memory": executor.memoryCommand(),
	}
	registered := make([]string, 0, len(commands))
	for _, name := range capabilities.RootCommandNames() {
		command, found := commands[name]
		if !found || command.Name() != name {
			panic(fmt.Sprintf("capability registry command %q is not constructed", name))
		}
		root.AddCommand(command)
		registered = append(registered, name)
		delete(commands, name)
	}
	if len(commands) != 0 {
		panic(fmt.Sprintf("root commands are absent from the capability registry: %v", commands))
	}
	if err := capabilities.ValidateRootCommands(registered); err != nil {
		panic(err)
	}
	return root
}

func (executor *executor) portfolioCommand() *cobra.Command {
	command := &cobra.Command{
		Use: "portfolio", Short: "Plan bounded changes across independent repository boundaries",
		Args: cobra.NoArgs,
	}
	command.AddCommand(executor.portfolioPlanCommand())
	return command
}

func (executor *executor) portfolioPlanCommand() *cobra.Command {
	options := app.PortfolioPlanOptions{MaxDepth: 8, MaxRepositories: 2000, Concurrency: 4}
	command := &cobra.Command{
		Use: "plan", Short: "Build one aggregate portfolio plan with independent repository subplans",
		Args: cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				return executor.services.PlanPortfolioChange(ctx, options)
			})
		},
	}
	command.Flags().StringVar(&options.Portfolio, "portfolio", "", "exact portfolio reference")
	command.Flags().StringVar(
		&options.Operation, "operation", "", "repository-change, projection-rollout, or policy-rollout",
	)
	command.Flags().StringVar(&options.Intent, "intent", "", "bounded single-line logical change intent")
	command.Flags().StringVar(&options.InventoryRoot, "inventory-root", "", "complete local repository analysis root")
	command.Flags().IntVar(&options.MaxDepth, "max-depth", 8, "maximum inventory directory depth")
	command.Flags().IntVar(&options.MaxRepositories, "max-repositories", 2000, "hard inventory repository count limit")
	command.Flags().IntVar(&options.Concurrency, "concurrency", 4, "bounded inventory inspection workers")
	return command
}

func (executor *executor) completeCommand() *cobra.Command {
	plan := false
	applyPlanID := ""
	verifyOperationID := ""
	options := app.CompleteOptions{}
	command := &cobra.Command{
		Use:   "complete",
		Short: "Integrate, publish, and safely clean one explicit completed task",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				if selectedModes(plan, applyPlanID, verifyOperationID) != 1 {
					return domain.NewEnvelope("gds complete", domain.ExitInput, nil, domain.Finding{
						Code: "GDS_COMPLETE_MODE_REQUIRED", Severity: domain.SeverityHigh,
						Message: "Use exactly one of --plan, --apply, or --verify.",
					})
				}
				if !plan {
					for _, name := range []string{"task-id", "checkout", "refresh-max-age"} {
						if child.Flags().Changed(name) {
							return domain.NewEnvelope("gds complete", domain.ExitInput, nil, domain.Finding{
								Code: "GDS_COMPLETE_PLAN_INPUT_CONFLICT", Severity: domain.SeverityHigh,
								Message: "Task, checkout, and refresh inputs cannot alter a stored completion plan.",
							})
						}
					}
				}
				switch {
				case plan:
					return executor.services.PlanComplete(ctx, executor.options.cwd, options)
				case applyPlanID != "":
					return executor.services.ApplyComplete(ctx, applyPlanID, options)
				default:
					return executor.services.VerifyComplete(ctx, verifyOperationID, options)
				}
			})
		},
	}
	command.Flags().BoolVar(&plan, "plan", false, "store an exact side-effect-free completion plan")
	command.Flags().StringVar(&applyPlanID, "apply", "", "apply one exact stored completion plan id")
	command.Flags().StringVar(&verifyOperationID, "verify", "", "verify one exact completion operation id")
	command.Flags().StringVar(&options.TaskID, "task-id", "", "canonical task identity for the completion saga")
	command.Flags().StringSliceVar(&options.Checkouts, "checkout", nil, "explicit affected checkout path; repeat for the bounded graph")
	command.Flags().DurationVar(&options.RefreshMaxAge, "refresh-max-age", 0, "maximum accepted refresh age; default 15m, maximum 1h")
	command.Flags().StringVar(&options.StatePath, "state-path", "", "local GDS state database path")
	command.Flags().StringVar(&options.DeviceID, "device-id", "", "canonical current device identity")
	command.Flags().StringVar(&options.SessionID, "session-id", "", "bounded non-secret session identity")
	command.Flags().StringVar(&options.ApprovalReference, "approval-ref", "", "signed exact-plan approval JSON file")
	return command
}

func (executor *executor) handoffCommand() *cobra.Command {
	plan := false
	applyPlanID := ""
	verifyOperationID := ""
	options := app.HandoffOptions{}
	command := &cobra.Command{
		Use:   "handoff",
		Short: "Checkpoint and publish unfinished work without integration or cleanup",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				if selectedModes(plan, applyPlanID, verifyOperationID) != 1 {
					return domain.NewEnvelope("gds handoff", domain.ExitInput, nil, domain.Finding{
						Code: "GDS_HANDOFF_MODE_REQUIRED", Severity: domain.SeverityHigh,
						Message: "Use exactly one of --plan, --apply, or --verify.",
					})
				}
				planningInputs := []string{"file", "message", "author-name", "author-email", "refresh-max-age"}
				if !plan {
					for _, name := range planningInputs {
						if child.Flags().Changed(name) {
							return domain.NewEnvelope("gds handoff", domain.ExitInput, nil, domain.Finding{
								Code: "GDS_HANDOFF_PLAN_INPUT_CONFLICT", Severity: domain.SeverityHigh,
								Message: "File, message, author, and refresh inputs cannot alter a stored handoff plan.",
							})
						}
					}
				}
				switch {
				case plan:
					return executor.services.PlanHandoff(ctx, executor.options.cwd, options)
				case applyPlanID != "":
					return executor.services.ApplyHandoff(ctx, applyPlanID, options)
				default:
					return executor.services.VerifyHandoff(ctx, verifyOperationID, options)
				}
			})
		},
	}
	command.Flags().BoolVar(&plan, "plan", false, "store an exact side-effect-free handoff plan")
	command.Flags().StringVar(&applyPlanID, "apply", "", "apply one exact stored handoff plan id")
	command.Flags().StringVar(&verifyOperationID, "verify", "", "verify one exact handoff operation id")
	command.Flags().StringSliceVar(&options.Files, "file", nil, "explicit repository-relative file; repeat for the approved set")
	command.Flags().StringVar(&options.Message, "message", "", "exact checkpoint commit message")
	command.Flags().StringVar(&options.AuthorName, "author-name", "", "exact non-secret commit author name")
	command.Flags().StringVar(&options.AuthorEmail, "author-email", "", "exact non-secret commit author email")
	command.Flags().DurationVar(&options.RefreshMaxAge, "refresh-max-age", 0, "maximum accepted refresh age; default 15m, maximum 1h")
	command.Flags().StringVar(&options.StatePath, "state-path", "", "local GDS state database path")
	command.Flags().StringVar(&options.DeviceID, "device-id", "", "canonical current device identity")
	command.Flags().StringVar(&options.SessionID, "session-id", "", "bounded non-secret session identity")
	command.Flags().StringVar(&options.ApprovalReference, "approval-ref", "", "signed exact-plan approval JSON file")
	return command
}

func (executor *executor) syncCommand() *cobra.Command {
	plan := false
	applyPlanID := ""
	verifyOperationID := ""
	options := app.SyncCheckoutOptions{}
	command := &cobra.Command{
		Use:   "sync",
		Short: "Plan and apply bounded synchronization without pull or implicit cleanup",
		Args:  cobra.NoArgs,
	}
	checkouts := &cobra.Command{
		Use:   "checkouts",
		Short: "Fast-forward explicitly selected clean checkouts from durable refresh evidence",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				if selectedModes(plan, applyPlanID, verifyOperationID) != 1 {
					return domain.NewEnvelope("gds sync checkouts", domain.ExitInput, nil, domain.Finding{
						Code: "GDS_SYNC_MODE_REQUIRED", Severity: domain.SeverityHigh,
						Message: "Use exactly one of --plan, --apply, or --verify.",
					})
				}
				if !plan && (child.Flags().Changed("checkout") || child.Flags().Changed("refresh-max-age")) {
					return domain.NewEnvelope("gds sync checkouts", domain.ExitInput, nil, domain.Finding{
						Code: "GDS_SYNC_PLAN_INPUT_CONFLICT", Severity: domain.SeverityHigh,
						Message: "--checkout and --refresh-max-age are planning inputs and cannot alter a stored plan.",
					})
				}
				switch {
				case plan:
					return executor.services.PlanSyncCheckouts(ctx, executor.options.cwd, options)
				case applyPlanID != "":
					return executor.services.ApplySyncCheckouts(ctx, applyPlanID, options)
				default:
					return executor.services.VerifySyncCheckouts(ctx, verifyOperationID, options)
				}
			})
		},
	}
	checkouts.Flags().BoolVar(&plan, "plan", false, "store an exact side-effect-free synchronization plan")
	checkouts.Flags().StringVar(&applyPlanID, "apply", "", "apply one exact stored synchronization plan id")
	checkouts.Flags().StringVar(&verifyOperationID, "verify", "", "verify one exact synchronization operation id")
	checkouts.Flags().StringSliceVar(&options.Checkouts, "checkout", nil, "explicit checkout path; repeat for a bounded set")
	checkouts.Flags().DurationVar(&options.RefreshMaxAge, "refresh-max-age", 0, "maximum accepted refresh age for planning; default 15m, maximum 1h")
	checkouts.Flags().StringVar(&options.StatePath, "state-path", "", "local GDS state database path")
	checkouts.Flags().StringVar(&options.DeviceID, "device-id", "", "canonical current device identity")
	checkouts.Flags().StringVar(&options.SessionID, "session-id", "", "bounded non-secret session identity")
	checkouts.Flags().StringVar(&options.ApprovalReference, "approval-ref", "", "signed exact-plan approval JSON file")
	command.AddCommand(checkouts)
	return command
}

func (executor *executor) sessionCommand() *cobra.Command {
	options := app.SessionStartOptions{Scope: "current", Refresh: "none"}
	command := &cobra.Command{
		Use:   "session",
		Short: "Resolve and classify session boundaries without integrating work",
		Args:  cobra.NoArgs,
	}
	start := &cobra.Command{
		Use:   "start",
		Short: "Classify relevant Git boundaries and optionally refresh origin refs",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				return executor.services.StartSession(ctx, executor.options.cwd, options)
			})
		},
	}
	start.Flags().StringVar(&options.Scope, "scope", "current", "session scope; current only")
	start.Flags().StringVar(&options.Refresh, "refresh", "none", "non-integrating remote refresh: none or origin")
	start.Flags().StringVar(&options.StatePath, "state-path", "", "local GDS state database path for durable refresh evidence")
	command.AddCommand(start)
	return command
}

func (executor *executor) recoverCommand() *cobra.Command {
	plan := false
	applyPlanID := ""
	verifyOperationID := ""
	options := app.RecoveryOperationOptions{}
	command := &cobra.Command{
		Use:   "recover",
		Short: "Plan and execute conservative recovery of interrupted operations",
		Args:  cobra.NoArgs,
	}
	operation := &cobra.Command{
		Use:   "operation <operation-id>",
		Short: "Plan, apply, or verify one exact journal recovery",
		Args:  cobra.ExactArgs(1),
		RunE: func(child *cobra.Command, arguments []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				selected := selectedModes(plan, applyPlanID, verifyOperationID)
				if selected != 1 {
					return domain.NewEnvelope(
						"gds recover operation", domain.ExitInput, nil, domain.Finding{
							Code: "GDS_RECOVERY_MODE_REQUIRED", Severity: domain.SeverityHigh,
							Message: "Use exactly one of --plan, --apply, or --verify.",
						},
					)
				}
				switch {
				case plan:
					return executor.services.PlanOperationRecovery(
						ctx, executor.options.cwd, arguments[0], options,
					)
				case applyPlanID != "":
					return executor.services.ApplyOperationRecovery(
						ctx, executor.options.cwd, applyPlanID, options,
					)
				default:
					return executor.services.VerifyOperationRecovery(
						ctx, executor.options.cwd, verifyOperationID, options,
					)
				}
			})
		},
	}
	operation.Flags().BoolVar(&plan, "plan", false, "store an exact recovery plan or report manual blockers")
	operation.Flags().StringVar(&applyPlanID, "apply", "", "apply one exact stored recovery plan id")
	operation.Flags().StringVar(&verifyOperationID, "verify", "", "verify one exact recovery operation id")
	operation.Flags().StringVar(&options.StatePath, "state-path", "", "local GDS state database path")
	operation.Flags().StringVar(&options.DeviceID, "device-id", "", "canonical current device identity")
	operation.Flags().StringVar(&options.SessionID, "session-id", "", "bounded non-secret session identity")
	operation.Flags().StringVar(&options.ApprovalReference, "approval-ref", "", "signed exact-plan approval JSON file")
	command.AddCommand(operation)
	return command
}

func (executor *executor) operationCommand() *cobra.Command {
	statePath := ""
	command := &cobra.Command{
		Use:   "operation",
		Short: "Inspect durable operation plans, steps, events, locks, and recovery state",
		Args:  cobra.NoArgs,
	}
	inspect := &cobra.Command{
		Use:   "inspect <operation-id>",
		Short: "Verify one durable operation journal without mutating it",
		Args:  cobra.ExactArgs(1),
		RunE: func(child *cobra.Command, arguments []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				return executor.services.InspectOperation(ctx, arguments[0], statePath)
			})
		},
	}
	inspect.Flags().StringVar(
		&statePath, "state-path", "", "local GDS state database path",
	)
	approveOptions := app.ApprovalIssueOptions{ActorType: "owner", TTL: 15 * time.Minute}
	approve := &cobra.Command{
		Use: "approve <plan-id>", Short: "Issue one signed exact-plan approval artifact",
		Args: cobra.ExactArgs(1),
		RunE: func(child *cobra.Command, arguments []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				return executor.services.IssuePlanApproval(ctx, arguments[0], approveOptions)
			})
		},
	}
	approve.Flags().StringVar(&approveOptions.StatePath, "state-path", "", "local GDS state database containing the exact plan")
	approve.Flags().StringVar(&approveOptions.PrivateKeyPath, "private-key", "", "private PKCS#8 Ed25519 PEM key file (mode 0600)")
	approve.Flags().StringVar(&approveOptions.OutputPath, "output", "", "new mode-0600 approval JSON output path")
	approve.Flags().StringVar(&approveOptions.ActorID, "actor-id", "", "canonical approving actor identity")
	approve.Flags().StringVar(&approveOptions.ActorType, "actor-type", "owner", "owner, delegate, or automation")
	approve.Flags().StringVar(&approveOptions.KeyID, "key-id", "", "public trust-policy key identity")
	approve.Flags().StringVar(&approveOptions.ExternalReference, "external-reference", "", "optional non-secret issue, PR, or ticket metadata")
	approve.Flags().DurationVar(&approveOptions.TTL, "ttl", 15*time.Minute, "approval lifetime (maximum 24h)")
	enableOptions := app.PlanEnableOptions{}
	enable := &cobra.Command{
		Use: "enable <plan-id>", Short: "Create one transparent exact-plan local mutation enablement",
		Args: cobra.ExactArgs(1),
		RunE: func(child *cobra.Command, arguments []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				return executor.services.EnablePlan(ctx, arguments[0], enableOptions)
			})
		},
	}
	enable.Flags().StringVar(&enableOptions.StatePath, "state-path", "", "local GDS state database containing the exact plan")
	enable.Flags().StringVar(&enableOptions.ApprovalPath, "approval-file", "", "signed exact-plan approval JSON file")
	enable.Flags().StringVar(&enableOptions.DeviceID, "device-id", "", "canonical current device identity")
	enable.Flags().StringVar(&enableOptions.SessionID, "session-id", "", "bounded non-secret session identity")
	command.AddCommand(inspect, approve, enable)
	return command
}

func (executor *executor) memoryCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "memory",
		Short: "Read, generate, and validate provenance-bearing Serena memories",
		Args:  cobra.NoArgs,
	}
	command.AddCommand(
		&cobra.Command{
			Use:   "read <name>",
			Short: "Read one memory and verify its current committed provenance",
			Args:  cobra.ExactArgs(1),
			RunE: func(child *cobra.Command, arguments []string) error {
				return executor.run(child, func(ctx context.Context) domain.Envelope {
					return executor.services.ReadMemory(ctx, executor.options.cwd, arguments[0])
				})
			},
		},
		&cobra.Command{
			Use:   "verify <name>",
			Short: "Record that a memory was read against its current sources",
			Args:  cobra.ExactArgs(1),
			RunE: func(child *cobra.Command, arguments []string) error {
				return executor.run(child, func(ctx context.Context) domain.Envelope {
					return executor.services.VerifyMemory(ctx, executor.options.cwd, arguments[0])
				})
			},
		},
		&cobra.Command{
			Use:   "generate <name>",
			Short: "Build a deterministic memory candidate without writing it",
			Args:  cobra.ExactArgs(1),
			RunE: func(child *cobra.Command, arguments []string) error {
				return executor.run(child, func(ctx context.Context) domain.Envelope {
					return executor.services.GenerateMemory(ctx, executor.options.cwd, arguments[0])
				})
			},
		},
		&cobra.Command{
			Use:   "validate",
			Short: "Validate all memory metadata, body contracts, and source digests",
			Args:  cobra.NoArgs,
			RunE: func(child *cobra.Command, _ []string) error {
				return executor.run(child, func(ctx context.Context) domain.Envelope {
					return executor.services.ValidateMemoryCommand(ctx, executor.options.cwd)
				})
			},
		},
	)
	return command
}

func (executor *executor) sourceCommand() *cobra.Command {
	asOf := ""
	sourceID := ""
	markPlan := false
	markApply := ""
	markVerify := ""
	markOptions := app.SourceVerificationOperationOptions{}
	command := &cobra.Command{
		Use:   "source",
		Short: "Inspect volatile source freshness without changing evidence",
		Args:  cobra.NoArgs,
	}
	status := &cobra.Command{
		Use:   "status",
		Short: "Classify source evidence, review dates, and content digests",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				return executor.services.SourceStatus(ctx, executor.options.cwd, asOf)
			})
		},
	}
	status.Flags().StringVar(&asOf, "as-of", "", "evaluation date in YYYY-MM-DD")
	check := &cobra.Command{
		Use:   "check",
		Short: "Fetch one registered official source and compare its pinned digest",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				return executor.services.SourceCheck(ctx, executor.options.cwd, sourceID)
			})
		},
	}
	check.Flags().StringVar(&sourceID, "id", "", "registered source id")
	_ = check.MarkFlagRequired("id")
	mark := &cobra.Command{
		Use:   "mark-verified",
		Short: "Plan, apply, or verify one evidence-backed source baseline update",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				selected := 0
				for _, active := range []bool{markPlan, markApply != "", markVerify != ""} {
					if active {
						selected++
					}
				}
				if selected != 1 {
					return domain.NewEnvelope(
						"gds source mark-verified", domain.ExitInput, nil, domain.Finding{
							Code: "GDS_SOURCE_VERIFICATION_MODE_REQUIRED", Severity: domain.SeverityHigh,
							Message: "Use exactly one of --plan, --apply, or --verify.",
						},
					)
				}
				switch {
				case markPlan:
					return executor.services.PlanSourceVerification(
						ctx, executor.options.cwd, markOptions,
					)
				case markApply != "":
					return executor.services.ApplySourceVerification(
						ctx, executor.options.cwd, markApply, markOptions.Operation,
					)
				default:
					return executor.services.VerifySourceVerification(
						ctx, executor.options.cwd, markVerify, markOptions.Operation,
					)
				}
			})
		},
	}
	mark.Flags().BoolVar(&markPlan, "plan", false, "store a side-effect-free exact verification plan")
	mark.Flags().StringVar(&markApply, "apply", "", "apply one exact stored plan id")
	mark.Flags().StringVar(&markVerify, "verify", "", "verify one exact completed operation id")
	mark.Flags().StringVar(&markOptions.Request.ID, "id", "", "registered source id")
	mark.Flags().StringVar(&markOptions.Request.Status, "status", "", "reviewed non-blocking status")
	mark.Flags().StringVar(&markOptions.Request.VerifiedAt, "verified-at", "", "semantic review date")
	mark.Flags().StringVar(&markOptions.Request.NextReview, "next-review", "", "next required review date")
	mark.Flags().StringVar(&markOptions.Request.EvidenceRef, "evidence-ref", "", "non-secret semantic review evidence")
	mark.Flags().StringVar(&markOptions.Operation.StatePath, "state-path", "", "local GDS state database path")
	mark.Flags().StringVar(&markOptions.Operation.DeviceID, "device-id", "", "canonical device identity")
	mark.Flags().StringVar(&markOptions.Operation.SessionID, "session-id", "", "bounded non-secret session identity")
	mark.Flags().StringVar(&markOptions.Operation.ApprovalReference, "approval-ref", "", "signed exact-plan approval JSON file")
	command.AddCommand(status, check, mark)
	return command
}

func (executor *executor) harnessCommand() *cobra.Command {
	harnessID := "all"
	skillProfile := "core"
	scope := "project"
	targetRoot := ""
	bridgeGDSRoot := ""
	bridgeNDDevRoot := ""
	command := &cobra.Command{
		Use:   "harness",
		Short: "Render, inspect, and manage canonical harness adapters",
		Args:  cobra.NoArgs,
	}
	bridge := &cobra.Command{
		Use:   "bridge",
		Short: "Validate the canonical NDDev module-to-harness identity bridge",
		Args:  cobra.NoArgs,
	}
	bridgeValidate := &cobra.Command{
		Use:   "validate",
		Short: "Validate GDS-owned bridge identity and device-selection contracts",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				return executor.services.ValidateModuleBridge(ctx, bridgeGDSRoot)
			})
		},
	}
	bridgeValidate.Flags().StringVar(
		&bridgeGDSRoot, "gds-root", "",
		"absolute top-level path of the GDS checkout",
	)
	bridgeParity := &cobra.Command{
		Use:   "parity",
		Short: "Compare the GDS bridge with one explicit NDDev harness checkout",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				return executor.services.ValidateModuleBridgeParity(
					ctx, app.ModuleBridgeParityOptions{
						GDSRoot: bridgeGDSRoot, NDDevRoot: bridgeNDDevRoot,
					},
				)
			})
		},
	}
	bridgeParity.Flags().StringVar(
		&bridgeGDSRoot, "gds-root", "",
		"absolute top-level path of the GDS checkout",
	)
	bridgeParity.Flags().StringVar(
		&bridgeNDDevRoot, "nddev-root", "",
		"absolute top-level path of the example-harnesses checkout",
	)
	bridge.AddCommand(bridgeValidate, bridgeParity)
	detect := &cobra.Command{
		Use:   "detect",
		Short: "Observe bounded binary and version evidence for one or all harnesses",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				return executor.services.DetectHarness(
					ctx, executor.options.cwd, harnessID,
				)
			})
		},
	}
	detect.Flags().StringVar(&harnessID, "harness", "all", "canonical harness ID or all")
	render := &cobra.Command{
		Use:   "render",
		Short: "Render one deterministic project-local adapter candidate",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				return executor.services.RenderHarnessAdapter(
					ctx, executor.options.cwd, harnessID,
					harness.RenderRequest{SkillProfile: skillProfile, Scope: scope},
				)
			})
		},
	}
	addHarnessAdapterFlags(render, &harnessID, &skillProfile, &scope, nil)
	inspect := &cobra.Command{
		Use:   "inspect",
		Short: "Compare one adapter candidate with an existing target",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				return executor.services.InspectHarnessAdapter(
					ctx, executor.options.cwd, harnessID, targetRoot,
					harness.RenderRequest{SkillProfile: skillProfile, Scope: scope},
				)
			})
		},
	}
	addHarnessAdapterFlags(inspect, &harnessID, &skillProfile, &scope, &targetRoot)
	doctor := &cobra.Command{
		Use:   "doctor",
		Short: "Combine static profile, runtime detection, and target drift evidence",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				return executor.services.DoctorHarnessAdapter(
					ctx, executor.options.cwd, harnessID, targetRoot,
					harness.RenderRequest{SkillProfile: skillProfile, Scope: scope},
				)
			})
		},
	}
	addHarnessAdapterFlags(doctor, &harnessID, &skillProfile, &scope, &targetRoot)
	devicePath := ""
	syncConverge := false
	syncOperation := app.ProjectionOperationOptions{}
	sync := &cobra.Command{
		Use:   "sync",
		Short: "Reconcile installed adapters with one device's declared harness selection",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				request := app.HarnessSyncOptions{
					Path: executor.options.cwd, DevicePath: devicePath,
					TargetRoot: targetRoot, SkillProfile: skillProfile, Scope: scope,
				}
				if !syncConverge {
					return executor.services.ReconcileDeviceHarnesses(ctx, request)
				}
				return domain.NewEnvelope("gds harness sync converge", domain.ExitUnsupported, nil, domain.Finding{
					Code: "GDS_HARNESS_SYNC_CONVERGE_REMOVED", Severity: domain.SeverityHigh,
					Message: "Combined convergence cannot bind one approval to multiple internally generated plans. Classify with harness sync, then use explicit per-harness plan, operation approve, operation enable, apply, and verify commands.",
				})
			})
		},
	}
	sync.Flags().BoolVar(
		&syncConverge, "converge", false,
		"removed unsafe legacy mode; use explicit per-harness exact-plan transactions",
	)
	sync.Flags().StringVar(&syncOperation.StatePath, "state-path", "", "local GDS state database path")
	sync.Flags().StringVar(&syncOperation.DeviceID, "device-id", "", "canonical current device identity")
	sync.Flags().StringVar(&syncOperation.SessionID, "session-id", "", "bounded non-secret session identity")
	sync.Flags().StringVar(
		&syncOperation.ApprovalReference, "approval-ref", "",
		"signed exact-plan approval JSON file; required by --converge",
	)
	sync.Flags().StringVar(
		&devicePath, "device", "", "device descriptor whose harnesses selection is authoritative",
	)
	sync.Flags().StringVar(&skillProfile, "skill-profile", "core", "canonical skill profile")
	sync.Flags().StringVar(&scope, "scope", "project", "adapter projection scope")
	sync.Flags().StringVar(&targetRoot, "target-root", "", "existing adapter target root")
	modelLabel := "not-proven"
	executionProfile := "read-only"
	tools := []string{}
	runtimeEvidence := ""
	runtimeDriver := ""
	evidenceDirectory := ""
	driverTimeout := time.Duration(0)
	evaluate := &cobra.Command{
		Use:   "eval",
		Short: "Run deterministic harness gates and emit typed runtime evidence",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				return executor.services.EvaluateHarnessAdapter(
					ctx, executor.options.cwd, harnessID, harness.EvalOptions{
						SkillProfile: skillProfile, ModelLabel: modelLabel,
						ExecutionProfile: executionProfile, Tools: tools,
						RuntimeEvidence: runtimeEvidence,
						RuntimeDriver:   runtimeDriver, EvidenceDirectory: evidenceDirectory,
						DriverTimeout: driverTimeout,
					},
				)
			})
		},
	}
	evaluate.Flags().StringVar(&harnessID, "harness", "all", "one exact canonical harness ID")
	evaluate.Flags().StringVar(&skillProfile, "skill-profile", "core", "canonical skill profile")
	evaluate.Flags().StringVar(&modelLabel, "model-label", "not-proven", "exact tested model label")
	evaluate.Flags().StringVar(
		&executionProfile, "execution-profile", "read-only", "tested execution profile identity",
	)
	evaluate.Flags().StringSliceVar(&tools, "tool", nil, "available tool identity; repeat as needed")
	evaluate.Flags().StringVar(
		&runtimeEvidence, "runtime-evidence", "",
		"strict native runtime evidence JSON with confined transcript references",
	)
	evaluate.Flags().StringVar(
		&runtimeDriver, "runtime-driver", "",
		"trusted executable implementing the GDS native runtime driver protocol",
	)
	evaluate.Flags().StringVar(
		&evidenceDirectory, "evidence-directory", "",
		"empty directory for native runtime request, transcripts, and validated evidence",
	)
	evaluate.Flags().DurationVar(
		&driverTimeout, "driver-timeout", 0,
		"native runtime driver timeout (default 30m, maximum 2h)",
	)
	for _, operation := range []string{"install", "update", "rollback", "remove"} {
		operation := operation
		plan := false
		applyPlanID := ""
		verifyOperationID := ""
		rollbackSourceRoot := ""
		operationOptions := app.ProjectionOperationOptions{}
		lifecycle := &cobra.Command{
			Use:   operation,
			Short: "Plan, apply, or verify one exact adapter " + operation + " transaction",
			Args:  cobra.NoArgs,
			RunE: func(child *cobra.Command, _ []string) error {
				return executor.run(child, func(ctx context.Context) domain.Envelope {
					selected := 0
					if plan {
						selected++
					}
					if applyPlanID != "" {
						selected++
					}
					if verifyOperationID != "" {
						selected++
					}
					if selected != 1 {
						return domain.NewEnvelope(
							"gds harness "+operation, domain.ExitInput, nil, domain.Finding{
								Code: "GDS_HARNESS_OPERATION_MODE_REQUIRED", Severity: domain.SeverityHigh,
								Message: "Use exactly one of --plan, --apply, or --verify.",
							},
						)
					}
					values := app.HarnessOperationOptions{
						ProjectionOperationOptions: operationOptions,
						HarnessID:                  harnessID, TargetRoot: targetRoot,
						RollbackSourceRoot: rollbackSourceRoot,
						SkillProfile:       skillProfile, Scope: scope,
					}
					switch {
					case plan:
						return executor.services.PlanHarnessOperation(
							ctx, executor.options.cwd, operation, values,
						)
					case applyPlanID != "":
						return executor.services.ApplyHarnessOperation(
							ctx, executor.options.cwd, operation, applyPlanID, values,
						)
					default:
						return executor.services.VerifyHarnessOperation(
							ctx, executor.options.cwd, operation, verifyOperationID, values,
						)
					}
				})
			},
		}
		addHarnessAdapterFlags(lifecycle, &harnessID, &skillProfile, &scope, &targetRoot)
		lifecycle.Flags().BoolVar(&plan, "plan", false, "build a side-effect-free exact lifecycle plan")
		lifecycle.Flags().StringVar(&applyPlanID, "apply", "", "apply one exact stored plan id")
		lifecycle.Flags().StringVar(&verifyOperationID, "verify", "", "verify one exact completed operation id")
		lifecycle.Flags().StringVar(&operationOptions.StatePath, "state-path", "", "local GDS state database path")
		lifecycle.Flags().StringVar(&operationOptions.DeviceID, "device-id", "", "canonical device identity")
		lifecycle.Flags().StringVar(&operationOptions.SessionID, "session-id", "", "bounded non-secret session identity")
		lifecycle.Flags().StringVar(&operationOptions.ApprovalReference, "approval-ref", "", "signed exact-plan approval JSON file")
		if operation == "rollback" {
			lifecycle.Flags().StringVar(
				&rollbackSourceRoot, "rollback-source", "",
				"exact prior installed projection root",
			)
		}
		command.AddCommand(lifecycle)
	}
	command.AddCommand(bridge, detect, render, inspect, doctor, sync, evaluate)
	return command
}

func addHarnessAdapterFlags(
	command *cobra.Command,
	harnessID *string,
	skillProfile *string,
	scope *string,
	targetRoot *string,
) {
	command.Flags().StringVar(harnessID, "harness", "all", "one exact canonical harness ID")
	command.Flags().StringVar(skillProfile, "skill-profile", "core", "canonical skill profile")
	command.Flags().StringVar(scope, "scope", "project", "adapter projection scope")
	if targetRoot != nil {
		command.Flags().StringVar(targetRoot, "target-root", "", "existing adapter target root")
	}
}

func (executor *executor) rolloutCommand() *cobra.Command {
	requestPath := ""
	command := &cobra.Command{
		Use:   "rollout",
		Short: "Plan bounded immutable bundle rollouts without applying them",
		Args:  cobra.NoArgs,
	}
	plan := &cobra.Command{
		Use:   "plan",
		Short: "Compile an exact canary and wave plan from a validated request",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				return executor.services.PlanRollout(ctx, requestPath)
			})
		},
	}
	plan.Flags().StringVar(&requestPath, "file", "", "rollout request JSON or YAML")
	_ = plan.MarkFlagRequired("file")
	command.AddCommand(plan)
	return command
}

func (executor *executor) releaseCommand() *cobra.Command {
	values := app.ReleaseCandidateOptions{MinimumCLIVersion: "0.1.0"}
	command := &cobra.Command{
		Use:   "release",
		Short: "Build, verify, install, upgrade, roll back, or remove immutable GDS releases",
		Args:  cobra.NoArgs,
	}
	candidate := &cobra.Command{
		Use:   "candidate",
		Short: "Build a reproducible bundle candidate in memory from a clean source tree",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				return executor.services.BuildReleaseCandidate(ctx, executor.options.cwd, values)
			})
		},
	}
	candidate.Flags().StringVar(&values.BundleVersion, "version", "", "candidate semantic version")
	candidate.Flags().IntVar(&values.ReleaseSequence, "sequence", 0, "monotonic release sequence")
	candidate.Flags().StringVar(&values.Channel, "channel", "canary", "release channel")
	candidate.Flags().StringVar(
		&values.MinimumCLIVersion, "minimum-cli-version", "0.1.0", "minimum compatible GDS CLI version",
	)
	_ = candidate.MarkFlagRequired("version")
	_ = candidate.MarkFlagRequired("sequence")
	command.AddCommand(
		candidate,
		executor.releaseScopeCommand(),
		executor.releaseEvidenceCommand(),
		executor.releaseLifecycleCommand("install"),
		executor.releaseLifecycleCommand("upgrade"),
		executor.releaseLifecycleCommand("rollback"),
		executor.releaseLifecycleCommand("remove"),
	)
	return command
}

func (executor *executor) releaseScopeCommand() *cobra.Command {
	options := app.ReleaseScopeOptions{}
	command := &cobra.Command{
		Use: "scope", Short: "Resolve the canonical installation scope and active release without mutation",
		Args: cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				return executor.services.InspectReleaseScope(ctx, options)
			})
		},
	}
	command.Flags().StringVar(&options.InstallRoot, "install-root", "", "exact local GDS installation root")
	command.Flags().StringVar(&options.TrustPolicyPath, "trust-policy", "", "independent local consumer trust policy")
	return command
}

func (executor *executor) releaseEvidenceCommand() *cobra.Command {
	options := newReleaseOperationOptions()
	command := &cobra.Command{
		Use: "verify", Short: "Verify one immutable release and its offline provenance without installing it",
		Args: cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				return executor.services.VerifyReleaseEvidence(ctx, options)
			})
		},
	}
	addReleaseEvidenceFlags(command, &options)
	command.Flags().StringVar(&options.InstallRoot, "install-root", "", "exact install root for authorized downgrade verification")
	command.Flags().StringVar(&options.RollbackAuthorizationPath, "rollback-authorization", "", "exact rollback authorization YAML or JSON")
	command.Flags().StringVar(&options.StatePath, "state-path", "", "local GDS state database path")
	return command
}

func (executor *executor) releaseLifecycleCommand(operation string) *cobra.Command {
	plan := false
	applyPlanID := ""
	verifyOperationID := ""
	options := newReleaseOperationOptions()
	command := &cobra.Command{
		Use: operation, Short: releaseLifecycleDescription(operation), Args: cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				baseCommand := "gds release " + operation
				if selectedModes(plan, applyPlanID, verifyOperationID) != 1 {
					return domain.NewEnvelope(baseCommand, domain.ExitInput, nil, domain.Finding{
						Code: "GDS_RELEASE_LIFECYCLE_MODE_REQUIRED", Severity: domain.SeverityHigh,
						Message: "Use exactly one of --plan, --apply, or --verify.",
					})
				}
				if verifyOperationID != "" && releaseLifecycleInputsChanged(child, operation) {
					return domain.NewEnvelope(baseCommand, domain.ExitInput, nil, domain.Finding{
						Code: "GDS_RELEASE_VERIFY_INPUT_CONFLICT", Severity: domain.SeverityHigh,
						Message: "Release identity inputs cannot alter a stored operation verification.",
					})
				}
				switch {
				case plan:
					return executor.services.PlanReleaseOperation(ctx, executor.options.cwd, operation, options)
				case applyPlanID != "":
					return executor.services.ApplyReleaseOperation(
						ctx, executor.options.cwd, operation, applyPlanID, options,
					)
				default:
					return executor.services.VerifyReleaseOperation(
						ctx, executor.options.cwd, operation, verifyOperationID, options,
					)
				}
			})
		},
	}
	command.Flags().BoolVar(&plan, "plan", false, "store an exact side-effect-free release lifecycle plan")
	command.Flags().StringVar(&applyPlanID, "apply", "", "apply one exact release lifecycle plan id")
	command.Flags().StringVar(&verifyOperationID, "verify", "", "verify one exact release lifecycle operation id")
	if operation == "install" || operation == "upgrade" {
		addReleaseEvidenceFlags(command, &options)
	}
	command.Flags().StringVar(&options.InstallRoot, "install-root", "", "exact local GDS installation root")
	if operation == "rollback" {
		command.Flags().StringVar(&options.TargetReleaseKey, "target-release-key", "", "exact installed release key")
		command.Flags().StringVar(&options.RollbackAuthorizationPath, "rollback-authorization", "", "exact rollback authorization YAML or JSON")
	}
	command.Flags().StringVar(&options.StatePath, "state-path", "", "local GDS state database path")
	command.Flags().StringVar(&options.DeviceID, "device-id", "", "canonical current device identity")
	command.Flags().StringVar(&options.SessionID, "session-id", "", "bounded non-secret session identity")
	command.Flags().StringVar(&options.ApprovalReference, "approval-ref", "", "signed exact-plan approval JSON file")
	return command
}

func newReleaseOperationOptions() app.ReleaseOperationOptions {
	return app.ReleaseOperationOptions{
		ConsumerVersion: Version, TargetOS: runtime.GOOS, TargetArch: runtime.GOARCH,
	}
}

func addReleaseEvidenceFlags(command *cobra.Command, options *app.ReleaseOperationOptions) {
	command.Flags().StringVar(&options.ReleaseDirectory, "release-directory", "", "exact six-file immutable release directory")
	command.Flags().StringVar(&options.EvidenceDirectory, "evidence-directory", "", "exact offline attestation evidence directory")
	command.Flags().StringVar(&options.TrustPolicyPath, "trust-policy", "", "local consumer trust policy YAML or JSON")
}

func releaseLifecycleInputsChanged(command *cobra.Command, operation string) bool {
	names := []string{"install-root"}
	if operation == "install" || operation == "upgrade" {
		names = append(names, "release-directory", "evidence-directory", "trust-policy")
	}
	if operation == "rollback" {
		names = append(names, "target-release-key", "rollback-authorization")
	}
	for _, name := range names {
		if command.Flags().Changed(name) {
			return true
		}
	}
	return false
}

func releaseLifecycleDescription(operation string) string {
	switch operation {
	case "install":
		return "Install and activate the first verified immutable GDS release"
	case "upgrade":
		return "Install and activate a higher verified immutable GDS release"
	case "rollback":
		return "Activate one exact lower installed release under explicit authorization"
	default:
		return "Remove the exact active verified GDS release"
	}
}

func (executor *executor) skillCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "skill",
		Short: "Inspect and package canonical profiled skills",
		Args:  cobra.NoArgs,
	}
	command.AddCommand(&cobra.Command{
		Use:   "package <plugin>",
		Short: "Build a deterministic standalone plugin candidate in memory",
		Args:  cobra.ExactArgs(1),
		RunE: func(child *cobra.Command, arguments []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				return executor.services.PackagePlugin(
					ctx, executor.options.cwd, arguments[0],
				)
			})
		},
	})
	return command
}

func (executor *executor) forkSyncCommand() *cobra.Command {
	plan := false
	applyPlanID := ""
	verifyOperationID := ""
	options := app.ForkSyncOptions{}
	command := &cobra.Command{
		Use: "sync", Short: "Fast-forward one fork without discarding fork-only commits",
		Args: cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				if selectedModes(plan, applyPlanID, verifyOperationID) != 1 {
					return domain.NewEnvelope("gds fork sync", domain.ExitInput, nil, domain.Finding{
						Code: "GDS_FORK_SYNC_MODE_REQUIRED", Severity: domain.SeverityHigh,
						Message: "Use exactly one of --plan, --apply, or --verify.",
					})
				}
				switch {
				case plan:
					return executor.services.PlanForkSync(ctx, executor.options.cwd, options)
				case applyPlanID != "":
					return executor.services.ApplyForkSync(ctx, applyPlanID, options)
				default:
					return executor.services.VerifyForkSync(ctx, verifyOperationID, options)
				}
			})
		},
	}
	command.Flags().BoolVar(&plan, "plan", false, "store an exact side-effect-free fork sync plan")
	command.Flags().StringVar(&applyPlanID, "apply", "", "apply one exact fork sync plan id")
	command.Flags().StringVar(&verifyOperationID, "verify", "", "verify one exact fork sync operation id")
	command.Flags().StringVar(&options.StatePath, "state-path", "", "local GDS state database path")
	command.Flags().StringVar(&options.DeviceID, "device-id", "", "canonical current device identity")
	command.Flags().StringVar(&options.SessionID, "session-id", "", "bounded non-secret session identity")
	command.Flags().StringVar(&options.ApprovalReference, "approval-ref", "", "signed exact-plan approval JSON file")
	return command
}

func (executor *executor) compileCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "compile",
		Short: "Compile canonical sources without writing projections",
		Args:  cobra.NoArgs,
	}
	command.AddCommand(&cobra.Command{
		Use:   "policy",
		Short: "Compile the effective repository policy with provenance",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				return executor.services.CompilePolicy(ctx, executor.options.cwd)
			})
		},
	})
	return command
}

func (executor *executor) generateCommand() *cobra.Command {
	check := false
	plan := false
	applyPlanID := ""
	verifyOperationID := ""
	operationOptions := app.ProjectionOperationOptions{}
	sourceOptions := app.ProjectionSourceOptions{}
	command := &cobra.Command{
		Use:   "generate",
		Short: "Render deterministic projection candidates in memory",
		Args:  cobra.NoArgs,
	}
	repository := &cobra.Command{
		Use:   "repository",
		Short: "Render, plan, apply, or verify exact repository projections",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			operationOptions.Source = sourceOptions
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				selected := 0
				for _, active := range []bool{check, plan, applyPlanID != "", verifyOperationID != ""} {
					if active {
						selected++
					}
				}
				if selected > 1 {
					return domain.NewEnvelope(
						"gds generate repository", domain.ExitInput, nil, domain.Finding{
							Code: "GDS_GENERATE_MODE_CONFLICT", Severity: domain.SeverityHigh,
							Message: "Use only one of --check, --plan, --apply, or --verify.",
						},
					)
				}
				switch {
				case plan:
					return executor.services.PlanRepositoryProjection(
						ctx, executor.options.cwd, operationOptions,
					)
				case applyPlanID != "":
					return executor.services.ApplyRepositoryProjection(
						ctx, executor.options.cwd, applyPlanID, operationOptions,
					)
				case verifyOperationID != "":
					return executor.services.VerifyRepositoryProjection(
						ctx, executor.options.cwd, verifyOperationID, operationOptions,
					)
				}
				return executor.services.GenerateRepository(ctx, executor.options.cwd, check, sourceOptions)
			})
		},
	}
	repository.Flags().BoolVar(
		&check, "check", false, "compare candidate digests with existing generated files",
	)
	repository.Flags().BoolVar(
		&plan, "plan", false, "store a side-effect-free exact materialization plan",
	)
	repository.Flags().StringVar(
		&applyPlanID, "apply", "", "apply one exact stored plan id",
	)
	repository.Flags().StringVar(
		&verifyOperationID, "verify", "", "verify one exact completed operation id",
	)
	repository.Flags().StringVar(
		&operationOptions.StatePath, "state-path", "", "local GDS state database path",
	)
	repository.Flags().StringVar(
		&operationOptions.DeviceID, "device-id", "", "canonical device identity",
	)
	repository.Flags().StringVar(
		&operationOptions.SessionID, "session-id", "", "bounded non-secret session identity",
	)
	repository.Flags().StringVar(
		&operationOptions.ApprovalReference, "approval-ref", "", "signed exact-plan approval JSON file",
	)
	repository.Flags().StringVar(
		&sourceOptions.BundleArchive, "bundle-archive", "", "verified immutable GDS bundle tar.gz used as projection policy source",
	)
	repository.Flags().StringVar(
		&sourceOptions.ReleaseEnvelope, "release-envelope", "", "detached release-envelope.json for --bundle-archive",
	)
	command.AddCommand(repository)
	return command
}

func (executor *executor) contextCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "context",
		Short: "Resolve the current GDS scope and Git boundaries",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return executor.run(command, func(ctx context.Context) domain.Envelope {
				return executor.services.ResolveContext(ctx, executor.options.cwd)
			})
		},
	}
}

func (executor *executor) statusCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "Classify local Git state without refreshing or integrating refs",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return executor.run(command, func(ctx context.Context) domain.Envelope {
				return executor.services.Status(ctx, executor.options.cwd)
			})
		},
	}
}

func (executor *executor) discoverCommand() *cobra.Command {
	values := app.DiscoveryOptions{}
	command := &cobra.Command{
		Use:   "discover",
		Short: "Discover local Git boundaries without cloning or provider writes",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return executor.run(command, func(ctx context.Context) domain.Envelope {
				if values.Root == "" {
					values.Root = executor.options.cwd
				}
				return executor.services.Discover(ctx, values)
			})
		},
	}
	addDiscoveryFlags(command, &values)
	return command
}

func (executor *executor) inventoryCommand() *cobra.Command {
	values := app.DiscoveryOptions{}
	command := &cobra.Command{
		Use:   "inventory",
		Short: "Compile an in-memory local observed inventory",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return executor.run(command, func(ctx context.Context) domain.Envelope {
				if values.Root == "" {
					values.Root = executor.options.cwd
				}
				return executor.services.CompileInventory(ctx, values)
			})
		},
	}
	addDiscoveryFlags(command, &values)
	relationshipValues := app.DiscoveryOptions{}
	relationships := &cobra.Command{
		Use: "relationships", Short: "Compile the stable identity and consumer graph",
		Args: cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				if relationshipValues.Root == "" {
					relationshipValues.Root = executor.options.cwd
				}
				return executor.services.CompileRelationshipIndex(ctx, relationshipValues)
			})
		},
	}
	addDiscoveryFlags(relationships, &relationshipValues)
	command.AddCommand(relationships)
	return command
}

func addDiscoveryFlags(command *cobra.Command, values *app.DiscoveryOptions) {
	command.Flags().StringVar(&values.Root, "root", "", "filesystem root to inspect")
	command.Flags().IntVar(&values.MaxDepth, "max-depth", 8, "maximum directory depth")
	command.Flags().IntVar(
		&values.MaxRepositories, "max-repositories", 2000, "hard repository count limit",
	)
	command.Flags().IntVar(&values.Concurrency, "concurrency", 4, "bounded Git inspection workers")
	command.Flags().BoolVar(
		&values.IncludeArchived, "include-archived", false,
		"also list repositories whose anchor declares lifecycle: archived",
	)
}

func (executor *executor) validateCommand() *cobra.Command {
	fixtures := true
	planFile := ""
	command := &cobra.Command{
		Use:   "validate",
		Short: "Validate GDS schemas and repository contracts without fixing them",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return executor.run(command, func(ctx context.Context) domain.Envelope {
				return executor.services.ValidateAll(ctx, executor.options.cwd, fixtures)
			})
		},
	}
	command.PersistentFlags().BoolVar(
		&fixtures, "fixtures", true, "validate the fixture corpus when present",
	)
	plan := &cobra.Command{
		Use:   "plan",
		Short: "Validate a mutation plan without storing or applying it",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				return executor.services.ValidatePlan(ctx, planFile)
			})
		},
	}
	plan.Flags().StringVar(&planFile, "file", "", "plan JSON or YAML file to validate")
	validationCommand := func(
		use string,
		short string,
		operation func(context.Context) domain.Envelope,
	) *cobra.Command {
		return &cobra.Command{
			Use: use, Short: short, Args: cobra.NoArgs,
			RunE: func(child *cobra.Command, _ []string) error {
				return executor.run(child, operation)
			},
		}
	}
	command.AddCommand(
		&cobra.Command{
			Use:   "schemas",
			Short: "Validate embedded schemas, canonical files, and fixtures",
			Args:  cobra.NoArgs,
			RunE: func(child *cobra.Command, _ []string) error {
				return executor.run(child, func(ctx context.Context) domain.Envelope {
					return executor.services.ValidateSchemas(ctx, executor.options.cwd, fixtures)
				})
			},
		},
		&cobra.Command{
			Use:   "repository",
			Short: "Validate the current repository anchor",
			Args:  cobra.NoArgs,
			RunE: func(child *cobra.Command, _ []string) error {
				return executor.run(child, func(ctx context.Context) domain.Envelope {
					return executor.services.ValidateRepository(ctx, executor.options.cwd)
				})
			},
		},
		&cobra.Command{
			Use:   "estate",
			Short: "Validate desired estate installations, owners, selectors, and references",
			Args:  cobra.NoArgs,
			RunE: func(child *cobra.Command, _ []string) error {
				return executor.run(child, func(ctx context.Context) domain.Envelope {
					return executor.services.ValidateEstate(ctx, executor.options.cwd)
				})
			},
		},
		&cobra.Command{
			Use:   "gitlinks",
			Short: "Validate .gitmodules, index gitlinks, and typed relationships",
			Args:  cobra.NoArgs,
			RunE: func(child *cobra.Command, _ []string) error {
				return executor.run(child, func(ctx context.Context) domain.Envelope {
					return executor.services.ValidateGitlinks(ctx, executor.options.cwd)
				})
			},
		},
		&cobra.Command{
			Use:   "projections",
			Short: "Validate generated repository projections without fixing drift",
			Args:  cobra.NoArgs,
			RunE: func(child *cobra.Command, _ []string) error {
				return executor.run(child, func(ctx context.Context) domain.Envelope {
					return executor.services.ValidateProjections(ctx, executor.options.cwd)
				})
			},
		},
		&cobra.Command{
			Use:   "skills",
			Short: "Validate canonical skills, profiles, and Codex sidecars",
			Args:  cobra.NoArgs,
			RunE: func(child *cobra.Command, _ []string) error {
				return executor.run(child, func(ctx context.Context) domain.Envelope {
					return executor.services.ValidateSkills(ctx, executor.options.cwd)
				})
			},
		},
		&cobra.Command{
			Use:   "plugins",
			Short: "Validate deterministic standalone Codex plugin packages",
			Args:  cobra.NoArgs,
			RunE: func(child *cobra.Command, _ []string) error {
				return executor.run(child, func(ctx context.Context) domain.Envelope {
					return executor.services.ValidatePlugins(ctx, executor.options.cwd)
				})
			},
		},
		&cobra.Command{
			Use:   "memories",
			Short: "Validate Serena memory provenance, sources, and semantic names",
			Args:  cobra.NoArgs,
			RunE: func(child *cobra.Command, _ []string) error {
				return executor.run(child, func(ctx context.Context) domain.Envelope {
					return executor.services.ValidateMemories(ctx, executor.options.cwd)
				})
			},
		},
		validationCommand("policies", "Compile policy, exceptions, and leaf provenance", func(ctx context.Context) domain.Envelope {
			return executor.services.ValidatePolicies(ctx, executor.options.cwd)
		}),
		validationCommand("context", "Validate deterministic repository context resolution", func(ctx context.Context) domain.Envelope {
			return executor.services.ValidateContext(ctx, executor.options.cwd)
		}),
		validationCommand("git-state", "Validate machine-readable local Git state", func(ctx context.Context) domain.Envelope {
			return executor.services.ValidateGitState(ctx, executor.options.cwd)
		}),
		validationCommand("security", "Scan tracked text and portable sources for security violations", func(ctx context.Context) domain.Envelope {
			return executor.services.ValidateSecurity(ctx, executor.options.cwd, "security")
		}),
		validationCommand("visibility", "Validate policy-to-projection visibility flow", func(ctx context.Context) domain.Envelope {
			return executor.services.ValidateVisibility(ctx, executor.options.cwd)
		}),
		validationCommand("absolute-paths", "Reject device-specific paths from portable sources", func(ctx context.Context) domain.Envelope {
			return executor.services.ValidateSecurity(ctx, executor.options.cwd, "absolute-paths")
		}),
		validationCommand("public-artifact", "Scan portable release sources for private or secret material", func(ctx context.Context) domain.Envelope {
			return executor.services.ValidateSecurity(ctx, executor.options.cwd, "public-artifact")
		}),
		validationCommand("source-freshness", "Validate source review dates and pinned evidence", func(ctx context.Context) domain.Envelope {
			return executor.services.ValidateSourceFreshness(ctx, executor.options.cwd)
		}),
		validationCommand("reproducibility", "Generate twice and compare exact candidate bytes", func(ctx context.Context) domain.Envelope {
			return executor.services.ValidateReproducibility(ctx, executor.options.cwd)
		}),
		plan,
	)
	harnessID := "all"
	harnessRuntime := false
	harnesses := &cobra.Command{
		Use:   "harnesses",
		Short: "Validate one versioned harness capability profile",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				if !harnessRuntime {
					return executor.services.ValidateHarnessStatic(
						ctx, executor.options.cwd, harnessID,
					)
				}
				return executor.services.ValidateHarness(
					ctx, executor.options.cwd, harnessID,
				)
			})
		},
	}
	harnesses.Flags().StringVar(
		&harnessID, "harness", "all",
		"harness capability profile, all, or selected (device-selected set only)",
	)
	harnesses.Flags().BoolVar(
		&harnessRuntime, "runtime", false, "require exact runtime evidence in addition to static contracts",
	)
	command.AddCommand(harnesses)
	return command
}

func (executor *executor) stateCommand() *cobra.Command {
	path := ""
	command := &cobra.Command{
		Use:   "state",
		Short: "Inspect and explicitly manage local durable GDS state",
		Args:  cobra.NoArgs,
	}
	inspect := &cobra.Command{
		Use:   "inspect",
		Short: "Read state metadata and counts in query-only mode",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				return executor.services.InspectState(ctx, path)
			})
		},
	}
	inspect.Flags().StringVar(&path, "path", "", "state database path; defaults to XDG state")
	initializePlan := false
	initializeApply := ""
	initializeVerify := ""
	initializeApproval := ""
	initializeEnable := ""
	initialize := &cobra.Command{
		Use:   "initialize",
		Short: "Plan, apply, or verify creation of the private local state database",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				selected := selectedModes(initializePlan, initializeApply, initializeVerify)
				if selected != 1 {
					return lifecycleModeRequired("gds state initialize")
				}
				switch {
				case initializePlan:
					return executor.services.PlanStateInitialize(ctx, path)
				case initializeApply != "":
					return executor.services.ApplyStateLifecycle(
						ctx, app.StateInitializeAction, path, initializeApply, initializeApproval, initializeEnable,
					)
				default:
					return executor.services.VerifyStateLifecycle(ctx, path, initializeVerify)
				}
			})
		},
	}
	addStateLifecycleFlags(
		initialize, &path, &initializePlan, &initializeApply,
		&initializeVerify, &initializeApproval,
		&initializeEnable,
	)
	migratePlan := false
	migrateApply := ""
	migrateVerify := ""
	migrateApproval := ""
	migrateEnable := ""
	migrate := &cobra.Command{
		Use:   "migrate",
		Short: "Plan, back up, apply, or verify an explicit state schema migration",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				selected := selectedModes(migratePlan, migrateApply, migrateVerify)
				if selected != 1 {
					return lifecycleModeRequired("gds state migrate")
				}
				switch {
				case migratePlan:
					return executor.services.PlanStateMigration(ctx, path)
				case migrateApply != "":
					return executor.services.ApplyStateLifecycle(
						ctx, app.StateMigrateAction, path, migrateApply, migrateApproval, migrateEnable,
					)
				default:
					return executor.services.VerifyStateLifecycle(ctx, path, migrateVerify)
				}
			})
		},
	}
	addStateLifecycleFlags(
		migrate, &path, &migratePlan, &migrateApply, &migrateVerify, &migrateApproval, &migrateEnable,
	)
	approvalOptions := app.StateApprovalIssueOptions{ActorType: "owner", TTL: 15 * time.Minute}
	approve := &cobra.Command{Use: "approve", Short: "Issue one signed state lifecycle approval artifact", Args: cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				return executor.services.IssueStateLifecycleApproval(ctx, approvalOptions)
			})
		},
	}
	approve.Flags().StringVar(&approvalOptions.PlanFile, "plan-file", "", "exact state lifecycle plan JSON extracted from plan output")
	approve.Flags().StringVar(&approvalOptions.PrivateKeyPath, "private-key", "", "private PKCS#8 Ed25519 PEM key file (mode 0600)")
	approve.Flags().StringVar(&approvalOptions.OutputPath, "output", "", "new mode-0600 approval JSON output path")
	approve.Flags().StringVar(&approvalOptions.ActorID, "actor-id", "", "canonical approving actor identity")
	approve.Flags().StringVar(&approvalOptions.ActorType, "actor-type", "owner", "owner, delegate, or automation")
	approve.Flags().StringVar(&approvalOptions.KeyID, "key-id", "", "public trust-policy key identity")
	approve.Flags().StringVar(&approvalOptions.ExternalReference, "external-reference", "", "optional non-secret issue, PR, or ticket metadata")
	approve.Flags().DurationVar(&approvalOptions.TTL, "ttl", 15*time.Minute, "approval lifetime (maximum 24h)")
	command.AddCommand(inspect, initialize, migrate, approve)
	return command
}

func addStateLifecycleFlags(
	command *cobra.Command,
	path *string,
	plan *bool,
	apply *string,
	verify *string,
	approval *string,
	enablement *string,
) {
	command.Flags().StringVar(path, "path", "", "state database path; defaults to XDG state")
	command.Flags().BoolVar(plan, "plan", false, "build a side-effect-free exact lifecycle plan")
	command.Flags().StringVar(apply, "apply", "", "apply the exact lifecycle plan digest")
	command.Flags().StringVar(verify, "verify", "", "verify durable evidence for the exact plan digest")
	command.Flags().StringVar(approval, "approval-ref", "", "signed exact-plan approval JSON file")
	command.Flags().StringVar(enablement, "enable", "", "explicitly enable this exact lifecycle plan ID once")
}

func selectedModes(plan bool, apply string, verify string) int {
	selected := 0
	for _, active := range []bool{plan, apply != "", verify != ""} {
		if active {
			selected++
		}
	}
	return selected
}

func lifecycleModeRequired(command string) domain.Envelope {
	return domain.NewEnvelope(command, domain.ExitInput, nil, domain.Finding{
		Code: "GDS_STATE_LIFECYCLE_MODE_REQUIRED", Severity: domain.SeverityHigh,
		Message: "Use exactly one of --plan, --apply, or --verify.",
	})
}

func (executor *executor) gitCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "git",
		Short: "Inspect local Git topology without refreshing refs",
		Args:  cobra.NoArgs,
	}
	command.AddCommand(&cobra.Command{
		Use:   "topology",
		Short: "Inspect remotes, submodule mappings, gitlinks, and worktree pins",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				return executor.services.GitTopology(ctx, executor.options.cwd)
			})
		},
	})
	return command
}

func (executor *executor) moduleCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "module",
		Short: "Manage module and git-submodule lifecycle",
		Args:  cobra.NoArgs,
	}
	command.AddCommand(&cobra.Command{
		Use:   "inspect",
		Short: "Compare typed module relationships with local Git topology",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				return executor.services.InspectModule(ctx, executor.options.cwd)
			})
		},
	})
	command.AddCommand(&cobra.Command{
		Use:   "drift-report",
		Short: "Report submodule gitlink drift in the current repository (read-only)",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				return executor.services.ModuleDriftReport(ctx, executor.options.cwd)
			})
		},
	})
	verify := &cobra.Command{
		Use:   "verify",
		Short: "Run each declared module's required verification lanes at the commit this repository pins",
		Args:  cobra.NoArgs,
	}
	verifyOptions := app.ModuleVerifyOptions{}
	verify.RunE = func(child *cobra.Command, _ []string) error {
		return executor.run(child, func(ctx context.Context) domain.Envelope {
			return executor.services.VerifyModules(ctx, executor.options.cwd, verifyOptions)
		})
	}
	verify.Flags().StringVar(&verifyOptions.Module, "module", "",
		"exact .gitmodules name to verify; every declared module when absent")
	verify.Flags().DurationVar(&verifyOptions.CommandTimeout, "command-timeout", 0,
		"per-command deadline; a bounded default applies when absent")
	command.AddCommand(verify)
	coverage := &cobra.Command{
		Use:   "coverage",
		Short: "Compare each declared module's claimed required checks with the ones its branch enforces",
		Args:  cobra.NoArgs,
	}
	coverageOptions := app.ModuleCoverageOptions{}
	coverage.RunE = func(child *cobra.Command, _ []string) error {
		return executor.run(child, func(ctx context.Context) domain.Envelope {
			return executor.services.CoverModules(ctx, executor.options.cwd, coverageOptions)
		})
	}
	coverage.Flags().StringVar(&coverageOptions.Module, "module", "",
		"exact .gitmodules name to compare; every declared module when absent")
	coverage.Flags().StringVar(&coverageOptions.RuntimeConfig, "runtime-config", "",
		"private device-local GitHub read runtime YAML/JSON; defaults to XDG config")
	coverage.Flags().StringVar(&coverageOptions.EstateRoot, "estate-root", "",
		"canonical estate root; resolved from the current context when absent")
	command.AddCommand(coverage)
	command.AddCommand(executor.moduleRelationshipCommand("add"), executor.moduleRelationshipCommand("remove"))
	command.AddCommand(executor.modulePinCommand())
	command.AddCommand(executor.moduleConsumerPlanCommand())
	command.AddCommand(executor.moduleReleaseCommand())
	return command
}

func (executor *executor) moduleConsumerPlanCommand() *cobra.Command {
	plan := false
	options := app.ModuleConsumerPlanOptions{MaxDepth: 8, MaxRepositories: 2000, Concurrency: 4}
	command := &cobra.Command{
		Use: "update-consumers", Short: "Plan independent updates for explicitly selected module consumers",
		Args: cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				if !plan {
					return domain.NewEnvelope("gds module update-consumers", domain.ExitInput, nil, domain.Finding{
						Code: "GDS_MODULE_CONSUMER_PLAN_REQUIRED", Severity: domain.SeverityHigh,
						Message: "Use --plan; each eligible consumer receives an independent stored subplan.",
					})
				}
				return executor.services.PlanModuleConsumerUpdates(ctx, options)
			})
		},
	}
	command.Flags().BoolVar(&plan, "plan", false, "build independent consumer subplans without applying them")
	command.Flags().StringVar(&options.ModulePath, "module", "", "exact local module repository boundary")
	command.Flags().StringVar(&options.InventoryRoot, "inventory-root", "", "complete local repository analysis root")
	command.Flags().StringSliceVar(&options.ConsumerIDs, "consumer-id", nil, "exact selected consumer repository ID; repeatable")
	command.Flags().IntVar(&options.MaxDepth, "max-depth", 8, "maximum inventory directory depth")
	command.Flags().IntVar(&options.MaxRepositories, "max-repositories", 2000, "hard inventory repository count limit")
	command.Flags().IntVar(&options.Concurrency, "concurrency", 4, "bounded inventory inspection workers")
	command.Flags().StringVar(&options.StatePath, "state-path", "", "local GDS state database path")
	command.Flags().StringVar(&options.DeviceID, "device-id", "", "canonical current device identity")
	command.Flags().StringVar(&options.SessionID, "session-id", "", "bounded non-secret session identity")
	return command
}

func (executor *executor) moduleReleaseCommand() *cobra.Command {
	plan := false
	applyPlanID := ""
	verifyOperationID := ""
	options := app.ModuleReleaseOptions{}
	command := &cobra.Command{
		Use: "release", Short: "Publish one policy-eligible immutable module version",
		Args: cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				if selectedModes(plan, applyPlanID, verifyOperationID) != 1 {
					return domain.NewEnvelope("gds module release", domain.ExitInput, nil, domain.Finding{
						Code: "GDS_MODULE_RELEASE_MODE_REQUIRED", Severity: domain.SeverityHigh,
						Message: "Use exactly one of --plan, --apply, or --verify.",
					})
				}
				if !plan && child.Flags().Changed("version") {
					return domain.NewEnvelope("gds module release", domain.ExitInput, nil, domain.Finding{
						Code: "GDS_MODULE_RELEASE_INPUT_CONFLICT", Severity: domain.SeverityHigh,
						Message: "--version cannot alter a stored release plan.",
					})
				}
				if !plan && child.Flags().Changed("asset") {
					return domain.NewEnvelope("gds module release", domain.ExitInput, nil, domain.Finding{
						Code: "GDS_MODULE_RELEASE_INPUT_CONFLICT", Severity: domain.SeverityHigh,
						Message: "--asset cannot alter a stored release plan.",
					})
				}
				switch {
				case plan:
					return executor.services.PlanModuleRelease(ctx, executor.options.cwd, options)
				case applyPlanID != "":
					return executor.services.ApplyModuleRelease(ctx, applyPlanID, options)
				default:
					return executor.services.VerifyModuleRelease(ctx, verifyOperationID, options)
				}
			})
		},
	}
	command.Flags().BoolVar(&plan, "plan", false, "store an exact side-effect-free release plan")
	command.Flags().StringVar(&applyPlanID, "apply", "", "apply one exact release plan id")
	command.Flags().StringVar(&verifyOperationID, "verify", "", "verify one exact release operation id")
	command.Flags().StringVar(&options.Version, "version", "", "canonical SemVer without a v prefix")
	command.Flags().StringSliceVar(&options.Assets, "asset", nil, "exact release asset path; repeatable and plan-only")
	command.Flags().StringVar(&options.StatePath, "state-path", "", "local GDS state database path")
	command.Flags().StringVar(&options.DeviceID, "device-id", "", "canonical current device identity")
	command.Flags().StringVar(&options.SessionID, "session-id", "", "bounded non-secret session identity")
	command.Flags().StringVar(&options.ApprovalReference, "approval-ref", "", "signed exact-plan approval JSON file")
	command.Flags().StringVar(&options.RuntimeConfig, "runtime-config", "", "private device-local GitHub read runtime YAML/JSON; defaults to XDG config (required-check plan/apply)")
	command.Flags().StringVar(&options.MutationRuntimeConfig, "mutation-runtime-config", "", "private device-local GitHub mutation runtime YAML/JSON (github-release apply)")
	command.Flags().StringVar(&options.HarnessEvidenceDirectory, "harness-evidence", "", "directory of signed harness evidence records; required for a module owned by an active harness")
	command.Flags().StringVar(&options.HarnessEvidenceTrustPolicy, "harness-evidence-trust", "", "trust policy that must verify the signed harness evidence")
	return command
}

func (executor *executor) modulePinCommand() *cobra.Command {
	plan := false
	applyPlanID := ""
	verifyOperationID := ""
	options := app.ModulePinOptions{}
	command := &cobra.Command{
		Use: "update-pin", Short: "Update one exact consumer gitlink to a finalized module commit",
		Args: cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				if selectedModes(plan, applyPlanID, verifyOperationID) != 1 {
					return domain.NewEnvelope("gds module update-pin", domain.ExitInput, nil, domain.Finding{
						Code: "GDS_MODULE_PIN_MODE_REQUIRED", Severity: domain.SeverityHigh,
						Message: "Use exactly one of --plan, --apply, or --verify.",
					})
				}
				if !plan && (child.Flags().Changed("module") || child.Flags().Changed("name")) {
					return domain.NewEnvelope("gds module update-pin", domain.ExitInput, nil, domain.Finding{
						Code: "GDS_MODULE_PIN_INPUT_CONFLICT", Severity: domain.SeverityHigh,
						Message: "Module identity flags cannot alter a stored pin plan.",
					})
				}
				switch {
				case plan:
					return executor.services.PlanModuleUpdatePin(ctx, executor.options.cwd, options)
				case applyPlanID != "":
					return executor.services.ApplyModuleUpdatePin(ctx, applyPlanID, options)
				default:
					return executor.services.VerifyModuleUpdatePin(ctx, verifyOperationID, options)
				}
			})
		},
	}
	command.Flags().BoolVar(&plan, "plan", false, "store an exact side-effect-free gitlink plan")
	command.Flags().StringVar(&applyPlanID, "apply", "", "apply one exact gitlink plan id")
	command.Flags().StringVar(&verifyOperationID, "verify", "", "verify one exact gitlink operation id")
	command.Flags().StringVar(&options.ModulePath, "module", "", "selected module checkout path")
	command.Flags().StringVar(&options.GitmodulesName, "name", "", "exact .gitmodules entry name")
	command.Flags().StringVar(&options.StatePath, "state-path", "", "local GDS state database path")
	command.Flags().StringVar(&options.DeviceID, "device-id", "", "canonical current device identity")
	command.Flags().StringVar(&options.SessionID, "session-id", "", "bounded non-secret session identity")
	command.Flags().StringVar(&options.ApprovalReference, "approval-ref", "", "signed exact-plan approval JSON file")
	return command
}

func (executor *executor) moduleRelationshipCommand(action string) *cobra.Command {
	plan := false
	applyPlanID := ""
	verifyOperationID := ""
	options := app.ModuleRelationshipOptions{}
	command := &cobra.Command{
		Use: action, Short: action + " one exact typed git-submodule relationship",
		Args: cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				baseCommand := "gds module " + action
				if selectedModes(plan, applyPlanID, verifyOperationID) != 1 {
					return domain.NewEnvelope(baseCommand, domain.ExitInput, nil, domain.Finding{
						Code: "GDS_MODULE_RELATIONSHIP_MODE_REQUIRED", Severity: domain.SeverityHigh,
						Message: "Use exactly one of --plan, --apply, or --verify.",
					})
				}
				if !plan {
					for _, name := range []string{"module-anchor", "module-id", "name"} {
						if child.Flags().Changed(name) {
							return domain.NewEnvelope(baseCommand, domain.ExitInput, nil, domain.Finding{
								Code: "GDS_MODULE_RELATIONSHIP_INPUT_CONFLICT", Severity: domain.SeverityHigh,
								Message: "Relationship identity flags cannot alter a stored plan.",
							})
						}
					}
				}
				switch action {
				case "add":
					switch {
					case plan:
						return executor.services.PlanModuleAdd(ctx, executor.options.cwd, options)
					case applyPlanID != "":
						return executor.services.ApplyModuleAdd(ctx, applyPlanID, options)
					default:
						return executor.services.VerifyModuleAdd(ctx, verifyOperationID, options)
					}
				default:
					switch {
					case plan:
						return executor.services.PlanModuleRemove(ctx, executor.options.cwd, options)
					case applyPlanID != "":
						return executor.services.ApplyModuleRemove(ctx, applyPlanID, options)
					default:
						return executor.services.VerifyModuleRemove(ctx, verifyOperationID, options)
					}
				}
			})
		},
	}
	command.Flags().BoolVar(&plan, "plan", false, "store an exact side-effect-free relationship plan")
	command.Flags().StringVar(&applyPlanID, "apply", "", "apply one exact relationship plan id")
	command.Flags().StringVar(&verifyOperationID, "verify", "", "verify one exact relationship operation id")
	command.Flags().StringVar(&options.ModuleAnchorPath, "module-anchor", "", "selected module repository anchor")
	command.Flags().StringVar(&options.ModuleID, "module-id", "", "stable module identity")
	command.Flags().StringVar(&options.GitmodulesName, "name", "", "exact .gitmodules entry name")
	command.Flags().StringVar(&options.StatePath, "state-path", "", "local GDS state database path")
	command.Flags().StringVar(&options.DeviceID, "device-id", "", "canonical current device identity")
	command.Flags().StringVar(&options.SessionID, "session-id", "", "bounded non-secret session identity")
	command.Flags().StringVar(&options.ApprovalReference, "approval-ref", "", "signed exact-plan approval JSON file")
	return command
}

func (executor *executor) repositoryCommand() *cobra.Command {
	command := &cobra.Command{
		Use: "repository", Short: "Manage stable repository identity and local lifecycle",
		Args: cobra.NoArgs,
	}
	plan := false
	applyPlanID := ""
	verifyOperationID := ""
	options := app.RepositoryOnboardOptions{}
	onboard := &cobra.Command{
		Use: "onboard", Short: "Materialize one schema-validated repository anchor",
		Args: cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				if selectedModes(plan, applyPlanID, verifyOperationID) != 1 {
					return domain.NewEnvelope("gds repository onboard", domain.ExitInput, nil, domain.Finding{
						Code: "GDS_REPOSITORY_ONBOARD_MODE_REQUIRED", Severity: domain.SeverityHigh,
						Message: "Use exactly one of --plan, --apply, or --verify.",
					})
				}
				if !plan && child.Flags().Changed("anchor") {
					return domain.NewEnvelope("gds repository onboard", domain.ExitInput, nil, domain.Finding{
						Code: "GDS_REPOSITORY_ONBOARD_INPUT_CONFLICT", Severity: domain.SeverityHigh,
						Message: "--anchor is a planning input and cannot alter a stored plan.",
					})
				}
				switch {
				case plan:
					return executor.services.PlanRepositoryOnboard(ctx, executor.options.cwd, options)
				case applyPlanID != "":
					return executor.services.ApplyRepositoryOnboard(ctx, applyPlanID, options)
				default:
					return executor.services.VerifyRepositoryOnboard(ctx, verifyOperationID, options)
				}
			})
		},
	}
	onboard.Flags().BoolVar(&plan, "plan", false, "store an exact side-effect-free onboarding plan")
	onboard.Flags().StringVar(&applyPlanID, "apply", "", "apply one exact onboarding plan id")
	onboard.Flags().StringVar(&verifyOperationID, "verify", "", "verify one exact onboarding operation id")
	onboard.Flags().StringVar(&options.AnchorPath, "anchor", "", "schema-validated repository anchor candidate")
	onboard.Flags().StringVar(&options.StatePath, "state-path", "", "local GDS state database path")
	onboard.Flags().StringVar(&options.DeviceID, "device-id", "", "canonical current device identity")
	onboard.Flags().StringVar(&options.SessionID, "session-id", "", "bounded non-secret session identity")
	onboard.Flags().StringVar(&options.ApprovalReference, "approval-ref", "", "signed exact-plan approval JSON file")
	command.AddCommand(onboard)
	command.AddCommand(
		executor.repositoryMaterializeCommand(),
		executor.repositoryRemoveCheckoutCommand(),
		executor.repositoryTransitionCommand("rename"),
		executor.repositoryTransitionCommand("transfer"),
		executor.repositoryTransitionCommand("archive"),
		executor.repositoryDeleteCommand(),
	)
	return command
}

func (executor *executor) repositoryRemoveCheckoutCommand() *cobra.Command {
	plan := false
	applyPlanID := ""
	verifyOperationID := ""
	options := app.WorkspaceRemoveOptions{}
	command := &cobra.Command{
		Use: "remove-checkout", Short: "Move one safe checkout into deterministic device quarantine",
		Args: cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				if selectedModes(plan, applyPlanID, verifyOperationID) != 1 {
					return domain.NewEnvelope("gds repository remove-checkout", domain.ExitInput, nil, domain.Finding{
						Code: "GDS_WORKSPACE_REMOVE_MODE_REQUIRED", Severity: domain.SeverityHigh,
						Message: "Use exactly one of --plan, --apply, or --verify.",
					})
				}
				if !plan && child.Flags().Changed("device") {
					return domain.NewEnvelope("gds repository remove-checkout", domain.ExitInput, nil, domain.Finding{
						Code: "GDS_WORKSPACE_REMOVE_INPUT_CONFLICT", Severity: domain.SeverityHigh,
						Message: "--device is a planning input and cannot alter a stored plan.",
					})
				}
				switch {
				case plan:
					return executor.services.PlanWorkspaceRemove(ctx, executor.options.cwd, options)
				case applyPlanID != "":
					return executor.services.ApplyWorkspaceRemove(ctx, applyPlanID, options)
				default:
					return executor.services.VerifyWorkspaceRemove(ctx, verifyOperationID, options)
				}
			})
		},
	}
	command.Flags().BoolVar(&plan, "plan", false, "store an exact side-effect-free checkout removal plan")
	command.Flags().StringVar(&applyPlanID, "apply", "", "apply one exact checkout removal plan id")
	command.Flags().StringVar(&verifyOperationID, "verify", "", "verify one exact checkout removal operation id")
	command.Flags().StringVar(&options.DevicePath, "device", "", "schema-validated device descriptor")
	command.Flags().StringVar(&options.StatePath, "state-path", "", "local GDS state database path")
	command.Flags().StringVar(&options.DeviceID, "device-id", "", "canonical current device identity")
	command.Flags().StringVar(&options.SessionID, "session-id", "", "bounded non-secret session identity")
	command.Flags().StringVar(&options.ApprovalReference, "approval-ref", "", "signed exact-plan approval JSON file")
	return command
}

func (executor *executor) repositoryMaterializeCommand() *cobra.Command {
	plan := false
	applyPlanID := ""
	verifyOperationID := ""
	options := app.WorkspaceMaterializeOptions{}
	command := &cobra.Command{
		Use: "materialize", Short: "Materialize one device-selected checkout through an exact local plan",
		Args: cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				if selectedModes(plan, applyPlanID, verifyOperationID) != 1 {
					return domain.NewEnvelope("gds repository materialize", domain.ExitInput, nil, domain.Finding{
						Code: "GDS_WORKSPACE_MATERIALIZATION_MODE_REQUIRED", Severity: domain.SeverityHigh,
						Message: "Use exactly one of --plan, --apply, or --verify.",
					})
				}
				if !plan && (child.Flags().Changed("anchor") || child.Flags().Changed("device") ||
					child.Flags().Changed("source")) {
					return domain.NewEnvelope("gds repository materialize", domain.ExitInput, nil, domain.Finding{
						Code: "GDS_WORKSPACE_MATERIALIZATION_INPUT_CONFLICT", Severity: domain.SeverityHigh,
						Message: "Anchor, device, and source are planning inputs and cannot alter a stored plan.",
					})
				}
				switch {
				case plan:
					return executor.services.PlanWorkspaceMaterialize(ctx, options)
				case applyPlanID != "":
					return executor.services.ApplyWorkspaceMaterialize(ctx, applyPlanID, options)
				default:
					return executor.services.VerifyWorkspaceMaterialize(ctx, verifyOperationID, options)
				}
			})
		},
	}
	command.Flags().BoolVar(&plan, "plan", false, "store an exact side-effect-free checkout plan")
	command.Flags().StringVar(&applyPlanID, "apply", "", "apply one exact materialization plan id")
	command.Flags().StringVar(&verifyOperationID, "verify", "", "verify one exact materialization operation id")
	command.Flags().StringVar(&options.AnchorPath, "anchor", "", "schema-validated repository anchor candidate")
	command.Flags().StringVar(&options.DevicePath, "device", "", "schema-validated device descriptor")
	command.Flags().StringVar(&options.SourcePath, "source", "", "isolated local bare repository source")
	command.Flags().StringVar(&options.StatePath, "state-path", "", "local GDS state database path")
	command.Flags().StringVar(&options.DeviceID, "device-id", "", "canonical current device identity")
	command.Flags().StringVar(&options.SessionID, "session-id", "", "bounded non-secret session identity")
	command.Flags().StringVar(&options.ApprovalReference, "approval-ref", "", "signed exact-plan approval JSON file")
	return command
}

func (executor *executor) workspaceCommand() *cobra.Command {
	command := &cobra.Command{
		Use: "workspace", Short: "Resolve portable device workspace intent",
		Args: cobra.NoArgs,
	}
	options := app.WorkspacePlacementOptions{}
	plan := &cobra.Command{
		Use: "plan", Short: "Resolve one repository to its exact device placement without mutation",
		Args: cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				return executor.services.ResolveWorkspacePlacement(ctx, options)
			})
		},
	}
	plan.Flags().StringVar(&options.AnchorPath, "anchor", "", "schema-validated repository anchor candidate")
	plan.Flags().StringVar(&options.DevicePath, "device", "", "schema-validated device descriptor")
	auditOptions := app.WorkspaceAuditOptions{}
	audit := &cobra.Command{
		Use: "audit", Short: "Audit standalone and embedded repository placement without mutation",
		Args: cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				return executor.services.AuditWorkspaceLayout(ctx, auditOptions)
			})
		},
	}
	audit.Flags().StringSliceVar(&auditOptions.Roots, "root", nil, "filesystem root to inspect; repeat for disjoint roots")
	audit.Flags().StringVar(&auditOptions.DevicePath, "device", "", "schema-validated device descriptor")
	audit.Flags().IntVar(&auditOptions.MaxDepth, "max-depth", 8, "maximum directory depth")
	audit.Flags().IntVar(&auditOptions.MaxRepositories, "max-repositories", 2000, "total repository count limit")
	audit.Flags().IntVar(&auditOptions.Concurrency, "concurrency", 4, "bounded Git inspection workers")
	command.AddCommand(plan, audit, executor.workspaceRegisterEstateCommand())
	return command
}

func (executor *executor) workspaceRegisterEstateCommand() *cobra.Command {
	plan := false
	applyPlanID := ""
	verifyOperationID := ""
	options := app.EstateRegistrationOptions{}
	command := &cobra.Command{
		Use: "register-estate", Short: "Register the trusted control-plane locator on this device",
		Args: cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				if selectedModes(plan, applyPlanID, verifyOperationID) != 1 {
					return domain.NewEnvelope("gds workspace register-estate", domain.ExitInput, nil, domain.Finding{
						Code: "GDS_ESTATE_REGISTRATION_MODE_REQUIRED", Severity: domain.SeverityHigh,
						Message: "Use exactly one of --plan, --apply, or --verify.",
					})
				}
				if !plan && (child.Flags().Changed("estate-root") || child.Flags().Changed("registration-path")) {
					return domain.NewEnvelope("gds workspace register-estate", domain.ExitInput, nil, domain.Finding{
						Code: "GDS_ESTATE_REGISTRATION_INPUT_CONFLICT", Severity: domain.SeverityHigh,
						Message: "Estate root and registration path are planning inputs and cannot alter a stored plan.",
					})
				}
				switch {
				case plan:
					return executor.services.PlanEstateRegistration(ctx, executor.options.cwd, options)
				case applyPlanID != "":
					return executor.services.ApplyEstateRegistration(ctx, applyPlanID, options)
				default:
					return executor.services.VerifyEstateRegistration(ctx, verifyOperationID, options)
				}
			})
		},
	}
	command.Flags().BoolVar(&plan, "plan", false, "store an exact side-effect-free registration plan")
	command.Flags().StringVar(&applyPlanID, "apply", "", "apply one exact registration plan id")
	command.Flags().StringVar(&verifyOperationID, "verify", "", "verify one exact registration operation id")
	command.Flags().StringVar(&options.EstateRoot, "estate-root", "", "control-plane root; defaults to --cwd")
	command.Flags().StringVar(&options.RegistrationPath, "registration-path", "", "absolute device-local registry path")
	command.Flags().StringVar(&options.StatePath, "state-path", "", "local GDS state database path")
	command.Flags().StringVar(&options.DeviceID, "device-id", "", "canonical current device identity")
	command.Flags().StringVar(&options.SessionID, "session-id", "", "bounded non-secret session identity")
	command.Flags().StringVar(&options.ApprovalReference, "approval-ref", "", "signed exact-plan approval JSON file")
	return command
}

func (executor *executor) repositoryTransitionCommand(operation string) *cobra.Command {
	plan := false
	applyPlanID := ""
	verifyOperationID := ""
	options := app.RepositoryTransitionOptions{}
	command := &cobra.Command{
		Use: operation, Short: operation + " one repository through an immutable provider-first plan",
		Args: cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				if selectedModes(plan, applyPlanID, verifyOperationID) != 1 {
					return domain.NewEnvelope("gds repository "+operation, domain.ExitInput, nil, domain.Finding{
						Code: "GDS_REPOSITORY_TRANSITION_MODE_REQUIRED", Severity: domain.SeverityHigh,
						Message: "Use exactly one of --plan, --apply, or --verify.",
					})
				}
				if !plan && child.Flags().Changed("anchor") {
					return domain.NewEnvelope("gds repository "+operation, domain.ExitInput, nil, domain.Finding{
						Code: "GDS_REPOSITORY_TRANSITION_INPUT_CONFLICT", Severity: domain.SeverityHigh,
						Message: "--anchor is a planning input and cannot alter a stored plan.",
					})
				}
				switch {
				case plan:
					return executor.services.PlanRepositoryTransition(
						ctx, executor.options.cwd, operation, options,
					)
				case applyPlanID != "":
					return executor.services.ApplyRepositoryTransition(
						ctx, executor.options.cwd, operation, applyPlanID, options,
					)
				default:
					return executor.services.VerifyRepositoryTransition(
						ctx, executor.options.cwd, operation, verifyOperationID, options,
					)
				}
			})
		},
	}
	command.Flags().BoolVar(&plan, "plan", false, "store an exact side-effect-free provider transition plan")
	command.Flags().StringVar(&applyPlanID, "apply", "", "apply one exact provider transition plan id")
	command.Flags().StringVar(&verifyOperationID, "verify", "", "verify one exact provider operation id")
	command.Flags().StringVar(&options.AnchorPath, "anchor", "", "schema-validated target repository anchor")
	command.Flags().StringVar(&options.StatePath, "state-path", "", "local GDS state database path")
	command.Flags().StringVar(&options.DeviceID, "device-id", "", "canonical current device identity")
	command.Flags().StringVar(&options.SessionID, "session-id", "", "bounded non-secret session identity")
	command.Flags().StringVar(&options.ApprovalReference, "approval-ref", "", "signed exact-plan approval JSON file")
	addGitHubReadFlags(command, &options.GitHubReadOptions)
	command.Flags().StringVar(
		&options.MutationRuntimeConfig, "mutation-runtime-config", "",
		"private device-local GitHub mutation runtime YAML/JSON",
	)
	return command
}

func (executor *executor) repositoryDeleteCommand() *cobra.Command {
	plan := false
	applyPlanID := ""
	verifyOperationID := ""
	options := app.RepositoryDeleteOptions{MaxDepth: 8, MaxRepositories: 2000, Concurrency: 4}
	command := &cobra.Command{
		Use: "delete", Short: "Plan a separately gated provider deletion after complete relationship analysis",
		Args: cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				if selectedModes(plan, applyPlanID, verifyOperationID) != 1 {
					return domain.NewEnvelope("gds repository delete", domain.ExitInput, nil, domain.Finding{
						Code: "GDS_REPOSITORY_DELETE_MODE_REQUIRED", Severity: domain.SeverityHigh,
						Message: "Use exactly one of --plan, --apply, or --verify.",
					})
				}
				if !plan && (child.Flags().Changed("inventory-root") ||
					child.Flags().Changed("confirm-repository-id") || child.Flags().Changed("confirm-provider-id")) {
					return domain.NewEnvelope("gds repository delete", domain.ExitInput, nil, domain.Finding{
						Code: "GDS_REPOSITORY_DELETE_INPUT_CONFLICT", Severity: domain.SeverityHigh,
						Message: "Inventory and confirmation values are planning inputs and cannot alter a stored plan.",
					})
				}
				switch {
				case plan:
					return executor.services.PlanRepositoryDelete(ctx, executor.options.cwd, options)
				case applyPlanID != "":
					return executor.services.ApplyRepositoryDelete(
						ctx, executor.options.cwd, applyPlanID, options,
					)
				default:
					return executor.services.VerifyRepositoryDelete(
						ctx, executor.options.cwd, verifyOperationID, options,
					)
				}
			})
		},
	}
	command.Flags().BoolVar(&plan, "plan", false, "store an exact side-effect-free deletion plan")
	command.Flags().StringVar(&applyPlanID, "apply", "", "apply one exact deletion plan id")
	command.Flags().StringVar(&verifyOperationID, "verify", "", "verify one exact deletion operation id")
	command.Flags().StringVar(&options.InventoryRoot, "inventory-root", "", "complete local Git-boundary analysis root")
	command.Flags().IntVar(&options.MaxDepth, "max-depth", 8, "maximum inventory directory depth")
	command.Flags().IntVar(&options.MaxRepositories, "max-repositories", 2000, "hard inventory repository count limit")
	command.Flags().IntVar(&options.Concurrency, "concurrency", 4, "bounded inventory inspection workers")
	command.Flags().StringArrayVar(&options.PreserveIdentities, "preserve", nil,
		"exact retirement identity the operator accepts losing; repeatable, never a blanket override")
	command.Flags().StringVar(&options.ConfirmRepositoryID, "confirm-repository-id", "", "exact stable GDS repository identity")
	command.Flags().StringVar(&options.ConfirmProviderID, "confirm-provider-id", "", "exact immutable provider repository id")
	command.Flags().StringVar(&options.StatePath, "state-path", "", "local GDS state database path")
	command.Flags().StringVar(&options.DeviceID, "device-id", "", "canonical current device identity")
	command.Flags().StringVar(&options.SessionID, "session-id", "", "bounded non-secret session identity")
	command.Flags().StringVar(&options.ApprovalReference, "approval-ref", "", "signed exact-plan approval JSON file")
	command.Flags().StringVar(
		&options.RuntimeConfig, "runtime-config", "", "private device-local GitHub runtime YAML/JSON; defaults to XDG config",
	)
	command.Flags().StringVar(
		&options.EstateRoot, "estate-root", "", "control-plane root containing canonical estate intent",
	)
	command.Flags().StringVar(
		&options.MutationRuntimeConfig, "mutation-runtime-config", "",
		"private device-local GitHub mutation runtime YAML/JSON",
	)
	return command
}

func (executor *executor) forkCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "fork",
		Short: "Inspect and manage declared fork lifecycle",
		Args:  cobra.NoArgs,
	}
	command.AddCommand(&cobra.Command{
		Use:   "inspect",
		Short: "Validate origin/upstream identity and compare cached refs",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				return executor.services.InspectFork(ctx, executor.options.cwd)
			})
		},
	})
	command.AddCommand(
		executor.forkSyncCommand(), executor.forkDetachCommand(), executor.forkArchiveCommand(),
	)
	return command
}

func (executor *executor) forkDetachCommand() *cobra.Command {
	plan := false
	applyPlanID := ""
	verifyOperationID := ""
	options := app.ForkDetachOptions{}
	command := &cobra.Command{
		Use: "detach", Short: "Detach one fork while preserving its upstream identity history",
		Args: cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				if selectedModes(plan, applyPlanID, verifyOperationID) != 1 {
					return domain.NewEnvelope("gds fork detach", domain.ExitInput, nil, domain.Finding{
						Code: "GDS_FORK_DETACH_MODE_REQUIRED", Severity: domain.SeverityHigh,
						Message: "Use exactly one of --plan, --apply, or --verify.",
					})
				}
				if !plan && child.Flags().Changed("anchor") {
					return domain.NewEnvelope("gds fork detach", domain.ExitInput, nil, domain.Finding{
						Code: "GDS_FORK_DETACH_INPUT_CONFLICT", Severity: domain.SeverityHigh,
						Message: "--anchor is a planning input and cannot alter a stored plan.",
					})
				}
				switch {
				case plan:
					return executor.services.PlanForkDetach(ctx, executor.options.cwd, options)
				case applyPlanID != "":
					return executor.services.ApplyForkDetach(ctx, applyPlanID, options)
				default:
					return executor.services.VerifyForkDetach(ctx, verifyOperationID, options)
				}
			})
		},
	}
	command.Flags().BoolVar(&plan, "plan", false, "store an exact side-effect-free fork detach plan")
	command.Flags().StringVar(&applyPlanID, "apply", "", "apply one exact fork detach plan id")
	command.Flags().StringVar(&verifyOperationID, "verify", "", "verify one exact fork detach operation id")
	command.Flags().StringVar(&options.AnchorPath, "anchor", "", "schema-validated detached fork anchor")
	command.Flags().StringVar(&options.StatePath, "state-path", "", "local GDS state database path")
	command.Flags().StringVar(&options.DeviceID, "device-id", "", "canonical current device identity")
	command.Flags().StringVar(&options.SessionID, "session-id", "", "bounded non-secret session identity")
	command.Flags().StringVar(&options.ApprovalReference, "approval-ref", "", "signed exact-plan approval JSON file")
	return command
}

func (executor *executor) forkArchiveCommand() *cobra.Command {
	plan := false
	applyPlanID := ""
	verifyOperationID := ""
	options := app.RepositoryTransitionOptions{}
	command := &cobra.Command{
		Use: "archive", Short: "Archive one declared fork through a provider-first plan",
		Args: cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				if selectedModes(plan, applyPlanID, verifyOperationID) != 1 {
					return domain.NewEnvelope("gds fork archive", domain.ExitInput, nil, domain.Finding{
						Code: "GDS_FORK_ARCHIVE_MODE_REQUIRED", Severity: domain.SeverityHigh,
						Message: "Use exactly one of --plan, --apply, or --verify.",
					})
				}
				if !plan && child.Flags().Changed("anchor") {
					return domain.NewEnvelope("gds fork archive", domain.ExitInput, nil, domain.Finding{
						Code: "GDS_FORK_ARCHIVE_INPUT_CONFLICT", Severity: domain.SeverityHigh,
						Message: "--anchor is a planning input and cannot alter a stored plan.",
					})
				}
				switch {
				case plan:
					return executor.services.PlanForkArchive(ctx, executor.options.cwd, options)
				case applyPlanID != "":
					return executor.services.ApplyRepositoryTransition(
						ctx, executor.options.cwd, "archive", applyPlanID, options,
					)
				default:
					return executor.services.VerifyRepositoryTransition(
						ctx, executor.options.cwd, "archive", verifyOperationID, options,
					)
				}
			})
		},
	}
	command.Flags().BoolVar(&plan, "plan", false, "store an exact side-effect-free fork archive plan")
	command.Flags().StringVar(&applyPlanID, "apply", "", "apply one exact fork archive plan id")
	command.Flags().StringVar(&verifyOperationID, "verify", "", "verify one exact fork archive operation id")
	command.Flags().StringVar(&options.AnchorPath, "anchor", "", "schema-validated archived fork anchor")
	command.Flags().StringVar(&options.StatePath, "state-path", "", "local GDS state database path")
	command.Flags().StringVar(&options.DeviceID, "device-id", "", "canonical current device identity")
	command.Flags().StringVar(&options.SessionID, "session-id", "", "bounded non-secret session identity")
	command.Flags().StringVar(&options.ApprovalReference, "approval-ref", "", "signed exact-plan approval JSON file")
	addGitHubReadFlags(command, &options.GitHubReadOptions)
	command.Flags().StringVar(
		&options.MutationRuntimeConfig, "mutation-runtime-config", "",
		"private device-local GitHub mutation runtime YAML/JSON",
	)
	return command
}

func (executor *executor) identityCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "identity",
		Short: "Generate typed GDS identities without persisting them",
		Args:  cobra.NoArgs,
	}
	command.AddCommand(&cobra.Command{
		Use:   "new <kind>",
		Short: "Generate one cryptographically random typed ULID",
		Args:  cobra.ExactArgs(1),
		RunE: func(child *cobra.Command, arguments []string) error {
			return executor.run(child, func(context.Context) domain.Envelope {
				return executor.services.NewIdentity(arguments[0])
			})
		},
	})
	return command
}

func (executor *executor) githubCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "github",
		Short: "Inspect GitHub provider capability without loading credentials by default",
		Args:  cobra.NoArgs,
	}
	command.AddCommand(&cobra.Command{
		Use:   "doctor",
		Short: "Report pinned provider contracts and live-runtime evidence gaps",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(context.Context) domain.Envelope {
				return executor.services.GitHubDoctor()
			})
		},
	})
	options := app.GitHubReadOptions{}
	inventory := &cobra.Command{
		Use:   "inventory",
		Short: "Read one current GitHub App installation inventory without provider mutations",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				return executor.services.GitHubInventory(ctx, executor.options.cwd, options)
			})
		},
	}
	addGitHubReadFlags(inventory, &options)
	inventory.Flags().StringVar(
		&options.InstallationID, "installation", "", "exact logical estate installation id",
	)
	command.AddCommand(inventory)
	governanceOptions := app.GitHubGovernanceOperationOptions{}
	governancePlan := false
	governanceApply := ""
	governanceVerify := ""
	governance := &cobra.Command{
		Use:   "governance",
		Short: "Inspect or reconcile one repository governance contract",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				modes := selectedModes(governancePlan, governanceApply, governanceVerify)
				if modes > 1 {
					return domain.NewEnvelope("gds github governance", domain.ExitInput, nil, domain.Finding{
						Code: "GDS_GITHUB_GOVERNANCE_MODE_CONFLICT", Severity: domain.SeverityHigh,
						Message: "Use at most one of --plan, --apply, or --verify.",
					})
				}
				if modes != 0 && child.Flags().Changed("compare-local") {
					return domain.NewEnvelope("gds github governance", domain.ExitInput, nil, domain.Finding{
						Code: "GDS_GITHUB_GOVERNANCE_INPUT_CONFLICT", Severity: domain.SeverityHigh,
						Message: "--compare-local belongs to read-only inspection; operation modes compile local policy automatically.",
					})
				}
				switch {
				case governancePlan:
					return executor.services.PlanGitHubGovernance(ctx, executor.options.cwd, governanceOptions)
				case governanceApply != "":
					return executor.services.ApplyGitHubGovernance(ctx, executor.options.cwd, governanceApply, governanceOptions)
				case governanceVerify != "":
					return executor.services.VerifyGitHubGovernance(ctx, executor.options.cwd, governanceVerify, governanceOptions)
				default:
					return executor.services.GitHubGovernance(
						ctx, executor.options.cwd, governanceOptions.GitHubGovernanceOptions,
					)
				}
			})
		},
	}
	addGitHubReadFlags(governance, &governanceOptions.GitHubReadOptions)
	governance.Flags().StringVar(
		&governanceOptions.InstallationID,
		"installation", "", "exact logical estate installation id",
	)
	governance.Flags().StringVar(
		&governanceOptions.Owner, "owner", "", "exact GitHub repository owner",
	)
	governance.Flags().StringVar(
		&governanceOptions.Repository, "repository", "", "exact GitHub repository name",
	)
	governance.Flags().BoolVar(
		&governanceOptions.CompareLocal, "compare-local", false,
		"compare the observation with the exact current local repository policy",
	)
	governance.Flags().BoolVar(&governancePlan, "plan", false, "store an exact side-effect-free governance plan")
	governance.Flags().StringVar(&governanceApply, "apply", "", "apply one exact governance plan id")
	governance.Flags().StringVar(&governanceVerify, "verify", "", "verify one exact governance operation id")
	governance.Flags().StringVar(&governanceOptions.MutationRuntimeConfig, "mutation-runtime-config", "", "private device-local GitHub mutation runtime YAML/JSON")
	governance.Flags().StringVar(&governanceOptions.StatePath, "state-path", "", "local GDS state database path")
	governance.Flags().StringVar(&governanceOptions.DeviceID, "device-id", "", "canonical current device identity")
	governance.Flags().StringVar(&governanceOptions.SessionID, "session-id", "", "bounded non-secret session identity")
	governance.Flags().StringVar(&governanceOptions.ApprovalReference, "approval-ref", "", "signed exact-plan approval JSON file")
	command.AddCommand(governance)

	rulesetOptions := app.GitHubGovernanceOperationOptions{}
	rulesetPlan := false
	rulesetApply := ""
	rulesetVerify := ""
	ruleset := &cobra.Command{
		Use:   "ruleset",
		Short: "Reconcile the default-branch ruleset with the tracked contract",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				if selectedModes(rulesetPlan, rulesetApply, rulesetVerify) > 1 {
					return domain.NewEnvelope("gds github ruleset", domain.ExitInput, nil, domain.Finding{
						Code: "GDS_GITHUB_RULESET_MODE_CONFLICT", Severity: domain.SeverityHigh,
						Message: "Use at most one of --plan, --apply, or --verify.",
					})
				}
				switch {
				case rulesetApply != "":
					return executor.services.ApplyGitHubRuleset(ctx, executor.options.cwd, rulesetApply, rulesetOptions)
				case rulesetVerify != "":
					return executor.services.VerifyGitHubRuleset(ctx, executor.options.cwd, rulesetVerify, rulesetOptions)
				default:
					// Planning is side-effect free, so it is also the read-only
					// drift report: it observes live state, compares the owned
					// fields, and stores a plan only when they differ.
					return executor.services.PlanGitHubRuleset(ctx, executor.options.cwd, rulesetOptions)
				}
			})
		},
	}
	addGitHubReadFlags(ruleset, &rulesetOptions.GitHubReadOptions)
	ruleset.Flags().StringVar(&rulesetOptions.InstallationID, "installation", "", "exact logical estate installation id")
	ruleset.Flags().StringVar(&rulesetOptions.Owner, "owner", "", "exact GitHub repository owner")
	ruleset.Flags().StringVar(&rulesetOptions.Repository, "repository", "", "exact GitHub repository name")
	ruleset.Flags().BoolVar(&rulesetPlan, "plan", false, "store an exact side-effect-free ruleset plan")
	ruleset.Flags().StringVar(&rulesetApply, "apply", "", "apply one exact ruleset plan id")
	ruleset.Flags().StringVar(&rulesetVerify, "verify", "", "verify one exact ruleset operation id")
	ruleset.Flags().StringVar(&rulesetOptions.MutationRuntimeConfig, "mutation-runtime-config", "", "private device-local GitHub mutation runtime YAML/JSON")
	ruleset.Flags().StringVar(&rulesetOptions.StatePath, "state-path", "", "local GDS state database path")
	ruleset.Flags().StringVar(&rulesetOptions.DeviceID, "device-id", "", "canonical current device identity")
	ruleset.Flags().StringVar(&rulesetOptions.SessionID, "session-id", "", "bounded non-secret session identity")
	ruleset.Flags().StringVar(&rulesetOptions.ApprovalReference, "approval-ref", "", "signed exact-plan approval JSON file")
	command.AddCommand(ruleset)
	projectionOptions := app.GitHubProjectionOperationOptions{}
	projectionPlan := false
	projectionApply := ""
	projectionVerify := ""
	projection := &cobra.Command{
		Use:   "projection-pr",
		Short: "Publish exact generated projections through one immutable draft pull request",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				if selectedModes(projectionPlan, projectionApply, projectionVerify) != 1 {
					return domain.NewEnvelope("gds github projection-pr", domain.ExitInput, nil, domain.Finding{
						Code: "GDS_GITHUB_PROJECTION_MODE_REQUIRED", Severity: domain.SeverityHigh,
						Message: "Use exactly one of --plan, --apply, or --verify.",
					})
				}
				if projectionApply == "" && child.Flags().Changed("mutation-runtime-config") {
					return domain.NewEnvelope("gds github projection-pr", domain.ExitInput, nil, domain.Finding{
						Code: "GDS_GITHUB_PROJECTION_INPUT_CONFLICT", Severity: domain.SeverityHigh,
						Message: "--mutation-runtime-config is valid only with --apply.",
					})
				}
				switch {
				case projectionPlan:
					return executor.services.PlanGitHubProjection(ctx, executor.options.cwd, projectionOptions)
				case projectionApply != "":
					return executor.services.ApplyGitHubProjection(
						ctx, executor.options.cwd, projectionApply, projectionOptions,
					)
				default:
					return executor.services.VerifyGitHubProjection(
						ctx, executor.options.cwd, projectionVerify, projectionOptions,
					)
				}
			})
		},
	}
	projection.Flags().BoolVar(&projectionPlan, "plan", false, "store an exact side-effect-free projection PR plan")
	projection.Flags().StringVar(&projectionApply, "apply", "", "apply one exact projection PR plan id")
	projection.Flags().StringVar(&projectionVerify, "verify", "", "verify one exact projection PR operation id")
	addGitHubReadFlags(projection, &projectionOptions.GitHubReadOptions)
	projection.Flags().StringVar(&projectionOptions.MutationRuntimeConfig, "mutation-runtime-config", "", "private device-local GitHub mutation runtime YAML/JSON")
	projection.Flags().StringVar(&projectionOptions.StatePath, "state-path", "", "local GDS state database path")
	projection.Flags().StringVar(&projectionOptions.DeviceID, "device-id", "", "canonical current device identity")
	projection.Flags().StringVar(&projectionOptions.SessionID, "session-id", "", "bounded non-secret session identity")
	projection.Flags().StringVar(&projectionOptions.ApprovalReference, "approval-ref", "", "signed exact-plan approval JSON file")
	command.AddCommand(projection)
	return command
}

func (executor *executor) reconcileCommand() *cobra.Command {
	plan := false
	options := app.GitHubReadOptions{}
	command := &cobra.Command{
		Use:   "reconcile",
		Short: "Compare current GitHub installation inventories with desired estate intent",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				if !plan {
					return domain.NewEnvelope("gds reconcile", domain.ExitInput, nil, domain.Finding{
						Code: "GDS_RECONCILE_PLAN_REQUIRED", Severity: domain.SeverityHigh,
						Message: "Use --plan for side-effect-free estate reconciliation.",
					})
				}
				return executor.services.ReconcileGitHub(ctx, executor.options.cwd, options)
			})
		},
	}
	command.Flags().BoolVar(&plan, "plan", false, "build a side-effect-free reconciliation plan")
	addGitHubReadFlags(command, &options)
	return command
}

func addGitHubReadFlags(command *cobra.Command, options *app.GitHubReadOptions) {
	command.Flags().StringVar(
		&options.RuntimeConfig, "runtime-config", "", "private device-local GitHub runtime YAML/JSON; defaults to XDG config",
	)
	command.Flags().StringVar(
		&options.EstateRoot, "estate-root", "", "control-plane root containing canonical estate intent",
	)
	command.Flags().IntVar(
		&options.MaxRepositories, "max-repositories", 0,
		"optional repository limit that can only narrow the runtime safety bound",
	)
}

func (executor *executor) reportCommand() *cobra.Command {
	command := &cobra.Command{
		Use:   "report",
		Short: "Render scoped evidence reports without applying remediation",
		Args:  cobra.NoArgs,
	}
	estateOptions := app.GitHubReadOptions{}
	estateSummary := &cobra.Command{
		Use:   "estate-summary",
		Short: "Summarize current provider inventory and desired classification",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				return executor.services.ReportEstateSummary(ctx, executor.options.cwd, estateOptions)
			})
		},
	}
	addGitHubReadFlags(estateSummary, &estateOptions)
	driftOptions := app.GitHubReadOptions{}
	drift := &cobra.Command{
		Use:   "drift",
		Short: "Report current estate drift without remediation",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				return executor.services.ReportDrift(ctx, executor.options.cwd, driftOptions)
			})
		},
	}
	addGitHubReadFlags(drift, &driftOptions)
	asOf := ""
	sourceFreshness := &cobra.Command{
		Use:   "source-freshness",
		Short: "Report official-source review and evidence freshness",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				return executor.services.ReportSourceFreshness(ctx, executor.options.cwd, asOf)
			})
		},
	}
	sourceFreshness.Flags().StringVar(&asOf, "as-of", "", "evaluation date in YYYY-MM-DD")
	harnessCompatibility := &cobra.Command{
		Use:   "harness-compatibility",
		Short: "Report static and observed runtime harness compatibility",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				return executor.services.ReportHarnessCompatibility(ctx, executor.options.cwd)
			})
		},
	}
	security := &cobra.Command{
		Use:   "security",
		Short: "Report tracked-source security and visibility findings",
		Args:  cobra.NoArgs,
		RunE: func(child *cobra.Command, _ []string) error {
			return executor.run(child, func(ctx context.Context) domain.Envelope {
				return executor.services.ReportSecurity(ctx, executor.options.cwd)
			})
		},
	}
	command.AddCommand(estateSummary, drift, sourceFreshness, harnessCompatibility, security)
	return command
}

func (executor *executor) doctorCommand() *cobra.Command {
	fixtures := true
	command := &cobra.Command{
		Use:   "doctor",
		Short: "Run aggregate read-only context, Git, and contract diagnostics",
		Args:  cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return executor.run(command, func(ctx context.Context) domain.Envelope {
				return executor.services.Doctor(ctx, executor.options.cwd, fixtures)
			})
		},
	}
	command.Flags().BoolVar(&fixtures, "fixtures", true, "validate fixture corpus when present")
	return command
}

func (executor *executor) run(
	command *cobra.Command,
	operation func(context.Context) domain.Envelope,
) error {
	if executor.options.timeout <= 0 {
		envelope := domain.NewEnvelope("gds "+command.Name(), domain.ExitInput, nil, domain.Finding{
			Code:     "GDS_TIMEOUT_INVALID",
			Severity: domain.SeverityHigh,
			Message:  "--timeout must be greater than zero.",
		})
		executor.result = &envelope
		return nil
	}
	ctx, cancel := context.WithTimeout(command.Context(), executor.options.timeout)
	defer cancel()
	envelope := operation(ctx)
	executor.result = &envelope
	return nil
}

func render(
	envelope domain.Envelope,
	jsonOutput bool,
	stdout io.Writer,
	stderr io.Writer,
) error {
	if jsonOutput {
		encoder := json.NewEncoder(stdout)
		encoder.SetEscapeHTML(false)
		encoder.SetIndent("", "  ")
		return encoder.Encode(envelope)
	}
	target := stdout
	if envelope.ExitCode != 0 {
		target = stderr
	}
	if _, err := fmt.Fprintf(
		target, "%s: %s (exit %d)\n", envelope.Command, envelope.Result, envelope.ExitCode,
	); err != nil {
		return err
	}
	for _, finding := range envelope.Findings {
		if _, err := fmt.Fprintf(
			target, "- [%s] %s: %s\n", finding.Severity, finding.Code, finding.Message,
		); err != nil {
			return err
		}
	}
	if envelope.Data != nil {
		raw, err := json.MarshalIndent(envelope.Data, "", "  ")
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(target, "%s\n", raw); err != nil {
			return err
		}
	}
	return nil
}

func containsJSONFlag(args []string) bool {
	for _, argument := range args {
		if argument == "--json" ||
			(strings.HasPrefix(argument, "--json=") && argument != "--json=false") {
			return true
		}
	}
	return false
}
