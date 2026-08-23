package store

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dingyu/offer-atlas/internal/domain"
	"github.com/google/uuid"
)

const maxInlineImageBytes int64 = 12 * 1024 * 1024

type attachmentFileRef struct {
	Kind       string
	OwnerID    string
	StoredName string
}

func (s *Store) attachmentsDirectory() string {
	return filepath.Join(filepath.Dir(s.path), "attachments")
}

func (s *Store) resumesDirectory() string {
	return filepath.Join(s.attachmentsDirectory(), "resumes")
}

func (s *Store) resumePath(storedName string) (string, error) {
	if storedName == "" || filepath.Base(storedName) != storedName {
		return "", errors.New("invalid resume filename")
	}
	return filepath.Join(s.resumesDirectory(), storedName), nil
}

func (s *Store) attachmentPositionDirectory(positionID string) (string, error) {
	if positionID == "" || filepath.Base(positionID) != positionID {
		return "", errors.New("invalid position attachment directory")
	}
	return filepath.Join(s.attachmentsDirectory(), "positions", positionID), nil
}

func (s *Store) attachmentPath(positionID, storedName string) (string, error) {
	directory, err := s.attachmentPositionDirectory(positionID)
	if err != nil {
		return "", err
	}
	if storedName == "" || filepath.Base(storedName) != storedName {
		return "", errors.New("invalid attachment filename")
	}
	return filepath.Join(directory, storedName), nil
}

func (s *Store) applicationResumeDirectory(applicationID string) (string, error) {
	if applicationID == "" || filepath.Base(applicationID) != applicationID {
		return "", errors.New("invalid application resume directory")
	}
	return filepath.Join(s.attachmentsDirectory(), "applications", applicationID), nil
}

func (s *Store) applicationResumePath(applicationID, storedName string) (string, error) {
	directory, err := s.applicationResumeDirectory(applicationID)
	if err != nil {
		return "", err
	}
	if storedName == "" || filepath.Base(storedName) != storedName {
		return "", errors.New("invalid resume filename")
	}
	return filepath.Join(directory, storedName), nil
}

func (s *Store) ListPositionAttachments(positionID string) ([]domain.PositionAttachment, error) {
	if _, err := s.positionByID(positionID); err != nil {
		return nil, err
	}
	return s.listPositionAttachments(positionID)
}

