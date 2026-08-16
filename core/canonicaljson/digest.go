// Package canonicaljson provides deterministic JSON digests for GDS contracts.
package canonicaljson

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"strconv"
)

func Digest(value any) (string, error) {
	normalised := normalise(value)
	raw, err := json.Marshal(normalised)
	if err != nil {
		return "", fmt.Errorf("encode canonical JSON: %w", err)
	}
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw)), nil
}

// normalise recursively replaces float64 values that represent integers with
// json.Number so json.Marshal emits them without a decimal point, keeping
// digests stable across callers that use UseNumber and callers that pass
// Go integer literals through any. Non-integer float64 values are formatted
// with the shortest representation that round-trips through strconv.
func normalise(value any) any {
	switch typed := value.(type) {
	case float64:
		if math.Trunc(typed) == typed && !math.IsInf(typed, 0) && math.Abs(typed) < 1e15 {
			return json.Number(strconv.FormatInt(int64(typed), 10))
		}
		return json.Number(strconv.FormatFloat(typed, 'g', -1, 64))
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = normalise(item)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = normalise(item)
		}
		return result
	default:
		return value
	}
}

func DigestObjectWithoutField(object map[string]any, field string) (string, error) {
	payload := make(map[string]any, len(object))
	for key, value := range object {
		if key != field {
			payload[key] = value
		}
	}
	return Digest(payload)
}
