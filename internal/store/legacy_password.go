package store

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"golang.org/x/crypto/pbkdf2"
	"strconv"
	"strings"
)

func legacyHashParts(hash string) (int, []byte, []byte, bool) {
	parts := strings.Split(hash, ":")
	if len(parts) != 4 || parts[0] != "pbkdf2_sha256" {
		return 0, nil, nil, false
	}
	iterations, err := strconv.Atoi(parts[1])
	if err != nil || iterations < 1 || iterations > 10000000 {
		return 0, nil, nil, false
	}
	salt, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil || len(salt) < 8 || len(salt) > 128 {
		return 0, nil, nil, false
	}
	want, err := base64.StdEncoding.DecodeString(parts[3])
	if err != nil || len(want) != 32 {
		return 0, nil, nil, false
	}
	return iterations, salt, want, true
}
func ValidLegacyHash(hash string) bool { _, _, _, ok := legacyHashParts(hash); return ok }
func VerifyLegacyPassword(password, hash string) bool {
	iterations, salt, want, ok := legacyHashParts(hash)
	if !ok {
		return false
	}
	got := pbkdf2.Key([]byte(password), salt, iterations, len(want), sha256.New)
	return subtle.ConstantTimeCompare(got, want) == 1
}
