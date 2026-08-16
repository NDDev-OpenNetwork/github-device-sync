package source

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
	"time"

	"go.yaml.in/yaml/v4"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/serialization"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/validation"
)

const RegisterPath = registerPath

var evidenceReferencePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)

type VerificationRequest struct {
	ID          string `json:"id"`
	Status      string `json:"status"`
	VerifiedAt  string `json:"verified_at"`
	NextReview  string `json:"next_review"`
	EvidenceRef string `json:"evidence_ref"`
}

type VerificationCandidate struct {
	Path            string `json:"path"`
	SourceID        string `json:"source_id"`
	ObservedAt      string `json:"observed_at"`
	HTTPStatus      int    `json:"http_status"`
	ContentBytes    int    `json:"content_bytes"`
	ObservedDigest  string `json:"observed_digest"`
	CurrentDigest   string `json:"current_digest"`
	CandidateDigest string `json:"candidate_digest"`
	Content         []byte `json:"-"`
}

func BuildVerificationCandidate(
	register Register,
	request VerificationRequest,
	check CheckResult,
	schemas *validation.Set,
) (VerificationCandidate, []domain.Finding) {
	findings := validateVerificationRequest(request, check)
	if len(findings) != 0 {
		return VerificationCandidate{}, findings
	}
	index := -1
	for candidateIndex := range register.Sources {
		if register.Sources[candidateIndex].ID == request.ID {
			index = candidateIndex
			break
		}
	}
	if index < 0 {
		return VerificationCandidate{}, []domain.Finding{{
			Code: "GDS_SOURCE_ID_UNKNOWN", Severity: domain.SeverityHigh,
			Message:  "Requested source id is not registered.",
			Evidence: map[string]any{"id": request.ID},
		}}
	}
	currentDigest := ""
	if register.Sources[index].ContentDigest != nil {
		currentDigest = *register.Sources[index].ContentDigest
	}
	observedDigest := check.ObservedDigest
	register.Sources[index].ContentDigest = &observedDigest
	register.Sources[index].VerifiedAt = request.VerifiedAt
	register.Sources[index].NextReview = request.NextReview
	register.Sources[index].Status = request.Status
	// Runtime observations belong to the operation journal and plan evidence,
	// not the tracked semantic baseline. Remove legacy observations so repeated
	// checks of identical content produce the same candidate bytes.
	register.Sources[index].Observations = nil
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(register); err != nil {
		return VerificationCandidate{}, []domain.Finding{{
			Code: "GDS_SOURCE_REGISTER_RENDER_FAILED", Severity: domain.SeverityHigh,
			Message: fmt.Sprintf("Source register cannot be rendered: %v", err),
		}}
	}
	if err := encoder.Close(); err != nil {
		return VerificationCandidate{}, []domain.Finding{{
			Code: "GDS_SOURCE_REGISTER_RENDER_FAILED", Severity: domain.SeverityHigh,
			Message: fmt.Sprintf("Source register renderer cannot be closed: %v", err),
		}}
	}
	content := output.Bytes()
	value, err := serialization.Decode("sources.yaml", content)
	if err != nil {
		return VerificationCandidate{}, []domain.Finding{{
			Code: "GDS_SOURCE_REGISTER_RENDER_FAILED", Severity: domain.SeverityHigh,
			Message: fmt.Sprintf("Rendered source register cannot be decoded: %v", err),
		}}
	}
	if schemaFindings := schemas.Validate(
		"source-register", value, "sources.yaml",
	); len(schemaFindings) != 0 {
		return VerificationCandidate{}, schemaFindings
	}
	return VerificationCandidate{
		Path: RegisterPath, SourceID: request.ID,
		ObservedAt: check.ObservedAt, HTTPStatus: check.HTTPStatus, ContentBytes: check.Bytes,
		ObservedDigest: check.ObservedDigest, CurrentDigest: currentDigest,
		CandidateDigest: fmt.Sprintf("sha256:%x", sha256.Sum256(content)),
		Content:         content,
	}, nil
}

func validateVerificationRequest(
	request VerificationRequest,
	check CheckResult,
) []domain.Finding {
	findings := []domain.Finding{}
	if request.ID == "" || request.ID != check.ID {
		findings = append(findings, verificationFinding(
			"GDS_SOURCE_VERIFICATION_ID_MISMATCH", "Verification request and check source ids differ.",
		))
	}
	if request.Status == "" || strings.Contains(request.Status, "changed-unreviewed") ||
		strings.Contains(request.Status, "release-blocking") {
		findings = append(findings, verificationFinding(
			"GDS_SOURCE_VERIFICATION_STATUS_INVALID",
			"Verified source status must be non-blocking and semantically reviewed.",
		))
	}
	verifiedAt, verifiedErr := time.Parse(time.DateOnly, request.VerifiedAt)
	nextReview, reviewErr := time.Parse(time.DateOnly, request.NextReview)
	if verifiedErr != nil || reviewErr != nil || nextReview.Before(verifiedAt) {
		findings = append(findings, verificationFinding(
			"GDS_SOURCE_VERIFICATION_DATES_INVALID",
			"Verification and review dates must be valid and ordered.",
		))
	}
	if len(request.EvidenceRef) > 256 || !evidenceReferencePattern.MatchString(request.EvidenceRef) {
		findings = append(findings, verificationFinding(
			"GDS_SOURCE_VERIFICATION_EVIDENCE_INVALID",
			"A bounded non-secret semantic-review evidence reference is required.",
		))
	}
	if check.ObservedDigest == "" || check.HTTPStatus != 200 {
		findings = append(findings, verificationFinding(
			"GDS_SOURCE_VERIFICATION_CHECK_INVALID",
			"A successful current source check is required.",
		))
	}
	return findings
}

func verificationFinding(code, message string) domain.Finding {
	return domain.Finding{Code: code, Severity: domain.SeverityHigh, Message: message}
}
