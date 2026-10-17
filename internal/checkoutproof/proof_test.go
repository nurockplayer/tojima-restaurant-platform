package checkoutproof

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"testing"
	"time"
)

func TestIssueVerifyAndRotation(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 30, 0, 123, time.FixedZone("JST", 9*60*60))
	oldKey := testKey(1)
	newKey := testKey(2)
	oldService := mustNew(t, Config{SigningKeys: [][]byte{oldKey}, TTL: 5 * time.Minute})
	token, issued, err := oldService.Issue(now)
	if err != nil {
		t.Fatalf("Issue() error = %v", err)
	}
	if len(token) == 0 || len(issued.Hash) != sha256.Size {
		t.Fatalf("Issue() returned token length %d, hash length %d", len(token), len(issued.Hash))
	}
	if want := now.Add(5 * time.Minute); !issued.ExpiresAt.Equal(want) {
		t.Fatalf("expiration = %s, want %s", issued.ExpiresAt, want)
	}

	rotated := mustNew(t, Config{SigningKeys: [][]byte{newKey, oldKey}, TTL: 5 * time.Minute})
	verified, err := rotated.Verify(token, now.Add(time.Minute))
	if err != nil {
		t.Fatalf("rotated Verify() error = %v", err)
	}
	if !hmac.Equal(issued.Hash, verified.Hash) || !issued.ExpiresAt.Equal(verified.ExpiresAt) {
		t.Fatalf("rotation changed proof: issued=%+v verified=%+v", issued, verified)
	}
	if want := sha256.Sum256([]byte(token)); !hmac.Equal(verified.Hash, want[:]) {
		t.Fatal("proof hash is not SHA-256 of the complete raw token")
	}

	newToken, _, err := rotated.Issue(now)
	if err != nil {
		t.Fatalf("rotated Issue() error = %v", err)
	}
	if token == newToken {
		t.Fatal("separate Issue calls produced the same context token")
	}
	if _, err := oldService.Verify(newToken, now); !errors.Is(err, ErrInvalidProof) {
		t.Fatalf("old service accepted new-key token: %v", err)
	}
}

func TestVerifyRejectsInvalidTokenConditions(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	key := testKey(3)
	service := mustNew(t, Config{SigningKeys: [][]byte{key}, TTL: time.Minute})
	token, _, err := service.Issue(now)
	if err != nil {
		t.Fatal(err)
	}

	tampered := token[:len(token)-1] + differentBase64Tail(token[len(token)-1:])
	otherKey := mustNew(t, Config{SigningKeys: [][]byte{testKey(4)}, TTL: time.Minute})
	future, _, err := service.Issue(now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	tooLongLifetime := signedPayload(t, key, now, now.Add(time.Minute+time.Nanosecond))
	wrongVersion := signedPayloadVersion(t, key, token, 2)

	cases := []struct {
		name  string
		check func() (Proof, error)
	}{
		{"empty", func() (Proof, error) { return service.Verify("", now) }},
		{"malformed", func() (Proof, error) { return service.Verify("a.b", now) }},
		{"tampered", func() (Proof, error) { return service.Verify(tampered, now) }},
		{"wrong key", func() (Proof, error) { return otherKey.Verify(token, now) }},
		{"expired", func() (Proof, error) { return service.Verify(token, now.Add(time.Minute)) }},
		{"future issued", func() (Proof, error) { return service.Verify(future, now) }},
		{"lifetime exceeds TTL", func() (Proof, error) { return service.Verify(tooLongLifetime, now) }},
		{"unknown version", func() (Proof, error) { return service.Verify(wrongVersion, now) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := tc.check(); !errors.Is(err, ErrInvalidProof) {
				t.Fatalf("error = %v, want ErrInvalidProof", err)
			}
		})
	}
}

func TestNewValidatesAndCopiesKeys(t *testing.T) {
	if _, err := New(Config{SigningKeys: [][]byte{testKey(1)}, TTL: 0}); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("zero TTL error = %v", err)
	}
	if _, err := New(Config{SigningKeys: [][]byte{[]byte("too short")}, TTL: time.Second}); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("short-key error = %v", err)
	}
	if _, err := New(Config{TTL: time.Second}); !errors.Is(err, ErrConfiguration) {
		t.Fatalf("no-key error = %v", err)
	}

	key := testKey(5)
	service := mustNew(t, Config{SigningKeys: [][]byte{key}, TTL: time.Minute})
	wantKey := append([]byte(nil), key...)
	for i := range key {
		key[i] ^= 0xff
	}
	token, _, err := service.Issue(time.Unix(100, 0))
	if err != nil {
		t.Fatal(err)
	}
	verifier := mustNew(t, Config{SigningKeys: [][]byte{wantKey}, TTL: time.Minute})
	if _, err := verifier.Verify(token, time.Unix(100, 0)); err != nil {
		t.Fatalf("mutating caller key changed service key: %v", err)
	}
}

func TestVerifyRejectsNonCanonicalAndOversizedTokens(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()
	service := mustNew(t, Config{SigningKeys: [][]byte{testKey(6)}, TTL: time.Minute})
	token, _, err := service.Issue(now)
	if err != nil {
		t.Fatal(err)
	}
	for _, raw := range []string{token + "=", token + "x", string(make([]byte, maxTokenSize+1))} {
		if _, err := service.Verify(raw, now); !errors.Is(err, ErrInvalidProof) {
			t.Fatalf("Verify(%q...) error = %v, want ErrInvalidProof", raw[:min(len(raw), 12)], err)
		}
	}
}

func mustNew(t *testing.T, cfg Config) *Service {
	t.Helper()
	service, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return service
}

func testKey(seed byte) []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = seed + byte(i)
	}
	return key
}

func differentBase64Tail(tail string) string {
	if tail == "A" {
		return "B"
	}
	return "A"
}

func signedPayload(t *testing.T, key []byte, issuedAt, expiresAt time.Time) string {
	t.Helper()
	var payload [payloadSize]byte
	payload[0] = tokenVersion
	writeTime(payload[1:13], issuedAt)
	writeTime(payload[13:25], expiresAt)
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(macInput(payload[:]))
	return base64.RawURLEncoding.EncodeToString(payload[:]) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func signedPayloadVersion(t *testing.T, key []byte, token string, version byte) string {
	t.Helper()
	payloadPart, _, ok := splitToken(token)
	if !ok {
		t.Fatal("test token could not be split")
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadPart)
	if err != nil {
		t.Fatal(err)
	}
	payload[0] = version
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(macInput(payload))
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func writeTime(dst []byte, value time.Time) {
	binary.BigEndian.PutUint64(dst[:8], uint64(value.Unix()))
	binary.BigEndian.PutUint32(dst[8:12], uint32(value.Nanosecond()))
}
