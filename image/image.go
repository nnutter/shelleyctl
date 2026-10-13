// Package image contains the build context distributed with shelleyctl.
package image

import (
	"embed"
	"errors"
	"os"
	"path/filepath"
)

//go:embed Dockerfile entrypoint.sh .dockerignore user-guidance.md
var contextFiles embed.FS

// Materialize writes the build context to a temporary directory.
// The caller must remove the returned directory after the runtime build finishes.
func Materialize() (dir string, err error) {
	dir, err = os.MkdirTemp("", "shelleyctl-image-*")
	if err != nil {
		return "", err
	}
	defer func() {
		if err != nil {
			err = errors.Join(err, os.RemoveAll(dir))
		}
	}()
	entries, err := contextFiles.ReadDir(".")
	if err != nil {
		return dir, err
	}
	for _, entry := range entries {
		name := entry.Name()
		data, readErr := contextFiles.ReadFile(name)
		if readErr != nil {
			return dir, readErr
		}
		if err = os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			return dir, err
		}
	}
	return dir, nil
}
