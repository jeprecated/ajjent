package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// removeWorkspaceDirectory anchors the selected parent before deleting anything.
// Go 1.24's os.Root has no RemoveAll, so deletion uses the same fd-relative native
// operations as RemoveAll. Neither removal nor permission repair follows links.
func removeWorkspaceDirectory(path string) error {
	path = filepath.Clean(path)
	if path == "." || path == string(filepath.Separator) {
		return fmt.Errorf("refusing Workspace removal at %s", path)
	}
	root, err := os.OpenRoot(filepath.Dir(path))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer root.Close()
	parent, err := root.Open(".")
	if err != nil {
		return err
	}
	defer parent.Close()
	return removeWorkspaceEntry(parent, filepath.Base(path))
}

func openWorkspaceDirectory(parent *os.File, name string) (*os.File, error) {
	fd, err := unix.Openat(int(parent.Fd()), name, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, &os.PathError{Op: "openat", Path: name, Err: err}
	}
	return os.NewFile(uintptr(fd), filepath.Join(parent.Name(), name)), nil
}

func removeWorkspaceEntry(parent *os.File, name string) error {
	dir, err := openWorkspaceDirectory(parent, name)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.ELOOP) {
		// Unlink a file or symlink itself, never its target.
		err = unix.Unlinkat(int(parent.Fd()), name, 0)
		if errors.Is(err, unix.ENOENT) {
			return nil
		}
		if err != nil {
			return &os.PathError{Op: "unlinkat", Path: name, Err: err}
		}
		return nil
	}
	if err != nil {
		return err
	}
	defer dir.Close()
	err = removeWorkspaceContents(dir)
	if errors.Is(err, os.ErrPermission) {
		// Chmod directories only: files may be hard-linked to shared caches.
		if repairErr := repairWorkspaceDirectories(dir); repairErr != nil {
			return fmt.Errorf("%w; repair Workspace directory permissions: %v", err, repairErr)
		}
		err = removeWorkspaceContents(dir)
	}
	if err != nil {
		return err
	}
	if err := unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR); err != nil && !errors.Is(err, unix.ENOENT) {
		return &os.PathError{Op: "unlinkat", Path: name, Err: err}
	}
	return nil
}

func removeWorkspaceContents(dir *os.File) error {
	// A fresh descriptor gives every attempt its own directory enumeration offset.
	scan, err := openWorkspaceDirectory(dir, ".")
	if err != nil {
		return err
	}
	defer scan.Close()
	entries, err := scan.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if err := removeWorkspaceEntry(dir, entry.Name()); err != nil {
			return err
		}
	}
	return nil
}

func repairWorkspaceDirectories(dir *os.File) error {
	info, err := dir.Stat()
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("permission repair target is not a directory: %s", dir.Name())
	}
	if info.Mode().Perm()&0300 != 0300 {
		if err := dir.Chmod(info.Mode() | 0300); err != nil {
			return err
		}
	}
	scan, err := openWorkspaceDirectory(dir, ".")
	if err != nil {
		return err
	}
	defer scan.Close()
	entries, err := scan.ReadDir(-1)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		// Single entry names relative to pinned descriptors prevent symlink races
		// between enumeration, opening, and File.Chmod from redirecting chmod.
		child, err := openWorkspaceDirectory(dir, entry.Name())
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) || errors.Is(err, unix.ELOOP) {
			continue
		}
		if err != nil {
			return err
		}
		err = repairWorkspaceDirectories(child)
		closeErr := child.Close()
		if err != nil {
			return err
		}
		if closeErr != nil {
			return closeErr
		}
	}
	return nil
}
