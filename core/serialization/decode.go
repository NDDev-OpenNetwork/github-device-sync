// Package serialization enforces the GDS JSON/YAML input contract before
// schema or domain decoding.
package serialization

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"go.yaml.in/yaml/v4"
)

var ambiguousPlainScalars = map[string]struct{}{
	"y": {}, "yes": {}, "n": {}, "no": {}, "on": {}, "off": {},
}

const (
	MaxInputBytes = 4 << 20
	MaxNesting    = 128
	MaxYAMLNodes  = 100_000
)

type ContractError struct {
	Code   string
	Path   string
	Line   int
	Column int
	Err    error
}

type codedError struct {
	code string
	err  error
}

func (e *codedError) Error() string { return e.err.Error() }
func (e *codedError) Unwrap() error { return e.err }

func (e *ContractError) Error() string {
	location := e.Path
	if e.Line > 0 {
		location = fmt.Sprintf("%s:%d:%d", location, e.Line, e.Column)
	}
	return fmt.Sprintf("%s: %v", location, e.Err)
}

func (e *ContractError) Unwrap() error { return e.Err }

func DecodeFile(path string) (any, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, &ContractError{
			Code: "GDS_INPUT_READ_FAILED", Path: path, Err: err,
		}
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, MaxInputBytes+1))
	if err != nil {
		return nil, &ContractError{
			Code: "GDS_INPUT_READ_FAILED", Path: path, Err: err,
		}
	}
	return Decode(path, data)
}

func Decode(path string, data []byte) (any, error) {
	if len(data) > MaxInputBytes {
		return nil, &ContractError{
			Code: "GDS_INPUT_TOO_LARGE",
			Path: path,
			Err:  fmt.Errorf("input exceeds %d-byte limit", MaxInputBytes),
		}
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		value, err := decodeJSON(data)
		if err != nil {
			code := "GDS_INPUT_PARSE_FAILED"
			var coded *codedError
			if errors.As(err, &coded) {
				code = coded.code
			}
			return nil, &ContractError{
				Code: code, Path: path, Err: err,
			}
		}
		return value, nil
	case ".yaml", ".yml":
		return decodeYAML(path, data)
	default:
		return nil, &ContractError{
			Code: "GDS_INPUT_FORMAT_UNSUPPORTED",
			Path: path,
			Err:  errors.New("expected .json, .yaml, or .yml"),
		}
	}
}

func DecodeInto(path string, data []byte, target any) error {
	value, err := Decode(path, data)
	if err != nil {
		return err
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return &ContractError{Code: "GDS_INPUT_NORMALIZE_FAILED", Path: path, Err: err}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return &ContractError{Code: "GDS_INPUT_DECODE_FAILED", Path: path, Err: err}
	}
	return nil
}

func decodeYAML(path string, data []byte) (any, error) {
	var documents []yaml.Node
	if err := yaml.Load(
		data,
		&documents,
		yaml.WithAllDocuments(),
		yaml.WithUniqueKeys(),
	); err != nil {
		return nil, &ContractError{
			Code: yamlErrorCode(err, "GDS_YAML_PARSE_FAILED"), Path: path, Err: err,
		}
	}
	if len(documents) != 1 {
		return nil, &ContractError{
			Code: "GDS_YAML_DOCUMENT_COUNT_INVALID",
			Path: path,
			Err:  fmt.Errorf("expected exactly one YAML document, found %d", len(documents)),
		}
	}
	if err := inspectYAMLNode(path, &documents[0]); err != nil {
		return nil, err
	}

	var value any
	if err := documents[0].Decode(&value); err != nil {
		return nil, &ContractError{
			Code: yamlErrorCode(err, "GDS_YAML_DECODE_FAILED"), Path: path, Err: err,
		}
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, &ContractError{
			Code: "GDS_YAML_NORMALIZE_FAILED", Path: path, Err: err,
		}
	}
	normalized, err := decodeJSON(raw)
	if err != nil {
		return nil, &ContractError{
			Code: "GDS_YAML_NORMALIZE_FAILED", Path: path, Err: err,
		}
	}
	return normalized, nil
}

