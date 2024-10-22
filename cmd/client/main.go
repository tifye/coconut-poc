package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/charmbracelet/log"
	"github.com/joho/godotenv"
	"github.com/tifye/tunnel/pkg"
)

func run(ctx context.Context, logger *log.Logger, args []string) error {
	target := "http://localhost:6280"
	if len(args) > 0 {
		target = args[0]
	}

	fmt.Println(target)
	client, err := pkg.NewClient(target, logger)
	if err != nil {
		return err
	}

	return client.Start(ctx, "127.0.0.1:9000")
}

func main() {
	log.SetLevel(log.DebugLevel)

	err := godotenv.Load()
	if err != nil {
		log.Fatal(err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	err = run(ctx, log.Default(), os.Args[1:])
	if err != nil {
		log.Fatal(err)
	}
}
