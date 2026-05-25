package configops

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"
)

type BackupInfo struct {
	Name    string
	Path    string
	Size    int64
	ModTime time.Time
}

type RestoreResult struct {
	BackupPath     string
	RestoredTo     string
	PreRestorePath string
}

func ListConfigBackups(cfgPath string) ([]BackupInfo, error) {
	if cfgPath == "" {
		return nil, nil
	}
	paths, err := filepath.Glob(cfgPath + ".bak*")
	if err != nil {
		return nil, fmt.Errorf("glob backups for %s: %w", cfgPath, err)
	}
	backups := make([]BackupInfo, 0, len(paths))
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || info.IsDir() {
			continue
		}
		backups = append(backups, BackupInfo{
			Name:    filepath.Base(path),
			Path:    path,
			Size:    info.Size(),
			ModTime: info.ModTime(),
		})
	}
	sort.Slice(backups, func(i, j int) bool {
		return backups[i].ModTime.After(backups[j].ModTime)
	})
	return backups, nil
}

func RestoreConfigBackup(cfgPath, backupPath string) (*RestoreResult, error) {
	if cfgPath == "" {
		return nil, fmt.Errorf("config path is required")
	}
	if backupPath == "" {
		backupPath = cfgPath + ".bak"
	}
	if _, err := os.Stat(backupPath); err != nil {
		return nil, fmt.Errorf("backup %s not found: %w", backupPath, err)
	}

	preRestorePath := fmt.Sprintf("%s.pre-restore-%s.bak", cfgPath, time.Now().UTC().Format("20060102T150405Z"))
	if _, err := os.Stat(cfgPath); err == nil {
		if err := copyFile(cfgPath, preRestorePath); err != nil {
			return nil, fmt.Errorf("snapshot current config: %w", err)
		}
	}
	if err := copyFile(backupPath, cfgPath); err != nil {
		return nil, fmt.Errorf("restore %s -> %s: %w", backupPath, cfgPath, err)
	}
	return &RestoreResult{
		BackupPath:     backupPath,
		RestoredTo:     cfgPath,
		PreRestorePath: preRestorePath,
	}, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src) //nolint:gosec
	if err != nil {
		return err
	}
	defer in.Close()

	if err := os.MkdirAll(filepath.Dir(dst), 0o750); err != nil {
		return err
	}
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