func yamlErrorCode(err error, fallback string) string {
	message := err.Error()
	switch {
	case strings.Contains(message, "already defined"),
		strings.Contains(message, "duplicate key"),
		strings.Contains(message, "mapping key"):
		return "GDS_YAML_DUPLICATE_KEY"
	case strings.Contains(message, "unknown anchor"),
		strings.Contains(message, "alias"),
		strings.Contains(message, "merge key"),
		strings.Contains(message, "unknown tag"):
		return "GDS_YAML_ALIAS_FORBIDDEN"
	default:
		return fallback
	}
}

func inspectYAMLNode(path string, root *yaml.Node) error {
	type pendingNode struct {
		node  *yaml.Node
		depth int
	}
	stack := []pendingNode{{node: root, depth: 0}}
	seen := 0
	for len(stack) > 0 {
		current := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		seen++
		if seen > MaxYAMLNodes {
			return yamlNodeError(
				"GDS_YAML_NODE_LIMIT_EXCEEDED", path, current.node,
				fmt.Sprintf("YAML exceeds %d-node limit", MaxYAMLNodes),
			)
		}
		if current.depth > MaxNesting {
			return yamlNodeError(
				"GDS_INPUT_NESTING_EXCEEDED", path, current.node,
				fmt.Sprintf("YAML exceeds nesting limit %d", MaxNesting),
			)
		}
		node := current.node
		if node.Anchor != "" {
			return yamlNodeError("GDS_YAML_ANCHOR_FORBIDDEN", path, node, "anchors are forbidden")
		}
		if node.Kind == yaml.AliasNode {
			return yamlNodeError("GDS_YAML_ALIAS_FORBIDDEN", path, node, "aliases are forbidden")
		}
		if node.Style&yaml.TaggedStyle != 0 {
			return yamlNodeError(
				"GDS_YAML_EXPLICIT_TAG_FORBIDDEN", path, node, "explicit YAML tags are forbidden",
			)
		}
		if node.Kind == yaml.ScalarNode && node.Style == 0 {
			if _, found := ambiguousPlainScalars[strings.ToLower(node.Value)]; found {
				return yamlNodeError(
					"GDS_YAML_AMBIGUOUS_SCALAR",
					path,
					node,
					fmt.Sprintf("ambiguous plain scalar %q must be quoted", node.Value),
				)
			}
		}
		if node.Kind == yaml.MappingNode {
			for index := 0; index+1 < len(node.Content); index += 2 {
				key := node.Content[index]
				if key.Value == "<<" || key.Tag == "!!merge" {
					return yamlNodeError(
						"GDS_YAML_MERGE_KEY_FORBIDDEN",
						path,
						key,
						"merge keys are forbidden",
					)
				}
			}
		}
		for index := len(node.Content) - 1; index >= 0; index-- {
			stack = append(stack, pendingNode{node: node.Content[index], depth: current.depth + 1})
		}
	}
	return nil
}

func yamlNodeError(code, path string, node *yaml.Node, message string) error {
	return &ContractError{
		Code: code, Path: path, Line: node.Line, Column: node.Column, Err: errors.New(message),
	}
}

func decodeJSON(data []byte) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	value, err := readJSONValue(decoder, 0)
	if err != nil {
		return nil, err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, errors.New("multiple JSON values are forbidden")
		}
		return nil, err
	}
	return value, nil
}

func readJSONValue(decoder *json.Decoder, depth int) (any, error) {
	if depth > MaxNesting {
		return nil, &codedError{
			code: "GDS_INPUT_NESTING_EXCEEDED",
			err:  fmt.Errorf("JSON exceeds nesting limit %d", MaxNesting),
		}
	}
	token, err := decoder.Token()
	if err != nil {
		return nil, err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return token, nil
	}
	switch delimiter {
	case '{':
		object := map[string]any{}
		for decoder.More() {
			keyToken, err := decoder.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, errors.New("JSON object key is not a string")
			}
			if _, duplicate := object[key]; duplicate {
				return nil, &codedError{
					code: "GDS_JSON_DUPLICATE_KEY",
					err:  fmt.Errorf("duplicate JSON key %q", key),
				}
			}
			value, err := readJSONValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			object[key] = value
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return object, nil
	case '[':
		array := []any{}
		for decoder.More() {
			value, err := readJSONValue(decoder, depth+1)
			if err != nil {
				return nil, err
			}
			array = append(array, value)
		}
		if _, err := decoder.Token(); err != nil {
			return nil, err
		}
		return array, nil
	default:
		return nil, fmt.Errorf("unexpected JSON delimiter %q", delimiter)
	}
}
