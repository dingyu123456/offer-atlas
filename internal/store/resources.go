package store

import (
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/dingyu/offer-atlas/internal/domain"
	"github.com/google/uuid"
)

type resourceOwnerRef struct {
	Type domain.ResourceOwnerType
	ID   string
}

func (s *Store) ListResourceLinks(ownerType domain.ResourceOwnerType, ownerID string) ([]domain.ResourceLink, error) {
	if err := s.validateResourceOwner(ownerType, ownerID); err != nil {
		return nil, err
	}
	return s.listResourceLinks(ownerType, ownerID)
}

func (s *Store) listResourceLinks(ownerType domain.ResourceOwnerType, ownerID string) ([]domain.ResourceLink, error) {
	rows, err := s.db.Query(`
		SELECT id, owner_type, owner_id, name, url, sort_order, created_at, updated_at
		FROM resource_links WHERE owner_type=? AND owner_id=?
		ORDER BY sort_order, created_at, id
	`, ownerType, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.ResourceLink, 0)
	for rows.Next() {
		item, scanErr := scanResourceLink(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// SaveResourceLinks replaces the ordered link collection for one object. Link
// IDs remain stable for unchanged entries so sync can merge individual links
// rather than treating a note edit as an opaque text replacement.
func (s *Store) SaveResourceLinks(ownerType domain.ResourceOwnerType, ownerID string, inputs []domain.ResourceLinkInput) ([]domain.ResourceLink, error) {
	if err := s.validateResourceOwner(ownerType, ownerID); err != nil {
		return nil, err
	}
	prepared, err := validateResourceLinkInputs(inputs)
	if err != nil {
		return nil, err
	}
	existing, err := s.listResourceLinks(ownerType, ownerID)
	if err != nil {
		return nil, err
	}
	existingByID := make(map[string]domain.ResourceLink, len(existing))
	for _, item := range existing {
		existingByID[item.ID] = item
	}
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	kept := make(map[string]bool, len(prepared))
	now := nowString()
	for index, input := range prepared {
		id := strings.TrimSpace(input.ID)
		if id == "" {
			id = uuid.NewString()
			if _, execErr := tx.Exec(`
				INSERT INTO resource_links(id, owner_type, owner_id, name, url, sort_order, created_at, updated_at)
				VALUES (?, ?, ?, ?, ?, ?, ?, ?)
			`, id, ownerType, ownerID, input.Name, input.URL, index+1, now, now); execErr != nil {
				return nil, databaseError(execErr)
			}
		} else {
			if _, ok := existingByID[id]; !ok {
				return nil, errors.New("related link no longer exists; refresh and try again")
			}
			if _, execErr := tx.Exec(`UPDATE resource_links SET name=?, url=?, sort_order=?, updated_at=? WHERE id=? AND owner_type=? AND owner_id=?`, input.Name, input.URL, index+1, now, id, ownerType, ownerID); execErr != nil {
				return nil, databaseError(execErr)
			}
		}
		kept[id] = true
	}
	for _, item := range existing {
		if !kept[item.ID] {
			if _, execErr := tx.Exec(`DELETE FROM resource_links WHERE id=?`, item.ID); execErr != nil {
				return nil, databaseError(execErr)
			}
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	s.syncSafetyMirror("resource_links.saved", string(ownerType)+":"+ownerID)
	return s.listResourceLinks(ownerType, ownerID)
}

func validateResourceLinkInputs(inputs []domain.ResourceLinkInput) ([]domain.ResourceLinkInput, error) {
	prepared := make([]domain.ResourceLinkInput, 0, len(inputs))
	seen := make(map[string]bool, len(inputs))
	for _, input := range inputs {
		name := strings.TrimSpace(input.Name)
		link := strings.TrimSpace(input.URL)
		if name == "" && link == "" {
			continue
		}
		if name == "" {
			return nil, errors.New("related link name is required")
		}
		if len([]rune(name)) > 80 {
			return nil, errors.New("related link name must be 80 characters or less")
		}
		parsed, err := url.ParseRequestURI(link)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return nil, errors.New("related link must be a valid http or https URL")
		}
		id := strings.TrimSpace(input.ID)
		if id != "" {
			if seen[id] {
				return nil, errors.New("a related link was added more than once")
			}
			seen[id] = true
		}
		prepared = append(prepared, domain.ResourceLinkInput{ID: id, Name: name, URL: parsed.String()})
	}
	return prepared, nil
}

func scanResourceLink(scanner interface{ Scan(...any) error }) (domain.ResourceLink, error) {
	var item domain.ResourceLink
	var ownerType string
	var createdAt, updatedAt string
	if err := scanner.Scan(&item.ID, &ownerType, &item.OwnerID, &item.Name, &item.URL, &item.SortOrder, &createdAt, &updatedAt); err != nil {
		return domain.ResourceLink{}, err
	}
	item.OwnerType = domain.ResourceOwnerType(ownerType)
	var err error
	if item.CreatedAt, err = parseStoredDateTime(createdAt); err != nil {
		return domain.ResourceLink{}, err
	}
	if item.UpdatedAt, err = parseStoredDateTime(updatedAt); err != nil {
		return domain.ResourceLink{}, err
	}
	return item, nil
}

func (s *Store) ListSupplementalAttachments(ownerType domain.ResourceOwnerType, ownerID string) ([]domain.SupplementalAttachment, error) {
	if err := s.validateSupplementalOwner(ownerType, ownerID); err != nil {
		return nil, err
	}
	return s.listSupplementalAttachments(ownerType, ownerID)
}

func (s *Store) listSupplementalAttachments(ownerType domain.ResourceOwnerType, ownerID string) ([]domain.SupplementalAttachment, error) {
	rows, err := s.db.Query(`
		SELECT id, owner_type, owner_id, original_name, stored_name, mime_type, size_bytes, created_at
		FROM supplemental_attachments WHERE owner_type=? AND owner_id=? ORDER BY created_at DESC, id DESC
	`, ownerType, ownerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.SupplementalAttachment, 0)
	for rows.Next() {
		item, scanErr := scanSupplementalAttachment(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) ImportSupplementalAttachmentData(ownerType domain.ResourceOwnerType, ownerID, originalName, mimeType string, contents []byte) ([]domain.SupplementalAttachment, error) {
	if err := s.validateSupplementalOwner(ownerType, ownerID); err != nil {
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
	directory, err := s.supplementalAttachmentDirectory(ownerType, ownerID)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return nil, fmt.Errorf("create supplemental attachment directory: %w", err)
	}
	storedName := uuid.NewString() + safeAttachmentExtension(originalName)
	if filepath.Ext(storedName) == "" {
		storedName += attachmentExtension(mimeType)
	}
	path := filepath.Join(directory, storedName)
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		return nil, fmt.Errorf("write supplemental attachment: %w", err)
	}
	item := domain.SupplementalAttachment{ID: uuid.NewString(), OwnerType: ownerType, OwnerID: ownerID, OriginalName: originalName, StoredName: storedName, MIMEType: mimeType, SizeBytes: int64(len(contents)), CreatedAt: time.Now().UTC()}
	if _, err := s.db.Exec(`
		INSERT INTO supplemental_attachments(id, owner_type, owner_id, original_name, stored_name, mime_type, size_bytes, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, item.ID, item.OwnerType, item.OwnerID, item.OriginalName, item.StoredName, item.MIMEType, item.SizeBytes, datetimeString(item.CreatedAt)); err != nil {
		_ = os.Remove(path)
		return nil, databaseError(err)
	}
	s.syncSafetyMirror("supplemental_attachment.uploaded", string(ownerType)+":"+ownerID)
	return s.listSupplementalAttachments(ownerType, ownerID)
}

func (s *Store) ImportPastedSupplementalImage(ownerType domain.ResourceOwnerType, ownerID, originalName, mimeType string, contents []byte) ([]domain.SupplementalAttachment, error) {
	if !isPreviewableImage(strings.ToLower(strings.TrimSpace(mimeType))) {
		return nil, errors.New("only common image formats can be pasted from the clipboard")
	}
	return s.ImportSupplementalAttachmentData(ownerType, ownerID, originalName, mimeType, contents)
}

func (s *Store) SupplementalAttachmentDataURL(id string) (string, error) {
	item, err := s.supplementalAttachmentByID(id)
	if err != nil {
		return "", err
	}
	if !isPreviewableImage(item.MIMEType) {
		return "", errors.New("this attachment cannot be previewed as an image")
	}
	if item.SizeBytes > maxInlineImageBytes {
		return "", fmt.Errorf("image is larger than the %d MB preview limit", maxInlineImageBytes/(1024*1024))
	}
	path, err := s.supplementalAttachmentPath(item.OwnerType, item.OwnerID, item.StoredName)
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
	return "data:" + item.MIMEType + ";base64," + base64.StdEncoding.EncodeToString(contents), nil
}

func (s *Store) SupplementalAttachmentPath(id string) (string, error) {
	item, err := s.supplementalAttachmentByID(id)
	if err != nil {
		return "", err
	}
	path, err := s.supplementalAttachmentPath(item.OwnerType, item.OwnerID, item.StoredName)
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

func (s *Store) DeleteSupplementalAttachment(id string) error {
	item, err := s.supplementalAttachmentByID(id)
	if err != nil {
		return err
	}
	path, err := s.supplementalAttachmentPath(item.OwnerType, item.OwnerID, item.StoredName)
	if err != nil {
		return err
	}
	stagedPath := path + ".deleting-" + uuid.NewString()
	if err := os.Rename(path, stagedPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("prepare supplemental attachment deletion: %w", err)
	}
	if _, err := s.db.Exec(`DELETE FROM supplemental_attachments WHERE id=?`, item.ID); err != nil {
		if _, statErr := os.Stat(stagedPath); statErr == nil {
			_ = os.Rename(stagedPath, path)
		}
		return databaseError(err)
	}
	_ = os.Remove(stagedPath)
	s.syncSafetyMirror("supplemental_attachment.deleted", item.ID)
	return nil
}

func (s *Store) supplementalAttachmentDirectory(ownerType domain.ResourceOwnerType, ownerID string) (string, error) {
	if !domain.SupportsSupplementalAttachments(ownerType) || ownerID == "" || filepath.Base(ownerID) != ownerID {
		return "", errors.New("invalid supplemental attachment owner")
	}
	return filepath.Join(s.attachmentsDirectory(), "resources", string(ownerType), ownerID), nil
}

func (s *Store) supplementalAttachmentPath(ownerType domain.ResourceOwnerType, ownerID, storedName string) (string, error) {
	directory, err := s.supplementalAttachmentDirectory(ownerType, ownerID)
	if err != nil {
		return "", err
	}
	if storedName == "" || filepath.Base(storedName) != storedName {
		return "", errors.New("invalid supplemental attachment filename")
	}
	return filepath.Join(directory, storedName), nil
}

func (s *Store) supplementalAttachmentByID(id string) (domain.SupplementalAttachment, error) {
	row := s.db.QueryRow(`
		SELECT id, owner_type, owner_id, original_name, stored_name, mime_type, size_bytes, created_at
		FROM supplemental_attachments WHERE id=?
	`, id)
	item, err := scanSupplementalAttachment(row)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.SupplementalAttachment{}, errors.New("supplemental attachment was not found")
	}
	return item, err
}

func scanSupplementalAttachment(scanner interface{ Scan(...any) error }) (domain.SupplementalAttachment, error) {
	var item domain.SupplementalAttachment
	var ownerType, createdAt string
	if err := scanner.Scan(&item.ID, &ownerType, &item.OwnerID, &item.OriginalName, &item.StoredName, &item.MIMEType, &item.SizeBytes, &createdAt); err != nil {
		return domain.SupplementalAttachment{}, err
	}
	item.OwnerType = domain.ResourceOwnerType(ownerType)
	parsed, err := parseStoredDateTime(createdAt)
	if err != nil {
		return domain.SupplementalAttachment{}, err
	}
	item.CreatedAt = parsed
	return item, nil
}

func (s *Store) validateSupplementalOwner(ownerType domain.ResourceOwnerType, ownerID string) error {
	if !domain.SupportsSupplementalAttachments(ownerType) {
		return errors.New("this record uses its existing attachment collection")
	}
	return s.validateResourceOwner(ownerType, ownerID)
}

func (s *Store) validateResourceOwner(ownerType domain.ResourceOwnerType, ownerID string) error {
	ownerID = strings.TrimSpace(ownerID)
	if !domain.ValidResourceOwnerType(ownerType) || ownerID == "" {
		return errors.New("invalid related material owner")
	}
	var table string
	switch ownerType {
	case domain.ResourceOwnerCompany:
		table = "companies"
	case domain.ResourceOwnerCampaign:
		table = "campaigns"
	case domain.ResourceOwnerPosition:
		table = "positions"
	case domain.ResourceOwnerApplication:
		table = "applications"
	case domain.ResourceOwnerStage:
		table = "application_stages"
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM `+table+` WHERE id=?`, ownerID).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return errors.New("the record for these materials was not found")
	}
	return nil
}

func (s *Store) deleteResourcesForOwners(tx *sql.Tx, owners []resourceOwnerRef) error {
	for _, owner := range owners {
		if _, err := tx.Exec(`DELETE FROM resource_links WHERE owner_type=? AND owner_id=?`, owner.Type, owner.ID); err != nil {
			return err
		}
		if domain.SupportsSupplementalAttachments(owner.Type) {
			if _, err := tx.Exec(`DELETE FROM supplemental_attachments WHERE owner_type=? AND owner_id=?`, owner.Type, owner.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *Store) cleanupSupplementalAttachmentFiles(owners []resourceOwnerRef) {
	for _, owner := range owners {
		if directory, err := s.supplementalAttachmentDirectory(owner.Type, owner.ID); err == nil {
			_ = os.RemoveAll(directory)
		}
	}
}

func (s *Store) resourceOwnersForDeletion(target domain.DeletionTargetType, id string) ([]resourceOwnerRef, error) {
	owners := make([]resourceOwnerRef, 0)
	add := func(ownerType domain.ResourceOwnerType, ownerID string) {
		if ownerID != "" {
			owners = append(owners, resourceOwnerRef{Type: ownerType, ID: ownerID})
		}
	}
	switch target {
	case domain.DeletionTargetCompany:
		add(domain.ResourceOwnerCompany, id)
		rows, err := s.db.Query(`SELECT id FROM campaigns WHERE company_id=?`, id)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				rows.Close()
				return nil, err
			}
			add(domain.ResourceOwnerCampaign, value)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		rows, err = s.db.Query(`SELECT p.id FROM positions p JOIN campaigns c ON c.id=p.campaign_id WHERE c.company_id=?`, id)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				rows.Close()
				return nil, err
			}
			add(domain.ResourceOwnerPosition, value)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		rows, err = s.db.Query(`SELECT a.id FROM applications a JOIN positions p ON p.id=a.position_id JOIN campaigns c ON c.id=p.campaign_id WHERE c.company_id=?`, id)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				rows.Close()
				return nil, err
			}
			add(domain.ResourceOwnerApplication, value)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		rows, err = s.db.Query(`SELECT s.id FROM application_stages s JOIN applications a ON a.id=s.application_id JOIN positions p ON p.id=a.position_id JOIN campaigns c ON c.id=p.campaign_id WHERE c.company_id=?`, id)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				rows.Close()
				return nil, err
			}
			add(domain.ResourceOwnerStage, value)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	case domain.DeletionTargetCampaign:
		add(domain.ResourceOwnerCampaign, id)
		return s.resourceOwnersForCampaignDeletion(owners, id)
	case domain.DeletionTargetPosition:
		add(domain.ResourceOwnerPosition, id)
		return s.resourceOwnersForPositionDeletion(owners, id)
	case domain.DeletionTargetApplication:
		add(domain.ResourceOwnerApplication, id)
		rows, err := s.db.Query(`SELECT id FROM application_stages WHERE application_id=?`, id)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var value string
			if err := rows.Scan(&value); err != nil {
				rows.Close()
				return nil, err
			}
			add(domain.ResourceOwnerStage, value)
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return owners, nil
}

func (s *Store) resourceOwnersForCampaignDeletion(owners []resourceOwnerRef, campaignID string) ([]resourceOwnerRef, error) {
	rows, err := s.db.Query(`SELECT id FROM positions WHERE campaign_id=?`, campaignID)
	if err != nil {
		return nil, err
	}
	positionIDs := make([]string, 0)
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			rows.Close()
			return nil, err
		}
		owners = append(owners, resourceOwnerRef{Type: domain.ResourceOwnerPosition, ID: value})
		positionIDs = append(positionIDs, value)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for _, positionID := range positionIDs {
		var applicationID string
		if err := s.db.QueryRow(`SELECT id FROM applications WHERE position_id=?`, positionID).Scan(&applicationID); err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		} else if applicationID != "" {
			owners = append(owners, resourceOwnerRef{Type: domain.ResourceOwnerApplication, ID: applicationID})
			stageRows, stageErr := s.db.Query(`SELECT id FROM application_stages WHERE application_id=?`, applicationID)
			if stageErr != nil {
				return nil, stageErr
			}
			for stageRows.Next() {
				var value string
				if err := stageRows.Scan(&value); err != nil {
					stageRows.Close()
					return nil, err
				}
				owners = append(owners, resourceOwnerRef{Type: domain.ResourceOwnerStage, ID: value})
			}
			if err := stageRows.Close(); err != nil {
				return nil, err
			}
		}
	}
	return owners, nil
}

func (s *Store) resourceOwnersForPositionDeletion(owners []resourceOwnerRef, positionID string) ([]resourceOwnerRef, error) {
	var applicationID string
	err := s.db.QueryRow(`SELECT id FROM applications WHERE position_id=?`, positionID).Scan(&applicationID)
	if errors.Is(err, sql.ErrNoRows) {
		return owners, nil
	}
	if err != nil {
		return nil, err
	}
	owners = append(owners, resourceOwnerRef{Type: domain.ResourceOwnerApplication, ID: applicationID})
	rows, err := s.db.Query(`SELECT id FROM application_stages WHERE application_id=?`, applicationID)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			rows.Close()
			return nil, err
		}
		owners = append(owners, resourceOwnerRef{Type: domain.ResourceOwnerStage, ID: value})
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	return owners, nil
}
