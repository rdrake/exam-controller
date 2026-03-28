package slug

import (
	"crypto/rand"
	"math/big"
)

const (
	letters  = "abcdefghijklmnopqrstuvwxyz"
	alphanum = letters + "0123456789"
)

// Generate returns a cryptographically random 8-character DNS-1035 label.
// The first character is always a letter to satisfy DNS naming rules.
func Generate() (string, error) {
	b := make([]byte, 8)
	for i := range b {
		charset := alphanum
		if i == 0 {
			charset = letters
		}
		n, err := rand.Int(rand.Reader, big.NewInt(int64(len(charset))))
		if err != nil {
			return "", err
		}
		b[i] = charset[n.Int64()]
	}
	return string(b), nil
}
