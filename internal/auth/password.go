// SPDX-License-Identifier: AGPL-3.0-only

// Package auth implements panel accounts: argon2id password hashing, user
// records, server-side sessions, login throttling and the one-time setup
// token used to create the first administrator.
package auth

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
)

// Argon2 parameters (RFC 9106 second recommended option, lowered parallelism
// for small servers). Stored in each hash, so they can change over time.
const (
	argonMemory  = 64 * 1024 // KiB
	argonTime    = 3
	argonThreads = 1
	argonSaltLen = 16
	argonKeyLen  = 32
)

// Account rules, mirrored by the web UI's client-side validation.
const (
	MinPasswordLen = 12
	MaxPasswordLen = 1024 // bounds hashing cost for hostile input
	MinUsernameLen = 3
	MaxUsernameLen = 32
)

var usernameRe = regexp.MustCompile(`^[a-z][a-z0-9_.-]*$`)

// FieldError describes an invalid input field; it maps to the API error code
// "validation.invalid" with params {field, rule, ...}.
type FieldError struct {
	Field string
	Rule  string // required | min_length | max_length | pattern | mismatch
	Min   int
	Max   int
}

func (e *FieldError) Error() string { return fmt.Sprintf("invalid %s: %s", e.Field, e.Rule) }

// ValidateUsername checks the username rules.
func ValidateUsername(u string) error {
	switch n := len(u); {
	case n == 0:
		return &FieldError{Field: "username", Rule: "required"}
	case n < MinUsernameLen:
		return &FieldError{Field: "username", Rule: "min_length", Min: MinUsernameLen}
	case n > MaxUsernameLen:
		return &FieldError{Field: "username", Rule: "max_length", Max: MaxUsernameLen}
	case !usernameRe.MatchString(u):
		return &FieldError{Field: "username", Rule: "pattern"}
	}
	return nil
}

// ValidatePassword checks the password rules (length counted in characters).
func ValidatePassword(p string) error {
	switch n := utf8.RuneCountInString(p); {
	case n == 0:
		return &FieldError{Field: "password", Rule: "required"}
	case n < MinPasswordLen:
		return &FieldError{Field: "password", Rule: "min_length", Min: MinPasswordLen}
	case len(p) > MaxPasswordLen:
		return &FieldError{Field: "password", Rule: "max_length", Max: MaxPasswordLen}
	}
	return nil
}

// hashSlots bounds how many argon2 computations run at once. Each takes
// argonMemory (64 MiB): unbounded, a burst of sign-in requests would run
// the server out of memory. Requests beyond it wait, as long as their
// context lets them and at most hashWait; then they fail with ErrBusy.
var (
	hashSlots = make(chan struct{}, 4)
	hashWait  = 10 * time.Second
)

// ErrBusy is returned when no hashing slot became free in time.
var ErrBusy = errors.New("auth: too many password checks at once")

// argon2Key runs argon2id in a slot.
func argon2Key(ctx context.Context, password, salt []byte, iter, mem uint32, threads uint8) ([]byte, error) {
	timer := time.NewTimer(hashWait)
	defer timer.Stop()
	select {
	case hashSlots <- struct{}{}:
	case <-timer.C:
		return nil, ErrBusy
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	defer func() { <-hashSlots }()
	return argon2.IDKey(password, salt, iter, mem, threads, argonKeyLen), nil
}

// HashPassword returns an argon2id hash in PHC string format.
func HashPassword(ctx context.Context, password string) (string, error) {
	if len(password) > MaxPasswordLen {
		return "", errors.New("auth: password too long")
	}
	salt := make([]byte, argonSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", err
	}
	key, err := argon2Key(ctx, []byte(password), salt, argonTime, argonMemory, argonThreads)
	if err != nil {
		return "", err
	}
	b64 := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, argonMemory, argonTime, argonThreads, b64.EncodeToString(salt), b64.EncodeToString(key)), nil
}

// VerifyPassword reports whether password matches the PHC-encoded hash.
func VerifyPassword(ctx context.Context, password, encoded string) (bool, error) {
	if len(password) > MaxPasswordLen {
		return false, nil
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != 6 || parts[1] != "argon2id" {
		return false, errors.New("auth: unsupported hash format")
	}
	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil || version != argon2.Version {
		return false, errors.New("auth: unsupported argon2 version")
	}
	var mem, iter uint32
	var threads uint8
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d", &mem, &iter, &threads); err != nil {
		return false, fmt.Errorf("auth: bad argon2 parameters: %w", err)
	}
	b64 := base64.RawStdEncoding
	salt, err := b64.DecodeString(parts[4])
	if err != nil {
		return false, err
	}
	want, err := b64.DecodeString(parts[5])
	if err != nil {
		return false, err
	}
	if len(want) != argonKeyLen {
		return false, errors.New("auth: unsupported argon2 key length")
	}
	got, err := argon2Key(ctx, []byte(password), salt, iter, mem, threads)
	if err != nil {
		return false, err
	}
	return subtle.ConstantTimeCompare(got, want) == 1, nil
}

// dummyHash is verified against when a username does not exist, so that the
// response time does not reveal which usernames are valid. Computed lazily:
// hashing costs 64 MiB and should not run at every process start.
var dummyHash = sync.OnceValue(func() string {
	h, err := HashPassword(context.Background(), "cairn-timing-equalizer-not-a-password")
	if err != nil {
		panic(err)
	}
	return h
})

// RandomToken returns n random bytes encoded as unpadded base64url.
func RandomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

// RandomPassword returns a password of length n from an unambiguous alphabet,
// drawing characters uniformly (rand.Text-style rejection sampling).
func RandomPassword(n int) (string, error) {
	const alphabet = "abcdefghijkmnpqrstuvwxyzABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	limit := 256 - 256%len(alphabet) // reject bytes that would bias the modulo
	out := make([]byte, 0, n)
	buf := make([]byte, 64)
	for len(out) < n {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, c := range buf {
			if int(c) < limit && len(out) < n {
				out = append(out, alphabet[int(c)%len(alphabet)])
			}
		}
	}
	return string(out), nil
}
