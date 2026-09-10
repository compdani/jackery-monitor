// Package secrets reads the Python runtime's AES-256-GCM envelope format.
// Keys and ciphertext stay outside PocketBase collections and API responses.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

type Credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Region   string `json:"region"`
}
type envelope struct {
	Version    string `json:"v"`
	Algorithm  string `json:"alg"`
	Nonce      []byte `json:"nonce"`
	Tag        []byte `json:"tag"`
	Ciphertext []byte `json:"ct"`
}
type Store struct {
	Path, KeyPath string
	mu            sync.Mutex
}

func (s *Store) key(create bool) ([]byte, error) {
	key, err := os.ReadFile(s.KeyPath)
	if err == nil {
		if len(key) != 32 {
			return nil, errors.New("credential key must be 32 bytes; refusing to replace it")
		}
		return key, nil
	}
	if !create || !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key = make([]byte, 32)
	if _, err = rand.Read(key); err != nil {
		return nil, err
	}
	if err = os.MkdirAll(filepath.Dir(s.KeyPath), 0700); err != nil {
		return nil, err
	}
	// Exclusive creation prevents silently replacing a key used by another process.
	f, err := os.OpenFile(s.KeyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if errors.Is(err, os.ErrExist) {
		return s.key(false)
	}
	if err != nil {
		return nil, err
	}
	_, err = f.Write(key)
	closeErr := f.Close()
	if err != nil {
		return nil, err
	}
	if closeErr != nil {
		return nil, closeErr
	}
	return key, nil
}
func (s *Store) Load() (Credentials, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var creds Credentials
	raw, err := os.ReadFile(s.Path)
	if err != nil {
		return creds, err
	}
	var blob envelope
	if json.Unmarshal(raw, &blob) != nil || blob.Version != "v1" || blob.Algorithm != "AES-256-GCM" {
		return creds, errors.New("unsupported credential envelope")
	}
	key, err := s.key(false)
	if err != nil {
		return creds, err
	}
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	if len(blob.Nonce) != gcm.NonceSize() || len(blob.Tag) != gcm.Overhead() {
		return creds, errors.New("invalid encrypted credential envelope")
	}
	plain, err := gcm.Open(nil, blob.Nonce, append(blob.Ciphertext, blob.Tag...), nil)
	if err != nil {
		return creds, errors.New("unable to decrypt credentials with the existing key")
	}
	if err = json.Unmarshal(plain, &creds); err != nil {
		return Credentials{}, errors.New("invalid credential payload")
	}
	if creds.Email == "" || creds.Password == "" {
		return Credentials{}, errors.New("empty saved credentials")
	}
	if creds.Region == "" {
		creds.Region = "US"
	}
	return creds, nil
}
func (s *Store) Save(creds Credentials) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if creds.Email == "" || creds.Password == "" {
		return errors.New("email and password required")
	}
	key, err := s.key(true)
	if err != nil {
		return err
	}
	block, _ := aes.NewCipher(key)
	gcm, _ := cipher.NewGCM(block)
	nonce := make([]byte, gcm.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return err
	}
	plain, err := json.Marshal(creds)
	if err != nil {
		return err
	}
	sealed := gcm.Seal(nil, nonce, plain, nil)
	raw, err := json.Marshal(envelope{"v1", "AES-256-GCM", nonce, sealed[len(sealed)-gcm.Overhead():], sealed[:len(sealed)-gcm.Overhead()]})
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(s.Path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(s.Path), ".creds-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err != nil {
		f.Close()
		return err
	}
	if err = f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	if err = os.Rename(f.Name(), s.Path); err != nil {
		return fmt.Errorf("save credentials: %w", err)
	}
	return nil
}
func (s *Store) Forget() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := os.Remove(s.Path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
