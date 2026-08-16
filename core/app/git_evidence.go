package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	gitprovider "github.com/NDDev-OpenNetwork/github-device-sync/core/providers/git"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/state"
)

type durableRemoteEvidence struct {
	Record         state.RemoteRefreshRecord
	Refs           []gitprovider.RemoteRef
	EvidenceDigest string
}

func (services *Services) durableOriginEvidence(
	ctx context.Context,
	store *state.Store,
	repositoryID string,
	worktreeRoot string,
	headOID string,
	maxAge time.Duration,
) (durableRemoteEvidence, string) {
	refs, err := services.GitMutations.RemoteTrackingRefs(ctx, worktreeRoot, "origin")
	if err != nil {
		return durableRemoteEvidence{}, "remote-refs-not-proven"
	}
	refsRaw, err := json.Marshal(refs)
	if err != nil {
		return durableRemoteEvidence{}, "remote-refs-not-proven"
	}
	refsDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(refsRaw))
	refresh, err := store.GetRemoteRefresh(ctx, repositoryID, worktreeRoot, "origin")
	if err != nil {
		if errors.Is(err, state.ErrNotFound) {
			return durableRemoteEvidence{}, "refresh-missing"
		}
		return durableRemoteEvidence{}, "refresh-not-proven"
	}
	now := services.Now().UTC()
	if refresh.ObservedAt.After(now.Add(time.Minute)) || now.Sub(refresh.ObservedAt) > maxAge {
		return durableRemoteEvidence{}, "refresh-stale"
	}
	if refresh.ForcedUpdate {
		return durableRemoteEvidence{}, "forced-update"
	}
	if refresh.HeadOID != headOID {
		return durableRemoteEvidence{}, "refresh-head-mismatch"
	}
	if refresh.RefsDigest != refsDigest {
		return durableRemoteEvidence{}, "remote-ref-drift"
	}
	evidenceDigest, err := canonicaljson.Digest(struct {
		RepositoryID string    `json:"repository_id"`
		WorktreeRoot string    `json:"worktree_root"`
		Remote       string    `json:"remote"`
		ObservedAt   time.Time `json:"observed_at"`
		HeadOID      string    `json:"head_oid"`
		RefsDigest   string    `json:"refs_digest"`
		ForcedUpdate bool      `json:"forced_update"`
	}{
		RepositoryID: refresh.RepositoryID, WorktreeRoot: refresh.WorktreeRoot,
		Remote: refresh.Remote, ObservedAt: refresh.ObservedAt,
		HeadOID: refresh.HeadOID, RefsDigest: refresh.RefsDigest,
		ForcedUpdate: refresh.ForcedUpdate,
	})
	if err != nil {
		return durableRemoteEvidence{}, "refresh-digest-not-proven"
	}
	return durableRemoteEvidence{
		Record: refresh, Refs: refs, EvidenceDigest: evidenceDigest,
	}, ""
}
