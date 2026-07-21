package gateway

import (
	"fmt"
	"os"
	"path/filepath"
)

type renderedSkillWriter func(string, []byte, os.FileMode) error

// installSkillBundle builds the complete installation beside dest before it
// changes any existing installation. The final same-filesystem rename keeps a
// partially extracted bundle or failed rendered-body write out of dest.
func installSkillBundle(raw []byte, renderedBody, dest string, overwrite bool) ([]string, error) {
	return installSkillBundleWithWriter(raw, renderedBody, dest, overwrite, os.WriteFile)
}

func installSkillBundleWithWriter(
	raw []byte,
	renderedBody, dest string,
	overwrite bool,
	writeRendered renderedSkillWriter,
) ([]string, error) {
	if writeRendered == nil {
		return nil, fmt.Errorf("rendered SKILL.md writer is nil")
	}
	if _, err := os.Lstat(dest); err == nil && !overwrite {
		return nil, fmt.Errorf("%s already exists (pass overwrite=true to replace)", dest)
	} else if err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("stat dest: %w", err)
	}

	parent := filepath.Dir(dest)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, fmt.Errorf("mkdir install parent: %w", err)
	}
	stage, err := os.MkdirTemp(parent, "."+filepath.Base(dest)+".install-*")
	if err != nil {
		return nil, fmt.Errorf("create install stage: %w", err)
	}
	stageLive := true
	defer func() {
		if stageLive {
			_ = os.RemoveAll(stage)
		}
	}()

	prefix, err := commonLeadingDir(raw)
	if err != nil {
		return nil, err
	}
	files, err := writeBundleEntries(raw, stage, prefix)
	if err != nil {
		return nil, err
	}

	// Bundle modes are archival data. In particular, a 0444 SKILL.md must
	// not prevent the deterministic rendered body from replacing the raw one.
	renderedPath := filepath.Join(stage, "SKILL.md")
	if err := os.Remove(renderedPath); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("replace bundled SKILL.md: %w", err)
	}
	if err := writeRendered(renderedPath, []byte(renderedBody), 0o644); err != nil {
		return nil, fmt.Errorf("write rendered SKILL.md: %w", err)
	}
	if err := os.Chmod(stage, 0o755); err != nil {
		return nil, fmt.Errorf("set installed directory mode: %w", err)
	}

	if err := activateStagedSkillInstall(stage, dest, overwrite); err != nil {
		return nil, err
	}
	stageLive = false
	return files, nil
}

// activateStagedSkillInstall moves a complete staged directory into place. An
// overwrite first parks the previous destination at a sibling path so a failed
// activation can restore it rather than leaving a partial or missing install.
func activateStagedSkillInstall(stage, dest string, overwrite bool) error {
	_, err := os.Lstat(dest)
	switch {
	case os.IsNotExist(err):
		if err := os.Rename(stage, dest); err != nil {
			return fmt.Errorf("activate staged install: %w", err)
		}
		return nil
	case err != nil:
		return fmt.Errorf("stat dest before activation: %w", err)
	case !overwrite:
		return fmt.Errorf("%s already exists (pass overwrite=true to replace)", dest)
	}

	backup, err := unusedInstallBackupPath(filepath.Dir(dest), filepath.Base(dest))
	if err != nil {
		return err
	}
	if err := os.Rename(dest, backup); err != nil {
		return fmt.Errorf("park existing dest: %w", err)
	}
	if err := os.Rename(stage, dest); err != nil {
		if restoreErr := os.Rename(backup, dest); restoreErr != nil {
			return fmt.Errorf("activate staged install: %v; restore previous dest from %s: %w", err, backup, restoreErr)
		}
		return fmt.Errorf("activate staged install (previous dest restored): %w", err)
	}
	if err := os.RemoveAll(backup); err != nil {
		return fmt.Errorf("installed skill but could not remove previous dest at %s: %w", backup, err)
	}
	return nil
}

func unusedInstallBackupPath(parent, base string) (string, error) {
	placeholder, err := os.MkdirTemp(parent, "."+base+".backup-*")
	if err != nil {
		return "", fmt.Errorf("reserve install backup: %w", err)
	}
	if err := os.Remove(placeholder); err != nil {
		return "", fmt.Errorf("prepare install backup: %w", err)
	}
	return placeholder, nil
}
