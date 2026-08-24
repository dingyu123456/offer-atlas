package store

import (
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/dingyu/offer-atlas/internal/domain"
	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

const (
	dateLayout     = "2006-01-02"
	datetimeLayout = time.RFC3339Nano
)

type Store struct {
	db            *sql.DB
	path          string
	safetyDir     string
	maintenanceMu sync.Mutex
	mirrorMu      sync.Mutex
	safetyMu      sync.RWMutex
	safety        domain.SafetyStatus
	cloud         *cloudSync
}

func Open(path string) (*Store, error) {
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("database path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("create database directory: %w", err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err := db.Exec(`PRAGMA foreign_keys = ON; PRAGMA journal_mode = WAL; PRAGMA busy_timeout = 5000;`); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("configure database: %w", err)
	}
	if err := migrate(db); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate database: %w", err)
	}

	safetyDir := filepath.Join(filepath.Dir(path), "safety-mirror")
	store := &Store{
		db:        db,
		path:      path,
		safetyDir: safetyDir,
		safety:    domain.SafetyStatus{MirrorDirectory: safetyDir},
	}
	if err := store.migrateLegacyApplicationResumes(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate legacy application resumes: %w", err)
	}
	if err := store.refreshAllApplicationStatuses(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("refresh application statuses: %w", err)
	}
	store.syncSafetyMirror("startup", "")
	store.cloud = newCloudSync(store)
	store.cloud.start()
	return store, nil
}

func (s *Store) Close() error {
	if s.cloud != nil {
		s.cloud.close()
	}
	return s.db.Close()
}

func (s *Store) Health() (domain.Health, error) {
	var version int
	if err := s.db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return domain.Health{}, err
	}
	s.safetyMu.RLock()
	safety := s.safety
	s.safetyMu.RUnlock()
	return domain.Health{DatabasePath: s.path, SchemaVersion: version, Safety: safety}, nil
}

func (s *Store) ListCompanies() ([]domain.Company, error) {
	rows, err := s.db.Query(`SELECT id, name, industry, homepage, notes, created_at, updated_at FROM companies ORDER BY name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.Company, 0)
	for rows.Next() {
		item, err := scanCompany(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) DirectoryStats() (domain.DirectoryStats, error) {
	stats := domain.DirectoryStats{}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM companies`).Scan(&stats.CompanyCount); err != nil {
		return domain.DirectoryStats{}, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM campaigns`).Scan(&stats.CampaignCount); err != nil {
		return domain.DirectoryStats{}, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM positions`).Scan(&stats.PositionCount); err != nil {
		return domain.DirectoryStats{}, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM applications`).Scan(&stats.ApplicationCount); err != nil {
		return domain.DirectoryStats{}, err
	}
	return stats, nil
}

func (s *Store) GetCompanyDetail(companyID string) (domain.CompanyDetail, error) {
	company, err := s.companyByID(companyID)
	if err != nil {
		return domain.CompanyDetail{}, err
	}
	detail := domain.CompanyDetail{Company: company}
	if detail.Campaigns, err = s.ListCampaigns(companyID); err != nil {
		return domain.CompanyDetail{}, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM campaigns WHERE company_id=?`, companyID).Scan(&detail.CampaignCount); err != nil {
		return domain.CompanyDetail{}, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM positions p JOIN campaigns c ON c.id=p.campaign_id WHERE c.company_id=?`, companyID).Scan(&detail.PositionCount); err != nil {
		return domain.CompanyDetail{}, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM applications a JOIN positions p ON p.id=a.position_id JOIN campaigns c ON c.id=p.campaign_id WHERE c.company_id=?`, companyID).Scan(&detail.ApplicationCount); err != nil {
		return domain.CompanyDetail{}, err
	}
	if detail.Links, err = s.listResourceLinks(domain.ResourceOwnerCompany, companyID); err != nil {
		return domain.CompanyDetail{}, err
	}
	if detail.Attachments, err = s.listSupplementalAttachments(domain.ResourceOwnerCompany, companyID); err != nil {
		return domain.CompanyDetail{}, err
	}
	return detail, nil
}

func (s *Store) SaveCompany(input domain.CompanyInput) (domain.Company, error) {
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return domain.Company{}, errors.New("company name is required")
	}
	now, id := nowString(), input.ID
	if id == "" {
		id = uuid.NewString()
		if _, err := s.db.Exec(`INSERT INTO companies(id, name, industry, homepage, notes, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, id, name, strings.TrimSpace(input.Industry), strings.TrimSpace(input.Homepage), input.Notes, now, now); err != nil {
			return domain.Company{}, databaseError(err)
		}
	} else {
		result, err := s.db.Exec(`UPDATE companies SET name=?, industry=?, homepage=?, notes=?, updated_at=? WHERE id=?`, name, strings.TrimSpace(input.Industry), strings.TrimSpace(input.Homepage), input.Notes, now, id)
		if err != nil {
			return domain.Company{}, databaseError(err)
		}
		if affected, _ := result.RowsAffected(); affected == 0 {
			return domain.Company{}, fmt.Errorf("company %q was not found", id)
		}
	}
	item, err := s.companyByID(id)
	if err == nil {
		s.syncSafetyMirror("company.saved", id)
	}
	return item, err
}

func (s *Store) ListCampaigns(companyID string) ([]domain.Campaign, error) {
	query := `SELECT id, company_id, name, opens_on, closes_on, source_url, last_verified_on, process_overview, notes, created_at, updated_at FROM campaigns`
	args := []any{}
	if companyID = strings.TrimSpace(companyID); companyID != "" {
		query += ` WHERE company_id=?`
		args = append(args, companyID)
	}
	query += ` ORDER BY closes_on IS NULL, closes_on, name COLLATE NOCASE`
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.Campaign, 0)
	for rows.Next() {
		item, err := scanCampaign(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) GetCampaignDetail(campaignID string) (domain.CampaignDetail, error) {
	campaign, err := s.campaignByID(campaignID)
	if err != nil {
		return domain.CampaignDetail{}, err
	}
	company, err := s.companyByID(campaign.CompanyID)
	if err != nil {
		return domain.CampaignDetail{}, err
	}
	detail := domain.CampaignDetail{Campaign: campaign, Company: company}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM positions WHERE campaign_id=?`, campaignID).Scan(&detail.PositionCount); err != nil {
		return domain.CampaignDetail{}, err
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM applications a JOIN positions p ON p.id=a.position_id WHERE p.campaign_id=?`, campaignID).Scan(&detail.ApplicationCount); err != nil {
		return domain.CampaignDetail{}, err
	}
	if detail.Links, err = s.listResourceLinks(domain.ResourceOwnerCampaign, campaignID); err != nil {
		return domain.CampaignDetail{}, err
	}
	if detail.Attachments, err = s.listSupplementalAttachments(domain.ResourceOwnerCampaign, campaignID); err != nil {
		return domain.CampaignDetail{}, err
	}
	return detail, nil
}

func (s *Store) SaveCampaign(input domain.CampaignInput) (domain.Campaign, error) {
	if strings.TrimSpace(input.CompanyID) == "" {
		return domain.Campaign{}, errors.New("company is required")
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return domain.Campaign{}, errors.New("campaign name is required")
	}
	opensOn, err := parseOptionalDate(input.OpensOn)
	if err != nil {
		return domain.Campaign{}, fmt.Errorf("open date: %w", err)
	}
	closesOn, err := parseOptionalDate(input.ClosesOn)
	if err != nil {
		return domain.Campaign{}, fmt.Errorf("close date: %w", err)
	}
	verifiedOn, err := parseOptionalDate(input.LastVerifiedOn)
	if err != nil {
		return domain.Campaign{}, fmt.Errorf("last verified date: %w", err)
	}
	if opensOn != nil && closesOn != nil && opensOn.After(*closesOn) {
		return domain.Campaign{}, errors.New("close date must not be before open date")
	}
	now, id := nowString(), input.ID
	if id == "" {
		id = uuid.NewString()
		_, err = s.db.Exec(`INSERT INTO campaigns(id, company_id, name, opens_on, closes_on, source_url, last_verified_on, process_overview, notes, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, input.CompanyID, name, nullableDate(opensOn), nullableDate(closesOn), strings.TrimSpace(input.SourceURL), nullableDate(verifiedOn), input.ProcessOverview, input.Notes, now, now)
	} else {
		var result sql.Result
		result, err = s.db.Exec(`UPDATE campaigns SET company_id=?, name=?, opens_on=?, closes_on=?, source_url=?, last_verified_on=?, process_overview=?, notes=?, updated_at=? WHERE id=?`, input.CompanyID, name, nullableDate(opensOn), nullableDate(closesOn), strings.TrimSpace(input.SourceURL), nullableDate(verifiedOn), input.ProcessOverview, input.Notes, now, id)
		if err == nil {
			if affected, _ := result.RowsAffected(); affected == 0 {
				return domain.Campaign{}, fmt.Errorf("campaign %q was not found", id)
			}
		}
	}
	if err != nil {
		return domain.Campaign{}, databaseError(err)
	}
	item, err := s.campaignByID(id)
	if err == nil {
		s.syncSafetyMirror("campaign.saved", id)
	}
	return item, err
}

func (s *Store) ListPositions(filter domain.PositionFilter) ([]domain.PositionSummary, error) {
	where, args, err := positionSummaryWhere(filter)
	if err != nil {
		return nil, err
	}
	orderBy, err := positionSummaryOrderBy(filter.SortBy, filter.SortOrder)
	if err != nil {
		return nil, err
	}
	query := `SELECT * FROM (` + positionSummaryQuery + `) AS summary` + where + ` ORDER BY ` + orderBy
	return s.queryPositionSummaries(query, args...)
}

