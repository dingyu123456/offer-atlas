package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/google/uuid"
)

const safetyHistoryRetentionDays = 30

func (s *Store) syncSafetyMirror(operation, recordID string) {
	s.mirrorMu.Lock()
	snapshot, err := s.writeSafetySnapshot(operation, recordID)
	s.safetyMu.Lock()
	if err != nil {
		s.safety.LastError = err.Error()
	} else {
		s.safety.LatestSnapshot = snapshot
		s.safety.LastSyncedAt = nowString()
		s.safety.LastError = ""
	}
	s.safetyMu.Unlock()
	s.mirrorMu.Unlock()
	// A mirror is created after every mutation already. Use that existing,
	// centralized mutation boundary to schedule a debounced cloud sync without
	// affecting single-machine writes when Gitee has not been connected.
	if s.cloud != nil && operation != "startup" {
		s.cloud.afterLocalMutation()
	}
}

func (s *Store) writeSafetySnapshot(operation, recordID string) (string, error) {
	if err := os.MkdirAll(s.safetyDir, 0o755); err != nil {
		return "", err
	}
	pending, err := os.MkdirTemp(s.safetyDir, ".pending-")
	if err != nil {
		return "", err
	}
	completed := false
	defer func() {
		if !completed {
			_ = os.RemoveAll(pending)
		}
	}()

	tx, err := s.db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return "", err
	}
	defer tx.Rollback()

	applications, err := readApplicationMirrorRows(tx)
	if err != nil {
		return "", err
	}
	pendingPositions, err := readPendingPositionMirrorRows(tx)
	if err != nil {
		return "", err
	}
	if err := tx.Commit(); err != nil {
		return "", err
	}

	if err := writeReadableMirrorWorkbook(filepath.Join(pending, readableMirrorWorkbookName), applications, pendingPositions); err != nil {
		return "", err
	}

	archiveToday := operation != "startup" || len(applications) > 0 || len(pendingPositions) > 0
	if archiveToday {
		archivePath := filepath.Join(s.safetyDir, "history", time.Now().Format(dateLayout))
		if err := archiveDailySnapshot(pending, archivePath); err != nil {
			return "", err
		}
	}
	if err := pruneDailyArchives(filepath.Join(s.safetyDir, "history")); err != nil {
		return "", err
	}

	latestPath := filepath.Join(s.safetyDir, "latest")
	if err := replaceDirectory(pending, latestPath); err != nil {
		return "", err
	}
	completed = true
	if err := writeAtomicFile(filepath.Join(s.safetyDir, "LATEST.txt"), []byte("latest\n")); err != nil {
		return "", err
	}
	if err := appendJournal(filepath.Join(s.safetyDir, "journal.jsonl"), operation, recordID, "latest"); err != nil {
		return "", err
	}
	return latestPath, nil
}

func archiveDailySnapshot(source, destination string) error {
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	pending, err := os.MkdirTemp(filepath.Dir(destination), ".pending-")
	if err != nil {
		return err
	}
	completed := false
	defer func() {
		if !completed {
			_ = os.RemoveAll(pending)
		}
	}()
	if err := copyDirectory(source, pending); err != nil {
		return err
	}
	if err := replaceDirectory(pending, destination); err != nil {
		return err
	}
	completed = true
	return nil
}

func replaceDirectory(source, destination string) error {
	previous := destination + ".previous-" + uuid.NewString()
	hadPrevious := false
	if err := os.Rename(destination, previous); err == nil {
		hadPrevious = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Rename(source, destination); err != nil {
		if hadPrevious {
			_ = os.Rename(previous, destination)
		}
		return err
	}
	if hadPrevious {
		_ = os.RemoveAll(previous)
	}
	return nil
}

func copyDirectory(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return os.MkdirAll(destination, 0o755)
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return fmt.Errorf("unsupported entry in safety mirror: %q", relative)
		}
		return copyAttachmentFile(path, target)
	})
}

func pruneDailyArchives(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	dates := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := time.Parse(dateLayout, entry.Name()); err == nil {
			dates = append(dates, entry.Name())
		}
	}
	sort.Strings(dates)
	excess := len(dates) - safetyHistoryRetentionDays
	if excess <= 0 {
		return nil
	}
	for _, date := range dates[:excess] {
		if err := os.RemoveAll(filepath.Join(root, date)); err != nil {
			return err
		}
	}
	return nil
}

func writeAtomicFile(path string, contents []byte) error {
	temporary := path + ".tmp-" + uuid.NewString()
	if err := writeSyncedFile(temporary, contents); err != nil {
		return err
	}
	return os.Rename(temporary, path)
}

func writeSyncedFile(path string, contents []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := file.Write(contents); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	return file.Close()
}

func appendJournal(path, operation, recordID, snapshot string) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer file.Close()
	entry, err := json.Marshal(map[string]string{"at": nowString(), "operation": operation, "record_id": recordID, "snapshot": snapshot})
	if err != nil {
		return err
	}
	if _, err := file.Write(append(entry, '\n')); err != nil {
		return err
	}
	return file.Sync()
}
