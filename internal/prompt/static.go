package prompt

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"unicode/utf8"
)

var (
	ErrBinaryFile  = errors.New("binary file not supported in context/static")
	ErrInvalidUTF8 = errors.New("invalid UTF-8 in context/static")
)

// StaticFile represents a single loaded static context file.
type StaticFile struct {
	RelPath string // relative path from context/static/
	Content string // UTF-8 text content
}

// LoadStaticContext loads all files from <taskDir>/context/static/ recursively
// in lexicographic relative-path order.
// Returns empty slice (not error) if context/static/ is missing or empty.
// Returns ErrBinaryFile if a binary file is found, ErrInvalidUTF8 for invalid UTF-8.
func LoadStaticContext(taskDir string) ([]StaticFile, error) {
	staticDir := filepath.Join(taskDir, "context", "static")

	info, err := os.Stat(staticDir)
	if err != nil {
		if os.IsNotExist(err) {
			return []StaticFile{}, nil
		}
		return nil, err
	}
	if !info.IsDir() {
		return []StaticFile{}, nil
	}

	files := []StaticFile{}
	err = filepath.WalkDir(staticDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}

		relPath, err := filepath.Rel(staticDir, path)
		if err != nil {
			return err
		}
		relPath = filepath.ToSlash(relPath)

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}

		if isBinary(data) {
			return fmt.Errorf("%s: %w", relPath, ErrBinaryFile)
		}

		if !utf8.Valid(data) {
			return fmt.Errorf("%s: %w", relPath, ErrInvalidUTF8)
		}

		files = append(files, StaticFile{
			RelPath: relPath,
			Content: string(data),
		})

		return nil
	})
	if err != nil {
		return nil, err
	}

	return files, nil
}

// isBinary checks if content contains null bytes in the first 8192 bytes.
func isBinary(data []byte) bool {
	limit := 8192
	if len(data) < limit {
		limit = len(data)
	}
	for i := 0; i < limit; i++ {
		if data[i] == 0 {
			return true
		}
	}
	return false
}
