package main

// A gateway key the Claude Desktop app wrote into its own configuration is
// sealed: the app keeps it encrypted with the operating system's key store and
// stores it as "__sealed__:v1:" followed by the base64 of an Electron
// safeStorage blob (a "v10" prefix plus the encrypted bytes).  Unsealing it is
// the difference between a hook that can read the gateway it runs behind and
// one that can only report that it cannot.

import (
	"encoding/base64"
	"errors"
	"strings"
)

// sealedPayload returns the encrypted bytes of a sealed value.
func sealedPayload(value string) ([]byte, error) {
	encoded := strings.TrimPrefix(strings.TrimSpace(value), sealedKeyPrefix)
	// The marker carries a version: "__sealed__:v1:<base64>".
	if index := strings.IndexByte(encoded, ':'); index > 0 && index <= 3 {
		encoded = encoded[index+1:]
	}
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		if alternative, altErr := base64.RawStdEncoding.DecodeString(encoded); altErr == nil {
			return alternative, nil
		}
		return nil, errors.New("sealed value is not base64")
	}
	return raw, nil
}

// unsealSecret returns the plaintext of a sealed value.  It only ever reports
// that this platform's key store could not be read from here; the caller
// decides what to tell the user.
func unsealSecret(value string) (string, error) {
	raw, err := sealedPayload(value)
	if err != nil {
		return "", err
	}
	plain, err := unsealBytes(raw)
	if err != nil || plain == "" {
		return "", errors.New("the sealed value could not be opened")
	}
	return plain, nil
}
