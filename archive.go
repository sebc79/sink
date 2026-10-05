package main

import (
	"archive/tar"
	"archive/zip"
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

var ErrBadFormat = errors.New("unsupported or mismatched archive format")

func canonicalFormat(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "tgz":
		return "tar.gz"
	default:
		return strings.ToLower(strings.TrimSpace(s))
	}
}

func packArchive(w io.Writer, srcAbs, rel, format string) error {
	format = canonicalFormat(format)
	switch format {
	case "", "zip":
		zw := zip.NewWriter(w)
		err := walkPack(srcAbs, rel, func(name string, info fs.FileInfo, open func() (io.ReadCloser, error)) error {
			hdr, err := zip.FileInfoHeader(info)
			if err != nil {
				return err
			}
			hdr.Name = name
			if info.IsDir() {
				hdr.Name = strings.TrimSuffix(hdr.Name, "/") + "/"
				hdr.Method = zip.Store
				_, err = zw.CreateHeader(hdr)
				return err
			}
			hdr.Method = zip.Deflate
			fw, err := zw.CreateHeader(hdr)
			if err != nil {
				return err
			}
			f, err := open()
			if err != nil {
				return err
			}
			defer f.Close()
			_, err = io.Copy(fw, f)
			return err
		})
		if err != nil {
			_ = zw.Close()
			return err
		}
		return zw.Close()
	case "tar":
		tw := tar.NewWriter(w)
		err := writeTar(tw, srcAbs, rel)
		if err != nil {
			_ = tw.Close()
			return err
		}
		return tw.Close()
	case "tar.gz":
		gz := gzip.NewWriter(w)
		tw := tar.NewWriter(gz)
		err := writeTar(tw, srcAbs, rel)
		if err != nil {
			_ = tw.Close()
			_ = gz.Close()
			return err
		}
		if err := tw.Close(); err != nil {
			_ = gz.Close()
			return err
		}
		return gz.Close()
	default:
		return fmt.Errorf("%w: %s", ErrBadFormat, format)
	}
}

func writeTar(tw *tar.Writer, srcAbs, rel string) error {
	return walkPack(srcAbs, rel, func(name string, info fs.FileInfo, open func() (io.ReadCloser, error)) error {
		hdr, err := tar.FileInfoHeader(info, "")
		if err != nil {
			return err
		}
		hdr.Name = name
		hdr.Format = tar.FormatPAX
		if info.IsDir() {
			hdr.Name = strings.TrimSuffix(hdr.Name, "/") + "/"
			hdr.Typeflag = tar.TypeDir
			return tw.WriteHeader(hdr)
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		f, err := open()
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	})
}

type packFn func(name string, info fs.FileInfo, open func() (io.ReadCloser, error)) error

func walkPack(srcAbs, rel string, fn packFn) error {
	info, err := os.Lstat(srcAbs)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to archive symlink")
	}
	if !info.IsDir() {
		name := info.Name()
		return fn(name, info, func() (io.ReadCloser, error) {
			return os.Open(srcAbs)
		})
	}

	prefix := ""
	if rel != "" {
		prefix = path.Base(rel)
	}

	rootDev, rootDevOK := devOf(info)
	return filepath.WalkDir(srcAbs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		st, err := d.Info()
		if err != nil {
			return err
		}
		if st.Mode()&os.ModeSymlink != 0 || ignoredName(d.Name()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() && p != srcAbs && rootDevOK {
			if dev, ok := devOf(st); ok && dev != rootDev {
				return fs.SkipDir
			}
		}
		relPath, err := filepath.Rel(srcAbs, p)
		if err != nil {
			return err
		}
		var name string
		if relPath == "." {
			if prefix == "" {
				return nil
			}
			name = prefix
		} else {
			name = filepath.ToSlash(relPath)
			if prefix != "" {
				name = prefix + "/" + name
			}
		}
		if d.IsDir() {
			return fn(name+"/", st, func() (io.ReadCloser, error) {
				return nil, fmt.Errorf("directory")
			})
		}
		if !st.Mode().IsRegular() {
			return nil
		}
		return fn(name, st, func() (io.ReadCloser, error) {
			return os.Open(p)
		})
	})
}

func archiveFilename(rel, format string) string {
	base := "storage"
	if rel != "" {
		base = path.Base(rel)
	}
	switch canonicalFormat(format) {
	case "tar":
		return base + ".tar"
	case "tar.gz":
		return base + ".tar.gz"
	default:
		return base + ".zip"
	}
}

func mimeForArchive(format string) string {
	switch canonicalFormat(format) {
	case "tar":
		return "application/x-tar"
	case "tar.gz":
		return "application/gzip"
	default:
		return "application/zip"
	}
}
