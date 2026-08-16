package releasebuilder

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

type archiveMember struct {
	name     string
	typeflag byte
	linkname string
	content  []byte
	size     int64
}

// privateTempDir returns a temporary directory the harness evidence boundary
// will accept as a destination parent under any process umask.
//
// t.TempDir creates its leaf with os.Mkdir(dir, 0777), so the umask decides the
// result: 0755 under the 0022 CI runners use, 0775 under the 0002 that Debian
// and Ubuntu set by default. privateOwnedDirectory requires Perm()&0o022 == 0,
// so the same test passes on one developer's machine and fails on another's for
// a reason that has nothing to do with the code under test. Any test that needs
// its parent accepted must state the mode instead of inheriting it.
func privateTempDir(t *testing.T) string {
	t.Helper()
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	return directory
}

func TestMaterializeHarnessEvidenceArchive(t *testing.T) {
	root := privateTempDir(t)
	archivePath := filepath.Join(root, "evidence.tar.gz")
	writeHarnessArchive(t, archivePath, validHarnessArchiveMembers())
	destination := filepath.Join(root, "records")
	if err := MaterializeHarnessEvidenceArchive(archivePath, destination); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(destination)
	if err != nil || info.Mode().Perm() != 0o700 {
		t.Fatalf("destination mode = %v, err = %v", info.Mode().Perm(), err)
	}
	for _, name := range harnessEvidenceMembers {
		info, err := os.Stat(filepath.Join(destination, name))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 {
			t.Fatalf("member %s mode = %v, err = %v", name, info.Mode(), err)
		}
	}
	if err := MaterializeHarnessEvidenceArchive(archivePath, destination); err == nil ||
		!strings.Contains(err.Error(), "must not exist") {
		t.Fatalf("existing destination error = %v", err)
	}
}

