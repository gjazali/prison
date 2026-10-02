// Package signing signs data for boxes, which hold no keys.
package signing

import (
	"crypto/hmac"
	"crypto/sha256"
)

func HMACSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}
