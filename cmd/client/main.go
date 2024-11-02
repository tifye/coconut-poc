package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"

	"github.com/charmbracelet/log"
	"github.com/joho/godotenv"
	"github.com/tifye/tunnel/cmd/cli"
	"github.com/tifye/tunnel/pkg"
	"golang.org/x/crypto/ssh"
)

func run(ctx context.Context, logger *log.Logger, args []string) error {
	target := "http://localhost:6280"
	if len(args) > 0 {
		target = args[0]
	}

	hostkey, _, _, _, err := ssh.ParseAuthorizedKey([]byte("ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIH4Rvid2IsaTT87t5nOcFXIimWRQejEaHB2LBwYkFqv1"))
	if err != nil {
		return err
	}

	config := &pkg.ClientConfig{
		ProxyPass:     target,
		User:          "tifye",
		ServerHostKey: hostkey,
		Auth: []ssh.AuthMethod{
			ssh.PublicKeysCallback(func() (signers []ssh.Signer, err error) {
				privateKeyBytes, err := os.ReadFile(filepath.Join(os.Getenv("KEYS_DIR") + "\\id_ed25519"))
				if err != nil {
					return nil, fmt.Errorf("failed to read private key, got: %s", err)
				}

				signer, err := ssh.ParsePrivateKey(privateKeyBytes)
				if err != nil {
					return nil, fmt.Errorf("failed to parse private key bytes, got: %s", err)
				}
				return []ssh.Signer{signer}, err
			}),
		},
		Logger: logger,
	}

	client, err := pkg.NewClient(config)
	if err != nil {
		return err
	}

	return client.Start(ctx, "127.0.0.1:9000")
}

func main() {
	logger := log.NewWithOptions(os.Stdout, log.Options{
		Level:           log.DebugLevel,
		TimeFormat:      "15:04:05",
		ReportTimestamp: true,
	})

	path, file, err := cli.OpenNewLogFile()
	if err != nil {
		logger.Fatal(err)
	}
	defer file.Close()

	w := io.MultiWriter(os.Stdout, file)
	logger.SetOutput(w)

	logger.Infof("Outputing logs to %s", path)

	err = godotenv.Load()
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	err = run(ctx, logger, os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}
}