func (s *Store) ListPositionPage(filter domain.PositionFilter) (domain.PositionPage, error) {
	page := filter.Page
	if page < 1 {
		page = 1
	}
	pageSize := filter.PageSize
	if pageSize == 0 {
		pageSize = 20
	}
	if pageSize < 1 || pageSize > 100 {
		return domain.PositionPage{}, errors.New("page size must be between 1 and 100")
	}
	where, args, err := positionSummaryWhere(filter)
	if err != nil {
		return domain.PositionPage{}, err
	}
	orderBy, err := positionSummaryOrderBy(filter.SortBy, filter.SortOrder)
	if err != nil {
		return domain.PositionPage{}, err
	}
	countQuery := `SELECT COUNT(*) FROM (` + positionSummaryQuery + `) AS summary` + where
	var total int
	if err := s.db.QueryRow(countQuery, args...).Scan(&total); err != nil {
		return domain.PositionPage{}, err
	}
	if lastPage := max(1, (total+pageSize-1)/pageSize); page > lastPage {
		page = lastPage
	}
	queryArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
	items, err := s.queryPositionSummaries(`SELECT * FROM (`+positionSummaryQuery+`) AS summary`+where+` ORDER BY `+orderBy+` LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return domain.PositionPage{}, err
	}
	return domain.PositionPage{Items: items, Page: page, PageSize: pageSize, Total: total}, nil
}

func positionSummaryWhere(filter domain.PositionFilter) (string, []any, error) {
	where := ` WHERE 1=1`
	args := []any{}
	if status := strings.TrimSpace(filter.Status); status != "" && status != "all" {
		if !domain.ValidPositionStatus(domain.PositionStatus(status)) {
			return "", nil, fmt.Errorf("unknown position status %q", status)
		}
		where += ` AND position_status=?`
		args = append(args, status)
	}
	if search := strings.TrimSpace(filter.Query); search != "" {
		where += ` AND (title LIKE ? OR job_code LIKE ? OR company_name LIKE ? OR campaign_name LIKE ?)`
		like := "%" + search + "%"
		args = append(args, like, like, like, like)
	}
	return where, args, nil
}

func (s *Store) ListApplications(filter domain.ApplicationFilter) (domain.ApplicationPage, error) {
	page := filter.Page
	if page < 1 {
		page = 1
	}
	pageSize := filter.PageSize
	if pageSize == 0 {
		pageSize = 20
	}
	if pageSize < 1 || pageSize > 100 {
		return domain.ApplicationPage{}, errors.New("page size must be between 1 and 100")
	}
	orderBy, err := applicationOrderBy(filter.SortBy, filter.SortOrder)
	if err != nil {
		return domain.ApplicationPage{}, err
	}
	where := ` WHERE 1=1`
	args := []any{}
	if status := strings.TrimSpace(filter.Status); status != "" && status != "all" {
		if !domain.ValidApplicationStatus(domain.ApplicationStatus(status)) {
			return domain.ApplicationPage{}, fmt.Errorf("unknown application status %q", status)
		}
		where += ` AND a.status=?`
		args = append(args, status)
	}
	if search := strings.TrimSpace(filter.Query); search != "" {
		where += ` AND (c.name LIKE ? OR ca.name LIKE ? OR p.title LIKE ? OR p.job_code LIKE ?)`
		like := "%" + search + "%"
		args = append(args, like, like, like, like)
	}
	if resumeID := strings.TrimSpace(filter.ResumeID); resumeID != "" {
		if resumeID == "__none__" {
			where += ` AND COALESCE(a.resume_id, '')=''`
		} else {
			where += ` AND a.resume_id=?`
			args = append(args, resumeID)
		}
	}
	stageType := domain.StageType(strings.TrimSpace(filter.StageType))
	if stageType != "" {
		where += ` AND EXISTS (SELECT 1 FROM application_stages stage_filter WHERE stage_filter.application_id=a.id AND stage_filter.type=?)`
		args = append(args, stageType)
	}
	if stageStatus := strings.TrimSpace(filter.StageStatus); stageStatus != "" {
		if !domain.ValidStageStatus(domain.StageStatus(stageStatus)) {
			return domain.ApplicationPage{}, fmt.Errorf("unknown stage status %q", stageStatus)
		}
		if stageType != "" {
			where += ` AND EXISTS (SELECT 1 FROM application_stages stage_status_filter WHERE stage_status_filter.application_id=a.id AND stage_status_filter.type=? AND stage_status_filter.status=?)`
			args = append(args, stageType, stageStatus)
		} else {
			where += ` AND EXISTS (SELECT 1 FROM application_stages stage_status_filter WHERE stage_status_filter.application_id=a.id AND stage_status_filter.status=?)`
			args = append(args, stageStatus)
		}
	}
	countQuery := `SELECT COUNT(*) FROM applications a JOIN positions p ON p.id=a.position_id JOIN campaigns ca ON ca.id=p.campaign_id JOIN companies c ON c.id=ca.company_id` + where
	var total int
	if err := s.db.QueryRow(countQuery, args...).Scan(&total); err != nil {
		return domain.ApplicationPage{}, err
	}
	if lastPage := max(1, (total+pageSize-1)/pageSize); page > lastPage {
		page = lastPage
	}
	queryArgs := append(append([]any{}, args...), pageSize, (page-1)*pageSize)
	rows, err := s.db.Query(applicationSummaryQuery+where+` ORDER BY `+orderBy+` LIMIT ? OFFSET ?`, queryArgs...)
	if err != nil {
		return domain.ApplicationPage{}, err
	}
	items := make([]domain.ApplicationSummary, 0)
	for rows.Next() {
		item, err := scanApplicationSummary(rows)
		if err != nil {
			rows.Close()
			return domain.ApplicationPage{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return domain.ApplicationPage{}, err
	}
	if err := rows.Close(); err != nil {
		return domain.ApplicationPage{}, err
	}
	for index := range items {
		items[index].Stages, err = s.ListApplicationStages(items[index].ID)
		if err != nil {
			return domain.ApplicationPage{}, err
		}
	}
	return domain.ApplicationPage{Items: items, Page: page, PageSize: pageSize, Total: total}, nil
}

func (s *Store) GetApplicationDetail(applicationID string) (domain.ApplicationDetail, error) {
	application, err := s.applicationByID(applicationID)
	if err != nil {
		return domain.ApplicationDetail{}, err
	}
	position, err := s.positionByID(application.PositionID)
	if err != nil {
		return domain.ApplicationDetail{}, err
	}
	campaign, err := s.campaignByID(position.CampaignID)
	if err != nil {
		return domain.ApplicationDetail{}, err
	}
	company, err := s.companyByID(campaign.CompanyID)
	if err != nil {
		return domain.ApplicationDetail{}, err
	}
	stages, err := s.ListApplicationStages(applicationID)
	if err != nil {
		return domain.ApplicationDetail{}, err
	}
	detail := domain.ApplicationDetail{Application: application, Position: position, Company: company, Campaign: campaign, Stages: stages}
	if detail.Links, err = s.listResourceLinks(domain.ResourceOwnerApplication, applicationID); err != nil {
		return domain.ApplicationDetail{}, err
	}
	if detail.Attachments, err = s.listSupplementalAttachments(domain.ResourceOwnerApplication, applicationID); err != nil {
		return domain.ApplicationDetail{}, err
	}
	if application.ResumeID != "" {
		resume, resumeErr := s.resumeByID(application.ResumeID)
		if resumeErr != nil {
			return domain.ApplicationDetail{}, resumeErr
		}
		detail.Resume = &resume
	} else {
		legacyResume, legacyErr := s.applicationResumeByApplicationID(applicationID)
		if legacyErr == nil {
			detail.Resume = &domain.Resume{ID: legacyResume.ID, Name: application.ResumeName, OriginalName: legacyResume.OriginalName, StoredName: legacyResume.StoredName, MIMEType: legacyResume.MIMEType, SizeBytes: legacyResume.SizeBytes, CreatedAt: legacyResume.CreatedAt, UpdatedAt: legacyResume.CreatedAt}
		} else if !errors.Is(legacyErr, sql.ErrNoRows) {
			return domain.ApplicationDetail{}, legacyErr
		}
	}
	return detail, nil
}

func (s *Store) SavePosition(input domain.PositionInput) (domain.Position, error) {
	if strings.TrimSpace(input.CampaignID) == "" {
		return domain.Position{}, errors.New("campaign is required")
	}
	title := strings.TrimSpace(input.Title)
	if title == "" {
		return domain.Position{}, errors.New("position title is required")
	}
	priority := input.Priority
	if priority == 0 {
		priority = 3
	}
	if priority < 1 || priority > 5 {
		return domain.Position{}, errors.New("priority must be between 1 and 5")
	}
	now, id := nowString(), input.ID
	if id == "" {
		id = uuid.NewString()
		_, err := s.db.Exec(`INSERT INTO positions(id, campaign_id, title, job_code, department, location, track, source_url, priority, notes, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, input.CampaignID, title, strings.TrimSpace(input.JobCode), strings.TrimSpace(input.Department), strings.TrimSpace(input.Location), strings.TrimSpace(input.Track), strings.TrimSpace(input.SourceURL), priority, input.Notes, now, now)
		if err != nil {
			return domain.Position{}, databaseError(err)
		}
	} else {
		result, err := s.db.Exec(`UPDATE positions SET campaign_id=?, title=?, job_code=?, department=?, location=?, track=?, source_url=?, priority=?, notes=?, updated_at=? WHERE id=?`, input.CampaignID, title, strings.TrimSpace(input.JobCode), strings.TrimSpace(input.Department), strings.TrimSpace(input.Location), strings.TrimSpace(input.Track), strings.TrimSpace(input.SourceURL), priority, input.Notes, now, id)
		if err != nil {
			return domain.Position{}, databaseError(err)
		}
		if affected, _ := result.RowsAffected(); affected == 0 {
			return domain.Position{}, fmt.Errorf("position %q was not found", id)
		}
	}
	item, err := s.positionByID(id)
	if err == nil {
		s.syncSafetyMirror("position.saved", id)
	}
	return item, err
}