func (s *Store) listPositionAttachments(positionID string) ([]domain.PositionAttachment, error) {
	rows, err := s.db.Query(`
		SELECT id, position_id, original_name, stored_name, mime_type, size_bytes, created_at
		FROM position_attachments WHERE position_id=? ORDER BY created_at DESC, id DESC
	`, positionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	attachments := make([]domain.PositionAttachment, 0)
	for rows.Next() {
		attachment, err := scanPositionAttachment(rows)
		if err != nil {
			return nil, err
		}
		attachments = append(attachments, attachment)
	}
	return attachments, rows.Err()
}

func (s *Store) ImportPositionAttachments(positionID string, sourcePaths []string) ([]domain.PositionAttachment, error) {
	if _, err := s.positionByID(positionID); err != nil {
		return nil, err
	}
	if len(sourcePaths) == 0 {
		return s.listPositionAttachments(positionID)
	}

	directory, err := s.attachmentPositionDirectory(positionID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, fmt.Errorf("create attachment directory: %w", err)
	}

	type pendingAttachment struct {
		attachment domain.PositionAttachment
		path       string
	}
	pending := make([]pendingAttachment, 0, len(sourcePaths))
	for _, sourcePath := range sourcePaths {
		attachment, path, err := importAttachmentFile(positionID, directory, sourcePath)
		if err != nil {
			for _, item := range pending {
				_ = os.Remove(item.path)
			}
			return nil, err
		}
		pending = append(pending, pendingAttachment{attachment: attachment, path: path})
	}

	tx, err := s.db.Begin()
	if err != nil {
		for _, item := range pending {
			_ = os.Remove(item.path)
		}
		return nil, err
	}
	defer tx.Rollback()
	for _, item := range pending {
		_, err = tx.Exec(`
			INSERT INTO position_attachments(id, position_id, original_name, stored_name, mime_type, size_bytes, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)
		`, item.attachment.ID, item.attachment.PositionID, item.attachment.OriginalName, item.attachment.StoredName, item.attachment.MIMEType, item.attachment.SizeBytes, datetimeString(item.attachment.CreatedAt))
		if err != nil {
			for _, copied := range pending {
				_ = os.Remove(copied.path)
			}
			return nil, databaseError(err)
		}
	}
	if err := tx.Commit(); err != nil {
		for _, item := range pending {
			_ = os.Remove(item.path)
		}
		return nil, err
	}
	s.syncSafetyMirror("position_attachments.imported", positionID)
	return s.listPositionAttachments(positionID)
}

// ImportPastedPositionImage stores screenshot bytes received from the app
// window. Clipboard payloads are deliberately limited to previewable images.
func (s *Store) ImportPastedPositionImage(positionID, originalName, mimeType string, contents []byte) ([]domain.PositionAttachment, error) {
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	if !isPreviewableImage(mimeType) {
		return nil, errors.New("only common image formats can be pasted from the clipboard")
	}
	return s.ImportPositionAttachmentData(positionID, originalName, mimeType, contents)
}

// ImportPositionAttachmentData stores bytes supplied by the app window. It is
// used for files staged before quick capture creates the target position.
func (s *Store) ImportPositionAttachmentData(positionID, originalName, mimeType string, contents []byte) ([]domain.PositionAttachment, error) {
	if _, err := s.positionByID(positionID); err != nil {
		return nil, err
	}
	if len(contents) == 0 {
		return nil, errors.New("attachment is empty")
	}
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	originalName = filepath.Base(strings.TrimSpace(originalName))
	if originalName == "" || originalName == "." {
		originalName = "attachment" + attachmentExtension(mimeType)
	}
	directory, err := s.attachmentPositionDirectory(positionID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, fmt.Errorf("create attachment directory: %w", err)
	}
	storedName := uuid.NewString() + safeAttachmentExtension(originalName)
	if filepath.Ext(storedName) == "" {
		storedName += attachmentExtension(mimeType)
	}
	path := filepath.Join(directory, storedName)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		return nil, fmt.Errorf("write uploaded attachment: %w", err)
	}
	attachment := domain.PositionAttachment{
		ID:           uuid.NewString(),
		PositionID:   positionID,
		OriginalName: originalName,
		StoredName:   storedName,
		MIMEType:     mimeType,
		SizeBytes:    int64(len(contents)),
		CreatedAt:    time.Now().UTC(),
	}
	if _, err := s.db.Exec(`
		INSERT INTO position_attachments(id, position_id, original_name, stored_name, mime_type, size_bytes, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`, attachment.ID, attachment.PositionID, attachment.OriginalName, attachment.StoredName, attachment.MIMEType, attachment.SizeBytes, datetimeString(attachment.CreatedAt)); err != nil {
		_ = os.Remove(path)
		return nil, databaseError(err)
	}
	s.syncSafetyMirror("position_attachment.uploaded", positionID)
	return s.listPositionAttachments(positionID)
}

// ImportApplicationResumeData is retained for older callers. New screens add
// files to the reusable resume library and associate the selected version with
// an application instead of creating an application-private file copy.
func (s *Store) ImportApplicationResumeData(applicationID, originalName, mimeType string, contents []byte) (domain.ApplicationResume, error) {
	if _, err := s.applicationByID(applicationID); err != nil {
		return domain.ApplicationResume{}, err
	}
	name := strings.TrimSuffix(filepath.Base(strings.TrimSpace(originalName)), filepath.Ext(originalName))
	resume, err := s.ImportResumeData(name, originalName, mimeType, contents)
	if err != nil {
		return domain.ApplicationResume{}, err
	}
	if err := s.SetApplicationResume(applicationID, resume.ID); err != nil {
		return domain.ApplicationResume{}, err
	}
	return domain.ApplicationResume{ID: resume.ID, ApplicationID: applicationID, OriginalName: resume.OriginalName, StoredName: resume.StoredName, MIMEType: resume.MIMEType, SizeBytes: resume.SizeBytes, CreatedAt: resume.CreatedAt}, nil
}

