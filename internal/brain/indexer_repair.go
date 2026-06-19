package brain

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// checkSubdir handles a directory entry found inside a flat record dir.
// Expected subdirs such as facts/ under memory/, and dot-dirs such as
// .history/ or .git/, are skipped silently. Anything else is logged and
// repaired by moving nested markdown records back into the flat dir.
func (ix *Indexer) checkSubdir(ctx context.Context, dir, name string, allowed []string) {
	if len(name) > 1 && name[0] == '.' {
		return
	}
	for _, a := range allowed {
		if name == a {
			return
		}
	}
	nested := filepath.Join(dir, name)
	ix.log.Warn("brain: unexpected subdirectory in flat record dir; repairing nested files",
		"dir", dir, "subdir", name)
	ix.repairNestedDir(ctx, nested, dir)
}

// repairNestedDir relocates every .md file under nestedDir into flatDir
// under a sanitized filename, indexes each repaired file, then prunes the
// emptied directories deepest first. Best-effort throughout.
func (ix *Indexer) repairNestedDir(ctx context.Context, nestedDir, flatDir string) {
	var files, dirs []string
	_ = filepath.WalkDir(nestedDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		switch {
		case d.IsDir():
			dirs = append(dirs, path)
		case isMarkdown(d.Name()):
			files = append(files, path)
		}
		return nil
	})
	for _, f := range files {
		ix.repairMisplacedFile(ctx, f, flatDir)
	}
	for i := len(dirs) - 1; i >= 0; i-- {
		_ = os.Remove(dirs[i])
	}
}

// repairMisplacedFile renames one nested .md file to a sanitized stem in
// flatDir and re-indexes it. The destination is marked in the self-write
// set after the explicit index pass, so the watcher's fsnotify echo of
// the rename is suppressed without the repair's own IndexFile being
// skipped.
func (ix *Indexer) repairMisplacedFile(ctx context.Context, path, flatDir string) {
	data, err := os.ReadFile(path)
	if err != nil {
		ix.log.Warn("brain: repair read", "path", path, "error", err)
		return
	}
	stem := repairStem(data, flatDir, path)
	if stem == "" {
		ix.log.Warn("brain: repair cannot derive a filename", "path", path)
		return
	}
	target := uniqueRepairTarget(flatDir, stem)
	if err := os.Rename(path, target); err != nil {
		ix.log.Warn("brain: repair rename", "from", path, "to", target, "error", err)
		return
	}
	ix.log.Warn("brain: repaired mis-pathed record file", "from", path, "to", target)
	if err := ix.IndexFile(ctx, target); err != nil {
		ix.log.Warn("brain: index repaired file", "path", target, "error", err)
	}
	ix.selfWrites.Mark(target, hashBytes(data))
}

// repairStem derives the canonical filename stem for a repaired file from
// frontmatter, keyed by the kind its destination dir implies. A parse
// failure falls back to the slug of the file's own relative stem so the
// file still lands flat and can surface as a validation error.
func repairStem(data []byte, flatDir, path string) string {
	switch kindForPath(filepath.Join(flatDir, baseName(path))) {
	case EntityKindMemory:
		if fm, _, err := ParseMemory(data); err == nil {
			return recordStem(fm.Name, fm.ID)
		}
	case EntityKindPerson:
		if fm, _, err := ParsePerson(data); err == nil {
			return recordStem(fm.Name, fm.ID)
		}
	default:
		if fm, _, err := ParseTask(data); err == nil && fm.ID != "" {
			if slug := slugify(fm.Title); slug != "" {
				return fm.ID + "-" + slug
			}
			return fm.ID
		}
	}
	return slugify(strings.TrimSuffix(baseName(path), filepath.Ext(path)))
}

// uniqueRepairTarget returns a non-existing <dir>/<stem>.md path,
// suffixing -2, -3, ... on collision.
func uniqueRepairTarget(dir, stem string) string {
	target := filepath.Join(dir, stem+".md")
	for i := 2; i < 100; i++ {
		if _, err := os.Stat(target); errors.Is(err, fs.ErrNotExist) {
			return target
		}
		target = filepath.Join(dir, fmt.Sprintf("%s-%d.md", stem, i))
	}
	return target
}
