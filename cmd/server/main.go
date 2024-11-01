package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"time"

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
	logger := log.NewWithOptions(os.Stdout, log.Options{
		Level:           log.DebugLevel,
		TimeFormat:      "15:04:05",
		ReportTimestamp: true,
	})

	path, file, err := openNewLogFile()
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

func openNewLogFile() (string, *os.File, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", nil, fmt.Errorf("failed to get path to executable: %w", err)
	}

	dir := filepath.Dir(exePath)
	logsDir := path.Join(dir, "logs")
	err = os.MkdirAll(logsDir, 0700)
	if err != nil {
		return "", nil, fmt.Errorf("failed to create logs dir: %w", err)
	}

	logName := fmt.Sprintf("%d.txt", time.Now().UTC().Unix())
	fullpath := path.Join(logsDir, logName)
	file, err := os.Create(fullpath)
	if err != nil {
		return "", nil, fmt.Errorf("failed to create log file: %s", err)
	}

	return fullpath, file, nil
}
