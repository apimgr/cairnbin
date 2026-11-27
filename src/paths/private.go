package paths

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// EnforcePrivatePath fixes a private file or directory tree to owner-only
// access. Directories are created when createDirectory is true. Symlinks are
// rejected so permission repair cannot escape the configured private tree.
func EnforcePrivatePath(path string, createDirectory bool) error {
	info, err := os.Lstat(path)
	if os.IsNotExist(err) {
		if !createDirectory {
			return nil
		}
		if err := os.MkdirAll(path, 0o700); err != nil {
			return privatePathError(path, 0, err)
		}
		info, err = os.Lstat(path)
	}
	if err != nil {
		return privatePathError(path, 0, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return privatePathError(path, info.Mode().Perm(), fmt.Errorf("symlink is not allowed"))
	}
	if !info.IsDir() {
		if err := os.Chmod(path, 0o600); err != nil {
			return privatePathError(path, info.Mode().Perm(), err)
		}
		if err := enforcePrivateACL(path, false); err != nil {
			return privatePathError(path, info.Mode().Perm(), err)
		}
		return nil
	}
	return filepath.WalkDir(path, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return privatePathError(current, 0, walkErr)
		}
		item, err := os.Lstat(current)
		if err != nil {
			return privatePathError(current, 0, err)
		}
		if item.Mode()&os.ModeSymlink != 0 {
			return privatePathError(current, item.Mode().Perm(), fmt.Errorf("symlink is not allowed"))
		}
		mode := os.FileMode(0o600)
		if entry.IsDir() {
			mode = 0o700
		}
		if err := os.Chmod(current, mode); err != nil {
			return privatePathError(current, item.Mode().Perm(), err)
		}
		if err := enforcePrivateACL(current, entry.IsDir()); err != nil {
			return privatePathError(current, item.Mode().Perm(), err)
		}
		return nil
	})
}

// EnforceUserPrivatePath secures one user-owned credential file. Windows
// custom paths receive a protected ACL for the current user; normal profile
// paths retain the platform's existing private-file behavior.
func EnforceUserPrivatePath(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return privatePathError(path, 0, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return privatePathError(path, info.Mode().Perm(), fmt.Errorf("expected a regular file, not a symlink"))
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return privatePathError(path, info.Mode().Perm(), err)
	}
	if err := enforceUserPrivateACL(path); err != nil {
		return privatePathError(path, info.Mode().Perm(), err)
	}
	return nil
}

func privatePathError(path string, mode os.FileMode, cause error) error {
	return fmt.Errorf("private path %s mode %04o: %w", path, mode.Perm(), cause)
}