// QuickCapturePosition creates the company/campaign only when their exact
// names do not already exist, then records the opportunity in one transaction.
// Reused records are deliberately never overwritten by this shortcut.
func (s *Store) QuickCapturePosition(input domain.QuickCapturePositionInput) (domain.Position, error) {
	companyName := strings.TrimSpace(input.CompanyName)
	campaignName := strings.TrimSpace(input.CampaignName)
	title := strings.TrimSpace(input.Title)
	if companyName == "" || campaignName == "" || title == "" {
		return domain.Position{}, errors.New("company, campaign, and position title are required")
	}
	priority := input.Priority
	if priority == 0 {
		priority = 3
	}
	if priority < 1 || priority > 5 {
		return domain.Position{}, errors.New("priority must be between 1 and 5")
	}
	opensOn, err := parseOptionalDate(input.CampaignOpensOn)
	if err != nil {
		return domain.Position{}, fmt.Errorf("campaign open date: %w", err)
	}
	closesOn, err := parseOptionalDate(input.CampaignClosesOn)
	if err != nil {
		return domain.Position{}, fmt.Errorf("campaign close date: %w", err)
	}
	verifiedOn, err := parseOptionalDate(input.CampaignLastVerifiedOn)
	if err != nil {
		return domain.Position{}, fmt.Errorf("campaign last verified date: %w", err)
	}
	if opensOn != nil && closesOn != nil && opensOn.After(*closesOn) {
		return domain.Position{}, errors.New("campaign close date must not be before open date")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return domain.Position{}, err
	}
	defer tx.Rollback()
	now := nowString()
	companyID := ""
	err = tx.QueryRow(`SELECT id FROM companies WHERE name=? COLLATE NOCASE`, companyName).Scan(&companyID)
	if errors.Is(err, sql.ErrNoRows) {
		companyID = uuid.NewString()
		if _, err = tx.Exec(`INSERT INTO companies(id, name, industry, homepage, notes, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, companyID, companyName, strings.TrimSpace(input.CompanyIndustry), strings.TrimSpace(input.CompanyHomepage), input.CompanyNotes, now, now); err != nil {
			return domain.Position{}, databaseError(err)
		}
	} else if err != nil {
		return domain.Position{}, err
	}

	campaignID := ""
	err = tx.QueryRow(`SELECT id FROM campaigns WHERE company_id=? AND name=? COLLATE NOCASE`, companyID, campaignName).Scan(&campaignID)
	if errors.Is(err, sql.ErrNoRows) {
		campaignID = uuid.NewString()
		if _, err = tx.Exec(`INSERT INTO campaigns(id, company_id, name, opens_on, closes_on, source_url, last_verified_on, process_overview, notes, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, campaignID, companyID, campaignName, nullableDate(opensOn), nullableDate(closesOn), strings.TrimSpace(input.CampaignSourceURL), nullableDate(verifiedOn), input.CampaignProcessOverview, input.CampaignNotes, now, now); err != nil {
			return domain.Position{}, databaseError(err)
		}
	} else if err != nil {
		return domain.Position{}, err
	}

	positionID := uuid.NewString()
	if _, err = tx.Exec(`INSERT INTO positions(id, campaign_id, title, job_code, department, location, track, source_url, priority, notes, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, positionID, campaignID, title, strings.TrimSpace(input.JobCode), strings.TrimSpace(input.Department), strings.TrimSpace(input.Location), strings.TrimSpace(input.Track), strings.TrimSpace(input.SourceURL), priority, input.Notes, now, now); err != nil {
		return domain.Position{}, databaseError(err)
	}
	if err = tx.Commit(); err != nil {
		return domain.Position{}, err
	}
	item, err := s.positionByID(positionID)
	if err == nil {
		s.syncSafetyMirror("position.quick_captured", positionID)
	}
	return item, err
}

func (s *Store) PreviewDeletion(input domain.DeleteInput) (domain.DeletionPreview, error) {
	target, id, err := validateDeletionInput(input)
	if err != nil {
		return domain.DeletionPreview{}, err
	}
	name, err := deletionEntityName(s.db, target, id)
	if err != nil {
		return domain.DeletionPreview{}, err
	}
	preview := domain.DeletionPreview{EntityType: target, EntityName: name, ConfirmationText: name}

	switch target {
	case domain.DeletionTargetCompany:
		if preview.CampaignCount, err = countRows(s.db, `SELECT COUNT(*) FROM campaigns WHERE company_id=?`, id); err != nil {
			return domain.DeletionPreview{}, err
		}
		if preview.PositionCount, err = countRows(s.db, `SELECT COUNT(*) FROM positions p JOIN campaigns ca ON ca.id=p.campaign_id WHERE ca.company_id=?`, id); err != nil {
			return domain.DeletionPreview{}, err
		}
		if preview.ApplicationCount, err = countRows(s.db, `SELECT COUNT(*) FROM applications a JOIN positions p ON p.id=a.position_id JOIN campaigns ca ON ca.id=p.campaign_id WHERE ca.company_id=?`, id); err != nil {
			return domain.DeletionPreview{}, err
		}
		preview.StageCount, err = countRows(s.db, `SELECT COUNT(*) FROM application_stages stage JOIN applications a ON a.id=stage.application_id JOIN positions p ON p.id=a.position_id JOIN campaigns ca ON ca.id=p.campaign_id WHERE ca.company_id=?`, id)
	case domain.DeletionTargetCampaign:
		preview.CampaignCount = 1
		if preview.PositionCount, err = countRows(s.db, `SELECT COUNT(*) FROM positions WHERE campaign_id=?`, id); err != nil {
			return domain.DeletionPreview{}, err
		}
		if preview.ApplicationCount, err = countRows(s.db, `SELECT COUNT(*) FROM applications a JOIN positions p ON p.id=a.position_id WHERE p.campaign_id=?`, id); err != nil {
			return domain.DeletionPreview{}, err
		}
		preview.StageCount, err = countRows(s.db, `SELECT COUNT(*) FROM application_stages stage JOIN applications a ON a.id=stage.application_id JOIN positions p ON p.id=a.position_id WHERE p.campaign_id=?`, id)
	case domain.DeletionTargetPosition:
		preview.PositionCount = 1
		if preview.ApplicationCount, err = countRows(s.db, `SELECT COUNT(*) FROM applications WHERE position_id=?`, id); err != nil {
			return domain.DeletionPreview{}, err
		}
		preview.StageCount, err = countRows(s.db, `SELECT COUNT(*) FROM application_stages stage JOIN applications a ON a.id=stage.application_id WHERE a.position_id=?`, id)
	case domain.DeletionTargetApplication:
		preview.ApplicationCount = 1
		preview.StageCount, err = countRows(s.db, `SELECT COUNT(*) FROM application_stages WHERE application_id=?`, id)
	}
	if err != nil {
		return domain.DeletionPreview{}, err
	}
	return preview, nil
}

func (s *Store) DeleteEntity(input domain.DeleteInput) error {
	target, id, err := validateDeletionInput(input)
	if err != nil {
		return err
	}
	name, err := deletionEntityName(s.db, target, id)
	if err != nil {
		return err
	}
	if strings.TrimSpace(input.ConfirmationText) != name {
		return fmt.Errorf("type %q to confirm deletion", name)
	}
	positionIDs, err := s.positionIDsForDeletion(target, id)
	if err != nil {
		return err
	}
	applicationIDs, err := s.applicationIDsForDeletion(target, id)
	if err != nil {
		return err
	}
	resourceOwners, err := s.resourceOwnersForDeletion(target, id)
	if err != nil {
		return err
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.deleteResourcesForOwners(tx, resourceOwners); err != nil {
		return err
	}

	switch target {
	case domain.DeletionTargetCompany:
		_, err = tx.Exec(`DELETE FROM events WHERE campaign_id IN (SELECT id FROM campaigns WHERE company_id=?)`, id)
		if err == nil {
			_, err = tx.Exec(`DELETE FROM applications WHERE position_id IN (SELECT p.id FROM positions p JOIN campaigns ca ON ca.id=p.campaign_id WHERE ca.company_id=?)`, id)
		}
		if err == nil {
			_, err = tx.Exec(`DELETE FROM positions WHERE campaign_id IN (SELECT id FROM campaigns WHERE company_id=?)`, id)
		}
		if err == nil {
			_, err = tx.Exec(`DELETE FROM campaigns WHERE company_id=?`, id)
		}
		if err == nil {
			_, err = tx.Exec(`DELETE FROM companies WHERE id=?`, id)
		}
	case domain.DeletionTargetCampaign:
		_, err = tx.Exec(`DELETE FROM events WHERE campaign_id=?`, id)
		if err == nil {
			_, err = tx.Exec(`DELETE FROM applications WHERE position_id IN (SELECT id FROM positions WHERE campaign_id=?)`, id)
		}
		if err == nil {
			_, err = tx.Exec(`DELETE FROM positions WHERE campaign_id=?`, id)
		}
		if err == nil {
			_, err = tx.Exec(`DELETE FROM campaigns WHERE id=?`, id)
		}
	case domain.DeletionTargetPosition:
		_, err = tx.Exec(`DELETE FROM events WHERE application_id IN (SELECT id FROM applications WHERE position_id=?)`, id)
		if err == nil {
			_, err = tx.Exec(`DELETE FROM applications WHERE position_id=?`, id)
		}
		if err == nil {
			_, err = tx.Exec(`DELETE FROM positions WHERE id=?`, id)
		}
	case domain.DeletionTargetApplication:
		_, err = tx.Exec(`DELETE FROM events WHERE application_id=?`, id)
		if err == nil {
			_, err = tx.Exec(`DELETE FROM applications WHERE id=?`, id)
		}
	}
	if err != nil {
		return databaseError(err)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.cleanupPositionAttachmentFiles(positionIDs)
	s.cleanupApplicationResumeFiles(applicationIDs)
	s.cleanupSupplementalAttachmentFiles(resourceOwners)
	s.syncSafetyMirror(string(target)+".deleted", id)
	return nil
}

func validateDeletionInput(input domain.DeleteInput) (domain.DeletionTargetType, string, error) {
	target := input.EntityType
	if !domain.ValidDeletionTargetType(target) {
		return "", "", fmt.Errorf("unknown deletion target %q", target)
	}
	id := strings.TrimSpace(input.ID)
	if id == "" {
		return "", "", errors.New("record ID is required")
	}
	return target, id, nil
}

func deletionEntityName(queryer interface {
	QueryRow(query string, args ...any) *sql.Row
}, target domain.DeletionTargetType, id string) (string, error) {
	table, column := "", "name"
	switch target {
	case domain.DeletionTargetCompany:
		table = "companies"
	case domain.DeletionTargetCampaign:
		table = "campaigns"
	case domain.DeletionTargetPosition:
		table = "positions"
		column = "title"
	case domain.DeletionTargetApplication:
		var name string
		err := queryer.QueryRow(`SELECT p.title FROM applications a JOIN positions p ON p.id=a.position_id WHERE a.id=?`, id).Scan(&name)
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("%s %q was not found", target, id)
		}
		return name, err
	}
	var name string
	if err := queryer.QueryRow(`SELECT `+column+` FROM `+table+` WHERE id=?`, id).Scan(&name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("%s %q was not found", target, id)
		}
		return "", err
	}
	return name, nil
}

func countRows(queryer interface {
	QueryRow(query string, args ...any) *sql.Row
}, query string, args ...any) (int, error) {
	var count int
	if err := queryer.QueryRow(query, args...).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (s *Store) SaveApplication(input domain.ApplicationInput) (domain.Application, error) {
	if strings.TrimSpace(input.PositionID) == "" {
		return domain.Application{}, errors.New("position is required")
	}
	submittedOn, err := parseOptionalDate(input.SubmittedOn)
	if err != nil {
		return domain.Application{}, fmt.Errorf("submitted date: %w", err)
	}
	now, id := nowString(), input.ID
	if id == "" {
		id = uuid.NewString()
		resumeID, resumeName, resolveErr := s.resolveApplicationResumeInput(input.ResumeID, input.ResumeName)
		if resolveErr != nil {
			return domain.Application{}, resolveErr
		}
		_, err = s.db.Exec(`INSERT INTO applications(id, position_id, status, submitted_on, channel, resume_id, resume_name, notes, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, input.PositionID, domain.ApplicationActive, nullableDate(submittedOn), strings.TrimSpace(input.Channel), nullableString(resumeID), resumeName, input.Notes, now, now)
	} else {
		resumeID, resumeName, resolveErr := s.resolveApplicationResumeInput(input.ResumeID, input.ResumeName)
		if resolveErr != nil {
			return domain.Application{}, resolveErr
		}
		var result sql.Result
		result, err = s.db.Exec(`UPDATE applications SET position_id=?, submitted_on=?, channel=?, resume_id=?, resume_name=?, notes=?, updated_at=? WHERE id=?`, input.PositionID, nullableDate(submittedOn), strings.TrimSpace(input.Channel), nullableString(resumeID), resumeName, input.Notes, now, id)
		if err == nil {
			if affected, _ := result.RowsAffected(); affected == 0 {
				return domain.Application{}, fmt.Errorf("application %q was not found", id)
			}
		}
	}
	if err != nil {
		return domain.Application{}, databaseError(err)
	}
	if err := s.refreshApplicationStatus(id); err != nil {
		return domain.Application{}, err
	}
	item, err := s.applicationByID(id)
	if err == nil {
		s.syncSafetyMirror("application.saved", id)
	}
	return item, err
}

func (s *Store) resolveApplicationResumeInput(resumeID, fallbackName string) (string, string, error) {
	resumeID = strings.TrimSpace(resumeID)
	if resumeID == "" {
		return "", strings.TrimSpace(fallbackName), nil
	}
	resume, err := s.resumeByID(resumeID)
	if err != nil {
		return "", "", err
	}
	return resume.ID, resume.Name, nil
}

func (s *Store) ListApplicationStages(applicationID string) ([]domain.ApplicationStage, error) {
	rows, err := s.db.Query(stageSelect+` WHERE application_id=? ORDER BY sort_order`, applicationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanStages(rows)
	if err != nil {
		return nil, err
	}
	for index := range items {
		if items[index].Links, err = s.listResourceLinks(domain.ResourceOwnerStage, items[index].ID); err != nil {
			return nil, err
		}
		if items[index].Attachments, err = s.listSupplementalAttachments(domain.ResourceOwnerStage, items[index].ID); err != nil {
			return nil, err
		}
	}
	return items, nil
}

// ListScheduleItems expands stage appointments and result notifications into
// calendar occurrences. They remain linked to the original application stage.
func (s *Store) ListScheduleItems(filter domain.ScheduleFilter) ([]domain.ScheduleItem, error) {
	return s.listScheduleItems(filter, time.Now().UTC())
}

func (s *Store) listScheduleItems(filter domain.ScheduleFilter, now time.Time) ([]domain.ScheduleItem, error) {
	from, err := parseOptionalDate(filter.From)
	if err != nil {
		return nil, fmt.Errorf("schedule start: %w", err)
	}
	to, err := parseOptionalDate(filter.To)
	if err != nil {
		return nil, fmt.Errorf("schedule end: %w", err)
	}
	if from != nil && to != nil && !from.Before(*to) {
		return nil, errors.New("schedule end must be after start")
	}

	where := ` WHERE (stage.scheduled_start IS NOT NULL OR stage.result_at IS NOT NULL)`
	args := make([]any, 0, 4)
	if from != nil {
		value := datetimeString(*from)
		where += ` AND (stage.scheduled_start >= ? OR stage.result_at >= ?)`
		args = append(args, value, value)
	}
	if to != nil {
		value := datetimeString(*to)
		where += ` AND (stage.scheduled_start < ? OR stage.result_at < ?)`
		args = append(args, value, value)
	}
	rows, err := s.db.Query(scheduleStageSelect+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]domain.ScheduleItem, 0)
	for rows.Next() {
		stage, item, err := scanScheduleStage(rows)
		if err != nil {
			return nil, err
		}
		if domain.IsUnscheduledStageType(stage.Type) {
			continue
		}
		if stage.ScheduledStart != nil && scheduleWithinRange(*stage.ScheduledStart, from, to) {
			items = append(items, makeScheduleItem(stage, item, "stage", *stage.ScheduledStart, stage.ScheduledEnd, now))
		}
		if stage.ResultAt != nil && scheduleWithinRange(*stage.ResultAt, from, to) {
			items = append(items, makeScheduleItem(stage, item, "result", *stage.ResultAt, nil, now))
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].StartsAt.Equal(items[j].StartsAt) {
			return items[i].ID < items[j].ID
		}
		return items[i].StartsAt.Before(items[j].StartsAt)
	})
	return items, nil
}

