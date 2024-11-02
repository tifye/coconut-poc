package pkg

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
)

var (
	rawTestKeys = []string{
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFcWXKvdMek6mamQu59ygy9ugCk0O3BtBWUUCI3g2uYp",
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJvpH9VT9PfzWGAM1WUl1Vi+fSNBYkAHIbWJOrG5LRdx",
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBDYy1jvK8urEDUBsIO6eXgjzzrbCwzs91so6izWlcQK",
	}
)

type authorizedKeys map[string]ssh.PublicKey

func (ak authorizedKeys) IsAuthorized(pk ssh.PublicKey) bool {
	hash := ssh.FingerprintSHA256(pk)
	_, exists := ak[hash]
	return exists
}

func loadAuthorizedKeys() (authorizedKeys, error) {
	keys := make(authorizedKeys)

	for _, rawKey := range rawTestKeys {
		pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(rawKey))
		if err != nil {
			return nil, err
		}

		hash := ssh.FingerprintSHA256(pk)
		keys[hash] = pk
	}

	return keys, nil
}

func getSigner() (ssh.Signer, error) {
	// Todo: how to properly read/create a key?
	privateKeyBytes, err := os.ReadFile(filepath.Join(os.Getenv("KEYS_DIR") + "\\id_ed25519"))
	if err != nil {
		return nil, fmt.Errorf("failed to read private key, got: %s", err)
	}

	signer, err := ssh.ParsePrivateKey(privateKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key bytes, got: %s", err)
	}

	return signer, nil
}
