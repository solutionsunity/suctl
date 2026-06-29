// SPDX-License-Identifier: Apache-2.0

package installer

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// resolveLatestTag returns the latest release tag for repo by following the
// releases/latest 302 to its final URL and reading the trailing path segment —
// the same redirect trick install.sh uses (no API call, no rate limit, no jq).
func resolveLatestTag(repo string) (string, error) {
	url := "https://github.com/" + repo + "/releases/latest"
	resp, err := http.Head(url) //nolint:gosec // repo is operator-controlled
	if err != nil {
		return "", err
	}
	defer resp.Body.Close() //nolint:errcheck
	final := resp.Request.URL.String()
	tag := final[strings.LastIndex(final, "/")+1:]
	if !strings.HasPrefix(tag, "v") {
		return "", fmt.Errorf("could not determine a valid version (got %q)", tag)
	}
	return tag, nil
}

// download fetches url into dst, failing on any non-200 status.
func download(url, dst string) error {
	resp, err := http.Get(url) //nolint:gosec // url derived from operator repo+tag
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close() //nolint:errcheck
	_, err = io.Copy(out, resp.Body)
	return err
}

// verifySHA256 checks that file matches the digest recorded in shaFile, whose
// format is the standard "<hex>  <name>" produced by sha256sum.
func verifySHA256(file, shaFile string) error {
	raw, err := os.ReadFile(shaFile)
	if err != nil {
		return err
	}
	fields := strings.Fields(string(raw))
	if len(fields) == 0 {
		return fmt.Errorf("malformed checksum file %s", shaFile)
	}
	want := strings.ToLower(fields[0])

	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return err
	}
	got := hex.EncodeToString(h.Sum(nil))
	if got != want {
		return fmt.Errorf("checksum mismatch: got %s, want %s", got, want)
	}
	return nil
}

// extractArchive unpacks archive into dest, dispatching on extension: .zip for
// Windows releases, .tar.gz everywhere else.
func extractArchive(archive, dest string) error {
	if strings.HasSuffix(archive, ".zip") {
		return extractZip(archive, dest)
	}
	return extractTarGz(archive, dest)
}

// safeJoin joins dest and name, rejecting entries that would escape dest
// (zip-slip / path traversal).
func safeJoin(dest, name string) (string, error) {
	target := filepath.Join(dest, name)
	if target != dest && !strings.HasPrefix(target, dest+string(os.PathSeparator)) {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	return target, nil
}

func extractTarGz(archive, dest string) error {
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close() //nolint:errcheck
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	defer gz.Close() //nolint:errcheck
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeJoin(dest, hdr.Name)
		if err != nil {
			return err
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, os.FileMode(hdr.Mode)); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		default:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, os.FileMode(hdr.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tr); err != nil { //nolint:gosec // trusted release archive
				out.Close() //nolint:errcheck
				return err
			}
			out.Close() //nolint:errcheck
		}
	}
}

func extractZip(archive, dest string) error {
	zr, err := zip.OpenReader(archive)
	if err != nil {
		return err
	}
	defer zr.Close() //nolint:errcheck
	for _, zf := range zr.File {
		target, err := safeJoin(dest, zf.Name)
		if err != nil {
			return err
		}
		if zf.FileInfo().IsDir() {
			if err := os.MkdirAll(target, zf.Mode()); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		rc, err := zf.Open()
		if err != nil {
			return err
		}
		out, err := os.OpenFile(target, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, zf.Mode())
		if err != nil {
			rc.Close() //nolint:errcheck
			return err
		}
		if _, err := io.Copy(out, rc); err != nil { //nolint:gosec // trusted release archive
			out.Close() //nolint:errcheck
			rc.Close()  //nolint:errcheck
			return err
		}
		out.Close() //nolint:errcheck
		rc.Close()  //nolint:errcheck
	}
	return nil
}
