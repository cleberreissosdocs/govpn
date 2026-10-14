package main

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// StoreHolder lets the running database be swapped out (for restore)
// without restarting the process. All request handlers go through
// Get() instead of holding a *Store directly.
type StoreHolder struct {
	mu         sync.RWMutex
	store      *Store
	subnetCIDR string
}

func NewStoreHolder(store *Store, subnetCIDR string) *StoreHolder {
	return &StoreHolder{store: store, subnetCIDR: subnetCIDR}
}

func (h *StoreHolder) Get() *Store {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.store
}

// Restore closes the current database, replaces the on-disk file with
// srcPath's contents, and reopens it. Everything is serialized behind
// the write lock so no request can observe a half-swapped state.
func (h *StoreHolder) Restore(srcPath string) error {
	h.mu.Lock()
	defer h.mu.Unlock()

	if err := validateSQLiteFile(srcPath); err != nil {
		return fmt.Errorf("arquivo de backup inválido: %w", err)
	}

	dbPath := h.store.DBPath()
	if err := h.store.Close(); err != nil {
		return fmt.Errorf("close current db: %w", err)
	}
	// also drop the WAL/SHM sidecar files from the db being replaced, if any
	os.Remove(dbPath + "-wal")
	os.Remove(dbPath + "-shm")

	if err := copyFile(srcPath, dbPath); err != nil {
		return fmt.Errorf("copy backup over live db: %w", err)
	}
	newStore, err := OpenStore(dbPath, h.subnetCIDR)
	if err != nil {
		return fmt.Errorf("reopen restored db: %w", err)
	}
	h.store = newStore
	return nil
}

func validateSQLiteFile(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	header := make([]byte, 16)
	if _, err := f.Read(header); err != nil {
		return err
	}
	if string(header) != "SQLite format 3\x00" {
		return fmt.Errorf("not a SQLite database file")
	}
	return nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

// --- backup settings persisted in the settings table ---

const (
	settingBackupDir       = "backup_dir"
	settingBackupEnabled   = "backup_enabled"
	settingBackupInterval  = "backup_interval_minutes"
	settingBackupLastAt    = "backup_last_at"
	settingBackupRetention = "backup_retention_count"
)

type BackupSettings struct {
	Dir             string
	Enabled         bool
	IntervalMinutes int
	LastAt          int64
	RetentionCount  int // 0 = keep every backup forever (no automatic deletion)
}

func (h *StoreHolder) GetBackupSettings() BackupSettings {
	s := h.Get()
	dir, _, _ := s.GetSetting(settingBackupDir)
	enabledStr, _, _ := s.GetSetting(settingBackupEnabled)
	intervalStr, ok, _ := s.GetSetting(settingBackupInterval)
	interval := 60
	if ok {
		fmt.Sscanf(intervalStr, "%d", &interval)
	}
	lastStr, _, _ := s.GetSetting(settingBackupLastAt)
	var last int64
	fmt.Sscanf(lastStr, "%d", &last)
	retentionStr, _, _ := s.GetSetting(settingBackupRetention)
	var retention int
	fmt.Sscanf(retentionStr, "%d", &retention)
	return BackupSettings{
		Dir:             dir,
		Enabled:         enabledStr == "1",
		IntervalMinutes: interval,
		LastAt:          last,
		RetentionCount:  retention,
	}
}

func (h *StoreHolder) SetBackupSettings(dir string, enabled bool, intervalMinutes, retentionCount int) error {
	s := h.Get()
	if err := s.SetSetting(settingBackupDir, dir); err != nil {
		return err
	}
	enabledVal := "0"
	if enabled {
		enabledVal = "1"
	}
	if err := s.SetSetting(settingBackupEnabled, enabledVal); err != nil {
		return err
	}
	if err := s.SetSetting(settingBackupInterval, fmt.Sprintf("%d", intervalMinutes)); err != nil {
		return err
	}
	return s.SetSetting(settingBackupRetention, fmt.Sprintf("%d", retentionCount))
}

// RunBackupNow takes an immediate snapshot into the configured backup
// directory (or dir if explicitly given) and returns the file written.
// After writing, it enforces the configured retention count (if any),
// deleting the oldest backups beyond that count.
func (h *StoreHolder) RunBackupNow(dir string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("diretório de backup não configurado")
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", fmt.Errorf("create backup dir: %w", err)
	}
	filename := fmt.Sprintf("govpn-backup-%s.db", time.Now().Format("20060102-150405"))
	dest := filepath.Join(dir, filename)
	if err := h.Get().Backup(dest); err != nil {
		return "", err
	}
	h.Get().SetSetting(settingBackupLastAt, fmt.Sprintf("%d", time.Now().Unix()))

	settings := h.GetBackupSettings()
	if settings.RetentionCount > 0 {
		if removed, err := h.applyRetention(dir, settings.RetentionCount); err != nil {
			log.Printf("backup retention cleanup failed: %v", err)
		} else if len(removed) > 0 {
			log.Printf("backup retention removed %d old backup(s): %v", len(removed), removed)
		}
	}
	return dest, nil
}

// applyRetention keeps only the `keep` newest backups in dir, deleting
// the rest, and returns the names of files it removed.
func (h *StoreHolder) applyRetention(dir string, keep int) ([]string, error) {
	files, err := h.ListBackups(dir)
	if err != nil {
		return nil, err
	}
	var removed []string
	for i, f := range files {
		if i < keep {
			continue // newest `keep` files are kept (ListBackups is newest-first)
		}
		if err := os.Remove(filepath.Join(dir, f.Name)); err != nil {
			return removed, err
		}
		removed = append(removed, f.Name)
	}
	return removed, nil
}

// DeleteBackup removes a single backup file from the configured
// directory. Returns an error if name tries to escape dir.
func (h *StoreHolder) DeleteBackup(dir, name string) error {
	if dir == "" || name == "" || strings.Contains(name, "/") || strings.Contains(name, "..") {
		return fmt.Errorf("nome de arquivo inválido")
	}
	full := filepath.Join(dir, name)
	cleanDir := filepath.Clean(dir)
	if rel, err := filepath.Rel(cleanDir, full); err != nil || strings.HasPrefix(rel, "..") {
		return fmt.Errorf("caminho de backup inválido")
	}
	return os.Remove(full)
}

type BackupFile struct {
	Name    string
	Size    int64
	ModTime time.Time
}

func (h *StoreHolder) ListBackups(dir string) ([]BackupFile, error) {
	if dir == "" {
		return nil, nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []BackupFile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".db") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, BackupFile{Name: e.Name(), Size: info.Size(), ModTime: info.ModTime()})
	}
	// newest first
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].ModTime.After(out[i].ModTime) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out, nil
}

// StartBackupScheduler runs forever in the background, taking a backup
// whenever auto-backup is enabled and the configured interval has
// elapsed since the last one.
func (h *StoreHolder) StartBackupScheduler() {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for range ticker.C {
			settings := h.GetBackupSettings()
			if !settings.Enabled || settings.Dir == "" {
				continue
			}
			due := time.Unix(settings.LastAt, 0).Add(time.Duration(settings.IntervalMinutes) * time.Minute)
			if settings.LastAt != 0 && time.Now().Before(due) {
				continue
			}
			path, err := h.RunBackupNow(settings.Dir)
			if err != nil {
				log.Printf("auto-backup failed: %v", err)
				continue
			}
			log.Printf("auto-backup written to %s", path)
		}
	}()
}
