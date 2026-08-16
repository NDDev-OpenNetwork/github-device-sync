// Package gitops provides exact operation handlers for narrowly approved local
// Git mutations. Network refresh and worktree changes remain separate actions.
package gitops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
)

const MaterializeRecoveryRefAction = "materialize-recovery-ref"

type RecoveryRefEvidence struct {
	Reference string `json:"reference"`
	OID       string `json:"oid"`
}

type RecoveryRefHandler struct {
	root   string
	runner *gitprovider.MutationRunner
}

func NewRecoveryRefHandler(root string, runner *gitprovider.MutationRunner) (*RecoveryRefHandler, error) {
	if root == "" || runner == nil {
		return nil, errors.New("recovery ref handler requires a repository root and mutation runner")
	}
	return &RecoveryRefHandler{root: root, runner: runner}, nil
}

func (handler *RecoveryRefHandler) Apply(
	ctx context.Context,
	step operations.Step,
) (operations.ApplyEvidence, error) {
	reference, newOID, oldOID, err := parameters(step)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	observed, err := handler.runner.ObserveRecoveryRef(ctx, handler.root, reference)
	if err != nil {
		return operations.ApplyEvidence{}, err
	}
	before := RecoveryRefEvidence{Reference: reference, OID: observed}
	if observed != oldOID {
		return operations.ApplyEvidence{Before: before}, fmt.Errorf(
			"recovery ref precondition changed for %s", reference,
		)
	}
	if err := handler.runner.UpdateRecoveryRef(ctx, handler.root, reference, newOID, oldOID); err != nil {
		return operations.ApplyEvidence{Before: before}, err
	}
	after := RecoveryRefEvidence{Reference: reference, OID: newOID}
	return operations.ApplyEvidence{Before: before, After: after}, nil
}

func (handler *RecoveryRefHandler) Verify(
	ctx context.Context,
	step operations.Step,
	afterRaw json.RawMessage,
) error {
	reference, newOID, _, err := parameters(step)
	if err != nil {
		return err
	}
	var expected RecoveryRefEvidence
	if len(afterRaw) != 0 {
		if err := json.Unmarshal(afterRaw, &expected); err != nil {
			return fmt.Errorf("decode recovery ref evidence: %w", err)
		}
		if expected.Reference != reference || expected.OID != newOID {
			return errors.New("recovery ref after evidence differs from the exact step")
		}
	}
	observed, err := handler.runner.ObserveRecoveryRef(ctx, handler.root, reference)
	if err != nil {
		return err
	}
	if observed != newOID {
		return fmt.Errorf("recovery ref %s is %s, expected %s", reference, observed, newOID)
	}
	return nil
}

func parameters(step operations.Step) (string, string, string, error) {
	if step.Action != MaterializeRecoveryRefAction {
		return "", "", "", fmt.Errorf("unexpected local Git action %q", step.Action)
	}
	raw, ok := step.Parameters["recovery_ref"].(map[string]any)
	if !ok {
		return "", "", "", errors.New("recovery_ref parameters are missing")
	}
	reference, _ := raw["reference"].(string)
	newOID, _ := raw["new_oid"].(string)
	oldOID, _ := raw["expected_old_oid"].(string)
	if reference == "" || newOID == "" || oldOID == "" {
		return "", "", "", errors.New("recovery_ref parameters are incomplete")
	}
	return reference, newOID, oldOID, nil
}
