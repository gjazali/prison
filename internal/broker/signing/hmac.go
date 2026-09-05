// Package signing produces HMAC-SHA256 and SSHSIG signatures on
// behalf of boxes that hold no keys.
package signing

import (
	"crypto/hmac"
	"crypto/sha256"
)

// HMACSHA256 returns the raw 32-byte HMAC-SHA256 of data using the
// given key.
func HMACSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}
