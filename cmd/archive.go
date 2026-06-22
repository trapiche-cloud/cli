package cmd

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

var excludedDirs = map[string]struct{}{
	"node_modules": {},
	".git":         {},
	".next":        {},
	"dist":         {},
	"out":          {},
	"build":        {},
	"coverage":     {},
}

func shouldExclude(relPath string, d os.DirEntry) bool {
	name := d.Name()

	if d.IsDir() {
		_, excluded := excludedDirs[name]
		return excluded
	}

	if name == ".DS_Store" {
		return true
	}
	if strings.HasSuffix(name, ".log") {
		return true
	}
	if name == ".env" || strings.HasPrefix(name, ".env.") {
		return true
	}

	_ = relPath
	return false
}

func createTarGz(root string) (string, int, error) {
	tempFile, err := os.CreateTemp("", "trapiche-*.tar.gz")
	if err != nil {
		return "", 0, fmt.Errorf("failed to create temp archive: %w", err)
	}

	gzipWriter := gzip.NewWriter(tempFile)
	tarWriter := tar.NewWriter(gzipWriter)
	fileCount := 0

	walkErr := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if path == root {
			return nil
		}

		relPath, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}

		if shouldExclude(relPath, d) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		if d.IsDir() {
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return err
		}

		header, err := tar.FileInfoHeader(info, "")
		if err != nil {
			_ = file.Close()
			return err
		}
		header.Name = filepath.ToSlash(relPath)

		if err := tarWriter.WriteHeader(header); err != nil {
			_ = file.Close()
			return err
		}
		if _, err := io.Copy(tarWriter, file); err != nil {
			_ = file.Close()
			return err
		}
		if err := file.Close(); err != nil {
			return err
		}

		fileCount++
		return nil
	})

	closeErr := tarWriter.Close()
	if walkErr == nil && closeErr != nil {
		walkErr = closeErr
	}
	closeErr = gzipWriter.Close()
	if walkErr == nil && closeErr != nil {
		walkErr = closeErr
	}
	closeErr = tempFile.Close()
	if walkErr == nil && closeErr != nil {
		walkErr = closeErr
	}

	if walkErr != nil {
		_ = os.Remove(tempFile.Name())
		return "", 0, fmt.Errorf("failed to create archive: %w", walkErr)
	}

	return tempFile.Name(), fileCount, nil
}

func printNewLogs(allLogs string, printedLogLen *int) {
	if len(allLogs) > *printedLogLen {
		fmt.Print(allLogs[*printedLogLen:])
		*printedLogLen = len(allLogs)
		return
	}

	if len(allLogs) < *printedLogLen {
		fmt.Print(allLogs)
		*printedLogLen = len(allLogs)
	}
}
