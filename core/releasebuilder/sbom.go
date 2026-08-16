package releasebuilder

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/gitauthority"
)

var spdxIDCharacters = regexp.MustCompile(`[^A-Za-z0-9.-]+`)

type spdxDocument struct {
	SPDXVersion       string             `json:"spdxVersion"`
	DataLicense       string             `json:"dataLicense"`
	SPDXID            string             `json:"SPDXID"`
	Name              string             `json:"name"`
	DocumentNamespace string             `json:"documentNamespace"`
	CreationInfo      spdxCreationInfo   `json:"creationInfo"`
	Packages          []spdxPackage      `json:"packages"`
	Files             []spdxFile         `json:"files"`
	Relationships     []spdxRelationship `json:"relationships"`
}

type spdxCreationInfo struct {
	Created  string   `json:"created"`
	Creators []string `json:"creators"`
}

type spdxPackage struct {
	Name             string         `json:"name"`
	SPDXID           string         `json:"SPDXID"`
	VersionInfo      string         `json:"versionInfo,omitempty"`
	DownloadLocation string         `json:"downloadLocation"`
	FilesAnalyzed    bool           `json:"filesAnalyzed"`
	LicenseConcluded string         `json:"licenseConcluded"`
	LicenseDeclared  string         `json:"licenseDeclared"`
	CopyrightText    string         `json:"copyrightText"`
	Checksums        []spdxChecksum `json:"checksums,omitempty"`
}

type spdxFile struct {
	FileName         string         `json:"fileName"`
	SPDXID           string         `json:"SPDXID"`
	Checksums        []spdxChecksum `json:"checksums"`
	LicenseConcluded string         `json:"licenseConcluded"`
	CopyrightText    string         `json:"copyrightText"`
}

type spdxChecksum struct {
	Algorithm     string `json:"algorithm"`
	ChecksumValue string `json:"checksumValue"`
}

type spdxRelationship struct {
	Element string `json:"spdxElementId"`
	Type    string `json:"relationshipType"`
	Related string `json:"relatedSpdxElement"`
}

func buildSBOM(
	version string,
	source Source,
	modules []moduleRecord,
	binaries []binaryRecord,
	gitIdentity gitauthority.Identity,
) ([]byte, error) {
	if version == "" || source.Commit == "" || source.Timestamp.IsZero() || len(binaries) == 0 ||
		gitIdentity.Version == "" || !strings.HasPrefix(gitIdentity.Digest, "sha256:") {
		return nil, errors.New("release SBOM input is incomplete")
	}
	modules = append([]moduleRecord(nil), modules...)
	sort.Slice(modules, func(left, right int) bool {
		if modules[left].Path == modules[right].Path {
			return modules[left].Version < modules[right].Version
		}
		return modules[left].Path < modules[right].Path
	})
	binaries = append([]binaryRecord(nil), binaries...)
	sort.Slice(binaries, func(left, right int) bool { return binaries[left].Path < binaries[right].Path })
	mainID := "SPDXRef-Package-gds"
	document := spdxDocument{
		SPDXVersion: "SPDX-2.3", DataLicense: "CC0-1.0", SPDXID: "SPDXRef-DOCUMENT",
		Name: "gds-" + version + "-sbom",
		DocumentNamespace: "https://github.com/NDDev-OpenNetwork/github-device-sync/spdx/" +
			version + "/" + source.Commit,
		CreationInfo: spdxCreationInfo{
			Created: source.Timestamp.UTC().Format(time.RFC3339),
			Creators: []string{
				"Tool: gds-release-builder-" + version,
				"Tool: git-" + gitIdentity.Version + "-" + gitIdentity.Digest,
			},
		},
		Packages: []spdxPackage{{
			Name: "github.com/NDDev-OpenNetwork/github-device-sync", SPDXID: mainID,
			VersionInfo: version, DownloadLocation: "NOASSERTION", FilesAnalyzed: false,
			LicenseConcluded: "NOASSERTION", LicenseDeclared: "NOASSERTION",
			CopyrightText: "NOASSERTION",
		}},
		Files: []spdxFile{}, Relationships: []spdxRelationship{{
			Element: "SPDXRef-DOCUMENT", Type: "DESCRIBES", Related: mainID,
		}},
	}
	seenPackages := map[string]struct{}{"github.com/NDDev-OpenNetwork/github-device-sync@" + version: {}}
	for _, module := range modules {
		moduleVersion := module.Version
		if module.Path == "github.com/NDDev-OpenNetwork/github-device-sync" {
			continue
		}
		key := module.Path + "@" + moduleVersion
		if module.Path == "" || moduleVersion == "" {
			return nil, errors.New("release SBOM module identity is incomplete")
		}
		if _, duplicate := seenPackages[key]; duplicate {
			return nil, fmt.Errorf("release SBOM repeats module %s", key)
		}
		seenPackages[key] = struct{}{}
		id := spdxID("SPDXRef-Package-", key)
		pkg := spdxPackage{
			Name: module.Path, SPDXID: id, VersionInfo: moduleVersion,
			DownloadLocation: "NOASSERTION", FilesAnalyzed: false,
			LicenseConcluded: "NOASSERTION", LicenseDeclared: "NOASSERTION",
			CopyrightText: "NOASSERTION",
		}
		if strings.HasPrefix(module.Sum, "h1:") {
			checksum, err := moduleSumDigest(module.Sum)
			if err != nil {
				return nil, err
			}
			pkg.Checksums = []spdxChecksum{{Algorithm: "SHA256", ChecksumValue: checksum}}
		}
		document.Packages = append(document.Packages, pkg)
		document.Relationships = append(document.Relationships, spdxRelationship{
			Element: mainID, Type: "DEPENDS_ON", Related: id,
		})
	}
	for _, binary := range binaries {
		if binary.Path == "" || binary.Digest != digestBytes(binary.Content) {
			return nil, errors.New("release SBOM binary digest is invalid")
		}
		id := spdxID("SPDXRef-File-", binary.Path)
		document.Files = append(document.Files, spdxFile{
			FileName: "./" + binary.Path, SPDXID: id,
			Checksums: []spdxChecksum{{
				Algorithm: "SHA256", ChecksumValue: strings.TrimPrefix(binary.Digest, "sha256:"),
			}},
			LicenseConcluded: "NOASSERTION", CopyrightText: "NOASSERTION",
		})
		document.Relationships = append(document.Relationships, spdxRelationship{
			Element: mainID, Type: "CONTAINS", Related: id,
		})
	}
	raw, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(raw, '\n'), validateSBOM(raw)
}

