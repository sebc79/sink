package main

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/bzip2"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/ulikunitz/xz"
)

var (
	ErrNotArchive   = errors.New("not a zip or tar archive")
	ErrBadFormat    = errors.New("unsupported or mismatched archive format")
	ErrArchiveSlip  = errors.New("archive entry escapes destination")
	ErrTooLarge     = errors.New("extracted content exceeds size limit")
	ErrTooManyFiles = errors.New("archive contains too many files")
)

type limiter struct {
	maxBytes int64
	maxFiles int
	bytes    int64
	files    int
}

func (l *limiter) add(n int64) error {
	if n < 0 {
		n = 0
	}
	l.files++
	l.bytes += n
	if l.maxFiles > 0 && l.files > l.maxFiles {
		return ErrTooManyFiles
	}
	if l.maxBytes > 0 && l.bytes > l.maxBytes {
		return ErrTooLarge
	}
	return nil
}

func canonicalFormat(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "tgz":
		return "tar.gz"
	case "tbz", "tbz2":
		return "tar.bz2"
	case "txz":
		return "tar.xz"
	default:
		return strings.ToLower(strings.TrimSpace(s))
	}
}

func parseUnpack(s string) (format string, unpack bool, err error) {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "", "0", "false", "no", "none":
		return "", false, nil
	case "1", "true", "yes", "auto":
		return "auto", true, nil
	case "zip", "tar", "tar.gz", "tgz", "tar.bz2", "tbz2", "tbz", "tar.xz", "txz":
		return canonicalFormat(s), true, nil
	default:
		return "", false, fmt.Errorf("invalid unpack value %q", s)
	}
}

func sniffBytes(b []byte) (string, error) {
	if len(b) >= 4 && b[0] == 'P' && b[1] == 'K' && (b[2] == 0x03 || b[2] == 0x05 || b[2] == 0x07) {
		return "zip", nil
	}
	if len(b) >= 2 && b[0] == 0x1f && b[1] == 0x8b {
		return "tar.gz", nil
	}
	if len(b) >= 3 && b[0] == 'B' && b[1] == 'Z' && b[2] == 'h' {
		return "tar.bz2", nil
	}
	if len(b) >= 6 && bytes.Equal(b[:6], []byte{0xFD, 0x37, 0x7A, 0x58, 0x5A, 0x00}) {
		return "tar.xz", nil
	}
	if len(b) >= 262 && string(b[257:262]) == "ustar" {
		return "tar", nil
	}
	return "", ErrNotArchive
}

