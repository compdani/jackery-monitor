package store

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
)

type Envelope struct {
	Version    string `json:"v"`
	Algorithm  string `json:"alg"`
	Nonce      string `json:"nonce"`
	Tag        string `json:"tag"`
	Ciphertext string `json:"ct"`
}

// LoadKey never replaces an invalid existing key. Exclusive creation prevents
// another startup from overwriting the key used to encrypt existing records.
func LoadKey(path string) ([]byte, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	k, e := os.ReadFile(path)
	if e == nil {
		if len(k) != 32 {
			return nil, errors.New("invalid encryption key length")
		}
		return k, nil
	}
	if !os.IsNotExist(e) {
		return nil, e
	}
	k = make([]byte, 32)
	if _, e = rand.Read(k); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if os.IsExist(e) {
		return LoadKey(path)
	}
	if e != nil {
		return nil, e
	}
	_, e = f.Write(k)
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e != nil {
		return nil, e
	}
	return k, ce
}
func Encrypt(key, plaintext []byte) (Envelope, error) {
	b, e := aes.NewCipher(key)
	if e != nil {
		return Envelope{}, e
	}
	g, e := cipher.NewGCM(b)
	if e != nil {
		return Envelope{}, e
	}
	n := make([]byte, g.NonceSize())
	if _, e = io.ReadFull(rand.Reader, n); e != nil {
		return Envelope{}, e
	}
	ct := g.Seal(nil, n, plaintext, nil)
	split := len(ct) - g.Overhead()
	enc := base64.StdEncoding.EncodeToString
	return Envelope{"v1", "AES-256-GCM", enc(n), enc(ct[split:]), enc(ct[:split])}, nil
}
func Decrypt(key []byte, v Envelope) ([]byte, error) {
	if v.Version != "v1" || v.Algorithm != "AES-256-GCM" {
		return nil, errors.New("unsupported encryption envelope")
	}
	dec := base64.StdEncoding.DecodeString
	n, e := dec(v.Nonce)
	if e != nil {
		return nil, e
	}
	tag, e := dec(v.Tag)
	if e != nil {
		return nil, e
	}
	ct, e := dec(v.Ciphertext)
	if e != nil {
		return nil, e
	}
	b, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	g, e := cipher.NewGCM(b)
	if e != nil {
		return nil, e
	}
	if len(n) != g.NonceSize() || len(tag) != g.Overhead() {
		return nil, errors.New("invalid nonce or tag")
	}
	return g.Open(nil, n, append(ct, tag...), nil)
}
func (s *Store) SaveSecret(key []byte, name string, value map[string]any) error {
	p, e := json.Marshal(value)
	if e != nil {
		return e
	}
	v, e := Encrypt(key, p)
	if e != nil {
		return e
	}
	p, e = json.Marshal(v)
	if e != nil {
		return e
	}
	var m map[string]any
	if e = json.Unmarshal(p, &m); e != nil {
		return e
	}
	return s.Put("credentials", name, "", m)
}
func (s *Store) Secret(key []byte, name string) (map[string]any, error) {
	m, e := s.Get("credentials", name)
	if e != nil {
		return nil, e
	}
	p, e := json.Marshal(m)
	if e != nil {
		return nil, e
	}
	var v Envelope
	if e = json.Unmarshal(p, &v); e != nil {
		return nil, e
	}
	p, e = Decrypt(key, v)
	if e != nil {
		return nil, e
	}
	var out map[string]any
	e = json.Unmarshal(p, &out)
	return out, e
}
