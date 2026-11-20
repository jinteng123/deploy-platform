package service

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

// ErrCipherNotConfigured means CREDENTIAL_ENCRYPTION_KEY was not provided.
var ErrCipherNotConfigured = errors.New("CREDENTIAL_ENCRYPTION_KEY 未配置")

// CredentialCipher encrypts cluster credentials at rest: AES-256-GCM with a
// random nonce per message, serialized as `v1:<base64(nonce|sealed)>`.
// Plaintexts, keys and ciphertexts must never be logged.
type CredentialCipher struct{ key []byte }

// NewCredentialCipher decodes a base64-encoded 32-byte key.
func NewCredentialCipher(b64Key string) (*CredentialCipher, error) {
	if strings.TrimSpace(b64Key) == "" {
		return nil, ErrCipherNotConfigured
	}
	key, err := base64.StdEncoding.DecodeString(b64Key)
	if err != nil {
		return nil, fmt.Errorf("CREDENTIAL_ENCRYPTION_KEY 不是合法 base64: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("CREDENTIAL_ENCRYPTION_KEY 必须是 32 字节 (base64 编码后 %d 字节)", len(key))
	}
	return &CredentialCipher{key: key}, nil
}

func (c *CredentialCipher) gcm() (cipher.AEAD, error) {
	block, err := aes.NewCipher(c.key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// Encrypt seals plaintext; every call uses a fresh random nonce so identical
// plaintexts never produce identical ciphertexts.
func (c *CredentialCipher) Encrypt(plaintext string) (string, error) {
	gcm, err := c.gcm()
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	sealed := gcm.Seal(nil, nonce, []byte(plaintext), nil)
	return "v1:" + base64.StdEncoding.EncodeToString(append(nonce, sealed...)), nil
}

// Decrypt opens a `v1:` ciphertext. Any format violation or authentication
// failure is reported without details that could aid tampering.
func (c *CredentialCipher) Decrypt(ciphertext string) (string, error) {
	if !strings.HasPrefix(ciphertext, "v1:") {
		return "", errors.New("credential ciphertext 格式非法")
	}
	data, err := base64.StdEncoding.DecodeString(ciphertext[3:])
	if err != nil {
		return "", errors.New("credential ciphertext 解码失败")
	}
	gcm, err := c.gcm()
	if err != nil {
		return "", err
	}
	if len(data) <= gcm.NonceSize() {
		return "", errors.New("credential ciphertext 长度非法")
	}
	plain, err := gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], nil)
	if err != nil {
		return "", errors.New("credential ciphertext 校验失败")
	}
	return string(plain), nil
}
