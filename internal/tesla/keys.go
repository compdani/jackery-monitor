package tesla

import (
	"crypto/ecdh"
	"crypto/rand"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"

	"github.com/teslamotors/vehicle-command/pkg/protocol"
)

// GenerateKey returns a new P-256 private scalar, hex encoded. This is the
// virtual key the car must pair with; keep it secret.
func GenerateKey() (string, error) {
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(k.Bytes()), nil
}

func parseScalar(privateHex string) (*ecdh.PrivateKey, error) {
	raw, err := hex.DecodeString(privateHex)
	if err != nil {
		return nil, fmt.Errorf("private key: %w", err)
	}
	k, err := ecdh.P256().NewPrivateKey(raw)
	if err != nil {
		return nil, fmt.Errorf("private key: %w", err)
	}
	return k, nil
}

// PublicKeyPEM returns the PEM-encoded public key Tesla fetches from
// PublicKeyPath (SubjectPublicKeyInfo, same as `openssl ec -pubout`).
func PublicKeyPEM(privateHex string) ([]byte, error) {
	k, err := parseScalar(privateHex)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKIXPublicKey(k.PublicKey())
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}), nil
}

// ImportPEM converts an "EC PRIVATE KEY" or PKCS#8 PEM (e.g. one made with
// openssl) to the hex scalar this package stores.
func ImportPEM(data []byte) (string, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return "", errors.New("no PEM block found")
	}
	if k, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		e, err := k.ECDH()
		if err != nil {
			return "", err
		}
		return hex.EncodeToString(e.Bytes()), nil
	}
	any, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return "", errors.New("unsupported private key format")
	}
	switch k := any.(type) {
	case *ecdh.PrivateKey:
		if k.Curve() != ecdh.P256() {
			return "", errors.New("key must be P-256")
		}
		return hex.EncodeToString(k.Bytes()), nil
	default:
		if ek, ok := any.(interface {
			ECDH() (*ecdh.PrivateKey, error)
		}); ok {
			e, err := ek.ECDH()
			if err != nil {
				return "", err
			}
			return hex.EncodeToString(e.Bytes()), nil
		}
		return "", errors.New("key must be an EC P-256 private key")
	}
}

func commandKey(privateHex string) (protocol.ECDHPrivateKey, error) {
	k, err := parseScalar(privateHex)
	if err != nil {
		return nil, err
	}
	key := protocol.UnmarshalECDHPrivateKey(k.Bytes())
	if key == nil {
		return nil, errors.New("private key rejected by vehicle-command")
	}
	return key, nil
}