func (s *Store) ListResumes(includeArchived bool) ([]domain.Resume, error) {
	where := ""
	if !includeArchived {
		where = " WHERE r.archived=0"
	}
	rows, err := s.db.Query(`
		SELECT r.id, r.name, r.original_name, r.stored_name, r.mime_type, r.size_bytes,
		       r.content_hash, r.archived, r.created_at, r.updated_at,
		       (SELECT COUNT(*) FROM applications a WHERE a.resume_id=r.id)
		FROM resumes r` + where + ` ORDER BY r.archived, r.updated_at DESC, r.name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.Resume, 0)
	for rows.Next() {
		item, err := scanResume(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ImportResumeData(name, originalName, mimeType string, contents []byte) (domain.Resume, error) {
	if len(contents) == 0 {
		return domain.Resume{}, errors.New("resume is empty")
	}
	mimeType = strings.ToLower(strings.TrimSpace(mimeType))
	if mimeType == "" {
		mimeType = "application/octet-stream"
	}
	originalName = filepath.Base(strings.TrimSpace(originalName))
	if originalName == "" || originalName == "." {
		originalName = "resume" + attachmentExtension(mimeType)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		name = strings.TrimSuffix(originalName, filepath.Ext(originalName))
	}
	if name == "" {
		return domain.Resume{}, errors.New("resume version name is required")
	}
	hash := contentHash(contents)
	var existingID string
	if err := s.db.QueryRow(`SELECT id FROM resumes WHERE content_hash=? ORDER BY archived, updated_at DESC LIMIT 1`, hash).Scan(&existingID); err == nil {
		return s.resumeByID(existingID)
	} else if !errors.Is(err, sql.ErrNoRows) {
		return domain.Resume{}, err
	}
	storedName := hash + attachmentExtension(mimeType)
	path, err := s.resumePath(storedName)
	if err != nil {
		return domain.Resume{}, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return domain.Resume{}, fmt.Errorf("create resume library directory: %w", err)
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(path, contents, 0o600); err != nil {
			return domain.Resume{}, fmt.Errorf("write resume file: %w", err)
		}
	} else if err != nil {
		return domain.Resume{}, err
	}
	now := nowString()
	resume := domain.Resume{ID: uuid.NewString(), Name: name, OriginalName: originalName, StoredName: storedName, MIMEType: mimeType, SizeBytes: int64(len(contents)), ContentHash: hash, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if _, err := s.db.Exec(`INSERT INTO resumes(id, name, original_name, stored_name, mime_type, size_bytes, content_hash, archived, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`, resume.ID, resume.Name, resume.OriginalName, resume.StoredName, resume.MIMEType, resume.SizeBytes, resume.ContentHash, now, now); err != nil {
		return domain.Resume{}, databaseError(err)
	}
	s.syncSafetyMirror("resume.imported", resume.ID)
	return s.resumeByID(resume.ID)
}

func (s *Store) SaveResume(input domain.ResumeInput) (domain.Resume, error) {
	id := strings.TrimSpace(input.ID)
	if id == "" {
		return domain.Resume{}, errors.New("resume is required")
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return domain.Resume{}, errors.New("resume version name is required")
	}
	result, err := s.db.Exec(`UPDATE resumes SET name=?, archived=?, updated_at=? WHERE id=?`, name, boolToInt(input.Archived), nowString(), id)
	if err != nil {
		return domain.Resume{}, databaseError(err)
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return domain.Resume{}, errors.New("resume was not found")
	}
	s.syncSafetyMirror("resume.saved", id)
	return s.resumeByID(id)
}

func (s *Store) SetApplicationResume(applicationID, resumeID string) error {
	if _, err := s.applicationByID(applicationID); err != nil {
		return err
	}
	resume, err := s.resumeByID(strings.TrimSpace(resumeID))
	if err != nil {
		return err
	}
	if resume.Archived {
		return errors.New("archived resume versions cannot be selected for a new application")
	}
	if _, err := s.db.Exec(`UPDATE applications SET resume_id=?, resume_name=?, updated_at=? WHERE id=?`, resume.ID, resume.Name, nowString(), applicationID); err != nil {
		return databaseError(err)
	}
	s.syncSafetyMirror("application.resume_selected", applicationID)
	return nil
}

func (s *Store) ClearApplicationResume(applicationID string) error {
	if _, err := s.applicationByID(applicationID); err != nil {
		return err
	}
	if _, err := s.db.Exec(`UPDATE applications SET resume_id=NULL, resume_name='', updated_at=? WHERE id=?`, nowString(), applicationID); err != nil {
		return databaseError(err)
	}
	s.syncSafetyMirror("application.resume_cleared", applicationID)
	return nil
}

func (s *Store) ResumePath(id string) (string, error) {
	resume, err := s.resumeByID(id)
	if err != nil {
		return "", err
	}
	path, err := s.resumePath(resume.StoredName)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.New("resume file is missing; restore it from a backup if needed")
		}
		return "", err
	}
	return path, nil
}

func (s *Store) DeleteResume(id string) error {
	resume, err := s.resumeByID(id)
	if err != nil {
		return err
	}
	if resume.UsageCount > 0 {
		return fmt.Errorf("this resume is used by %d application(s); archive it instead", resume.UsageCount)
	}
	if _, err := s.db.Exec(`DELETE FROM resumes WHERE id=?`, resume.ID); err != nil {
		return databaseError(err)
	}
	var remaining int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM resumes WHERE stored_name=?`, resume.StoredName).Scan(&remaining); err != nil {
		return err
	}
	if remaining == 0 {
		if path, pathErr := s.resumePath(resume.StoredName); pathErr == nil {
			_ = os.Remove(path)
		}
	}
	s.syncSafetyMirror("resume.deleted", resume.ID)
	return nil
}

// migrateLegacyApplicationResumes turns the former one-file-per-application
// storage into reusable library versions. It is intentionally idempotent:
// a remote legacy attachment can arrive after startup and the same migration
// can safely run again after it is materialized.
func (s *Store) migrateLegacyApplicationResumes() error {
	rows, err := s.db.Query(`
		SELECT ar.application_id, ar.original_name, ar.stored_name, ar.mime_type,
		       ar.size_bytes, ar.created_at, COALESCE(a.resume_name, '')
		FROM application_resumes ar
		JOIN applications a ON a.id=ar.application_id
		WHERE COALESCE(a.resume_id, '')=''
		ORDER BY ar.created_at, ar.id
	`)
	if err != nil {
		return err
	}
	type legacyResume struct {
		applicationID, originalName, storedName, mimeType, createdAt, displayName string
		sizeBytes                                                                 int64
	}
	items := make([]legacyResume, 0)
	for rows.Next() {
		var item legacyResume
		if err := rows.Scan(&item.applicationID, &item.originalName, &item.storedName, &item.mimeType, &item.sizeBytes, &item.createdAt, &item.displayName); err != nil {
			rows.Close()
			return err
		}
		items = append(items, item)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, legacy := range items {
		legacyPath, pathErr := s.applicationResumePath(legacy.applicationID, legacy.storedName)
		if pathErr != nil {
			return pathErr
		}
		contents, readErr := os.ReadFile(legacyPath)
		if errors.Is(readErr, os.ErrNotExist) {
			// Keep only the metadata for now. A legacy Gitee operation may still
			// materialize the file later in this application's lifetime.
			continue
		}
		if readErr != nil {
			return readErr
		}
		hash := contentHash(contents)
		var existingID string
		err := s.db.QueryRow(`SELECT id FROM resumes WHERE content_hash=? ORDER BY created_at LIMIT 1`, hash).Scan(&existingID)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if errors.Is(err, sql.ErrNoRows) {
			storedName := hash + safeAttachmentExtension(legacy.originalName)
			if filepath.Ext(storedName) == "" {
				storedName += attachmentExtension(legacy.mimeType)
			}
			libraryPath, pathErr := s.resumePath(storedName)
			if pathErr != nil {
				return pathErr
			}
			if err := os.MkdirAll(filepath.Dir(libraryPath), 0o755); err != nil {
				return err
			}
			if _, statErr := os.Stat(libraryPath); errors.Is(statErr, os.ErrNotExist) {
				if err := copyAttachmentFile(legacyPath, libraryPath); err != nil {
					return err
				}
			} else if statErr != nil {
				return statErr
			}
			name := strings.TrimSpace(legacy.displayName)
			if name == "" {
				name = strings.TrimSuffix(legacy.originalName, filepath.Ext(legacy.originalName))
			}
			existingID = uuid.NewString()
			now := nowString()
			if _, err := s.db.Exec(`INSERT INTO resumes(id, name, original_name, stored_name, mime_type, size_bytes, content_hash, archived, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, 0, ?, ?)`, existingID, name, legacy.originalName, storedName, legacy.mimeType, len(contents), hash, legacy.createdAt, now); err != nil {
				return err
			}
		}
		resume, err := s.resumeByID(existingID)
		if err != nil {
			return err
		}
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`UPDATE applications SET resume_id=?, resume_name=?, updated_at=? WHERE id=?`, resume.ID, resume.Name, nowString(), legacy.applicationID); err == nil {
			_, err = tx.Exec(`DELETE FROM application_resumes WHERE application_id=?`, legacy.applicationID)
		}
		if err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
		_ = os.Remove(legacyPath)
		if directory, directoryErr := s.applicationResumeDirectory(legacy.applicationID); directoryErr == nil {
			_ = os.Remove(directory)
		}
	}
	return nil
}

func importAttachmentFile(positionID, directory, sourcePath string) (domain.PositionAttachment, string, error) {
	sourcePath = strings.TrimSpace(sourcePath)
	if sourcePath == "" {
		return domain.PositionAttachment{}, "", errors.New("attachment path is required")
	}
	source, err := os.Open(sourcePath)
	if err != nil {
		return domain.PositionAttachment{}, "", fmt.Errorf("open attachment: %w", err)
	}
	defer source.Close()
	info, err := source.Stat()
	if err != nil {
		return domain.PositionAttachment{}, "", err
	}
	if !info.Mode().IsRegular() {
		return domain.PositionAttachment{}, "", errors.New("only regular files can be added as attachments")
	}
	originalName := filepath.Base(sourcePath)
	if originalName == "." || originalName == "" {
		return domain.PositionAttachment{}, "", errors.New("attachment filename is invalid")
	}
	storedName := uuid.NewString() + safeAttachmentExtension(originalName)
	destination := filepath.Join(directory, storedName)
	file, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return domain.PositionAttachment{}, "", err
	}
	_, copyErr := io.Copy(file, source)
	closeErr := file.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(destination)
		if copyErr != nil {
			return domain.PositionAttachment{}, "", fmt.Errorf("copy attachment: %w", copyErr)
		}
		return domain.PositionAttachment{}, "", fmt.Errorf("close attachment: %w", closeErr)
	}

	return domain.PositionAttachment{
		ID:           uuid.NewString(),
		PositionID:   positionID,
		OriginalName: originalName,
		StoredName:   storedName,
		MIMEType:     detectAttachmentMIMEType(destination, originalName),
		SizeBytes:    info.Size(),
		CreatedAt:    time.Now().UTC(),
	}, destination, nil
}

