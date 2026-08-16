package identity

import (
	"bytes"
	"regexp"
	"testing"
	"time"
)

func TestNewProducesTypedCanonicalULID(t *testing.T) {
	now := time.Date(2026, 7, 11, 5, 0, 0, 0, time.UTC)
	first, err := New("plan", now, bytes.NewReader(make([]byte, 10)))
	if err != nil {
		t.Fatal(err)
	}
	secondEntropy := make([]byte, 10)
	secondEntropy[9] = 1
	second, err := New("plan", now, bytes.NewReader(secondEntropy))
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`^plan_[0-7][0-9A-HJKMNP-TV-Z]{25}$`)
	if !pattern.MatchString(first) || !pattern.MatchString(second) {
		t.Fatalf("invalid IDs: %q %q", first, second)
	}
	if first == second {
		t.Fatal("distinct entropy produced duplicate IDs")
	}
}

func TestNewRejectsInvalidPrefixAndEntropyFailure(t *testing.T) {
	if _, err := New("Bad", time.Now(), bytes.NewReader(make([]byte, 10))); err == nil {
		t.Fatal("expected prefix error")
	}
	if _, err := New("plan", time.Now(), bytes.NewReader(nil)); err == nil {
		t.Fatal("expected entropy error")
	}
}

func TestValidRequiresExactTypedPrefix(t *testing.T) {
	value := "device_01JEXAMPZ00000000000000000"
	if !Valid("device", value) || Valid("repo", value) || Valid("device", "device_invalid") {
		t.Fatal("typed identity validation is not exact")
	}
}