func sniffFile(f *os.File) (string, error) {
	var buf [512]byte
	n, err := io.ReadFull(f, buf[:])
	if err != nil && err != io.EOF && err != io.ErrUnexpectedEOF {
		return "", err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	return sniffBytes(buf[:n])
}

func archiveMember(name string) (string, error) {
	name = strings.ReplaceAll(name, `\`, "/")
	name = strings.TrimPrefix(name, "./")
	if name == "" || name == "." || name == "/" {
		return "", errSkipMember
	}
	if strings.HasPrefix(name, "/") || strings.Contains(name, ":") {
		return "", ErrArchiveSlip
	}
	isDir := strings.HasSuffix(name, "/")
	cleaned, err := cleanRel(name)
	if err != nil {
		if errors.Is(err, ErrPathEscape) {
			return "", ErrArchiveSlip
		}
		return "", ErrArchiveSlip
	}
	if cleaned == "" {
		return "", errSkipMember
	}
	if isDir {
		return cleaned + "/", nil
	}
	return cleaned, nil
}

var errSkipMember = errors.New("skip archive member")

func (s *Server) unpackFile(src *os.File, destAbs, format string) (files int, bytes int64, used string, err error) {
	if format == "" || format == "auto" {
		used, err = sniffFile(src)
		if err != nil {
			return 0, 0, "", err
		}
	} else {
		used = canonicalFormat(format)
		sniffed, sniffErr := sniffFile(src)
		if sniffErr == nil && !formatCompatible(used, sniffed) {
			return 0, 0, "", fmt.Errorf("%w: declared %s, detected %s", ErrBadFormat, used, sniffed)
		}
	}
	if _, err := src.Seek(0, io.SeekStart); err != nil {
		return 0, 0, "", err
	}
	lim := &limiter{maxBytes: s.maxExtract, maxFiles: s.maxFiles}
	switch used {
	case "zip":
		err = unpackZip(src, destAbs, lim)
	case "tar":
		err = unpackTar(src, destAbs, lim)
	case "tar.gz":
		var gz *gzip.Reader
		gz, err = gzip.NewReader(src)
		if err != nil {
			return 0, 0, used, err
		}
		defer gz.Close()
		err = unpackTar(gz, destAbs, lim)
	case "tar.bz2":
		err = unpackTar(bzip2.NewReader(src), destAbs, lim)
	case "tar.xz":
		var xr *xz.Reader
		xr, err = xz.NewReader(src)
		if err != nil {
			return 0, 0, used, err
		}
		err = unpackTar(xr, destAbs, lim)
	default:
		err = fmt.Errorf("%w: %s", ErrBadFormat, used)
	}
	return lim.files, lim.bytes, used, err
}

func formatCompatible(declared, sniffed string) bool {
	if declared == sniffed {
		return true
	}
	// gzip/bzip2/xz streams are sniffed as compressed tar; a declared
	// uncompressed tar that is actually compressed is a mismatch.
	return false
}

func unpackZip(src *os.File, destAbs string, lim *limiter) error {
	st, err := src.Stat()
	if err != nil {
		return err
	}
	zr, err := zip.NewReader(src, st.Size())
	if err != nil {
		return fmt.Errorf("%w: %v", ErrNotArchive, err)
	}
	for _, f := range zr.File {
		if err := extractZipFile(f, destAbs, lim); err != nil {
			return err
		}
	}
	return nil
}

func extractZipFile(f *zip.File, destAbs string, lim *limiter) error {
	name, err := archiveMember(f.Name)
	if err != nil {
		if errors.Is(err, errSkipMember) {
			return nil
		}
		return err
	}
	target, err := destJoin(destAbs, name)
	if err != nil {
		return err
	}
	mode := f.Mode()
	if mode&os.ModeSymlink != 0 || !mode.IsRegular() && !mode.IsDir() && !strings.HasSuffix(name, "/") {
		if strings.HasSuffix(name, "/") || mode.IsDir() {
			return os.MkdirAll(target, 0755)
		}
		return nil
	}
	if strings.HasSuffix(name, "/") || mode.IsDir() {
		return os.MkdirAll(target, 0755)
	}
	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()
	return writeExtractedFile(target, rc, int64(f.UncompressedSize64), lim)
}

func unpackTar(r io.Reader, destAbs string, lim *limiter) error {
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeXHeader, tar.TypeXGlobalHeader, tar.TypeGNULongName, tar.TypeGNULongLink:
			continue
		}
		name, err := archiveMember(hdr.Name)
		if err != nil {
			if errors.Is(err, errSkipMember) {
				continue
			}
			return err
		}
		target, err := destJoin(destAbs, name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0755); err != nil {
				return err
			}
		case tar.TypeReg, tar.TypeRegA:
			if err := writeExtractedFile(target, tr, hdr.Size, lim); err != nil {
				return err
			}
		default:
			// skip symlinks, links, devices, fifos
			continue
		}
	}
}

func destJoin(destAbs, name string) (string, error) {
	trimmed := strings.TrimSuffix(name, "/")
	abs := filepath.Join(destAbs, filepath.FromSlash(trimmed))
	rel, err := filepath.Rel(destAbs, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", ErrArchiveSlip
	}
	return abs, nil
}

func writeExtractedFile(target string, r io.Reader, declared int64, lim *limiter) error {
	if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
		return err
	}
	if declared > 0 && lim.maxBytes > 0 && lim.bytes+declared > lim.maxBytes {
		return ErrTooLarge
	}
	f, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	reader := io.Reader(r)
	if lim.maxBytes > 0 {
		remain := lim.maxBytes - lim.bytes
		if remain < 0 {
			return ErrTooLarge
		}
		reader = io.LimitReader(r, remain+1)
	}
	n, err := io.Copy(f, reader)
	if err != nil {
		return err
	}
	if lim.maxBytes > 0 && lim.bytes+n > lim.maxBytes {
		_ = os.Remove(target)
		return ErrTooLarge
	}
	return lim.add(n)
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

	return filepath.WalkDir(srcAbs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		st, err := d.Info()
		if err != nil {
			return err
		}
		if st.Mode()&os.ModeSymlink != 0 {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
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
