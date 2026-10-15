package linux

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"

	"github.com/vilanobeachflorida/racksail/internal/system"
)

func (p *packages) InstallArchive(ctx context.Context, a system.Archive) error {
	if err := a.Validate(); err != nil {
		return err
	}
	dest := p.h.path(a.Dest)
	if _, err := os.Lstat(dest); err == nil {
		return nil
	}
	parentDir := path.Dir(a.Dest)
	parent := p.h.path(parentDir)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return mapErr("install archive", a.Dest, err)
	}
	download, err := os.CreateTemp(parent, system.TreeTempPrefix+"download-*")
	if err != nil {
		return mapErr("install archive", a.Dest, err)
	}
	defer os.Remove(download.Name())
	defer download.Close()
	sum, err := p.fetch(ctx, a.URL, a.Size, download)
	if err != nil {
		return err
	}
	var signature, checksums []byte
	if s := a.Signature; s != nil {
		if signature, err = p.get(ctx, s.URL, system.MaxSignatureBytes); err != nil {
			return err
		}
		if s.Checksums != "" {
			if checksums, err = p.get(ctx, s.Checksums, system.MaxChecksumsBytes); err != nil {
				return err
			}
		}
	}
	open := func() (io.ReadCloser, error) { return os.Open(download.Name()) }
	if err := a.Verify(sum, open, signature, checksums); err != nil {
		return err
	}
	staging, err := os.MkdirTemp(parent, system.TreeTempPrefix+"unpack-*")
	if err != nil {
		return mapErr("install archive", a.Dest, err)
	}
	defer os.RemoveAll(staging)
	if err := p.unpack(ctx, a, parentDir, filepath.Base(download.Name()), filepath.Base(staging)); err != nil {
		return fmt.Errorf("unpacking %s: %w", a.URL, err)
	}
	if err := os.Chmod(staging, 0o755); err != nil {
		return err
	}
	return mapErr("install archive", a.Dest, os.Rename(staging, dest))
}

// unpack unpacks the downloaded archive into the staging directory, both
// named inside parent. xz has no decoder in the standard library, so tar
// handles those archives; every other format goes through a Tree.
func (p *packages) unpack(ctx context.Context, a system.Archive, parent, archive, staging string) error {
	if a.Format == "tar.xz" {
		_, err := p.h.exec(ctx, progTar, "--extract", "--xz", "--file", p.h.path(path.Join(parent, archive)),
			"--directory", p.h.path(path.Join(parent, staging)),
			"--strip-components="+strconv.Itoa(a.StripComponents), "--no-same-owner", "--no-same-permissions")
		return err
	}
	t, err := (&files{p.h}).OpenTree(ctx, parent)
	if err != nil {
		return err
	}
	defer t.Close()
	_, err = system.Unpack(ctx, t, system.UnpackSpec{
		Archive: archive, Dest: staging, Format: system.ArchiveFormat(a.Format), StripComponents: a.StripComponents,
	})
	return err
}

// fetch downloads url into w, refusing anything bigger than size, and
// returns the SHA-256 of what it got.
func (p *packages) fetch(ctx context.Context, url string, size int64, w io.WriteSeeker) (string, error) {
	resp, err := p.request(ctx, url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	sum := sha256.New()
	n, err := io.Copy(io.MultiWriter(w, sum), io.LimitReader(resp.Body, size+1))
	if err != nil {
		return "", fmt.Errorf("downloading %s: %w", url, err)
	}
	if n > size {
		return "", fmt.Errorf("%s is larger than the expected %d bytes: %w", url, size, system.ErrTooLarge)
	}
	if _, err := w.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// get downloads a small file, such as a signature, into memory.
func (p *packages) get(ctx context.Context, url string, max int64) ([]byte, error) {
	resp, err := p.request(ctx, url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", url, err)
	}
	if int64(len(data)) > max {
		return nil, fmt.Errorf("%s is larger than %d bytes: %w", url, max, system.ErrTooLarge)
	}
	return data, nil
}

func (p *packages) request(ctx context.Context, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := p.h.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading %s: %w", url, err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("downloading %s: %s", url, resp.Status)
	}
	return resp, nil
}
