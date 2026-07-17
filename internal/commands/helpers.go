package commands

import (
	"encoding/json"
	"os"
)

// jsonMarshalIndent is a thin wrapper around encoding/json used by
// the /config and /dump-* commands. Centralizing it means a future
// change to the rendering (e.g. a custom redactor for API keys)
// has one edit site.
func jsonMarshalIndent(v any) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}

// fileExists reports whether path exists. Any stat error other than
// os.IsNotExist is returned as-is so the caller can surface it.
func fileExists(path string) (bool, error) {
	_, err := os.Stat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// writeFile writes data to path, creating parent directories first
// (mkdir -p semantics). The /init command uses this so a fresh
// working dir without the expected layout still works.
func writeFile(path string, data []byte, mode os.FileMode) error {
	if dir := dirOf(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	return os.WriteFile(path, data, mode)
}

// dirOf is filepath.Dir without the import, since helpers.go would
// otherwise only need filepath for this one call.
func dirOf(path string) string {
	for i := len(path) - 1; i >= 0; i-- {
		if path[i] == '/' || path[i] == '\\' {
			return path[:i]
		}
	}
	return "."
}
