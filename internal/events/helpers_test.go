package events

import "os"

func writeFile(path, s string) error { return os.WriteFile(path, []byte(s), 0o600) }

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
