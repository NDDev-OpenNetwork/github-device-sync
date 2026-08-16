package contextresolver

import (
	"context"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/compiler"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/projections"
)

// CanonicalPolicyProver independently reconstructs development projections
// from committed estate sources instead of treating an applied lock as its own
// trust root.
type CanonicalPolicyProver struct {
	git       committedSourceReader
	compiler  *compiler.Compiler
	projector *projections.Generator
}

type committedSourceReader interface {
	CommittedSourceOID(context.Context, string, []string) (string, error)
	SourceTreeDigest(context.Context, string, []string) (string, error)
}

func NewCanonicalPolicyProver(
	git committedSourceReader,
	policyCompiler *compiler.Compiler,
	projector *projections.Generator,
) *CanonicalPolicyProver {
	return &CanonicalPolicyProver{git: git, compiler: policyCompiler, projector: projector}
}

func (prover *CanonicalPolicyProver) Verify(
	ctx context.Context,
	repositoryRoot string,
	estateRoot string,
	anchor domain.RepositoryAnchor,
	document bundleLockDocument,
) []domain.Finding {
	if prover == nil || prover.git == nil || prover.compiler == nil || prover.projector == nil {
		return []domain.Finding{policyProvenanceFinding(
			"GDS_CONTEXT_POLICY_PROVENANCE_UNAVAILABLE",
			"Canonical policy provenance dependencies are unavailable.",
			repositoryRoot,
			nil,
		)}
	}
	if document.Bundle.Channel != "development" ||
		document.Bundle.Version != compiler.DevelopmentBundleVersion {
		return []domain.Finding{policyProvenanceFinding(
			"GDS_CONTEXT_POLICY_PROVENANCE_NOT_PROVEN",
			"A non-development applied bundle requires independently verified release evidence.",
			repositoryRoot,
			nil,
		)}
	}
	if estateRoot == "" {
		return []domain.Finding{policyProvenanceFinding(
			"GDS_CONTEXT_POLICY_ESTATE_NOT_PROVEN",
			"A trusted estate root is required to reconstruct applied policy provenance.",
			repositoryRoot,
			nil,
		)}
	}
	if err := prover.projector.VerifyEmbeddedSources(estateRoot); err != nil {
		return []domain.Finding{policyProvenanceFinding(
			"GDS_CONTEXT_POLICY_EMBEDDED_TEMPLATE_MISMATCH",
			"The generator's embedded projection templates differ from the claimed canonical source.",
			estateRoot,
			err,
		)}
	}

	if _, err := prover.git.CommittedSourceOID(
		ctx, repositoryRoot, []string{".gds/repository.yaml"},
	); err != nil {
		return []domain.Finding{policyProvenanceFinding(
			"GDS_CONTEXT_POLICY_ANCHOR_NOT_COMMITTED",
			"The repository anchor must be committed before applied policy can be trusted.",
			repositoryRoot,
			err,
		)}
	}
	appliedPaths := make([]string, 0, len(document.Projection.Files)+1)
	appliedPaths = append(appliedPaths, ".gds/bundle.lock.yaml")
	for _, file := range document.Projection.Files {
		appliedPaths = append(appliedPaths, file.Path)
	}
	if _, err := prover.git.CommittedSourceOID(ctx, repositoryRoot, appliedPaths); err != nil {
		return []domain.Finding{policyProvenanceFinding(
			"GDS_CONTEXT_POLICY_PROJECTION_NOT_COMMITTED",
			"The applied policy lock and every managed projection must match committed Git state.",
			repositoryRoot,
			err,
		)}
	}

	// A v1 lock is identified by the commit that carried its sources, so that
	// commit must resolve for it to be verifiable at all. A content-addressed
	// lock does not need it, and requiring it there would reintroduce the very
	// dependency this contract removes.
	sourceOID, sourceOIDErr := prover.git.CommittedSourceOID(
		ctx, estateRoot, projections.DevelopmentBundleSourcePaths(),
	)
	if sourceOIDErr != nil && document.Bundle.SourceTreeDigest == "" {
		return []domain.Finding{policyProvenanceFinding(
			"GDS_CONTEXT_POLICY_SOURCE_NOT_COMMITTED",
			"Canonical estate policy sources must be committed before provenance reconstruction.",
			estateRoot,
			sourceOIDErr,
		)}
	}
	sourceTreeDigest, err := prover.git.SourceTreeDigest(
		ctx, estateRoot, projections.DevelopmentBundleSourcePaths(),
	)
	if err != nil {
		return []domain.Finding{policyProvenanceFinding(
			"GDS_CONTEXT_POLICY_SOURCE_DIGEST_UNAVAILABLE",
			"Canonical estate source content could not be digested.",
			estateRoot,
			err,
		)}
	}
	// The identity is the source content, not the commit that carries it. A
	// lock generated before this contract records no source_tree_digest; it is
	// still validated, by the commit it was actually built against, until it is
	// regenerated once.
	if document.Bundle.SourceTreeDigest == "" {
		if sourceOID != document.Bundle.SourceCommit {
			return []domain.Finding{domain.Finding{
				Code:     "GDS_CONTEXT_POLICY_SOURCE_COMMIT_MISMATCH",
				Severity: domain.SeverityHigh,
				Message:  "The applied bundle source commit differs from committed canonical estate sources.",
				Evidence: map[string]any{
					"estate_root": estateRoot,
					"expected":    sourceOID,
					"observed":    document.Bundle.SourceCommit,
				},
			}}
		}
	} else if sourceTreeDigest != document.Bundle.SourceTreeDigest {
		return []domain.Finding{domain.Finding{
			Code:     "GDS_CONTEXT_POLICY_SOURCE_DIGEST_MISMATCH",
			Severity: domain.SeverityHigh,
			Message:  "The applied bundle source digest differs from the canonical estate source content.",
			Evidence: map[string]any{
				"estate_root": estateRoot,
				"expected":    sourceTreeDigest,
				"observed":    document.Bundle.SourceTreeDigest,
			},
		}}
	}

	compiled := prover.compiler.CompileDirectory(
		estateRoot, anchor, compiler.DevelopmentBundleVersion,
	)
	if len(compiled.Findings) != 0 {
		return compiled.Findings
	}
	// A lock generated before the content contract is reconstructed the way it
	// was built, so it keeps verifying in full instead of being weakened to a
	// partial check while it waits to be regenerated once.
	var bundle projections.Bundle
	if document.Bundle.SourceTreeDigest == "" {
		bundle, err = prover.projector.DevelopmentBundleFromSourceCommit(compiled.Document, sourceOID)
	} else {
		bundle, err = prover.projector.DevelopmentBundle(compiled.Document, sourceOID, sourceTreeDigest)
	}
	if err != nil {
		return []domain.Finding{policyProvenanceFinding(
			"GDS_CONTEXT_POLICY_BUNDLE_RECONSTRUCTION_FAILED",
			"The canonical development bundle could not be reconstructed.",
			estateRoot,
			err,
		)}
	}
	// SourceCommit is trace metadata and deliberately not compared once the
	// bundle is content-addressed: the commit carrying identical sources may
	// legitimately differ, which is the whole point.
	if bundle.Version != document.Bundle.Version ||
		bundle.ReleaseSequence != document.Bundle.ReleaseSequence ||
		bundle.Channel != document.Bundle.Channel ||
		bundle.Digest != document.Bundle.Digest {
		return []domain.Finding{{
			Code:     "GDS_CONTEXT_POLICY_BUNDLE_MISMATCH",
			Severity: domain.SeverityHigh,
			Message:  "The applied bundle metadata differs from canonical estate reconstruction.",
			Evidence: map[string]any{
				"estate_root":     estateRoot,
				"expected_digest": bundle.Digest,
				"observed_digest": document.Bundle.Digest,
			},
		}}
	}
	candidate, findings := prover.projector.Generate(anchor, compiled.Document, bundle)
	if len(findings) != 0 {
		return findings
	}
	return projections.Verify(repositoryRoot, candidate)
}

func policyProvenanceFinding(
	code string,
	message string,
	root string,
	err error,
) domain.Finding {
	evidence := map[string]any{"root": root}
	if err != nil {
		evidence["error"] = err.Error()
	}
	return domain.Finding{
		Code: code, Severity: domain.SeverityHigh, Message: message, Evidence: evidence,
	}
}
