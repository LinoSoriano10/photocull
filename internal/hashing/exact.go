// Package hashing computes the fingerprints photocull relies on: an exact
// content hash to find byte-identical files, and two content fingerprints to
// find files that are merely alike — a perceptual hash for photographs, a
// SimHash for text.
//
// The last two live side by side rather than in packages of their own, and that
// arrangement is the point. Both are 64 bits, both are compared by the same
// Distance function, and both feed the same union-find. Seeing them next to each
// other is what makes it obvious that "does this picture look like that one" and
// "does this document say the same as that one" are the same question asked of
// different bytes — which is the whole reason one binary can do both.
package hashing

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"os"
)

// NewSHA256 returns a hasher for exact, byte-for-byte file comparison.
//
// SHA-256 is overkill for detecting accidental duplicates, but it costs little
// next to the disk read and it removes any doubt about collisions: if two
// photos share a hash, they are the same photo.
func NewSHA256() hash.Hash {
	return sha256.New()
}

// HexSum renders the digest accumulated in h as a lowercase hex string.
func HexSum(h hash.Hash) string {
	return hex.EncodeToString(h.Sum(nil))
}

// SumReader hashes everything r yields.
func SumReader(r io.Reader) (string, error) {
	h := NewSHA256()
	if _, err := io.Copy(h, r); err != nil {
		return "", fmt.Errorf("hashing: read failed: %w", err)
	}
	return HexSum(h), nil
}

// SumFile hashes the contents of the file at path.
func SumFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("hashing: %w", err)
	}
	defer f.Close()

	return SumReader(f)
}