func TestHarnessEvidenceTransactionAcceptsPrivateDirectoriesUnderPlatformTemp(t *testing.T) {
	root, err := os.MkdirTemp("", "gds-harness-boundary-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(root)
	archivePath := filepath.Join(root, "evidence.tar.gz")
	writeHarnessArchive(t, archivePath, validHarnessArchiveMembers())
	if err := MaterializeHarnessEvidenceArchive(archivePath, filepath.Join(root, "records")); err != nil {
		t.Fatal(err)
	}
}

// The acceptance path must depend on the destination parent's actual mode and
// on nothing ambient. Issue #171 closed this class in core/materialize and
// core/app but left this package, where it stayed invisible because CI runners
// happen to use 0022. Asserting across the masks that matter is what keeps the
// three fixes from becoming four.
//
// This test mutates process-global state and must stay serial: Go resumes
// t.Parallel tests only after the serial ones in their parent finish, and this
// package has parallel tests elsewhere. Do not add t.Parallel here.
func TestMaterializeHarnessEvidenceArchiveIsIndependentOfProcessUmask(t *testing.T) {
	for _, mask := range []int{0o000, 0o002, 0o022, 0o077} {
		t.Run(fmt.Sprintf("umask-%04o", mask), func(t *testing.T) {
			previous := syscall.Umask(mask)
			defer syscall.Umask(previous)
			root := privateTempDir(t)
			archivePath := filepath.Join(root, "evidence.tar.gz")
			writeHarnessArchive(t, archivePath, validHarnessArchiveMembers())
			destination := filepath.Join(root, "records")
			if err := MaterializeHarnessEvidenceArchive(archivePath, destination); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(destination)
			if err != nil || info.Mode().Perm() != 0o700 {
				t.Fatalf("destination mode = %v, err = %v", info.Mode().Perm(), err)
			}
			for _, name := range harnessEvidenceMembers {
				member, err := os.Stat(filepath.Join(destination, name))
				if err != nil || !member.Mode().IsRegular() || member.Mode().Perm() != 0o600 {
					t.Fatalf("member %s mode = %v, err = %v", name, member.Mode(), err)
				}
			}
		})
	}
}

// A parent that only passes because the ambient umask cleared its group and
// other write bits is the exact shape of the defect above. Prove the guard
// rejects it, so a future test cannot reintroduce t.TempDir as an accepted
// parent and pass on a 0022 machine.
func TestHarnessEvidenceTransactionRejectsGroupWritableParent(t *testing.T) {
	root := t.TempDir()
	parent := filepath.Join(root, "group-writable")
	if err := os.Mkdir(parent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(parent, 0o775); err != nil {
		t.Fatal(err)
	}
	archivePath := filepath.Join(root, "evidence.tar.gz")
	writeHarnessArchive(t, archivePath, validHarnessArchiveMembers())
	destination := filepath.Join(parent, "records")
	if err := MaterializeHarnessEvidenceArchive(archivePath, destination); err == nil {
		t.Fatal("group-writable destination parent was accepted")
	}
	assertPathAbsent(t, destination)
}

func TestHarnessEvidenceTransactionRejectsUnsafeParents(t *testing.T) {
	t.Run("direct-world-writable", func(t *testing.T) {
		root := t.TempDir()
		shared := filepath.Join(root, "shared")
		if err := os.Mkdir(shared, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(shared, 0o777); err != nil {
			t.Fatal(err)
		}
		archivePath := filepath.Join(root, "evidence.tar.gz")
		writeHarnessArchive(t, archivePath, validHarnessArchiveMembers())
		if err := MaterializeHarnessEvidenceArchive(archivePath, filepath.Join(shared, "records")); err == nil {
			t.Fatal("world-writable destination parent was accepted")
		}
	})
	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		realParent := filepath.Join(root, "real")
		if err := os.Mkdir(realParent, 0o700); err != nil {
			t.Fatal(err)
		}
		linkedParent := filepath.Join(root, "linked")
		if err := os.Symlink(realParent, linkedParent); err != nil {
			t.Fatal(err)
		}
		archivePath := filepath.Join(root, "evidence.tar.gz")
		writeHarnessArchive(t, archivePath, validHarnessArchiveMembers())
		if err := MaterializeHarnessEvidenceArchive(archivePath, filepath.Join(linkedParent, "records")); err == nil {
			t.Fatal("symlink destination parent was accepted")
		}
	})
	t.Run("wrong-owner", func(t *testing.T) {
		info, err := os.Stat(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		if privateOwnedDirectory(fileInfoWithStat{FileInfo: info, stat: &syscall.Stat_t{Uid: uint32(os.Geteuid() + 1)}}) {
			t.Fatal("directory owned by a different uid was accepted")
		}
	})
}

func TestHarnessEvidenceTransactionRejectsAncestorReplacement(t *testing.T) {
	for _, point := range []string{"open", "publish"} {
		t.Run(point, func(t *testing.T) {
			root := t.TempDir()
			parent := filepath.Join(root, "owned")
			if err := os.Mkdir(parent, 0o700); err != nil {
				t.Fatal(err)
			}
			archivePath := filepath.Join(root, "evidence.tar.gz")
			writeHarnessArchive(t, archivePath, validHarnessArchiveMembers())
			replaced := false
			replace := func() {
				if replaced {
					return
				}
				replaced = true
				if err := os.Rename(parent, parent+"-moved"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(parent, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			hooks := harnessArchiveHooks{}
			if point == "open" {
				hooks.afterParentInspect = replace
			} else {
				hooks.beforePublish = replace
			}
			err := materializeHarnessEvidenceArchive(archivePath, filepath.Join(parent, "records"), hooks)
			if err == nil {
				t.Fatal("replaced transaction ancestor was accepted")
			}
			if _, err := os.Lstat(filepath.Join(parent, "records")); !os.IsNotExist(err) {
				t.Fatalf("replacement received published records: %v", err)
			}
		})
	}
}

type fileInfoWithStat struct {
	os.FileInfo
	stat *syscall.Stat_t
}

func (info fileInfoWithStat) Sys() any { return info.stat }

func TestHarnessEvidenceArchiveRejectsUnsafeMembersBeforeMaterialization(t *testing.T) {
	types := []struct {
		name     string
		typeflag byte
		linkname string
	}{
		{"symlink", tar.TypeSymlink, "../outside"},
		{"hardlink", tar.TypeLink, "manifest.json"},
		{"character-device", tar.TypeChar, ""},
		{"block-device", tar.TypeBlock, ""},
		{"fifo", tar.TypeFifo, ""},
		{"socket", byte('s'), ""},
		{"regular-with-link-target", tar.TypeReg, "../outside"},
	}
	for _, candidate := range types {
		t.Run(candidate.name, func(t *testing.T) {
			members := validHarnessArchiveMembers()
			members[0].typeflag = candidate.typeflag
			members[0].linkname = candidate.linkname
			assertHarnessArchiveRejectedWithoutDestination(t, members)
		})
	}
}

func TestHarnessEvidenceArchiveRejectsPathAndSetViolations(t *testing.T) {
	tests := map[string]func([]archiveMember) []archiveMember{
		"duplicate": func(m []archiveMember) []archiveMember { return append(m, m[0]) },
		"extra": func(m []archiveMember) []archiveMember {
			return append(m, archiveMember{name: "extra.json", typeflag: tar.TypeReg, content: []byte("{}")})
		},
		"missing":        func(m []archiveMember) []archiveMember { return m[:len(m)-1] },
		"absolute":       func(m []archiveMember) []archiveMember { m[0].name = "/manifest.json"; return m },
		"traversal":      func(m []archiveMember) []archiveMember { m[0].name = "../manifest.json"; return m },
		"non-normalized": func(m []archiveMember) []archiveMember { m[0].name = "./claude-code.json"; return m },
		"backslash-alias": func(m []archiveMember) []archiveMember {
			m[0].name = "folder\\claude-code.json"
			return m
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			assertHarnessArchiveRejectedWithoutDestination(t, mutate(validHarnessArchiveMembers()))
		})
	}
}

func TestHarnessEvidenceArchiveRejectsSizeAndStreamViolations(t *testing.T) {
	t.Run("archive-symlink", func(t *testing.T) {
		root := t.TempDir()
		realPath := filepath.Join(root, "real.tar.gz")
		writeHarnessArchive(t, realPath, validHarnessArchiveMembers())
		archivePath := filepath.Join(root, "evidence.tar.gz")
		if err := os.Symlink(realPath, archivePath); err != nil {
			t.Fatal(err)
		}
		if err := MaterializeHarnessEvidenceArchive(archivePath, filepath.Join(root, "records")); err == nil {
			t.Fatal("archive symlink was accepted")
		}
	})
	t.Run("compressed-archive", func(t *testing.T) {
		root := t.TempDir()
		archivePath := filepath.Join(root, "evidence.tar.gz")
		file, err := os.OpenFile(archivePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(maxHarnessEvidenceArchiveSize + 1); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
		destination := filepath.Join(root, "records")
		if err := MaterializeHarnessEvidenceArchive(archivePath, destination); err == nil {
			t.Fatal("oversized compressed archive was accepted")
		}
		assertPathAbsent(t, destination)
	})
	t.Run("per-member", func(t *testing.T) {
		members := validHarnessArchiveMembers()
		members[0].size = maxHarnessEvidenceMemberSize + 1
		assertHarnessArchiveRejectedWithoutDestination(t, members)
	})
	t.Run("total", func(t *testing.T) {
		members := validHarnessArchiveMembers()
		for index := range members {
			members[index].content = bytes.Repeat([]byte{'x'}, maxHarnessEvidenceMemberSize)
		}
		assertHarnessArchiveRejectedWithoutDestination(t, members)
	})
	t.Run("truncated", func(t *testing.T) {
		root := t.TempDir()
		archivePath := filepath.Join(root, "evidence.tar.gz")
		writeHarnessArchive(t, archivePath, validHarnessArchiveMembers())
		raw, err := os.ReadFile(archivePath)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(archivePath, raw[:len(raw)-8], 0o600); err != nil {
			t.Fatal(err)
		}
		destination := filepath.Join(root, "records")
		if err := MaterializeHarnessEvidenceArchive(archivePath, destination); err == nil {
			t.Fatal("truncated archive was accepted")
		}
		assertPathAbsent(t, destination)
	})
	t.Run("trailing-stream", func(t *testing.T) {
		root := t.TempDir()
		archivePath := filepath.Join(root, "evidence.tar.gz")
		writeHarnessArchive(t, archivePath, validHarnessArchiveMembers())
		file, err := os.OpenFile(archivePath, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			t.Fatal(err)
		}
		_, writeErr := file.Write([]byte("trailing"))
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			t.Fatalf("append trailing data: %v / %v", writeErr, closeErr)
		}
		destination := filepath.Join(root, "records")
		if err := MaterializeHarnessEvidenceArchive(archivePath, destination); err == nil {
			t.Fatal("archive with trailing data was accepted")
		}
		assertPathAbsent(t, destination)
	})
}

func assertHarnessArchiveRejectedWithoutDestination(t *testing.T, members []archiveMember) {
	t.Helper()
	root := t.TempDir()
	archivePath := filepath.Join(root, "evidence.tar.gz")
	writeHarnessArchive(t, archivePath, members)
	destination := filepath.Join(root, "records")
	if err := MaterializeHarnessEvidenceArchive(archivePath, destination); err == nil {
		t.Fatal("unsafe archive was accepted")
	}
	assertPathAbsent(t, destination)
}

func assertPathAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Fatalf("path %s exists after rejection: %v", path, err)
	}
}

func validHarnessArchiveMembers() []archiveMember {
	members := make([]archiveMember, 0, len(harnessEvidenceMembers))
	for _, name := range harnessEvidenceMembers {
		members = append(members, archiveMember{name: name, typeflag: tar.TypeReg, content: []byte("{}")})
	}
	return members
}

func writeHarnessArchive(t *testing.T, target string, members []archiveMember) {
	t.Helper()
	file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	gzipWriter := gzip.NewWriter(file)
	tarWriter := tar.NewWriter(gzipWriter)
	for _, member := range members {
		size := member.size
		if size == 0 && (member.typeflag == tar.TypeReg || member.typeflag == tar.TypeRegA) {
			size = int64(len(member.content))
		}
		header := &tar.Header{Name: member.name, Mode: 0o600, Size: size, Typeflag: member.typeflag, Linkname: member.linkname}
		if err := tarWriter.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if member.typeflag == tar.TypeReg || member.typeflag == tar.TypeRegA {
			content := member.content
			if int64(len(content)) < size {
				content = append(content, bytes.Repeat([]byte{'x'}, int(size)-len(content))...)
			}
			if _, err := tarWriter.Write(content[:size]); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
