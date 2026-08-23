package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/dingyu/offer-atlas/internal/domain"
	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

func (s *Store) backupDirectory() string {
	return filepath.Join(filepath.Dir(s.path), "backups")
}

// BackupCenter describes both safety mechanisms without conflating them:
// readable Excel mirrors and complete, restorable SQLite backups.
func (s *Store) BackupCenter() (domain.BackupCenter, error) {
	backups, err := s.listBackups()
	if err != nil {
		return domain.BackupCenter{}, err
	}
	archives, err := countDailyArchives(filepath.Join(s.safetyDir, "history"))
	if err != nil {
		return domain.BackupCenter{}, err
	}
	s.safetyMu.RLock()
	safety := s.safety
	s.safetyMu.RUnlock()
	cloud, err := s.CloudSyncStatus()
	if err != nil {
		return domain.BackupCenter{}, err
	}
	return domain.BackupCenter{
		DataDirectory:   filepath.Dir(s.path),
		BackupDirectory: s.backupDirectory(),
		MirrorDirectory: s.safetyDir,
		LastSyncedAt:    safety.LastSyncedAt,
		ArchivesCount:   archives,
		Backups:         backups,
		CloudSync:       cloud,
	}, nil
}

func (s *Store) listBackups() ([]domain.BackupRecord, error) {
	entries, err := os.ReadDir(s.backupDirectory())
	if errors.Is(err, os.ErrNotExist) {
		return []domain.BackupRecord{}, nil
	}
	if err != nil {
		return nil, err
	}
	items := make([]domain.BackupRecord, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || !strings.HasPrefix(entry.Name(), "offer-atlas-") {
			continue
		}
		item, err := backupRecord(filepath.Join(s.backupDirectory(), entry.Name()))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CreatedAt > items[j].CreatedAt })
	return items, nil
}

func backupRecord(directory string) (domain.BackupRecord, error) {
	name := filepath.Base(directory)
	if name == "." || name == "" || filepath.Base(name) != name || !strings.HasPrefix(name, "offer-atlas-") {
		return domain.BackupRecord{}, errors.New("invalid backup directory")
	}
	databasePath := filepath.Join(directory, "offer-atlas.db")
	databaseInfo, err := os.Stat(databasePath)
	if err != nil {
		return domain.BackupRecord{}, err
	}
	if !databaseInfo.Mode().IsRegular() {
		return domain.BackupRecord{}, errors.New("backup database is not a regular file")
	}
	count, size, err := directoryFileStats(filepath.Join(directory, "attachments"))
	if err != nil {
		return domain.BackupRecord{}, err
	}
	return domain.BackupRecord{
		ID:              name,
		CreatedAt:       datetimeString(databaseInfo.ModTime().UTC()),
		DatabaseSize:    databaseInfo.Size(),
		AttachmentCount: count,
		AttachmentSize:  size,
	}, nil
}

func directoryFileStats(root string) (int, int64, error) {
	if _, err := os.Stat(root); errors.Is(err, os.ErrNotExist) {
		return 0, 0, nil
	} else if err != nil {
		return 0, 0, err
	}
	count, size := 0, int64(0)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("unsupported attachment in backup: %q", path)
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		count++
		size += info.Size()
		return nil
	})
	return count, size, err
}

func countDailyArchives(root string) (int, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	count := 0
	for _, entry := range entries {
		if entry.IsDir() {
			if _, err := time.Parse(dateLayout, entry.Name()); err == nil {
				count++
			}
		}
	}
	return count, nil
}

