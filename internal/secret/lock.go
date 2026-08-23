package secret

import (
	"fmt"
	"os"
)

func acquireFileLock(path string) (func() error, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open vault lock: %w", err)
	}
	if err := platformLock(file); err != nil {
		file.Close()
		return nil, fmt.Errorf("lock vault: %w", err)
	}
	return func() error {
		unlockErr := platformUnlock(file)
		closeErr := file.Close()
		if unlockErr != nil {
			return fmt.Errorf("unlock vault: %w", unlockErr)
		}
		return closeErr
	}, nil
}
