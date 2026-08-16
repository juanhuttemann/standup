// Package obsidian publishes rendered reports into managed Markdown blocks.
package obsidian

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	startMarker = "<!-- standup:start -->"
	endMarker   = "<!-- standup:end -->"
)

// Publish writes report to the managed block in notePath below vault.
func Publish(vault, notePath, report string) (target string, err error) {
	if strings.Contains(report, startMarker) || strings.Contains(report, endMarker) {
		return "", errors.New("report contains reserved standup marker")
	}
	target, err = resolveTarget(vault, notePath)
	if err != nil {
		return "", err
	}

	contents, mode, err := readNote(target)
	if err != nil {
		return "", err
	}
	updated, err := update(contents, report)
	if err != nil {
		return "", err
	}
	if err := atomicWrite(target, []byte(updated), mode); err != nil {
		return "", err
	}
	return target, nil
}

func resolveTarget(vault, notePath string) (string, error) {
	clean, err := cleanNotePath(notePath)
	if err != nil {
		return "", err
	}
	root, err := resolveVault(vault)
	if err != nil {
		return "", err
	}
	parent, err := prepareParent(root, clean)
	if err != nil {
		return "", err
	}
	target := filepath.Join(parent, filepath.Base(clean))
	if err := rejectTargetSymlink(target, notePath); err != nil {
		return "", err
	}
	return target, nil
}

func cleanNotePath(notePath string) (string, error) {
	if notePath == "" || filepath.IsAbs(notePath) {
		return "", fmt.Errorf("note path must be relative to the vault")
	}
	clean := filepath.Clean(notePath)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("note path escapes the vault: %q", notePath)
	}
	return clean, nil
}

func resolveVault(vault string) (string, error) {
	root, err := filepath.Abs(vault)
	if err != nil {
		return "", fmt.Errorf("resolve vault: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve vault symlinks: %w", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("inspect vault: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("vault is not a directory: %q", vault)
	}
	return root, nil
}

func prepareParent(root, clean string) (string, error) {
	parent := filepath.Dir(filepath.Join(root, clean))
	if err := rejectExistingSymlinks(root, parent); err != nil {
		return "", err
	}
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return "", fmt.Errorf("create note directory: %w", err)
	}
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", fmt.Errorf("resolve note directory: %w", err)
	}
	if resolvedParent != parent {
		return "", fmt.Errorf("note directory contains a symlink")
	}
	return parent, nil
}

func rejectTargetSymlink(target, notePath string) error {
	if info, statErr := os.Lstat(target); statErr == nil && info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("note path is a symlink: %q", notePath)
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect note: %w", statErr)
	}
	return nil
}

func rejectExistingSymlinks(root, parent string) error {
	rel, err := filepath.Rel(root, parent)
	if err != nil {
		return fmt.Errorf("resolve note path: %w", err)
	}
	current := root
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		if part == "." {
			continue
		}
		current = filepath.Join(current, part)
		info, statErr := os.Lstat(current)
		if errors.Is(statErr, os.ErrNotExist) {
			return nil
		}
		if statErr != nil {
			return fmt.Errorf("inspect note directory: %w", statErr)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("note directory contains a symlink")
		}
	}
	return nil
}

func readNote(path string) (string, os.FileMode, error) {
	contents, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", 0o644, nil
	}
	if err != nil {
		return "", 0, fmt.Errorf("read note: %w", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", 0, fmt.Errorf("inspect note: %w", err)
	}
	if !info.Mode().IsRegular() {
		return "", 0, fmt.Errorf("note is not a regular file")
	}
	return string(contents), info.Mode().Perm(), nil
}

func update(contents, report string) (string, error) {
	starts := strings.Count(contents, startMarker)
	ends := strings.Count(contents, endMarker)
	if starts != ends || starts > 1 {
		return "", fmt.Errorf("note has malformed standup markers")
	}
	block := startMarker + "\n" + report
	if !strings.HasSuffix(block, "\n") {
		block += "\n"
	}
	block += endMarker

	if starts == 0 {
		separator := ""
		if contents != "" && !strings.HasSuffix(contents, "\n\n") {
			separator = "\n\n"
			if strings.HasSuffix(contents, "\n") {
				separator = "\n"
			}
		}
		return contents + separator + block + "\n", nil
	}
	start := strings.Index(contents, startMarker)
	end := strings.Index(contents, endMarker)
	if end < start {
		return "", fmt.Errorf("note has malformed standup markers")
	}
	return contents[:start] + block + contents[end+len(endMarker):], nil
}

func atomicWrite(path string, contents []byte, mode os.FileMode) (err error) {
	temp, err := os.CreateTemp(filepath.Dir(path), ".standup-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary note: %w", err)
	}
	tempPath := temp.Name()
	defer func() {
		if removeErr := os.Remove(tempPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			err = errors.Join(err, fmt.Errorf("remove temporary note: %w", removeErr))
		}
	}()
	if err := temp.Chmod(mode); err != nil {
		return errors.Join(fmt.Errorf("set note permissions: %w", err), temp.Close())
	}
	if _, err := temp.Write(contents); err != nil {
		return errors.Join(fmt.Errorf("write temporary note: %w", err), temp.Close())
	}
	if err := temp.Sync(); err != nil {
		return errors.Join(fmt.Errorf("sync temporary note: %w", err), temp.Close())
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close temporary note: %w", err)
	}
	if err := replaceFile(tempPath, path); err != nil {
		return fmt.Errorf("replace note: %w", err)
	}
	return nil
}