func (s *Store) RestoreBackup(id, confirmation string) (domain.RestoreResult, error) {
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	if id == "" || filepath.Base(id) != id || confirmation != id {
		return domain.RestoreResult{}, errors.New("type the backup name exactly to confirm restoration")
	}
	restored, err := backupRecord(filepath.Join(s.backupDirectory(), id))
	if err != nil {
		return domain.RestoreResult{}, fmt.Errorf("read backup: %w", err)
	}
	source := filepath.Join(s.backupDirectory(), id)
	if err := validateBackupDatabase(filepath.Join(source, "offer-atlas.db")); err != nil {
		return domain.RestoreResult{}, fmt.Errorf("backup verification failed: %w", err)
	}

	currentPath, err := s.createBackup()
	if err != nil {
		return domain.RestoreResult{}, fmt.Errorf("create current safety backup: %w", err)
	}
	safetyBackup, err := backupRecord(filepath.Dir(currentPath))
	if err != nil {
		return domain.RestoreResult{}, err
	}

	root := filepath.Dir(s.path)
	staging, err := os.MkdirTemp(root, ".restore-pending-")
	if err != nil {
		return domain.RestoreResult{}, err
	}
	defer os.RemoveAll(staging)
	stagedDatabase := filepath.Join(staging, "offer-atlas.db")
	if err := copyAttachmentFile(filepath.Join(source, "offer-atlas.db"), stagedDatabase); err != nil {
		return domain.RestoreResult{}, err
	}
	if _, err := os.Stat(filepath.Join(source, "attachments")); err == nil {
		if err := copyDirectory(filepath.Join(source, "attachments"), filepath.Join(staging, "attachments")); err != nil {
			return domain.RestoreResult{}, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return domain.RestoreResult{}, err
	}
	if err := validateBackupDatabase(stagedDatabase); err != nil {
		return domain.RestoreResult{}, fmt.Errorf("staged backup verification failed: %w", err)
	}

	if err := s.db.Close(); err != nil {
		return domain.RestoreResult{}, fmt.Errorf("close current database: %w", err)
	}
	currentDatabase := s.path + ".before-restore-" + uuid.NewString()
	currentAttachments := filepath.Join(root, "attachments.before-restore-"+uuid.NewString())
	if err := os.Rename(s.path, currentDatabase); err != nil {
		return domain.RestoreResult{}, s.reopenAfterRestoreFailure(err)
	}
	for _, temporary := range []string{s.path + "-wal", s.path + "-shm"} {
		if err := os.Remove(temporary); err != nil && !errors.Is(err, os.ErrNotExist) {
			return domain.RestoreResult{}, s.restoreOriginalDatabase(currentDatabase, currentAttachments, false, err)
		}
	}
	if err := os.Rename(stagedDatabase, s.path); err != nil {
		return domain.RestoreResult{}, s.restoreOriginalDatabase(currentDatabase, currentAttachments, false, err)
	}
	attachmentsPath := filepath.Join(root, "attachments")
	if err := os.Rename(attachmentsPath, currentAttachments); err != nil && !errors.Is(err, os.ErrNotExist) {
		return domain.RestoreResult{}, s.restoreOriginalDatabase(currentDatabase, currentAttachments, false, err)
	}
	if _, err := os.Stat(filepath.Join(staging, "attachments")); err == nil {
		if err := os.Rename(filepath.Join(staging, "attachments"), attachmentsPath); err != nil {
			return domain.RestoreResult{}, s.restoreOriginalDatabase(currentDatabase, currentAttachments, true, err)
		}
	}
	if err := s.reopenRestoredDatabase(); err != nil {
		return domain.RestoreResult{}, s.restoreOriginalDatabase(currentDatabase, currentAttachments, true, err)
	}
	_ = os.Remove(currentDatabase)
	_ = os.RemoveAll(currentAttachments)
	s.syncSafetyMirror("database.restored", restored.ID)
	return domain.RestoreResult{RestoredBackup: restored, SafetyBackup: safetyBackup}, nil
}

func validateBackupDatabase(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return errors.New("backup database is not a regular file")
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	defer db.Close()
	var integrity string
	if err := db.QueryRow(`PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return err
	}
	if integrity != "ok" {
		return fmt.Errorf("integrity check returned %q", integrity)
	}
	var count int
	if err := db.QueryRow(`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('companies', 'campaigns', 'positions', 'applications', 'application_stages')`).Scan(&count); err != nil {
		return err
	}
	if count != 5 {
		return errors.New("backup does not contain an OfferAtlas database")
	}
	return nil
}

func (s *Store) reopenRestoredDatabase() error {
	db, err := sql.Open("sqlite", s.path)
	if err != nil {
		return err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec(`PRAGMA foreign_keys = ON; PRAGMA journal_mode = WAL; PRAGMA busy_timeout = 5000;`); err != nil {
		_ = db.Close()
		return err
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return err
	}
	s.db = db
	if err := s.migrateLegacyApplicationResumes(); err != nil {
		_ = db.Close()
		return err
	}
	return s.refreshAllApplicationStatuses()
}

func (s *Store) reopenAfterRestoreFailure(cause error) error {
	if err := s.reopenRestoredDatabase(); err != nil {
		return fmt.Errorf("%v; reopen current database: %w", cause, err)
	}
	return cause
}

func (s *Store) restoreOriginalDatabase(databasePath, attachmentsPath string, attachmentsWereMoved bool, cause error) error {
	_ = s.db.Close()
	_ = os.Remove(s.path)
	if err := os.Rename(databasePath, s.path); err != nil {
		return fmt.Errorf("%v; restore original database: %w", cause, err)
	}
	if attachmentsWereMoved {
		currentAttachments := filepath.Join(filepath.Dir(s.path), "attachments")
		_ = os.RemoveAll(currentAttachments)
		if _, err := os.Stat(attachmentsPath); err == nil {
			if err := os.Rename(attachmentsPath, currentAttachments); err != nil {
				return fmt.Errorf("%v; restore original attachments: %w", cause, err)
			}
		}
	}
	if err := s.reopenRestoredDatabase(); err != nil {
		return fmt.Errorf("%v; reopen original database: %w", cause, err)
	}
	return cause
}
