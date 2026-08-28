package store

// Password handling, kept in its own file so it can be reasoned about — and
// tested — without the rest of the store in the way.
//
// Why PBKDF2 and not bcrypt or argon2: `crypto/pbkdf2` and `crypto/subtle` are
// in the Go standard library as of 1.24, and this repository has no other
// reason to take a dependency on golang.org/x/crypto. A password hash is not
// somewhere to be adventurous; PBKDF2-HMAC-SHA256 at a high iteration count is
// still an accepted choice, and it costs nothing to carry.

import (
	"crypto/pbkdf2"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
)

const (
	// pbkdf2Iterations follows OWASP's guidance for PBKDF2-HMAC-SHA256. It is
	// recorded in every hash rather than assumed, so raising it later does not
	// lock anybody out: an old hash keeps verifying with the count it was made
	// with.
	pbkdf2Iterations = 600_000
	pbkdf2SaltBytes  = 16
	pbkdf2KeyBytes   = 32
	pbkdf2Scheme     = "pbkdf2-sha256"
)

// HashPassword returns an encoded hash of the form
//
//	pbkdf2-sha256$<iterations>$<salt-b64>$<key-b64>
//
// Every parameter needed to verify it travels with it.
func HashPassword(password string) (string, error) {
	salt := make([]byte, pbkdf2SaltBytes)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("PASSWORD_HASH_FAILED: no randomness available: %w", err)
	}
	return encodeHash(salt, pbkdf2Iterations, derive(password, salt, pbkdf2Iterations)), nil
}

// VerifyPassword reports whether password matches encoded.
//
// It answers false rather than an error for every malformed input: a caller
// that has to distinguish "wrong password" from "corrupt hash" would leak that
// distinction to whoever is guessing.
func VerifyPassword(encoded, password string) bool {
	scheme, iter, salt, want, ok := decodeHash(encoded)
	if !ok || scheme != pbkdf2Scheme {
		return false
	}
	got := derive(password, salt, iter)
	return subtle.ConstantTimeCompare(got, want) == 1
}

// dummyHash is verified against when no such account exists, so that a request
// for an unknown username costs the same as one for a known username with the
// wrong password. Without it, response time answers "does this person have an
// account here" — which, for a service about benefits and unemployment, is a
// disclosure in its own right.
var dummyHash = func() string {
	salt := make([]byte, pbkdf2SaltBytes)
	// A fixed salt is fine: nothing verifies against this successfully. It only
	// has to cost the same as a real verification.
	return encodeHash(salt, pbkdf2Iterations, derive("", salt, pbkdf2Iterations))
}()

// SpendVerificationTime performs the work of a password check without an
// account to check against. Callers use it on the unknown-username path.
func SpendVerificationTime(password string) {
	_ = VerifyPassword(dummyHash, password)
}

func derive(password string, salt []byte, iter int) []byte {
	// pbkdf2.Key returns an error only for an invalid key length, which is a
	// constant here.
	key, err := pbkdf2.Key(sha256.New, password, salt, iter, pbkdf2KeyBytes)
	if err != nil {
		panic("store: pbkdf2 with constant parameters failed: " + err.Error())
	}
	return key
}

func encodeHash(salt []byte, iter int, key []byte) string {
	return fmt.Sprintf("%s$%d$%s$%s", pbkdf2Scheme, iter,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key))
}

func decodeHash(encoded string) (scheme string, iter int, salt, key []byte, ok bool) {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 {
		return "", 0, nil, nil, false
	}
	iter, err := strconv.Atoi(parts[1])
	if err != nil || iter <= 0 {
		return "", 0, nil, nil, false
	}
	if salt, err = base64.RawStdEncoding.DecodeString(parts[2]); err != nil {
		return "", 0, nil, nil, false
	}
	if key, err = base64.RawStdEncoding.DecodeString(parts[3]); err != nil {
		return "", 0, nil, nil, false
	}
	return parts[0], iter, salt, key, true
}

// NewSignInToken returns a token to put in the cookie and the hash to persist.
// The raw token is never stored: a leaked state file must not hand anybody a
// live sign-in.
func NewSignInToken() (token, hash string, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("SIGNIN_TOKEN_FAILED: no randomness available: %w", err)
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, HashSignInToken(token), nil
}

// HashSignInToken is the one-way map from a cookie value to the key it is
// stored under. SHA-256 without a work factor is right here and wrong for a
// password: the input is 256 bits of randomness, so there is no dictionary to
// run against it.
func HashSignInToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}