func (s *Store) SaveApplicationStage(input domain.ApplicationStageInput) (domain.ApplicationStage, error) {
	if strings.TrimSpace(input.ApplicationID) == "" {
		return domain.ApplicationStage{}, errors.New("application is required")
	}
	content := strings.TrimSpace(input.Content)
	if content == "" {
		content = strings.TrimSpace(input.Name)
	}
	stageType := input.Type
	if stageType == "" {
		stageType = domain.StageFirstInterview
	}
	var typeExists int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM stage_types WHERE id=?`, stageType).Scan(&typeExists); err != nil {
		return domain.ApplicationStage{}, err
	}
	if typeExists == 0 {
		return domain.ApplicationStage{}, fmt.Errorf("unknown stage type %q", stageType)
	}
	stageStatus := input.Status
	if stageStatus == "" {
		stageStatus = domain.StageScheduled
	}
	if !domain.ValidStageStatus(stageStatus) {
		return domain.ApplicationStage{}, fmt.Errorf("unknown stage status %q", stageStatus)
	}
	start, err := parseOptionalDateTime(input.ScheduledStart)
	if err != nil {
		return domain.ApplicationStage{}, fmt.Errorf("scheduled start: %w", err)
	}
	end, err := parseOptionalDateTime(input.ScheduledEnd)
	if err != nil {
		return domain.ApplicationStage{}, fmt.Errorf("scheduled end: %w", err)
	}
	resultAt, err := parseOptionalDateTime(input.ResultAt)
	if err != nil {
		return domain.ApplicationStage{}, fmt.Errorf("result time: %w", err)
	}
	if start != nil && end != nil && end.Before(*start) {
		return domain.ApplicationStage{}, errors.New("scheduled end must not be before start")
	}
	if start != nil && end != nil && !sameInputCalendarDay(input.ScheduledStart, input.ScheduledEnd, *start, *end) {
		return domain.ApplicationStage{}, errors.New("scheduled start and end must be on the same day")
	}
	if domain.IsUnscheduledStageType(stageType) && (start != nil || end != nil || resultAt != nil) {
		return domain.ApplicationStage{}, errors.New("resume screening does not support scheduled or result notification times")
	}
	if err := validateResultTiming(resultAt, start, end); err != nil {
		return domain.ApplicationStage{}, err
	}
	if err := validateStageStatusTiming(stageStatus, start, end, time.Now().UTC()); err != nil {
		return domain.ApplicationStage{}, err
	}
	now, id := nowString(), input.ID
	if id == "" {
		id = uuid.NewString()
		order := input.SortOrder
		if order <= 0 {
			if err := s.db.QueryRow(`SELECT COALESCE(MAX(sort_order), 0) + 1 FROM application_stages WHERE application_id=?`, input.ApplicationID).Scan(&order); err != nil {
				return domain.ApplicationStage{}, err
			}
		}
		_, err = s.db.Exec(`INSERT INTO application_stages(id, application_id, sort_order, name, type, status, scheduled_start, scheduled_end, result_at, source_url, notes, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, id, input.ApplicationID, order, content, stageType, stageStatus, nullableDateTime(start), nullableDateTime(end), nullableDateTime(resultAt), strings.TrimSpace(input.SourceURL), input.Notes, now, now)
	} else {
		result, updateErr := s.db.Exec(`UPDATE application_stages SET name=?, type=?, status=?, scheduled_start=?, scheduled_end=?, result_at=?, source_url=?, notes=?, updated_at=? WHERE id=? AND application_id=?`, content, stageType, stageStatus, nullableDateTime(start), nullableDateTime(end), nullableDateTime(resultAt), strings.TrimSpace(input.SourceURL), input.Notes, now, id, input.ApplicationID)
		err = updateErr
		if err == nil {
			if affected, _ := result.RowsAffected(); affected == 0 {
				return domain.ApplicationStage{}, fmt.Errorf("application stage %q was not found", id)
			}
		}
	}
	if err != nil {
		return domain.ApplicationStage{}, databaseError(err)
	}
	if err := s.refreshApplicationStatus(input.ApplicationID); err != nil {
		return domain.ApplicationStage{}, err
	}
	item, err := s.stageByID(id)
	if err == nil {
		s.syncSafetyMirror("application_stage.saved", id)
	}
	return item, err
}

