package gitauthority

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestControlledEnvironmentDropsAmbientGitAndLoaderAuthority(t *testing.T) {
	for _, key := range []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE",
		"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE",
		"GIT_REPLACE_REF_BASE", "GIT_CONFIG_PARAMETERS", "GIT_SSH_COMMAND",
		"GIT_ASKPASS", "LD_PRELOAD", "DYLD_INSERT_LIBRARIES", "DYLD_LIBRARY_PATH",
	} {
		t.Setenv(key, "/attacker-controlled")
	}
	environment, err := controlledEnvironment(filepath.Join(t.TempDir(), "repository"), nil)
	if err != nil {
		t.Fatal(err)
	}
	joined := "\n" + strings.Join(environment, "\n") + "\n"
	for _, key := range []string{
		"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE",
		"GIT_OBJECT_DIRECTORY", "GIT_ALTERNATE_OBJECT_DIRECTORIES", "GIT_NAMESPACE",
		"GIT_REPLACE_REF_BASE", "GIT_CONFIG_PARAMETERS", "GIT_SSH_COMMAND",
		"GIT_ASKPASS", "LD_PRELOAD", "DYLD_INSERT_LIBRARIES", "DYLD_LIBRARY_PATH",
	} {
		if strings.Contains(joined, "\n"+key+"=") {
			t.Fatalf("ambient authority variable %s survived: %v", key, environment)
		}
	}
	for _, expected := range []string{
		"GIT_CONFIG_GLOBAL=" + filepath.Clean("/dev/null"),
		"GIT_CONFIG_NOSYSTEM=1",
		"PATH=/usr/bin:/bin:/usr/sbin:/sbin",
	} {
		if !strings.Contains(joined, "\n"+expected+"\n") {
			t.Fatalf("controlled environment omitted %q: %v", expected, environment)
		}
	}
}

func TestProductionGitCallersCannotBypassAuthority(t *testing.T) {
	for _, root := range []string{"../providers/git", "../releasebuilder", "../harness"} {
		err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil || entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
				return walkErr
			}
			file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
			if err != nil {
				return err
			}
			ast.Inspect(file, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				selector, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				packageName, ok := selector.X.(*ast.Ident)
				if !ok {
					return true
				}
				if root != "../harness" && packageName.Name == "os" && selector.Sel.Name == "Environ" {
					t.Errorf("%s inherits ambient environment", path)
					return true
				}
				if packageName.Name != "exec" ||
					(selector.Sel.Name != "Command" && selector.Sel.Name != "CommandContext" && selector.Sel.Name != "LookPath") {
					return true
				}
				argument := 0
				if selector.Sel.Name == "CommandContext" {
					argument = 1
				}
				if len(call.Args) <= argument {
					return true
				}
				literal, ok := call.Args[argument].(*ast.BasicLit)
				if !ok || literal.Kind != token.STRING {
					return true
				}
				value, err := strconv.Unquote(literal.Value)
				if err == nil && value == "git" {
					t.Errorf("%s executes Git outside gitauthority", path)
				}
				return true
			})
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestControlledEnvironmentRejectsUnownedExtraAuthority(t *testing.T) {
	if _, err := controlledEnvironment(t.TempDir(), []string{"GIT_DIR=/tmp/other"}); err == nil {
		t.Fatal("repository-selection override was accepted")
	}
}
