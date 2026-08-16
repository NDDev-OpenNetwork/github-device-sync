package harness

import (
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/NDDev-OpenNetwork/github-device-sync/core/domain"
)

const (
	codexInstructionLimitBytes = 32 << 10
	codexInstructionAlertBytes = 24 << 10
	maxInstructionFileBytes    = 1 << 20
	maxInstructionFiles        = 1024
)

type InstructionFile struct {
	Path     string `json:"path"`
	Kind     string `json:"kind"`
	Bytes    int    `json:"bytes"`
	Digest   string `json:"digest"`
	Selected bool   `json:"selected"`
}

type InstructionChain struct {
	Directory     string   `json:"directory"`
	Files         []string `json:"files"`
	CombinedBytes int      `json:"combined_bytes"`
}

type InstructionReport struct {
	Files        []InstructionFile `json:"files"`
	LongestChain InstructionChain  `json:"longest_chain"`
	AlertBytes   int               `json:"alert_bytes"`
	LimitBytes   int               `json:"limit_bytes"`
}

func inspectCodexInstructions(root string) (InstructionReport, []domain.Finding) {
	report := InstructionReport{
		Files: []InstructionFile{}, AlertBytes: codexInstructionAlertBytes,
		LimitBytes: codexInstructionLimitBytes,
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return report, []domain.Finding{harnessFinding(
			"GDS_CODEX_INSTRUCTION_ROOT_INVALID", "Cannot resolve the instruction root.",
			map[string]any{"root": root, "error": err.Error()},
		)}
	}
	byDirectory := map[string][]int{}
	findings := []domain.Finding{}
	err = filepath.WalkDir(absolute, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if path != absolute && skippedInstructionDirectory(entry.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		name := entry.Name()
		if name != "AGENTS.md" && name != "AGENTS.override.md" {
			return nil
		}
		if len(report.Files) >= maxInstructionFiles {
			return fmt.Errorf("instruction file count exceeds %d", maxInstructionFiles)
		}
		info, err := os.Lstat(path)
		if err != nil || info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() ||
			info.Size() > maxInstructionFileBytes {
			findings = append(findings, harnessFinding(
				"GDS_CODEX_INSTRUCTION_FILE_INVALID",
				"Codex instruction input must be a bounded regular file.",
				map[string]any{"path": path, "error": errorText(err)},
			))
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			findings = append(findings, harnessFinding(
				"GDS_CODEX_INSTRUCTION_FILE_INVALID", "Cannot read a Codex instruction input.",
				map[string]any{"path": path, "error": err.Error()},
			))
			return nil
		}
		relative, err := filepath.Rel(absolute, path)
		if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("instruction path escapes root: %s", path)
		}
		kind := "agents"
		if name == "AGENTS.override.md" {
			kind = "override"
		}
		index := len(report.Files)
		report.Files = append(report.Files, InstructionFile{
			Path: filepath.ToSlash(relative), Kind: kind, Bytes: len(content),
			Digest: fmt.Sprintf("sha256:%x", sha256.Sum256(content)),
		})
		directory := filepath.ToSlash(filepath.Dir(relative))
		byDirectory[directory] = append(byDirectory[directory], index)
		return nil
	})
	if err != nil {
		findings = append(findings, harnessFinding(
			"GDS_CODEX_INSTRUCTION_DISCOVERY_FAILED", "Cannot enumerate Codex instruction inputs.",
			map[string]any{"root": absolute, "error": err.Error()},
		))
		return report, findings
	}

	selected := map[string]int{}
	for directory, indexes := range byDirectory {
		chosen := -1
		for _, index := range indexes {
			if report.Files[index].Kind == "override" {
				chosen = index
				findings = append(findings, harnessFinding(
					"GDS_CODEX_OVERRIDE_ACTIVE",
					"AGENTS.override.md masks the ordinary instruction file in its directory.",
					map[string]any{"path": report.Files[index].Path},
				))
				break
			}
		}
		if chosen < 0 {
			chosen = indexes[0]
		}
		report.Files[chosen].Selected = true
		selected[directory] = chosen
	}
	for directory := range selected {
		chain := instructionChain(directory, selected, report.Files)
		if chain.CombinedBytes > report.LongestChain.CombinedBytes ||
			(chain.CombinedBytes == report.LongestChain.CombinedBytes && chain.Directory < report.LongestChain.Directory) {
			report.LongestChain = chain
		}
	}
	if report.LongestChain.CombinedBytes > codexInstructionLimitBytes {
		findings = append(findings, harnessFinding(
			"GDS_CODEX_INSTRUCTION_LIMIT_EXCEEDED",
			"A possible root-to-CWD Codex instruction chain exceeds the product byte limit.",
			map[string]any{
				"directory": report.LongestChain.Directory,
				"observed":  report.LongestChain.CombinedBytes,
				"limit":     codexInstructionLimitBytes,
			},
		))
	} else if report.LongestChain.CombinedBytes > codexInstructionAlertBytes {
		findings = append(findings, harnessFinding(
			"GDS_CODEX_INSTRUCTION_BUDGET_ALERT",
			"A possible root-to-CWD Codex instruction chain exceeds the GDS operational budget.",
			map[string]any{
				"directory": report.LongestChain.Directory,
				"observed":  report.LongestChain.CombinedBytes,
				"limit":     codexInstructionAlertBytes,
			},
		))
	}
	if duplicate := duplicateInstructionDigest(report.LongestChain, report.Files); duplicate != "" {
		findings = append(findings, harnessFinding(
			"GDS_CODEX_INSTRUCTION_DUPLICATE",
			"A Codex instruction chain injects byte-identical instruction files more than once.",
			map[string]any{"digest": duplicate, "directory": report.LongestChain.Directory},
		))
	}
	sort.Slice(report.Files, func(left, right int) bool {
		return report.Files[left].Path < report.Files[right].Path
	})
	sort.SliceStable(findings, func(left, right int) bool { return findings[left].Code < findings[right].Code })
	return report, findings
}

func instructionChain(
	directory string,
	selected map[string]int,
	files []InstructionFile,
) InstructionChain {
	parts := []string{"."}
	if directory != "." {
		current := ""
		for _, part := range strings.Split(directory, "/") {
			if current == "" {
				current = part
			} else {
				current += "/" + part
			}
			parts = append(parts, current)
		}
	}
	chain := InstructionChain{Directory: directory, Files: []string{}}
	for _, part := range parts {
		index, found := selected[part]
		if !found {
			continue
		}
		chain.Files = append(chain.Files, files[index].Path)
		chain.CombinedBytes += files[index].Bytes
	}
	return chain
}

func duplicateInstructionDigest(chain InstructionChain, files []InstructionFile) string {
	byPath := map[string]InstructionFile{}
	for _, file := range files {
		byPath[file.Path] = file
	}
	seen := map[string]struct{}{}
	for _, path := range chain.Files {
		digest := byPath[path].Digest
		if _, duplicate := seen[digest]; duplicate {
			return digest
		}
		seen[digest] = struct{}{}
	}
	return ""
}

// "golden" holds expected projection output, and a projection can contain an
// instruction file. Once the fixture and the applied projection agree -- which
// is the state the golden test exists to enforce -- walking into it reports the
// repository's own AGENTS.md twice and calls the chain duplicated. The copy
// under a golden directory is test data, not a chain an agent would ever read.
func skippedInstructionDirectory(name string) bool {
	return name == ".git" || name == "var" || name == "node_modules" ||
		name == "vendor" || name == "golden"
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
