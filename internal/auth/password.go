// Package auth handles password hashing and JSON Web Tokens.
package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// ErrMismatch is returned when a password does not match its hash. It is
// deliberately the same error whether the user exists or not, so an attacker
// cannot learn which emails are registered by timing or by error text.
var ErrMismatch = errors.New("password does not match")

// argon2id parameters. Memory is the main cost knob: it is what makes GPU
// cracking expensive, which is the whole reason to prefer argon2id over a
// plain hash. These are the OWASP-recommended minimums.
const (
	argonMemoryKiB  = 19 * 1024 // 19 MiB
	argonIterations = 2
	argonThreads    = 1
	argonSaltLen    = 16
	argonKeyLen     = 32
)

// HashPassword returns an encoded argon2id hash in the standard PHC format:
//
//	$argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>
//
// The parameters travel inside the string, so they can be raised later
// without invalidating hashes already stored.
func HashPassword(password string) (string, error) {
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("generate salt: %w", err)
	}

	key := argon2.IDKey([]byte(password), salt,
		argonIterations, argonMemoryKiB, argonThreads, argonKeyLen)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemoryKiB, argonIterations, argonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// VerifyPassword reports whether password matches encodedHash.
func VerifyPassword(password, encodedHash string) error {
	params, salt, want, err := decodeHash(encodedHash)
	if err != nil {
		return err
	}

	got := argon2.IDKey([]byte(password), salt,
		params.iterations, params.memoryKiB, params.threads, uint32(len(want)))

	// Constant time: a byte-by-byte comparison leaks how much of the hash
	// matched through timing.
	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrMismatch
	}
	return nil
}

type argonParams struct {
	memoryKiB  uint32
	iterations uint32
	threads    uint8
}

func decodeHash(encoded string) (argonParams, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return argonParams{}, nil, nil, errors.New("hash is not in argon2id PHC format")
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return argonParams{}, nil, nil, fmt.Errorf("parse hash version: %w", err)
	}
	if version != argon2.Version {
		return argonParams{}, nil, nil, fmt.Errorf("unsupported argon2 version %d", version)
	}

	var p argonParams
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d",
		&p.memoryKiB, &p.iterations, &p.threads); err != nil {
		return argonParams{}, nil, nil, fmt.Errorf("parse hash parameters: %w", err)
	}

	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil {
		return argonParams{}, nil, nil, fmt.Errorf("decode salt: %w", err)
	}
	key, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil {
		return argonParams{}, nil, nil, fmt.Errorf("decode hash: %w", err)
	}
	return p, salt, key, nil
}
