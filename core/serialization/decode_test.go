package serialization

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestDecodeSchemaFixtures(t *testing.T) {
	t.Parallel()
	root := repositoryRoot(t)
	cases := []struct {
		name     string
		file     string
		wantCode string
	}{
		{name: "valid yaml", file: "valid-repository.yaml"},
		{
			name:     "ambiguous scalar",
			file:     "invalid-yaml-ambiguous-scalar.yaml",
			wantCode: "GDS_YAML_AMBIGUOUS_SCALAR",
		},
		{
			name:     "anchor",
			file:     "invalid-yaml-anchor.yaml",
			wantCode: "GDS_YAML_ANCHOR_FORBIDDEN",
		},
		{
			name:     "duplicate key",
			file:     "invalid-yaml-duplicate-key.yaml",
			wantCode: "GDS_YAML_DUPLICATE_KEY",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(root, "tests", "fixtures", "schemas", "v1", testCase.file)
			_, err := DecodeFile(path)
			if testCase.wantCode == "" {
				if err != nil {
					t.Fatalf("DecodeFile() error = %v", err)
				}
				return
			}
			assertContractCode(t, err, testCase.wantCode)
		})
	}
}

func TestDecodeRejectsAliasAndMergeKey(t *testing.T) {
	t.Parallel()
	for _, testCase := range []struct {
		name     string
		input    string
		wantCode string
	}{
		{
			name:     "alias",
			input:    "value: *missing\n",
			wantCode: "GDS_YAML_ALIAS_FORBIDDEN",
		},
		{
			name:     "merge key",
			input:    "value:\n  <<: {enabled: true}\n",
			wantCode: "GDS_YAML_MERGE_KEY_FORBIDDEN",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			_, err := Decode("input.yaml", []byte(testCase.input))
			assertContractCode(t, err, testCase.wantCode)
		})
	}
}

func TestDecodeRejectsMultipleYAMLDocuments(t *testing.T) {
	t.Parallel()
	_, err := Decode("input.yaml", []byte("value: 1\n---\nvalue: 2\n"))
	assertContractCode(t, err, "GDS_YAML_DOCUMENT_COUNT_INVALID")
}

func TestDecodeRejectsDuplicateJSONKey(t *testing.T) {
	t.Parallel()
	_, err := Decode("input.json", []byte(`{"value": 1, "value": 2}`))
	assertContractCode(t, err, "GDS_JSON_DUPLICATE_KEY")
}

func TestDecodeRejectsOversizedInput(t *testing.T) {
	t.Parallel()
	_, err := Decode("input.json", []byte(`{"value":"`+strings.Repeat("x", MaxInputBytes)+`"}`))
	assertContractCode(t, err, "GDS_INPUT_TOO_LARGE")
}

func TestDecodeRejectsDeepJSON(t *testing.T) {
	t.Parallel()
	input := strings.Repeat("[", MaxNesting+2) + "0" + strings.Repeat("]", MaxNesting+2)
	_, err := Decode("input.json", []byte(input))
	assertContractCode(t, err, "GDS_INPUT_NESTING_EXCEEDED")
}

func TestDecodeRejectsExplicitYAMLTag(t *testing.T) {
	t.Parallel()
	_, err := Decode("input.yaml", []byte("value: !!str tagged\n"))
	assertContractCode(t, err, "GDS_YAML_EXPLICIT_TAG_FORBIDDEN")
}

func TestDecodeIntoRejectsUnknownFields(t *testing.T) {
	t.Parallel()
	type target struct {
		Known string `json:"known"`
	}
	var result target
	err := DecodeInto("input.json", []byte(`{"known": "ok", "unknown": true}`), &result)
	assertContractCode(t, err, "GDS_INPUT_DECODE_FAILED")
}

func assertContractCode(t *testing.T, err error, expected string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected %s, received nil", expected)
	}
	var contractError *ContractError
	if !errors.As(err, &contractError) {
		t.Fatalf("expected ContractError, received %T: %v", err, err)
	}
	if contractError.Code != expected {
		t.Fatalf("code = %q, want %q; error: %v", contractError.Code, expected, err)
	}
}

func repositoryRoot(t *testing.T) string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(currentFile), "..", ".."))
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatalf("repository root is invalid: %v", err)
	}
	return root
}
