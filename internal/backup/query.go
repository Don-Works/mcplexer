package backup

import (
	"archive/tar"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// List returns every backup in the backup dir, newest first.
func (s *Service) List() ([]Manifest, error) {
	entries, err := os.ReadDir(s.backupDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []Manifest{}, nil
		}
		return nil, err
	}
	out := make([]Manifest, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tar.gz") {
			continue
		}
		mf, err := s.readManifest(filepath.Join(s.backupDir, e.Name()))
		if err != nil {
			continue // skip unreadable; don't fail the whole list
		}
		out = append(out, mf)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.After(out[j].CreatedAt) })
	return out, nil
}

// Get returns the manifest for one backup.
func (s *Service) Get(id string) (Manifest, error) {
	if !validID(id) {
		return Manifest{}, ErrNotFound
	}
	return s.readManifest(s.tarPath(id))
}

// Path returns the absolute filesystem path of a backup tarball, useful
// for streaming downloads. Returns ErrNotFound if the backup is missing.
func (s *Service) Path(id string) (string, error) {
	if !validID(id) {
		return "", ErrNotFound
	}
	p := s.tarPath(id)
	if _, err := os.Stat(p); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", ErrNotFound
		}
		return "", err
	}
	return p, nil
}

// Delete removes a backup tarball.
func (s *Service) Delete(id string) error {
	if !validID(id) {
		return ErrNotFound
	}
	p := s.tarPath(id)
	if err := os.Remove(p); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

func (s *Service) tarPath(id string) string { return filepath.Join(s.backupDir, id+".tar.gz") }

func (s *Service) readManifest(tarPath string) (Manifest, error) {
	f, err := os.Open(tarPath)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Manifest{}, ErrNotFound
		}
		return Manifest{}, err
	}
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return Manifest{}, err
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return Manifest{}, err
		}
		if hdr.Name == "manifest.json" {
			data, err := io.ReadAll(tr)
			if err != nil {
				return Manifest{}, err
			}
			var mf Manifest
			if err := json.Unmarshal(data, &mf); err != nil {
				return Manifest{}, err
			}
			return mf, nil
		}
	}
	return Manifest{}, fmt.Errorf("manifest.json missing in %s", filepath.Base(tarPath))
}