func (s *Store) ListStageTypes() ([]domain.StageTypeDefinition, error) {
	rows, err := s.db.Query(`SELECT id, name, created_at, updated_at FROM stage_types ORDER BY CASE id
		WHEN 'resume_screening' THEN 1 WHEN 'written_test' THEN 2 WHEN 'assessment' THEN 3 WHEN 'ai_interview' THEN 4
		WHEN 'first_interview' THEN 5 WHEN 'second_interview' THEN 6 WHEN 'third_interview' THEN 7
		WHEN 'fourth_interview' THEN 8 WHEN 'hr_interview' THEN 9 WHEN 'offer' THEN 10 ELSE 11 END, name COLLATE NOCASE`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.StageTypeDefinition, 0)
	for rows.Next() {
		var item domain.StageTypeDefinition
		var createdAt, updatedAt string
		if err := rows.Scan(&item.ID, &item.Name, &createdAt, &updatedAt); err != nil {
			return nil, err
		}
		var err error
		if item.CreatedAt, err = parseStoredDateTime(createdAt); err != nil {
			return nil, err
		}
		if item.UpdatedAt, err = parseStoredDateTime(updatedAt); err != nil {
			return nil, err
		}
		item.System = domain.IsSystemStageType(item.ID)
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) SaveStageType(input domain.StageTypeInput) (domain.StageTypeDefinition, error) {
	if domain.IsSystemStageType(input.ID) {
		return domain.StageTypeDefinition{}, errors.New("系统节点类型用于统一统计，不能修改")
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return domain.StageTypeDefinition{}, errors.New("stage type name is required")
	}
	now := nowString()
	id := input.ID
	if id == "" {
		id = domain.StageType(uuid.NewString())
		if _, err := s.db.Exec(`INSERT INTO stage_types(id, name, created_at, updated_at) VALUES (?, ?, ?, ?)`, id, name, now, now); err != nil {
			return domain.StageTypeDefinition{}, databaseError(err)
		}
	} else {
		result, err := s.db.Exec(`UPDATE stage_types SET name=?, updated_at=? WHERE id=?`, name, now, id)
		if err != nil {
			return domain.StageTypeDefinition{}, databaseError(err)
		}
		if affected, _ := result.RowsAffected(); affected == 0 {
			return domain.StageTypeDefinition{}, fmt.Errorf("stage type %q was not found", id)
		}
	}
	var item domain.StageTypeDefinition
	var createdAt, updatedAt string
	if err := s.db.QueryRow(`SELECT id, name, created_at, updated_at FROM stage_types WHERE id=?`, id).Scan(&item.ID, &item.Name, &createdAt, &updatedAt); err != nil {
		return domain.StageTypeDefinition{}, err
	}
	var err error
	if item.CreatedAt, err = parseStoredDateTime(createdAt); err != nil {
		return domain.StageTypeDefinition{}, err
	}
	if item.UpdatedAt, err = parseStoredDateTime(updatedAt); err != nil {
		return domain.StageTypeDefinition{}, err
	}
	item.System = false
	s.syncSafetyMirror("stage_type.saved", string(id))
	return item, nil
}

func (s *Store) DeleteStageType(id domain.StageType) error {
	if domain.IsSystemStageType(id) {
		return errors.New("系统节点类型用于统一统计，不能删除")
	}
	var name string
	if err := s.db.QueryRow(`SELECT name FROM stage_types WHERE id=?`, id).Scan(&name); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("stage type %q was not found", id)
		}
		return err
	}
	var usage int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM application_stages WHERE type=?`, id).Scan(&usage); err != nil {
		return err
	}
	if usage > 0 {
		return fmt.Errorf("节点类型“%s”正在被 %d 个流程节点使用，请先修改这些节点", name, usage)
	}
	if _, err := s.db.Exec(`DELETE FROM stage_types WHERE id=?`, id); err != nil {
		return err
	}
	s.syncSafetyMirror("stage_type.deleted", string(id))
	return nil
}

func (s *Store) DeleteApplicationStage(id string) error {
	var applicationID string
	if err := s.db.QueryRow(`SELECT application_id FROM application_stages WHERE id=?`, id).Scan(&applicationID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("application stage %q was not found", id)
		}
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := s.deleteResourcesForOwners(tx, []resourceOwnerRef{{Type: domain.ResourceOwnerStage, ID: id}}); err != nil {
		return err
	}
	result, err := tx.Exec(`DELETE FROM application_stages WHERE id=?`, id)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return fmt.Errorf("application stage %q was not found", id)
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	s.cleanupSupplementalAttachmentFiles([]resourceOwnerRef{{Type: domain.ResourceOwnerStage, ID: id}})
	if err := s.refreshApplicationStatus(applicationID); err != nil {
		return err
	}
	s.syncSafetyMirror("application_stage.deleted", id)
	return nil
}

func (s *Store) ReorderApplicationStages(applicationID string, stageIDs []string) ([]domain.ApplicationStage, error) {
	if strings.TrimSpace(applicationID) == "" {
		return nil, errors.New("application is required")
	}
	stages, err := s.ListApplicationStages(applicationID)
	if err != nil {
		return nil, err
	}
	if len(stages) != len(stageIDs) {
		return nil, errors.New("the reordered stage list is incomplete")
	}
	known := make(map[string]bool, len(stages))
	for _, stage := range stages {
		known[stage.ID] = true
	}
	for _, id := range stageIDs {
		if !known[id] {
			return nil, errors.New("a stage does not belong to this application")
		}
		delete(known, id)
	}
	if len(known) != 0 {
		return nil, errors.New("the reordered stage list contains duplicates")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE application_stages SET sort_order = sort_order + 10000 WHERE application_id=?`, applicationID); err != nil {
		return nil, err
	}
	for index, id := range stageIDs {
		if _, err := tx.Exec(`UPDATE application_stages SET sort_order=?, updated_at=? WHERE id=?`, index+1, nowString(), id); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	if err := s.refreshApplicationStatus(applicationID); err != nil {
		return nil, err
	}
	s.syncSafetyMirror("application_stage.reordered", applicationID)
	return s.ListApplicationStages(applicationID)
}

func (s *Store) refreshAllApplicationStatuses() error {
	rows, err := s.db.Query(`SELECT id FROM applications`)
	if err != nil {
		return err
	}
	ids := make([]string, 0)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, id := range ids {
		if err := s.refreshApplicationStatus(id); err != nil {
			return err
		}
	}
	return nil
}