func safeAttachmentExtension(name string) string {
	extension := strings.ToLower(filepath.Ext(name))
	if len(extension) > 16 || strings.ContainsAny(extension, `\\/:*?"<>|`) {
		return ""
	}
	return extension
}

func detectAttachmentMIMEType(path, originalName string) string {
	if value := mime.TypeByExtension(filepath.Ext(originalName)); value != "" {
		return strings.Split(value, ";")[0]
	}
	file, err := os.Open(path)
	if err != nil {
		return "application/octet-stream"
	}
	defer file.Close()
	buffer := make([]byte, 512)
	n, _ := file.Read(buffer)
	return http.DetectContentType(buffer[:n])
}

func (s *Store) PositionAttachmentDataURL(id string) (string, error) {
	attachment, err := s.positionAttachmentByID(id)
	if err != nil {
		return "", err
	}
	if !isPreviewableImage(attachment.MIMEType) {
		return "", errors.New("this attachment cannot be previewed as an image")
	}
	if attachment.SizeBytes > maxInlineImageBytes {
		return "", fmt.Errorf("image is larger than the %d MB preview limit", maxInlineImageBytes/(1024*1024))
	}
	path, err := s.attachmentPath(attachment.PositionID, attachment.StoredName)
	if err != nil {
		return "", err
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.New("attachment file is missing; restore it from a backup if needed")
		}
		return "", err
	}
	return "data:" + attachment.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(contents), nil
}

