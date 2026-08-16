// Package identity generates typed GDS ULID identities.
package identity

import (
	"crypto/rand"
	"fmt"
	"io"
	"math/big"
	"regexp"
	"time"
)

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var prefixPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
var identityPattern = regexp.MustCompile(`^([a-z][a-z0-9-]{0,31})_[0-7][0-9A-HJKMNP-TV-Z]{25}$`)

func New(prefix string, now time.Time, entropy io.Reader) (string, error) {
	if !prefixPattern.MatchString(prefix) {
		return "", fmt.Errorf("invalid GDS identity prefix %q", prefix)
	}
	if entropy == nil {
		entropy = rand.Reader
	}
	milliseconds := now.UTC().UnixMilli()
	if milliseconds < 0 || milliseconds > (1<<45)-1 {
		return "", fmt.Errorf("timestamp is outside the ULID range")
	}
	raw := make([]byte, 16)
	for index := 5; index >= 0; index-- {
		raw[index] = byte(milliseconds)
		milliseconds >>= 8
	}
	if _, err := io.ReadFull(entropy, raw[6:]); err != nil {
		return "", fmt.Errorf("read ULID entropy: %w", err)
	}
	value := new(big.Int).SetBytes(raw)
	base := big.NewInt(32)
	remainder := new(big.Int)
	encoded := make([]byte, 26)
	for index := len(encoded) - 1; index >= 0; index-- {
		value.QuoRem(value, base, remainder)
		encoded[index] = crockford[remainder.Int64()]
	}
	return prefix + "_" + string(encoded), nil
}

func Valid(prefix string, value string) bool {
	matches := identityPattern.FindStringSubmatch(value)
	return len(matches) == 2 && matches[1] == prefix
}