// The last manually ordered process node is the source of truth for an
// application's summary state. Passing an interview means the application is
// still in progress; only a failed latest node or a passed Offer closes it.
func (s *Store) refreshApplicationStatus(applicationID string) error {
	var stageType domain.StageType
	var stageStatus domain.StageStatus
	err := s.db.QueryRow(`
		SELECT type, status FROM application_stages
		WHERE application_id=? ORDER BY sort_order DESC LIMIT 1
	`, applicationID).Scan(&stageType, &stageStatus)
	status := domain.ApplicationActive
	switch {
	case errors.Is(err, sql.ErrNoRows):
	case err != nil:
		return err
	case stageStatus == domain.StageFailed:
		status = domain.ApplicationRejected
	case stageType == domain.StageOffer && stageStatus == domain.StagePassed:
		status = domain.ApplicationOffer
	}
	// This runs during startup and after a remote stage import. Do not turn a
	// read-time status reconciliation into a business edit: updated_at is part
	// of the sync snapshot, so an unconditional write would create a fake
	// application change and potentially a cross-device conflict.
	result, err := s.db.Exec(`UPDATE applications SET status=?, updated_at=? WHERE id=? AND status<>?`, status, nowString(), applicationID, status)
	if err != nil {
		return err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		var exists int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM applications WHERE id=?`, applicationID).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return fmt.Errorf("application %q was not found", applicationID)
		}
	}
	return nil
}

func (s *Store) GetPositionDetail(positionID string) (domain.PositionDetail, error) {
	position, err := s.positionByID(positionID)
	if err != nil {
		return domain.PositionDetail{}, err
	}
	campaign, err := s.campaignByID(position.CampaignID)
	if err != nil {
		return domain.PositionDetail{}, err
	}
	company, err := s.companyByID(campaign.CompanyID)
	if err != nil {
		return domain.PositionDetail{}, err
	}
	attachments, err := s.listPositionAttachments(positionID)
	if err != nil {
		return domain.PositionDetail{}, err
	}
	detail := domain.PositionDetail{Position: position, Status: domain.PositionUnapplied, Campaign: campaign, Company: company, Attachments: attachments}
	if detail.Links, err = s.listResourceLinks(domain.ResourceOwnerPosition, positionID); err != nil {
		return domain.PositionDetail{}, err
	}

	application, err := s.applicationByPositionID(positionID)
	if err == sql.ErrNoRows {
		detail.Stages = []domain.ApplicationStage{}
		return detail, nil
	}
	if err != nil {
		return domain.PositionDetail{}, err
	}
	detail.Application = &application
	detail.Status = domain.PositionApplied
	detail.Stages, err = s.ListApplicationStages(application.ID)
	if err != nil {
		return domain.PositionDetail{}, err
	}
	if application.ResumeID != "" {
		resume, resumeErr := s.resumeByID(application.ResumeID)
		if resumeErr != nil {
			return domain.PositionDetail{}, resumeErr
		}
		detail.Resume = &resume
	} else {
		legacyResume, legacyErr := s.applicationResumeByApplicationID(application.ID)
		if legacyErr == nil {
			detail.Resume = &domain.Resume{ID: legacyResume.ID, Name: application.ResumeName, OriginalName: legacyResume.OriginalName, StoredName: legacyResume.StoredName, MIMEType: legacyResume.MIMEType, SizeBytes: legacyResume.SizeBytes, CreatedAt: legacyResume.CreatedAt, UpdatedAt: legacyResume.CreatedAt}
		} else if !errors.Is(legacyErr, sql.ErrNoRows) {
			return domain.PositionDetail{}, legacyErr
		}
	}
	return detail, nil
}

func (s *Store) Dashboard(now time.Time) (domain.Dashboard, error) {
	allScheduleItems, err := s.listScheduleItems(domain.ScheduleFilter{}, now)
	if err != nil {
		return domain.Dashboard{}, err
	}
	dashboard := domain.Dashboard{}
	for _, item := range allScheduleItems {
		if !item.IsCompleted {
			dashboard.TodoCount++
		}
	}
	if err := s.db.QueryRow(`
		SELECT COUNT(*),
		       COALESCE(SUM(CASE WHEN status='active' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN status='offer' THEN 1 ELSE 0 END), 0),
		       COALESCE(SUM(CASE WHEN status='rejected' THEN 1 ELSE 0 END), 0)
		FROM applications
	`).Scan(&dashboard.TotalApplications, &dashboard.ActiveApplications, &dashboard.OfferApplications, &dashboard.RejectedApplications); err != nil {
		return domain.Dashboard{}, err
	}
	if dashboard.WrittenTestStats, err = s.stageProgressStats(domain.StageWrittenTest); err != nil {
		return domain.Dashboard{}, err
	}
	if dashboard.ResumeScreeningStats, err = s.stageProgressStats(domain.StageResumeScreening); err != nil {
		return domain.Dashboard{}, err
	}
	if dashboard.AssessmentStats, err = s.stageProgressStats(domain.StageAssessment); err != nil {
		return domain.Dashboard{}, err
	}
	if err := s.db.QueryRow(`
		SELECT COUNT(DISTINCT application_id) FROM application_stages
		WHERE type IN (?, ?, ?, ?, ?, ?)
	`, domain.StageAIInterview, domain.StageFirstInterview, domain.StageSecondInterview, domain.StageThirdInterview, domain.StageFourthInterview, domain.StageHRInterview).Scan(&dashboard.InterviewedApplications); err != nil {
		return domain.Dashboard{}, err
	}
	interviewTypes := []domain.StageType{
		domain.StageAIInterview, domain.StageFirstInterview, domain.StageSecondInterview,
		domain.StageThirdInterview, domain.StageFourthInterview, domain.StageHRInterview,
	}
	dashboard.InterviewStats = make([]domain.StageProgressStats, 0, len(interviewTypes))
	for _, stageType := range interviewTypes {
		stats, statsErr := s.stageProgressStats(stageType)
		if statsErr != nil {
			return domain.Dashboard{}, statsErr
		}
		dashboard.InterviewStats = append(dashboard.InterviewStats, stats)
	}
	return dashboard, nil
}

func (s *Store) stageProgressStats(stageType domain.StageType) (domain.StageProgressStats, error) {
	stats := domain.StageProgressStats{Type: stageType}
	err := s.db.QueryRow(`
		SELECT COUNT(DISTINCT application_id),
		       COUNT(DISTINCT CASE WHEN status='passed' THEN application_id END),
		       COUNT(DISTINCT CASE WHEN status='failed' THEN application_id END)
		FROM application_stages WHERE type=?
	`, stageType).Scan(&stats.Entered, &stats.Passed, &stats.Failed)
	return stats, err
}

func (s *Store) CreateBackup() (string, error) {
	s.maintenanceMu.Lock()
	defer s.maintenanceMu.Unlock()
	return s.createBackup()
}

func (s *Store) createBackup() (string, error) {
	if _, err := s.db.Exec(`PRAGMA wal_checkpoint(FULL)`); err != nil {
		return "", fmt.Errorf("checkpoint database: %w", err)
	}
	refs, err := s.attachmentFileRefs(s.db)
	if err != nil {
		return "", err
	}
	backupDir := filepath.Join(filepath.Dir(s.path), "backups")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return "", err
	}
	pending, err := os.MkdirTemp(backupDir, ".pending-")
	if err != nil {
		return "", err
	}
	completed := false
	defer func() {
		if !completed {
			_ = os.RemoveAll(pending)
		}
	}()
	if _, err := s.db.Exec(`VACUUM INTO ?`, filepath.Join(pending, "offer-atlas.db")); err != nil {
		return "", fmt.Errorf("write SQLite backup: %w", err)
	}
	if err := s.copyCurrentAttachmentFiles(pending, refs); err != nil {
		return "", err
	}
	destination := filepath.Join(backupDir, "offer-atlas-"+time.Now().Format("20060102-150405")+"-"+uuid.NewString()[:8])
	if err := os.Rename(pending, destination); err != nil {
		return "", err
	}
	completed = true
	return filepath.Join(destination, "offer-atlas.db"), nil
}

const positionSummaryQuery = `
	SELECT
		p.id, p.campaign_id, p.title, p.job_code, p.department, p.location, p.track, p.source_url, p.priority, p.notes, p.created_at, p.updated_at,
		c.name AS company_name, ca.name AS campaign_name,
		CASE WHEN a.id IS NULL THEN 'unapplied' ELSE 'applied' END AS position_status,
		COALESCE(a.id, ''), COALESCE(a.status, ''), COALESCE(a.submitted_on, '') AS submitted_on,
		COALESCE(current_stage.name, ''), COALESCE(current_stage.type, ''), COALESCE(current_stage.status, ''), COALESCE(stage_totals.stage_count, 0)
	FROM positions p
	JOIN campaigns ca ON ca.id=p.campaign_id
	JOIN companies c ON c.id=ca.company_id
	LEFT JOIN applications a ON a.position_id=p.id
	LEFT JOIN application_stages current_stage ON current_stage.id=(
		SELECT stage.id FROM application_stages stage
		WHERE stage.application_id=a.id
		ORDER BY stage.sort_order DESC LIMIT 1
	)
	LEFT JOIN (
		SELECT application_id, COUNT(*) AS stage_count FROM application_stages GROUP BY application_id
	) stage_totals ON stage_totals.application_id=a.id`

const applicationSummaryQuery = `
	SELECT
		a.id, a.position_id, a.status, a.submitted_on, a.channel, COALESCE(a.resume_id, ''), a.resume_name, a.notes, a.created_at, a.updated_at,
		c.name, ca.name, p.title, p.priority,
		COALESCE(current_stage.name, ''), COALESCE(current_stage.type, ''), COALESCE(current_stage.status, ''), COALESCE(stage_totals.stage_count, 0)
	FROM applications a
	JOIN positions p ON p.id=a.position_id
	JOIN campaigns ca ON ca.id=p.campaign_id
	JOIN companies c ON c.id=ca.company_id
	LEFT JOIN application_stages current_stage ON current_stage.id=(
		SELECT stage.id FROM application_stages stage
		WHERE stage.application_id=a.id
		ORDER BY stage.sort_order DESC LIMIT 1
	)
	LEFT JOIN (
		SELECT application_id, COUNT(*) AS stage_count FROM application_stages GROUP BY application_id
	) stage_totals ON stage_totals.application_id=a.id`

const stageSelect = `
	SELECT id, application_id, sort_order, name, type, status, scheduled_start, scheduled_end, result_at, source_url, notes, created_at, updated_at
	FROM application_stages`

const scheduleStageSelect = `
	SELECT stage.id, stage.application_id, stage.sort_order, stage.name, stage.type, stage.status,
	       stage.scheduled_start, stage.scheduled_end, stage.result_at, stage.source_url, stage.notes, stage.created_at, stage.updated_at,
	       c.name, ca.name, p.title, p.id, a.status
	FROM application_stages stage
	JOIN applications a ON a.id=stage.application_id
	JOIN positions p ON p.id=a.position_id
	JOIN campaigns ca ON ca.id=p.campaign_id
	JOIN companies c ON c.id=ca.company_id`

func (s *Store) queryPositionSummaries(query string, args ...any) ([]domain.PositionSummary, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]domain.PositionSummary, 0)
	for rows.Next() {
		item, err := scanPositionSummary(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (s *Store) companyByID(id string) (domain.Company, error) {
	return scanCompany(s.db.QueryRow(`SELECT id, name, industry, homepage, notes, created_at, updated_at FROM companies WHERE id=?`, id))
}

func (s *Store) campaignByID(id string) (domain.Campaign, error) {
	return scanCampaign(s.db.QueryRow(`SELECT id, company_id, name, opens_on, closes_on, source_url, last_verified_on, process_overview, notes, created_at, updated_at FROM campaigns WHERE id=?`, id))
}

func (s *Store) positionByID(id string) (domain.Position, error) {
	var item domain.Position
	var createdAt, updatedAt string
	err := s.db.QueryRow(`SELECT id, campaign_id, title, job_code, department, location, track, source_url, priority, notes, created_at, updated_at FROM positions WHERE id=?`, id).Scan(&item.ID, &item.CampaignID, &item.Title, &item.JobCode, &item.Department, &item.Location, &item.Track, &item.SourceURL, &item.Priority, &item.Notes, &createdAt, &updatedAt)
	if err != nil {
		return domain.Position{}, err
	}
	item.CreatedAt, err = parseStoredDateTime(createdAt)
	if err != nil {
		return domain.Position{}, err
	}
	item.UpdatedAt, err = parseStoredDateTime(updatedAt)
	return item, err
}

func (s *Store) applicationByID(id string) (domain.Application, error) {
	return scanApplication(s.db.QueryRow(`SELECT id, position_id, status, submitted_on, channel, COALESCE(resume_id, ''), resume_name, notes, created_at, updated_at FROM applications WHERE id=?`, id))
}

func (s *Store) applicationByPositionID(positionID string) (domain.Application, error) {
	return scanApplication(s.db.QueryRow(`SELECT id, position_id, status, submitted_on, channel, COALESCE(resume_id, ''), resume_name, notes, created_at, updated_at FROM applications WHERE position_id=?`, positionID))
}

func (s *Store) stageByID(id string) (domain.ApplicationStage, error) {
	return scanStage(s.db.QueryRow(stageSelect+` WHERE id=?`, id))
}

type rowScanner interface{ Scan(dest ...any) error }

func scanCompany(row rowScanner) (domain.Company, error) {
	var item domain.Company
	var createdAt, updatedAt string
	err := row.Scan(&item.ID, &item.Name, &item.Industry, &item.Homepage, &item.Notes, &createdAt, &updatedAt)
	if err != nil {
		return domain.Company{}, err
	}
	item.CreatedAt, err = parseStoredDateTime(createdAt)
	if err != nil {
		return domain.Company{}, err
	}
	item.UpdatedAt, err = parseStoredDateTime(updatedAt)
	return item, err
}

func scanCampaign(row rowScanner) (domain.Campaign, error) {
	var item domain.Campaign
	var opens, closes, verified sql.NullString
	var createdAt, updatedAt string
	err := row.Scan(&item.ID, &item.CompanyID, &item.Name, &opens, &closes, &item.SourceURL, &verified, &item.ProcessOverview, &item.Notes, &createdAt, &updatedAt)
	if err != nil {
		return domain.Campaign{}, err
	}
	if item.OpensOn, err = parseNullableStoredDate(opens); err != nil {
		return domain.Campaign{}, err
	}
	if item.ClosesOn, err = parseNullableStoredDate(closes); err != nil {
		return domain.Campaign{}, err
	}
	if item.LastVerifiedOn, err = parseNullableStoredDate(verified); err != nil {
		return domain.Campaign{}, err
	}
	item.CreatedAt, err = parseStoredDateTime(createdAt)
	if err != nil {
		return domain.Campaign{}, err
	}
	item.UpdatedAt, err = parseStoredDateTime(updatedAt)
	return item, err
}

func scanApplication(row rowScanner) (domain.Application, error) {
	var item domain.Application
	var submitted sql.NullString
	var createdAt, updatedAt string
	err := row.Scan(&item.ID, &item.PositionID, &item.Status, &submitted, &item.Channel, &item.ResumeID, &item.ResumeName, &item.Notes, &createdAt, &updatedAt)
	if err != nil {
		return domain.Application{}, err
	}
	if item.SubmittedOn, err = parseNullableStoredDate(submitted); err != nil {
		return domain.Application{}, err
	}
	item.CreatedAt, err = parseStoredDateTime(createdAt)
	if err != nil {
		return domain.Application{}, err
	}
	item.UpdatedAt, err = parseStoredDateTime(updatedAt)
	return item, err
}

func scanPositionSummary(row rowScanner) (domain.PositionSummary, error) {
	var item domain.PositionSummary
	var createdAt, updatedAt string
	err := row.Scan(&item.ID, &item.CampaignID, &item.Title, &item.JobCode, &item.Department, &item.Location, &item.Track, &item.SourceURL, &item.Priority, &item.Notes, &createdAt, &updatedAt, &item.CompanyName, &item.CampaignName, &item.Status, &item.ApplicationID, &item.ApplicationStatus, &item.SubmittedOn, &item.CurrentStageName, &item.CurrentStageType, &item.CurrentStageStatus, &item.StageCount)
	if err != nil {
		return domain.PositionSummary{}, err
	}
	item.CreatedAt, err = parseStoredDateTime(createdAt)
	if err != nil {
		return domain.PositionSummary{}, err
	}
	item.UpdatedAt, err = parseStoredDateTime(updatedAt)
	return item, err
}

func scanApplicationSummary(row rowScanner) (domain.ApplicationSummary, error) {
	var item domain.ApplicationSummary
	var submitted sql.NullString
	var createdAt, updatedAt string
	err := row.Scan(&item.ID, &item.PositionID, &item.Status, &submitted, &item.Channel, &item.ResumeID, &item.ResumeName, &item.Notes, &createdAt, &updatedAt, &item.CompanyName, &item.CampaignName, &item.PositionTitle, &item.PositionPriority, &item.CurrentStageName, &item.CurrentStageType, &item.CurrentStageStatus, &item.StageCount)
	if err != nil {
		return domain.ApplicationSummary{}, err
	}
	if item.SubmittedOn, err = parseNullableStoredDate(submitted); err != nil {
		return domain.ApplicationSummary{}, err
	}
	item.CreatedAt, err = parseStoredDateTime(createdAt)
	if err != nil {
		return domain.ApplicationSummary{}, err
	}
	item.UpdatedAt, err = parseStoredDateTime(updatedAt)
	return item, err
}

func scanStage(row rowScanner) (domain.ApplicationStage, error) {
	var item domain.ApplicationStage
	var start, end, result sql.NullString
	var createdAt, updatedAt string
	err := row.Scan(&item.ID, &item.ApplicationID, &item.SortOrder, &item.Content, &item.Type, &item.Status, &start, &end, &result, &item.SourceURL, &item.Notes, &createdAt, &updatedAt)
	if err != nil {
		return domain.ApplicationStage{}, err
	}
	if item.ScheduledStart, err = parseNullableStoredDateTime(start); err != nil {
		return domain.ApplicationStage{}, err
	}
	if item.ScheduledEnd, err = parseNullableStoredDateTime(end); err != nil {
		return domain.ApplicationStage{}, err
	}
	if item.ResultAt, err = parseNullableStoredDateTime(result); err != nil {
		return domain.ApplicationStage{}, err
	}
	item.CreatedAt, err = parseStoredDateTime(createdAt)
	if err != nil {
		return domain.ApplicationStage{}, err
	}
	item.UpdatedAt, err = parseStoredDateTime(updatedAt)
	item.Name = item.Content
	return item, err
}

func scanStages(rows *sql.Rows) ([]domain.ApplicationStage, error) {
	items := make([]domain.ApplicationStage, 0)
	for rows.Next() {
		item, err := scanStage(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

type scheduleStageMeta struct {
	companyName       string
	campaignName      string
	positionTitle     string
	positionID        string
	applicationStatus domain.ApplicationStatus
}

func scanScheduleStage(row rowScanner) (domain.ApplicationStage, scheduleStageMeta, error) {
	var stage domain.ApplicationStage
	var meta scheduleStageMeta
	var start, end, result sql.NullString
	var createdAt, updatedAt string
	err := row.Scan(
		&stage.ID, &stage.ApplicationID, &stage.SortOrder, &stage.Content, &stage.Type, &stage.Status,
		&start, &end, &result, &stage.SourceURL, &stage.Notes, &createdAt, &updatedAt,
		&meta.companyName, &meta.campaignName, &meta.positionTitle, &meta.positionID, &meta.applicationStatus,
	)
	if err != nil {
		return domain.ApplicationStage{}, scheduleStageMeta{}, err
	}
	if stage.ScheduledStart, err = parseNullableStoredDateTime(start); err != nil {
		return domain.ApplicationStage{}, scheduleStageMeta{}, err
	}
	if stage.ScheduledEnd, err = parseNullableStoredDateTime(end); err != nil {
		return domain.ApplicationStage{}, scheduleStageMeta{}, err
	}
	if stage.ResultAt, err = parseNullableStoredDateTime(result); err != nil {
		return domain.ApplicationStage{}, scheduleStageMeta{}, err
	}
	if stage.CreatedAt, err = parseStoredDateTime(createdAt); err != nil {
		return domain.ApplicationStage{}, scheduleStageMeta{}, err
	}
	if stage.UpdatedAt, err = parseStoredDateTime(updatedAt); err != nil {
		return domain.ApplicationStage{}, scheduleStageMeta{}, err
	}
	stage.Name = stage.Content
	return stage, meta, nil
}

func scheduleWithinRange(value time.Time, from, to *time.Time) bool {
	return (from == nil || !value.Before(*from)) && (to == nil || value.Before(*to))
}

func stageIsTerminal(status domain.StageStatus) bool {
	switch status {
	case domain.StagePassed, domain.StageFailed:
		return true
	default:
		return false
	}
}

func stageAppointmentCompleted(status domain.StageStatus) bool {
	return stageIsTerminal(status)
}

// A scheduled node with a passed appointment and an explicit future result
// notification is waiting for its outcome. This is derived from time rather
// than persisted as a fourth node state.
func stageWaitingForResult(stage domain.ApplicationStage, now time.Time) bool {
	if stage.Status != domain.StageScheduled || stage.ResultAt == nil || stage.ScheduledStart == nil {
		return false
	}
	cutoff := *stage.ScheduledStart
	if stage.ScheduledEnd != nil {
		cutoff = *stage.ScheduledEnd
	}
	return !cutoff.After(now)
}

// A recorded outcome must not predate a scheduled appointment. This keeps a
// future interview in the actionable "scheduled" state until it can happen.
func validateStageStatusTiming(status domain.StageStatus, start, end *time.Time, now time.Time) error {
	if start == nil {
		return nil
	}
	if !stageIsTerminal(status) {
		return nil
	}
	cutoff := *start
	if end != nil {
		cutoff = *end
	}
	if cutoff.After(now) {
		return errors.New("a future appointment cannot be marked as passed or failed")
	}
	return nil
}

func validateResultTiming(resultAt, start, end *time.Time) error {
	if resultAt == nil {
		return nil
	}
	cutoff := start
	if end != nil {
		cutoff = end
	}
	if cutoff != nil && resultAt.Before(*cutoff) {
		return errors.New("result time must not be before the scheduled appointment ends")
	}
	return nil
}

func sortDirection(value string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "desc":
		return "DESC", nil
	case "asc":
		return "ASC", nil
	default:
		return "", fmt.Errorf("unknown sort order %q", value)
	}
}

func positionSummaryOrderBy(sortBy, sortOrder string) (string, error) {
	direction, err := sortDirection(sortOrder)
	if err != nil {
		return "", err
	}
	switch strings.ToLower(strings.TrimSpace(sortBy)) {
	case "", "priority":
		return `priority ` + direction + `, updated_at DESC, company_name COLLATE NOCASE`, nil
	case "submitted_on":
		// Positions without an application have no submission date and always
		// follow dated positions, regardless of the selected direction.
		return `CASE WHEN submitted_on='' THEN 1 ELSE 0 END ASC, submitted_on ` + direction + `, updated_at DESC, company_name COLLATE NOCASE`, nil
	default:
		return "", fmt.Errorf("unknown position sort field %q", sortBy)
	}
}

func applicationOrderBy(sortBy, sortOrder string) (string, error) {
	direction, err := sortDirection(sortOrder)
	if err != nil {
		return "", err
	}
	switch strings.ToLower(strings.TrimSpace(sortBy)) {
	case "", "priority":
		return `p.priority ` + direction + `, a.updated_at DESC, a.created_at DESC`, nil
	case "submitted_on":
		return `CASE WHEN a.submitted_on IS NULL THEN 1 ELSE 0 END ASC, a.submitted_on ` + direction + `, a.updated_at DESC, a.created_at DESC`, nil
	default:
		return "", fmt.Errorf("unknown application sort field %q", sortBy)
	}
}

func systemStageTypeLabel(stageType domain.StageType) string {
	switch stageType {
	case domain.StageResumeScreening:
		return "简历筛选"
	case domain.StageWrittenTest:
		return "笔试"
	case domain.StageAssessment:
		return "测评"
	case domain.StageAIInterview:
		return "AI 面"
	case domain.StageFirstInterview:
		return "一面"
	case domain.StageSecondInterview:
		return "二面"
	case domain.StageThirdInterview:
		return "三面"
	case domain.StageFourthInterview:
		return "四面"
	case domain.StageHRInterview:
		return "HR 面"
	case domain.StageOffer:
		return "Offer"
	default:
		return "自定义流程"
	}
}

func stageDisplayName(stage domain.ApplicationStage) string {
	label := systemStageTypeLabel(stage.Type)
	if strings.TrimSpace(stage.Content) == "" {
		return label
	}
	return label + " · " + strings.TrimSpace(stage.Content)
}

func makeScheduleItem(stage domain.ApplicationStage, meta scheduleStageMeta, kind string, startsAt time.Time, endsAt *time.Time, now time.Time) domain.ScheduleItem {
	completed := stageIsTerminal(stage.Status)
	waitingResult := stageWaitingForResult(stage, now)
	if kind == "stage" {
		completed = stageAppointmentCompleted(stage.Status) || waitingResult
	}
	dueAt := startsAt
	if endsAt != nil {
		dueAt = *endsAt
	}
	return domain.ScheduleItem{
		ID:                stage.ID + ":" + kind,
		StageID:           stage.ID,
		ApplicationID:     stage.ApplicationID,
		PositionID:        meta.positionID,
		CompanyName:       meta.companyName,
		CampaignName:      meta.campaignName,
		PositionTitle:     meta.positionTitle,
		ApplicationStatus: meta.applicationStatus,
		Name:              stage.Content,
		Type:              stage.Type,
		Status:            stage.Status,
		Kind:              kind,
		StartsAt:          startsAt,
		EndsAt:            endsAt,
		IsCompleted:       completed,
		IsOverdue:         !completed && dueAt.Before(now),
		IsWaitingResult:   waitingResult,
	}
}

func parseOptionalDate(value string) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := time.Parse(dateLayout, value)
	if err != nil {
		return nil, errors.New("expected YYYY-MM-DD")
	}
	return &parsed, nil
}

func parseRequiredDateTime(value string) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, errors.New("is required")
	}
	for _, layout := range []string{datetimeLayout, "2006-01-02T15:04"} {
		if parsed, err := time.ParseInLocation(layout, value, time.Local); err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, errors.New("expected ISO 8601 date and time")
}

func parseOptionalDateTime(value string) (*time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	parsed, err := parseRequiredDateTime(value)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func sameInputCalendarDay(leftInput, rightInput string, left, right time.Time) bool {
	leftInput, rightInput = strings.TrimSpace(leftInput), strings.TrimSpace(rightInput)
	if len(leftInput) >= len(dateLayout) && len(rightInput) >= len(dateLayout) {
		return leftInput[:len(dateLayout)] == rightInput[:len(dateLayout)]
	}
	return left.In(time.Local).Format(dateLayout) == right.In(time.Local).Format(dateLayout)
}

func parseStoredDateTime(value string) (time.Time, error) { return time.Parse(datetimeLayout, value) }

func parseNullableStoredDate(value sql.NullString) (*time.Time, error) {
	if !value.Valid || value.String == "" {
		return nil, nil
	}
	parsed, err := time.Parse(dateLayout, value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func parseNullableStoredDateTime(value sql.NullString) (*time.Time, error) {
	if !value.Valid || value.String == "" {
		return nil, nil
	}
	parsed, err := parseStoredDateTime(value.String)
	if err != nil {
		return nil, err
	}
	return &parsed, nil
}

func nowString() string                     { return datetimeString(time.Now().UTC()) }
func datetimeString(value time.Time) string { return value.UTC().Format(datetimeLayout) }
func dateString(value time.Time) string     { return value.UTC().Format(dateLayout) }
func nullableDate(value *time.Time) any {
	if value == nil {
		return nil
	}
	return dateString(*value)
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}
func nullableDateTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return datetimeString(*value)
}

func databaseError(err error) error {
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "unique constraint") {
		return errors.New("a record with the same unique fields already exists")
	}
	if strings.Contains(message, "foreign key constraint") {
		return errors.New("the selected related record does not exist")
	}
	return err
}
