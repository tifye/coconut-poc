package cli

import (
	"fmt"
	"os"
	"path"
	"path/filepath"
	"time"
)

func OpenNewLogFile() (string, *os.File, error) {
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
