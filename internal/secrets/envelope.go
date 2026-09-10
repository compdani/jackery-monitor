package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

// KeyFile accepts both the preview override and the original Python override.
func KeyFile(root string) string {
	for _, name := range []string{"JACKERY_CREDS_KEY_FILE", "JACKERY_AT_REST_KEY_FILE"} {
		if path := os.Getenv(name); path != "" {
			return path
		}
	}
	return filepath.Join(root, ".jackery-creds.key")
}

// Seal and Open share the credential envelope without imposing its payload shape.
func (s *Store) Seal(value any) (json.RawMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key, err := s.key(true)
	if err != nil {
		return nil, err
	}
	plain, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return nil, err
	}
	sealed := gcm.Seal(nil, nonce, plain, nil)
	split := len(sealed) - gcm.Overhead()
	return json.Marshal(envelope{"v1", "AES-256-GCM", nonce, sealed[split:], sealed[:split]})
}
func (s *Store) Open(raw []byte, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var blob envelope
	if json.Unmarshal(raw, &blob) != nil || blob.Version != "v1" || blob.Algorithm != "AES-256-GCM" {
		return errors.New("unsupported encrypted envelope")
	}
	key, err := s.key(false)
	if err != nil {
		return err
	}
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	if len(blob.Nonce) != gcm.NonceSize() || len(blob.Tag) != gcm.Overhead() {
		return errors.New("invalid encrypted envelope")
	}
	plain, err := gcm.Open(nil, blob.Nonce, append(blob.Ciphertext, blob.Tag...), nil)
	if err != nil {
		return errors.New("unable to decrypt saved data with the existing key")
	}
	return json.Unmarshal(plain, value)
}
