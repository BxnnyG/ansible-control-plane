package worker

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// maxBundleBytes bounds the unpacked size (the run directory lives on tmpfs).
const maxBundleBytes = 200 << 20

// extract unpacks a tar.gz bundle into dir. All writes go through os.Root, so
// neither ".." paths nor symlinks in the archive can escape dir, and symlinks
// that leave the project tree are rejected (L-05).
func extract(r io.Reader, dir string) error {
	zr, err := gzip.NewReader(r)
	if err != nil {
		return fmt.Errorf("open bundle: %w", err)
	}
	defer func() { _ = zr.Close() }()
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("open run dir: %w", err)
	}
	defer func() { _ = root.Close() }()
	tr := tar.NewReader(zr)
	var total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return verifyProjectLinks(dir)
		}
		if err != nil {
			return fmt.Errorf("read bundle: %w", err)
		}
		name := path.Clean(strings.TrimPrefix(h.Name, "./"))
		if name == "." || path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			if h.Typeflag == tar.TypeDir && name == "." {
				continue
			}
			return fmt.Errorf("bundle entry %q has an unsafe path", h.Name)
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := root.MkdirAll(name, 0o700); err != nil {
				return fmt.Errorf("create %s: %w", name, err)
			}
		case tar.TypeReg:
			total += h.Size
			if total > maxBundleBytes {
				return fmt.Errorf("bundle larger than %d MiB", maxBundleBytes>>20)
			}
			if err := root.MkdirAll(path.Dir(name), 0o700); err != nil {
				return fmt.Errorf("create %s: %w", path.Dir(name), err)
			}
			mode := os.FileMode(0o600)
			if h.Mode&0o111 != 0 {
				mode = 0o700
			}
			f, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, mode)
			if err != nil {
				return fmt.Errorf("create %s: %w", name, err)
			}
			if _, err := io.Copy(f, io.LimitReader(tr, h.Size)); err != nil {
				_ = f.Close()
				return fmt.Errorf("write %s: %w", name, err)
			}
			if err := f.Close(); err != nil {
				return fmt.Errorf("write %s: %w", name, err)
			}
		case tar.TypeSymlink:
			if err := checkLinkTarget(name, h.Linkname); err != nil {
				return err
			}
			if err := root.MkdirAll(path.Dir(name), 0o700); err != nil {
				return fmt.Errorf("create %s: %w", path.Dir(name), err)
			}
			if err := root.Symlink(h.Linkname, name); err != nil {
				return fmt.Errorf("symlink %s: %w", name, err)
			}
		default:
			// Hard links, devices and other types are not part of project trees.
		}
	}
}

// checkLinkTarget rejects symlinks that cannot stay inside the project tree
// (L-05, ADR-0020): only links below project/ are allowed, absolute targets
// are refused and the lexical target must not climb above project/.
func checkLinkTarget(name, target string) error {
	if !strings.HasPrefix(name, projectDir+"/") {
		return fmt.Errorf("bundle entry %q: symlinks are only allowed inside the project", name)
	}
	if target == "" || path.IsAbs(target) || strings.ContainsRune(target, 0) {
		return fmt.Errorf("symlink %q points outside the project", name)
	}
	resolved := path.Join(path.Dir(name), target)
	if resolved != projectDir && !strings.HasPrefix(resolved, projectDir+"/") {
		return fmt.Errorf("symlink %q points outside the project", name)
	}
	return nil
}

// verifyProjectLinks resolves every symlink of the extracted project for real
// (chains and links through links included) and fails when one resolves
// outside the project root. Dangling links are accepted: the lexical check
// of checkLinkTarget already ran and nothing can create their target before
// the job starts.
func verifyProjectLinks(dir string) error {
	base, err := filepath.EvalSymlinks(filepath.Join(dir, projectDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("resolve project: %w", err)
	}
	return filepath.WalkDir(filepath.Join(dir, projectDir), func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.Type()&fs.ModeSymlink == 0 {
			return nil
		}
		real, err := filepath.EvalSymlinks(p)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("resolve symlink %s: %w", p, err)
		}
		if real != base && !strings.HasPrefix(real, base+string(filepath.Separator)) {
			rel, _ := filepath.Rel(dir, p)
			return fmt.Errorf("symlink %q resolves outside the project", rel)
		}
		return nil
	})
}
