package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"

	"github.com/charmbracelet/log"
	"github.com/joho/godotenv"
	"github.com/tifye/tunnel/cmd/cli"
	"github.com/tifye/tunnel/pkg"
	"golang.org/x/crypto/ssh"
)

var (
	authorizedTestKeys map[string]ssh.PublicKey
)

func init() {
	rawTestKeys := []string{
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIFcWXKvdMek6mamQu59ygy9ugCk0O3BtBWUUCI3g2uYp",
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIJvpH9VT9PfzWGAM1WUl1Vi+fSNBYkAHIbWJOrG5LRdx",
		"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBDYy1jvK8urEDUBsIO6eXgjzzrbCwzs91so6izWlcQK",
	}

	keys := make(map[string]ssh.PublicKey)
	for _, rawKey := range rawTestKeys {
		pk, _, _, _, err := ssh.ParseAuthorizedKey([]byte(rawKey))
		if err != nil {
			panic(err)
		}
		hash := ssh.FingerprintSHA256(pk)
		keys[hash] = pk
	}
	authorizedTestKeys = keys
}

func run(ctx context.Context, logger *log.Logger) error {
	signer, err := getSigner()
	if err != nil {
		return err
	}

	config := &pkg.ServerConfig{
		ClientListenerAddress:   "127.0.0.1:9000",
		Logger:                  logger,
		Signer:                  signer,
		PublicKeyAuthAlgorithms: []string{"ssh-ed25519"},
		PublicKeyCallback: func(conn ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			// todo: ssh.CertChecker, look into this more it seems like would be useful here
			hash := ssh.FingerprintSHA256(key)
			_, exists := authorizedTestKeys[hash]
			if !exists {
				return nil, errors.New("not authorized")
			}
			return nil, nil
		},
	}

	server, err := pkg.NewServer(config)
	if err != nil {
		return err
	}

	return server.Start(ctx)
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

	err = run(ctx, logger)
	if err != nil {
		log.Fatal(err)
	}
}

func getSigner() (ssh.Signer, error) {
	rawKey := os.Getenv("HOSTKEY")

	signer, err := ssh.ParsePrivateKey([]byte(rawKey))
	if err != nil {
		return nil, fmt.Errorf("failed to parse private key bytes, got: %s", err)
	}

	return signer, nil
}
