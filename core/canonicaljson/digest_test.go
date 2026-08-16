package canonicaljson

import "testing"

func TestDigestIsIndependentOfMapInsertionOrder(t *testing.T) {
	left, err := Digest(map[string]any{"b": 2, "a": 1})
	if err != nil {
		t.Fatal(err)
	}
	right, err := Digest(map[string]any{"a": 1, "b": 2})
	if err != nil {
		t.Fatal(err)
	}
	if left != right {
		t.Fatalf("digests differ: %s %s", left, right)
	}
}

func TestDigestNormalisesFloat64IntegerToJSONNumber(t *testing.T) {
	fromFloat, err := Digest(map[string]any{"n": float64(2)})
	if err != nil {
		t.Fatal(err)
	}
	fromInt, err := Digest(map[string]any{"n": 2})
	if err != nil {
		t.Fatal(err)
	}
	if fromFloat != fromInt {
		t.Fatalf("float64(2) digest %s differs from int digest %s — normalise is broken", fromFloat, fromInt)
	}
}

func TestDigestNormalisesNestedFloat64InSlicesAndMaps(t *testing.T) {
	val := map[string]any{
		"a": float64(1),
		"b": []any{float64(2), "c", map[string]any{"d": float64(3)}},
	}
	digest1, err := Digest(val)
	if err != nil {
		t.Fatal(err)
	}
	digest2, err := Digest(val)
	if err != nil {
		t.Fatal(err)
	}
	if digest1 != digest2 {
		t.Fatalf("same input produced different digests: %s vs %s", digest1, digest2)
	}
}

func TestDigestDoesNotMutateCallerInput(t *testing.T) {
	val := map[string]any{
		"a": []any{float64(1), map[string]any{"b": float64(2)}},
	}
	_, err := Digest(val)
	if err != nil {
		t.Fatal(err)
	}
	slice, ok := val["a"].([]any)
	if !ok {
		t.Fatal("input slice was replaced")
	}
	if _, ok := slice[0].(float64); !ok {
		t.Fatalf("input slice[0] was mutated to %T, want float64", slice[0])
	}
	nested, ok := slice[1].(map[string]any)
	if !ok {
		t.Fatal("input nested map was replaced")
	}
	if _, ok := nested["b"].(float64); !ok {
		t.Fatalf("input nested[\"b\"] was mutated to %T, want float64", nested["b"])
	}
}
