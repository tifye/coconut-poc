package main

import (
	"context"
	"os"
	"os/signal"

	"github.com/charmbracelet/log"
	"github.com/joho/godotenv"
	"github.com/tifye/tunnel/pkg"
)

func run(ctx context.Context, logger *log.Logger) error {
	server, err := pkg.NewServer("127.0.0.1:9000", logger)
	if err != nil {
		return err
	}

	return server.Start(ctx)
}

func main() {
	log.SetLevel(log.DebugLevel)

	err := godotenv.Load()
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	err = run(ctx, log.Default())
	if err != nil {
		log.Fatal(err)
	}
}
