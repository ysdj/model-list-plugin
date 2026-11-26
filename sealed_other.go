//go:build !windows && !darwin

package main

import "errors"

// Linux keeps the key in the desktop keyring, which a headless hook cannot read
// without the Secret Service session the app itself uses.
func unsealBytes([]byte) (string, error) {
	return "", errors.New("this platform's key store cannot be read from a hook")
}
