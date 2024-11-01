package pkg

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
)

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
