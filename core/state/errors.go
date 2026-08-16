package state

import "errors"

var (
	ErrNotFound        = errors.New("state record not found")
	ErrPlanConflict    = errors.New("plan identity already has different immutable content")
	ErrStateConflict   = errors.New("state transition precondition failed")
	ErrLockHeld        = errors.New("lock scope is already held")
	ErrLockOwnership   = errors.New("lock ownership or fencing token does not match")
	ErrReadOnly        = errors.New("state store is read-only")
	ErrWebhookConflict = errors.New("webhook delivery identity has conflicting payload")
	ErrBundleConflict  = errors.New("release sequence is already bound to different bundle evidence")
	ErrRollbackBlocked = errors.New("bundle sequence downgrade requires exact rollback authorization")
	ErrRolloutConflict = errors.New("rollout identity already has different immutable content")
)