func isPreviewableImage(mimeType string) bool {
	switch strings.ToLower(mimeType) {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp":
		return true
	default:
		return false
	}
}

func imageExtension(mimeType string) string {
	switch mimeType {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	case "image/bmp":
		return ".bmp"
	default:
		return ""
	}
}

func attachmentExtension(mimeType string) string {
	if extension := imageExtension(mimeType); extension != "" {
		return extension
	}
	switch mimeType {
	case "text/plain":
		return ".txt"
	case "text/markdown":
		return ".md"
	case "application/pdf":
		return ".pdf"
	case "application/msword":
		return ".doc"
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return ".docx"
	}
	if extensions, err := mime.ExtensionsByType(mimeType); err == nil && len(extensions) > 0 {
		return safeAttachmentExtension(extensions[0])
	}
	return ""
}

func (s *Store) PositionAttachmentPath(id string) (string, error) {
	attachment, err := s.positionAttachmentByID(id)
	if err != nil {
		return "", err
	}
	path, err := s.attachmentPath(attachment.PositionID, attachment.StoredName)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.New("attachment file is missing; restore it from a backup if needed")
		}
		return "", err
	}
	return path, nil
}

func (s *Store) ApplicationResumePath(applicationID string) (string, error) {
	application, err := s.applicationByID(applicationID)
	if err != nil {
		return "", err
	}
	if application.ResumeID != "" {
		return s.ResumePath(application.ResumeID)
	}
	resume, err := s.applicationResumeByApplicationID(applicationID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", errors.New("no resume copy is saved for this application")
		}
		return "", err
	}
	path, err := s.applicationResumePath(applicationID, resume.StoredName)
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", errors.New("resume copy is missing; restore it from a backup if needed")
		}
		return "", err
	}
	return path, nil
}

