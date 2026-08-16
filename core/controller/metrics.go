package controller

import (
	"bytes"
	"fmt"
	"sync/atomic"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
)

type Metrics struct {
	WebhookRequests         atomic.Uint64
	WebhookAccepted         atomic.Uint64
	WebhookRejected         atomic.Uint64
	WorkerSucceeded         atomic.Uint64
	WorkerFailed            atomic.Uint64
	WorkerDeadLetter        atomic.Uint64
	ReconciliationSucceeded atomic.Uint64
	ReconciliationPartial   atomic.Uint64
	ReconciliationFailed    atomic.Uint64
	BackupSucceeded         atomic.Uint64
	BackupFailed            atomic.Uint64
	LastReconciliationUnix  atomic.Int64
	LastBackupUnix          atomic.Int64
}

func (metrics *Metrics) Prometheus() []byte {
	return metrics.PrometheusWithState(state.WebhookQueueSummary{}, state.Summary{})
}

func (metrics *Metrics) PrometheusWithState(
	queue state.WebhookQueueSummary,
	summary state.Summary,
) []byte {
	if metrics == nil {
		return []byte{}
	}
	values := []struct {
		name  string
		value uint64
	}{
		{"gds_webhook_requests_total", metrics.WebhookRequests.Load()},
		{"gds_webhook_accepted_total", metrics.WebhookAccepted.Load()},
		{"gds_webhook_rejected_total", metrics.WebhookRejected.Load()},
		{"gds_webhook_worker_succeeded_total", metrics.WorkerSucceeded.Load()},
		{"gds_webhook_worker_failed_total", metrics.WorkerFailed.Load()},
		{"gds_webhook_dead_letter_total", metrics.WorkerDeadLetter.Load()},
		{"gds_reconciliation_succeeded_total", metrics.ReconciliationSucceeded.Load()},
		{"gds_reconciliation_partial_total", metrics.ReconciliationPartial.Load()},
		{"gds_reconciliation_failed_total", metrics.ReconciliationFailed.Load()},
		{"gds_backup_succeeded_total", metrics.BackupSucceeded.Load()},
		{"gds_backup_failed_total", metrics.BackupFailed.Load()},
	}
	var output bytes.Buffer
	for _, value := range values {
		_, _ = fmt.Fprintf(&output, "%s %d\n", value.name, value.value)
	}
	_, _ = fmt.Fprintf(
		&output, "gds_last_reconciliation_unixtime %d\n", metrics.LastReconciliationUnix.Load(),
	)
	_, _ = fmt.Fprintf(&output, "gds_last_backup_unixtime %d\n", metrics.LastBackupUnix.Load())
	_, _ = fmt.Fprintf(&output, "gds_webhook_queue_queued %d\n", queue.Queued)
	_, _ = fmt.Fprintf(&output, "gds_webhook_queue_processing %d\n", queue.Processing)
	_, _ = fmt.Fprintf(&output, "gds_webhook_queue_failed %d\n", queue.Failed)
	_, _ = fmt.Fprintf(&output, "gds_webhook_queue_dead_letter %d\n", queue.DeadLetter)
	_, _ = fmt.Fprintf(&output, "gds_repository_observations %d\n", summary.Observations)
	_, _ = fmt.Fprintf(&output, "gds_reconciliation_journals %d\n", summary.Reconciliations)
	_, _ = fmt.Fprintf(&output, "gds_telemetry_outbox_pending %d\n", summary.TelemetryPending)
	_, _ = fmt.Fprintf(&output, "gds_telemetry_outbox_sent %d\n", summary.TelemetrySent)
	_, _ = fmt.Fprintf(&output, "gds_telemetry_outbox_dropped %d\n", summary.TelemetryDropped)
	return output.Bytes()
}
