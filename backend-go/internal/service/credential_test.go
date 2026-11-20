package service

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func testKey(t *testing.T) string {
	t.Helper()
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(key)
}

func TestCredentialCipherRoundTrip(t *testing.T) {
	c, err := NewCredentialCipher(testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	plain := "apiVersion: v1\nkind: Config\nsecret-token"
	sealed, err := c.Encrypt(plain)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(sealed, "v1:") {
		t.Fatalf("ciphertext must carry v1 prefix: %q", sealed)
	}
	if strings.Contains(sealed, plain) {
		t.Fatal("ciphertext embeds plaintext")
	}
	got, err := c.Decrypt(sealed)
	if err != nil || got != plain {
		t.Fatalf("round trip = %q,%v", got, err)
	}
}

func TestCredentialCipherRandomNonce(t *testing.T) {
	c, err := NewCredentialCipher(testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	a, _ := c.Encrypt("same-plaintext")
	b, _ := c.Encrypt("same-plaintext")
	if a == b {
		t.Fatal("identical plaintext produced identical ciphertext — nonce reuse")
	}
}

func TestCredentialCipherTamper(t *testing.T) {
	c, err := NewCredentialCipher(testKey(t))
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := c.Encrypt("sensitive")
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(sealed, "v1:"))
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)-1] ^= 0xff // flip a bit in the sealed payload
	tampered := "v1:" + base64.StdEncoding.EncodeToString(raw)
	if _, err := c.Decrypt(tampered); err == nil {
		t.Fatal("tampered ciphertext must not decrypt")
	}
}

func TestCredentialCipherWrongKey(t *testing.T) {
	a, _ := NewCredentialCipher(testKey(t))
	b, _ := NewCredentialCipher(testKey(t))
	sealed, _ := a.Encrypt("x")
	if _, err := b.Decrypt(sealed); err == nil {
		t.Fatal("decrypt with wrong key must fail")
	}
}

func TestCredentialCipherConfigErrors(t *testing.T) {
	if _, err := NewCredentialCipher(""); !errors.Is(err, ErrCipherNotConfigured) {
		t.Fatalf("empty key err=%v", err)
	}
	if _, err := NewCredentialCipher("!!!not-base64!!!"); err == nil {
		t.Fatal("invalid base64 must fail")
	}
	if _, err := NewCredentialCipher(base64.StdEncoding.EncodeToString(make([]byte, 16))); err == nil {
		t.Fatal("16-byte key must fail")
	}
	c, _ := NewCredentialCipher(testKey(t))
	for _, bad := range []string{"", "no-version", "v2:AAAA", "v1:", "v1:not-base64", "v1:AA=="} {
		if _, err := c.Decrypt(bad); err == nil {
			t.Fatalf("Decrypt(%q) must fail", bad)
		}
	}
}
