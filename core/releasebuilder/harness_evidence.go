package releasebuilder

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/harnessevidence"
	"github.com/NDDev-OpenNetwork/github-device-sync/core/trust"
)

func verifyHarnessEvidence(request Request, root string) (string, bool, error) {
	if request.HarnessEvidenceDirectory == "" {
		return "", request.Channel == "canary", nil
	}
	directory, err := filepath.Abs(request.HarnessEvidenceDirectory)
	if err != nil || directory == root {
		return "", false, errors.New("harness evidence directory is invalid")
	}
	policy, err := trust.LoadPolicy(request.HarnessEvidenceTrustPolicy)
	if err != nil {
		return "", false, fmt.Errorf("load harness evidence trust: %w", err)
	}
	producerCommit, moduleSHAs, err := harnessevidence.AnchoredIdentity(policy)
	if err != nil {
		return "", false, err
	}
	var manifest harnessevidence.Manifest
	if err := readEvidenceJSON(filepath.Join(directory, "manifest.json"), &manifest); err != nil {
		return "", false, err
	}
	expected := harnessevidence.Expectation{
		Channel: request.Channel, HarnessRootSHA: producerCommit,
		ModuleSHAs: moduleSHAs, Now: time.Now().UTC(),
		ExecutableVersions: map[string]string{}, ProfileDigests: map[string]string{}, BridgeDigests: map[string]string{},
	}
	bridgeRaw, err := os.ReadFile(filepath.Join(root, "harnesses", "module-bridge.yaml"))
	if err != nil {
		return "", false, err
	}
	bridgeDigest := evidenceBytesDigest(bridgeRaw)
	records := make([]harnessevidence.Record, 0, len(manifest.Payload.Evidence))
	for _, entry := range manifest.Payload.Evidence {
		var record harnessevidence.Record
		if err := readEvidenceJSON(filepath.Join(directory, entry.HarnessID+".json"), &record); err != nil {
			return "", false, err
		}
		records = append(records, record)
		expected.ExecutableVersions[entry.HarnessID] = record.Payload.ExecutableVersion
		profileRaw, readErr := os.ReadFile(filepath.Join(root, "harnesses", entry.HarnessID, "profile.yaml"))
		if readErr != nil {
			return "", false, readErr
		}
		expected.ProfileDigests[entry.HarnessID] = evidenceBytesDigest(profileRaw)
		expected.BridgeDigests[entry.HarnessID] = bridgeDigest
	}
	result, err := (harnessevidence.Verifier{Trust: trust.Verifier{Policy: policy}}).EvaluateChannel(manifest, records, expected)
	if err != nil {
		return "", false, fmt.Errorf("verify signed active harness evidence: %w", err)
	}
	return manifest.ManifestDigest, result.Provisional, nil
}

func readEvidenceJSON(path string, target any) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect harness evidence %s: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || info.Size() < 2 || info.Size() > 2<<20 {
		return fmt.Errorf("harness evidence %s is not a bounded regular file", path)
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode harness evidence %s: %w", path, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return fmt.Errorf("harness evidence %s has trailing JSON", path)
	}
	return nil
}

func evidenceBytesDigest(raw []byte) string {
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
}