func (s *Store) DeleteApplicationResume(applicationID string) error {
	application, err := s.applicationByID(applicationID)
	if err != nil {
		return err
	}
	if application.ResumeID != "" {
		return s.ClearApplicationResume(applicationID)
	}
	resume, err := s.applicationResumeByApplicationID(applicationID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	path, err := s.applicationResumePath(applicationID, resume.StoredName)
	if err != nil {
		return err
	}
	stagedPath := path + ".deleting-" + uuid.NewString()
	if err := os.Rename(path, stagedPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("prepare resume deletion: %w", err)
	}
	if _, err := s.db.Exec(`DELETE FROM application_resumes WHERE application_id=?`, applicationID); err != nil {
		if _, statErr := os.Stat(stagedPath); statErr == nil {
			_ = os.Rename(stagedPath, path)
		}
		return err
	}
	if _, err := s.db.Exec(`UPDATE applications SET resume_name='', updated_at=? WHERE id=?`, nowString(), applicationID); err != nil {
		return databaseError(err)
	}
	_ = os.Remove(stagedPath)
	if directory, dirErr := s.applicationResumeDirectory(applicationID); dirErr == nil {
		_ = os.Remove(directory)
	}
	s.syncSafetyMirror("application_resume.deleted", applicationID)
	return nil
}

func (s *Store) DeletePositionAttachment(id string) error {
	attachment, err := s.positionAttachmentByID(id)
	if err != nil {
		return err
	}
	path, err := s.attachmentPath(attachment.PositionID, attachment.StoredName)
	if err != nil {
		return err
	}
	stagedPath := path + ".deleting-" + uuid.NewString()
	if err := os.Rename(path, stagedPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("prepare attachment deletion: %w", err)
	}
	if _, err := s.db.Exec(`DELETE FROM position_attachments WHERE id=?`, id); err != nil {
		if _, statErr := os.Stat(stagedPath); statErr == nil {
			_ = os.Rename(stagedPath, path)
		}
		return err
	}
	_ = os.Remove(stagedPath)
	s.syncSafetyMirror("position_attachment.deleted", id)
	return nil
}

func (s *Store) positionAttachmentByID(id string) (domain.PositionAttachment, error) {
	row := s.db.QueryRow(`
		SELECT id, position_id, original_name, stored_name, mime_type, size_bytes, created_at
		FROM position_attachments WHERE id=?
	`, id)
	attachment, err := scanPositionAttachment(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.PositionAttachment{}, errors.New("attachment was not found")
	}
	return attachment, err
}

func (s *Store) applicationResumeByApplicationID(applicationID string) (domain.ApplicationResume, error) {
	row := s.db.QueryRow(`
		SELECT id, application_id, original_name, stored_name, mime_type, size_bytes, created_at
		FROM application_resumes WHERE application_id=?
	`, applicationID)
	return scanApplicationResume(row)
}

func (s *Store) resumeByID(id string) (domain.Resume, error) {
	row := s.db.QueryRow(`
		SELECT r.id, r.name, r.original_name, r.stored_name, r.mime_type, r.size_bytes,
		       r.content_hash, r.archived, r.created_at, r.updated_at,
		       (SELECT COUNT(*) FROM applications a WHERE a.resume_id=r.id)
		FROM resumes r WHERE r.id=?
	`, id)
	resume, err := scanResume(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Resume{}, errors.New("resume was not found")
	}
	return resume, err
}

func scanPositionAttachment(scanner interface{ Scan(...any) error }) (domain.PositionAttachment, error) {
	var attachment domain.PositionAttachment
	var createdAt string
	if err := scanner.Scan(&attachment.ID, &attachment.PositionID, &attachment.OriginalName, &attachment.StoredName, &attachment.MIMEType, &attachment.SizeBytes, &createdAt); err != nil {
		return domain.PositionAttachment{}, err
	}
	parsed, err := parseStoredDateTime(createdAt)
	if err != nil {
		return domain.PositionAttachment{}, err
	}
	attachment.CreatedAt = parsed
	return attachment, nil
}

func scanApplicationResume(scanner interface{ Scan(...any) error }) (domain.ApplicationResume, error) {
	var resume domain.ApplicationResume
	var createdAt string
	if err := scanner.Scan(&resume.ID, &resume.ApplicationID, &resume.OriginalName, &resume.StoredName, &resume.MIMEType, &resume.SizeBytes, &createdAt); err != nil {
		return domain.ApplicationResume{}, err
	}
	parsed, err := parseStoredDateTime(createdAt)
	if err != nil {
		return domain.ApplicationResume{}, err
	}
	resume.CreatedAt = parsed
	return resume, nil
}

func scanResume(scanner interface{ Scan(...any) error }) (domain.Resume, error) {
	var resume domain.Resume
	var archived int
	var createdAt, updatedAt string
	if err := scanner.Scan(&resume.ID, &resume.Name, &resume.OriginalName, &resume.StoredName, &resume.MIMEType, &resume.SizeBytes, &resume.ContentHash, &archived, &createdAt, &updatedAt, &resume.UsageCount); err != nil {
		return domain.Resume{}, err
	}
	resume.Archived = archived != 0
	var err error
	if resume.CreatedAt, err = parseStoredDateTime(createdAt); err != nil {
		return domain.Resume{}, err
	}
	if resume.UpdatedAt, err = parseStoredDateTime(updatedAt); err != nil {
		return domain.Resume{}, err
	}
	return resume, nil
}

func (s *Store) attachmentFileRefs(queryer interface {
	Query(query string, args ...any) (*sql.Rows, error)
}) ([]attachmentFileRef, error) {
	rows, err := queryer.Query(`
		SELECT 'positions', position_id, stored_name FROM position_attachments
		UNION ALL
		SELECT 'resumes', '', stored_name FROM resumes
		UNION ALL
		SELECT 'applications', application_id, stored_name FROM application_resumes
		ORDER BY 1, 2, 3
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	refs := make([]attachmentFileRef, 0)
	for rows.Next() {
		var ref attachmentFileRef
		if err := rows.Scan(&ref.Kind, &ref.OwnerID, &ref.StoredName); err != nil {
			return nil, err
		}
		refs = append(refs, ref)
	}
	return refs, rows.Err()
}

func (s *Store) copyCurrentAttachmentFiles(destination string, refs []attachmentFileRef) error {
	for _, ref := range refs {
		var sourcePath string
		var err error
		switch ref.Kind {
		case "positions":
			sourcePath, err = s.attachmentPath(ref.OwnerID, ref.StoredName)
		case "resumes":
			sourcePath, err = s.resumePath(ref.StoredName)
		case "applications":
			sourcePath, err = s.applicationResumePath(ref.OwnerID, ref.StoredName)
		default:
			return fmt.Errorf("unknown attachment kind %q", ref.Kind)
		}
		if err != nil {
			return err
		}
		targetDirectory := filepath.Join(destination, "attachments", ref.Kind, ref.OwnerID)
		if ref.Kind == "resumes" {
			targetDirectory = filepath.Join(destination, "attachments", "resumes")
		}
		if err := os.MkdirAll(targetDirectory, 0o755); err != nil {
			return err
		}
		if err := copyAttachmentFile(sourcePath, filepath.Join(targetDirectory, ref.StoredName)); err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("registered attachment %q is missing from storage", ref.StoredName)
			}
			return err
		}
	}
	return nil
}

func copyAttachmentFile(source, destination string) error {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(output, input)
	closeErr := output.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(destination)
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	}
	return nil
}

func (s *Store) positionIDsForDeletion(target domain.DeletionTargetType, id string) ([]string, error) {
	var query string
	switch target {
	case domain.DeletionTargetCompany:
		query = `SELECT p.id FROM positions p JOIN campaigns ca ON ca.id=p.campaign_id WHERE ca.company_id=?`
	case domain.DeletionTargetCampaign:
		query = `SELECT id FROM positions WHERE campaign_id=?`
	case domain.DeletionTargetPosition:
		query = `SELECT id FROM positions WHERE id=?`
	default:
		return nil, nil
	}
	rows, err := s.db.Query(query, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	positionIDs := make([]string, 0)
	for rows.Next() {
		var positionID string
		if err := rows.Scan(&positionID); err != nil {
			return nil, err
		}
		positionIDs = append(positionIDs, positionID)
	}
	return positionIDs, rows.Err()
}

func (s *Store) cleanupPositionAttachmentFiles(positionIDs []string) {
	for _, positionID := range positionIDs {
		directory, err := s.attachmentPositionDirectory(positionID)
		if err == nil {
			_ = os.RemoveAll(directory)
		}
	}
}

func (s *Store) applicationIDsForDeletion(target domain.DeletionTargetType, id string) ([]string, error) {
	var query string
	switch target {
	case domain.DeletionTargetCompany:
		query = `SELECT a.id FROM applications a JOIN positions p ON p.id=a.position_id JOIN campaigns ca ON ca.id=p.campaign_id WHERE ca.company_id=?`
	case domain.DeletionTargetCampaign:
		query = `SELECT a.id FROM applications a JOIN positions p ON p.id=a.position_id WHERE p.campaign_id=?`
	case domain.DeletionTargetPosition:
		query = `SELECT id FROM applications WHERE position_id=?`
	case domain.DeletionTargetApplication:
		query = `SELECT id FROM applications WHERE id=?`
	default:
		return nil, nil
	}
	rows, err := s.db.Query(query, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ids := make([]string, 0)
	for rows.Next() {
		var applicationID string
		if err := rows.Scan(&applicationID); err != nil {
			return nil, err
		}
		ids = append(ids, applicationID)
	}
	return ids, rows.Err()
}

func (s *Store) cleanupApplicationResumeFiles(applicationIDs []string) {
	for _, applicationID := range applicationIDs {
		directory, err := s.applicationResumeDirectory(applicationID)
		if err == nil {
			_ = os.RemoveAll(directory)
		}
	}
}