func validateSBOM(raw []byte) error {
	var document spdxDocument
	if err := json.Unmarshal(raw, &document); err != nil {
		return err
	}
	if document.SPDXVersion != "SPDX-2.3" || document.DataLicense != "CC0-1.0" ||
		document.SPDXID != "SPDXRef-DOCUMENT" || document.Name == "" ||
		document.DocumentNamespace == "" || len(document.Packages) == 0 || len(document.Files) == 0 ||
		len(document.Relationships) == 0 {
		return errors.New("release SPDX document is incomplete")
	}
	seen := map[string]struct{}{document.SPDXID: {}}
	for _, pkg := range document.Packages {
		if pkg.Name == "" || !strings.HasPrefix(pkg.SPDXID, "SPDXRef-") {
			return errors.New("release SPDX package is invalid")
		}
		if _, duplicate := seen[pkg.SPDXID]; duplicate {
			return errors.New("release SPDX identifiers are duplicated")
		}
		seen[pkg.SPDXID] = struct{}{}
	}
	for _, file := range document.Files {
		if file.FileName == "" || len(file.Checksums) != 1 || file.Checksums[0].Algorithm != "SHA256" ||
			len(file.Checksums[0].ChecksumValue) != 64 {
			return errors.New("release SPDX file is invalid")
		}
		if _, duplicate := seen[file.SPDXID]; duplicate {
			return errors.New("release SPDX identifiers are duplicated")
		}
		seen[file.SPDXID] = struct{}{}
	}
	for _, relationship := range document.Relationships {
		if _, found := seen[relationship.Element]; !found {
			return errors.New("release SPDX relationship source is unknown")
		}
		if _, found := seen[relationship.Related]; !found {
			return errors.New("release SPDX relationship target is unknown")
		}
	}
	return nil
}

func spdxID(prefix string, value string) string {
	normalized := strings.Trim(spdxIDCharacters.ReplaceAllString(value, "-"), "-.")
	if len(normalized) > 80 {
		normalized = normalized[:60] + "-" + shortDigest(value)
	}
	return prefix + normalized
}

func moduleSumDigest(sum string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sum, "h1:"))
	if err != nil || len(raw) != sha256.Size {
		return "", errors.New("release SBOM Go module sum is invalid")
	}
	return hex.EncodeToString(raw), nil
}

func shortDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:8])
}
