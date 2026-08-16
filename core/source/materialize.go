package source

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/canonicaljson"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/materialize"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/operations"
)

const MaterializeVerificationAction = "materialize-source-verification"

type VerificationSpec struct {
	ID              string `json:"id"`
	Status          string `json:"status"`
	VerifiedAt      string `json:"verified_at"`
	NextReview      string `json:"next_review"`
	EvidenceRef     string `json:"evidence_ref"`
	ObservedAt      string `json:"observed_at"`
	HTTPStatus      int    `json:"http_status"`
	ContentBytes    int    `json:"content_bytes"`
	ObservedDigest  string `json:"observed_digest"`
	CandidateDigest string `json:"candidate_digest"`
}

type VerificationMaterializer struct {
	candidate VerificationCandidate
	request   VerificationRequest
	files     *materialize.Set
}

func NewVerificationMaterializer(
	root string,
	candidate VerificationCandidate,
	request VerificationRequest,
) (*VerificationMaterializer, error) {
	files, err := materialize.NewSet(root, []materialize.File{{
		Path: candidate.Path, Content: candidate.Content, Digest: candidate.CandidateDigest,
	}})
	if err != nil {
		return nil, err
	}
	return &VerificationMaterializer{candidate: candidate, request: request, files: files}, nil
}

func VerificationParameters(
	candidate VerificationCandidate,
	request VerificationRequest,
) map[string]any {
	spec := verificationSpec(candidate, request)
	raw, _ := json.Marshal(spec)
	var value map[string]any
	_ = json.Unmarshal(raw, &value)
	return map[string]any{"source_verification": value}
}

func VerificationFingerprint(
	root string,
	candidate VerificationCandidate,
	request VerificationRequest,
) (string, error) {
	materializer, err := NewVerificationMaterializer(root, candidate, request)
	if err != nil {
		return "", err
	}
	specDigest, err := canonicaljson.Digest(verificationSpec(candidate, request))
	if err != nil {
		return "", err
	}
	return materializer.files.Fingerprint(specDigest)
}

func (materializer *VerificationMaterializer) Apply(
	_ context.Context,
	step operations.Step,
) (operations.ApplyEvidence, error) {
	if step.Action != MaterializeVerificationAction {
		return operations.ApplyEvidence{}, fmt.Errorf(
			"unsupported source verification action %q", step.Action,
		)
	}
	if err := matchVerificationParameters(
		step.Parameters, materializer.candidate, materializer.request,
	); err != nil {
		return operations.ApplyEvidence{}, err
	}
	before, after, err := materializer.files.Apply()
	return operations.ApplyEvidence{Before: before, After: after}, err
}

func (materializer *VerificationMaterializer) Verify(
	_ context.Context,
	step operations.Step,
	_ json.RawMessage,
) error {
	if err := matchVerificationParameters(
		step.Parameters, materializer.candidate, materializer.request,
	); err != nil {
		return err
	}
	return materializer.files.Verify()
}

func verificationSpec(
	candidate VerificationCandidate,
	request VerificationRequest,
) VerificationSpec {
	return VerificationSpec{
		ID: request.ID, Status: request.Status,
		VerifiedAt: request.VerifiedAt, NextReview: request.NextReview,
		EvidenceRef: request.EvidenceRef,
		ObservedAt:  candidate.ObservedAt, HTTPStatus: candidate.HTTPStatus,
		ContentBytes: candidate.ContentBytes, ObservedDigest: candidate.ObservedDigest,
		CandidateDigest: candidate.CandidateDigest,
	}
}

func matchVerificationParameters(
	parameters map[string]any,
	candidate VerificationCandidate,
	request VerificationRequest,
) error {
	rawValue, found := parameters["source_verification"]
	if !found || len(parameters) != 1 {
		return errors.New("source verification parameters are missing or contain unknown fields")
	}
	raw, err := json.Marshal(rawValue)
	if err != nil {
		return fmt.Errorf("encode source verification parameters: %w", err)
	}
	var observed VerificationSpec
	if err := json.Unmarshal(raw, &observed); err != nil {
		return fmt.Errorf("decode source verification parameters: %w", err)
	}
	expected := verificationSpec(candidate, request)
	expectedRaw, _ := json.Marshal(expected)
	observedRaw, _ := json.Marshal(observed)
	if string(expectedRaw) != string(observedRaw) {
		return errors.New("source verification candidate differs from approved parameters")
	}
	return nil
}

func RequestFromParameters(parameters map[string]any) (VerificationRequest, error) {
	spec, err := SpecFromParameters(parameters)
	if err != nil {
		return VerificationRequest{}, err
	}
	return VerificationRequest{
		ID: spec.ID, Status: spec.Status, VerifiedAt: spec.VerifiedAt,
		NextReview: spec.NextReview, EvidenceRef: spec.EvidenceRef,
	}, nil
}

func SpecFromParameters(parameters map[string]any) (VerificationSpec, error) {
	rawValue, found := parameters["source_verification"]
	if !found || len(parameters) != 1 {
		return VerificationSpec{}, errors.New(
			"source verification parameters are missing or contain unknown fields",
		)
	}
	raw, err := json.Marshal(rawValue)
	if err != nil {
		return VerificationSpec{}, fmt.Errorf("encode source verification parameters: %w", err)
	}
	var spec VerificationSpec
	if err := json.Unmarshal(raw, &spec); err != nil {
		return VerificationSpec{}, fmt.Errorf("decode source verification parameters: %w", err)
	}
	return spec, nil
}
