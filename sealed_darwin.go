//go:build darwin

package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/pbkdf2"
	"crypto/sha1"
	"errors"
	"os/exec"
	"strings"
)

// Electron's safeStorage on macOS encrypts with a key held in the login
// keychain: AES-128-CBC under PBKDF2-HMAC-SHA1 of the "<App> Safe Storage"
// password, salt "saltysalt", 1003 rounds, and an IV of sixteen spaces.  The
// decrypted string is what the app itself reads back.

var safeStorageServices = []string{
	"Claude Safe Storage",
	"Claude-3p Safe Storage",
	"Claude Code Safe Storage",
	"Chromium Safe Storage",
}

func unsealBytes(raw []byte) (string, error) {
	if len(raw) < 3 || string(raw[:3]) != "v10" {
		return "", errors.New("not a safeStorage blob")
	}
	body := raw[3:]
	if len(body) == 0 || len(body)%aes.BlockSize != 0 {
		return "", errors.New("sealed value is not a whole number of blocks")
	}
	for _, service := range safeStorageServices {
		password := keychainPassword(service)
		if password == "" {
			continue
		}
		key, err := pbkdf2.Key(sha1.New, password, []byte("saltysalt"), 1003, 16)
		if err != nil {
			continue
		}
		block, err := aes.NewCipher(key)
		if err != nil {
			continue
		}
		plain := make([]byte, len(body))
		cipher.NewCBCDecrypter(block, []byte("                ")).CryptBlocks(plain, body)
		plain, ok := unpad(plain)
		if !ok || len(plain) == 0 {
			continue
		}
		if text := string(plain); printable(text) {
			return text, nil
		}
	}
	return "", errors.New("the keychain holds no key that opens this value")
}

// keychainPassword reads one generic password from the login keychain.
func keychainPassword(service string) string {
	output, err := exec.Command("security", "find-generic-password", "-s", service, "-w").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

func unpad(value []byte) ([]byte, bool) {
	if len(value) == 0 {
		return nil, false
	}
	padding := int(value[len(value)-1])
	if padding == 0 || padding > aes.BlockSize || padding > len(value) {
		return nil, false
	}
	for _, b := range value[len(value)-padding:] {
		if int(b) != padding {
			return nil, false
		}
	}
	return value[:len(value)-padding], true
}

func printable(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
