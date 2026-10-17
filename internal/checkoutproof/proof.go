// Package checkoutproof issues short-lived browser-context proofs for a single
// checkout attempt. The raw token is returned only to the caller that issues
// it; persistent state should store Proof.Hash instead.
package checkoutproof

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"time"
)

const (
	tokenVersion  = byte(1)
	nonceSize     = 32
	payloadSize   = 1 + 8 + 4 + 8 + 4 + nonceSize
	signatureSize = sha256.Size
	maxTokenSize  = 128
)

var (
	// ErrInvalidProof is returned for every invalid token condition so callers
	// do not learn whether a token was malformed, expired, or signed elsewhere.
	ErrInvalidProof  = errors.New("invalid checkout proof")
	ErrConfiguration = errors.New("invalid checkout proof configuration")
)

// Config supplies the signing key followed by any previous verification keys.
// Keys must contain at least 32 bytes. New copies every key.
type Config struct {
	SigningKeys [][]byte
	TTL         time.Duration
}

// Proof is the database-safe identity of an issued browser context. Hash is
// SHA-256 over the complete raw token and is always returned as a fresh slice.
type Proof struct {
	Hash      []byte
	ExpiresAt time.Time
}

// Service signs with the first configured key and verifies with every key.
type Service struct {
	keys [][]byte
	ttl  time.Duration
}

// New constructs a proof service. The first key signs new tokens; all keys
// verify existing tokens to support rotation from current to previous keys.
func New(cfg Config) (*Service, error) {
	if cfg.TTL <= 0 || len(cfg.SigningKeys) == 0 {
		return nil, ErrConfiguration
	}
	keys := make([][]byte, len(cfg.SigningKeys))
	for i, key := range cfg.SigningKeys {
		if len(key) < 32 {
			return nil, ErrConfiguration
		}
		keys[i] = append([]byte(nil), key...)
	}
	return &Service{keys: keys, ttl: cfg.TTL}, nil
}

// Issue creates an unrelated random token and its stable database-safe proof.
func (s *Service) Issue(now time.Time) (string, Proof, error) {
	if s == nil || len(s.keys) == 0 || s.ttl <= 0 {
		return "", Proof{}, ErrConfiguration
	}
	var nonce [nonceSize]byte
	if _, err := io.ReadFull(rand.Reader, nonce[:]); err != nil {
		return "", Proof{}, err
	}

	expiresAt := now.Add(s.ttl)
	payload := make([]byte, payloadSize)
	payload[0] = tokenVersion
	putTime(payload[1:13], now)
	putTime(payload[13:25], expiresAt)
	copy(payload[25:], nonce[:])

	mac := hmac.New(sha256.New, s.keys[0])
	_, _ = mac.Write(macInput(payload))
	token := base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return token, Proof{Hash: hashToken(token), ExpiresAt: expiresAt}, nil
}

// Verify authenticates a token and returns the same hash and expiration that
// Issue produced. It rejects malformed, future-issued, expired, and over-TTL
// tokens with the same error.
func (s *Service) Verify(raw string, now time.Time) (Proof, error) {
	if s == nil || len(s.keys) == 0 || s.ttl <= 0 || len(raw) == 0 || len(raw) > maxTokenSize {
		return Proof{}, ErrInvalidProof
	}
	payloadPart, signaturePart, ok := splitToken(raw)
	if !ok {
		return Proof{}, ErrInvalidProof
	}
	payload, ok := decodeCanonical(payloadPart)
	if !ok || len(payload) != payloadSize || payload[0] != tokenVersion {
		return Proof{}, ErrInvalidProof
	}
	signature, ok := decodeCanonical(signaturePart)
	if !ok || len(signature) != signatureSize {
		return Proof{}, ErrInvalidProof
	}

	validMAC := 0
	input := macInput(payload)
	for _, key := range s.keys {
		mac := hmac.New(sha256.New, key)
		_, _ = mac.Write(input)
		validMAC |= subtle.ConstantTimeCompare(signature, mac.Sum(nil))
	}
	if validMAC != 1 {
		return Proof{}, ErrInvalidProof
	}
	issuedAt, okIssued := readTime(payload[1:13])
	expiresAt, okExpires := readTime(payload[13:25])
	if !okIssued || !okExpires || issuedAt.After(now) || !now.Before(expiresAt) {
		return Proof{}, ErrInvalidProof
	}
	if !expiresAt.After(issuedAt) || expiresAt.After(issuedAt.Add(s.ttl)) {
		return Proof{}, ErrInvalidProof
	}
	return Proof{Hash: hashToken(raw), ExpiresAt: expiresAt}, nil
}

func splitToken(raw string) (string, string, bool) {
	for i := 0; i < len(raw); i++ {
		if raw[i] == '.' {
			if i == 0 || i == len(raw)-1 || strings.IndexByte(raw[i+1:], '.') >= 0 {
				return "", "", false
			}
			return raw[:i], raw[i+1:], true
		}
	}
	return "", "", false
}

func decodeCanonical(part string) ([]byte, bool) {
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(part)
	if err != nil || base64.RawURLEncoding.EncodeToString(decoded) != part {
		return nil, false
	}
	return decoded, true
}

func macInput(payload []byte) []byte {
	const domain = "tojima-checkout-proof/v1\x00"
	input := make([]byte, len(domain)+len(payload))
	copy(input, domain)
	copy(input[len(domain):], payload)
	return input
}

func putTime(dst []byte, value time.Time) {
	binary.BigEndian.PutUint64(dst[:8], uint64(value.Unix()))
	binary.BigEndian.PutUint32(dst[8:12], uint32(value.Nanosecond()))
}

func readTime(src []byte) (time.Time, bool) {
	seconds := int64(binary.BigEndian.Uint64(src[:8]))
	nanos := binary.BigEndian.Uint32(src[8:12])
	if nanos >= 1_000_000_000 {
		return time.Time{}, false
	}
	return time.Unix(seconds, int64(nanos)).UTC(), true
}

func hashToken(raw string) []byte {
	sum := sha256.Sum256([]byte(raw))
	return append([]byte(nil), sum[:]...)
}
