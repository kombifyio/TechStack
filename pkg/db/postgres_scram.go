package db

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"strings"

	"golang.org/x/crypto/pbkdf2"
	"golang.org/x/text/secure/precis"
)

// PostgresSCRAMSecretMatches proves custody against the actual stored verifier
// without exposing or rotating it. The existing initializer's exact SCRAM
// profile is required; unsupported/malformed custody is not adopted.
func PostgresSCRAMSecretMatches(password, verifier string) bool {
	parts := strings.Split(verifier, "$")
	if len(parts) != 3 || parts[0] != "SCRAM-SHA-256" || password == "" {
		return false
	}
	parameters := strings.Split(parts[1], ":")
	keys := strings.Split(parts[2], ":")
	if len(parameters) != 2 || parameters[0] != "4096" || len(keys) != 2 {
		return false
	}
	salt, err := base64.StdEncoding.DecodeString(parameters[1])
	if err != nil || len(salt) != 16 {
		return false
	}
	storedKey, err := base64.StdEncoding.DecodeString(keys[0])
	if err != nil || len(storedKey) != sha256.Size {
		return false
	}
	serverKey, err := base64.StdEncoding.DecodeString(keys[1])
	if err != nil || len(serverKey) != sha256.Size {
		return false
	}
	if normalized, err := precis.OpaqueString.String(password); err == nil {
		password = normalized
	}
	salted := pbkdf2.Key([]byte(password), salt, 4096, sha256.Size, sha256.New)
	defer clear(salted)
	clientMAC := hmac.New(sha256.New, salted)
	clientMAC.Write([]byte("Client Key"))
	clientKey := clientMAC.Sum(nil)
	defer clear(clientKey)
	actualStored := sha256.Sum256(clientKey)
	serverMAC := hmac.New(sha256.New, salted)
	serverMAC.Write([]byte("Server Key"))
	actualServer := serverMAC.Sum(nil)
	defer clear(actualServer)
	return subtle.ConstantTimeCompare(actualStored[:], storedKey)&subtle.ConstantTimeCompare(actualServer, serverKey) == 1
}
