package store

import (
	"crypto/rand"
	"encoding/base32"
	"strings"
)

var idEnc = base32.StdEncoding.WithPadding(base32.NoPadding)

// NewID returns a prefixed opaque id per the contract's registry, e.g.
// dev_x7k2m9q4. 40 random bits, lowercase base32.
func NewID(prefix string) string {
	b := make([]byte, 5)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	return prefix + "_" + strings.ToLower(idEnc.EncodeToString(b))
}
