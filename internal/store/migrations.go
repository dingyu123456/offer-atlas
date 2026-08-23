package store

import (
	"database/sql"

	"github.com/dingyu/offer-atlas/internal/domain"
)

const schemaVersion = 17

func migrate(db *sql.DB) error {
	if _, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS schema_migrations (
			version INTEGER PRIMARY KEY,
			applied_at TEXT NOT NULL
		);
	`); err != nil {
		return err
	}

	var current int
	if err := db.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&current); err != nil {
		return err
	}
	if current < 1 {
		if err := applyMigration(db, 1, initialSchema); err != nil {
			return err
		}
		current = 1
	}
	if current < 2 {
		if err := applyMigration(db, 2, applicationFlowSchema); err != nil {
			return err
		}
		current = 2
	}
	if current < 3 {
		if err := removeLegacyPositionColumns(db); err != nil {
			return err
		}
		current = 3
	}
	if current < 4 {
		if err := removeCompletedStageTime(db); err != nil {
			return err
		}
		current = 4
	}
	if current < 5 {
		if err := applyMigration(db, 5, stageTypeSchema); err != nil {
			return err
		}
		current = 5
	}
	if current < 6 {
		if err := applyMigration(db, 6, simplifyStageStatusSchema); err != nil {
			return err
		}
		current = 6
	}
	if current < 7 {
		if err := applyMigration(db, 7, positionSourceURLSchema); err != nil {
			return err
		}
		current = 7
	}
	if current < 8 {
		if err := applyMigration(db, 8, positionAttachmentSchema); err != nil {
			return err
		}
		current = 8
	}
	if current < 9 {
		if err := applyMigration(db, 9, attendedStageStatusSchema); err != nil {
			return err
		}
		current = 9
	}
	if current < 10 {
		if err := applyMigration(db, 10, normalizedSystemStageTypesSchema); err != nil {
			return err
		}
		current = 10
	}
	if current < 11 {
		if err := applyMigration(db, 11, simplifyNodeStatusesSchema); err != nil {
			return err
		}
		current = 11
	}
	if current < 12 {
		if err := applyMigration(db, 12, applicationResumeSchema); err != nil {
			return err
		}
		current = 12
	}
	if current < 13 {
		if err := applyMigration(db, 13, giteeSyncSchema); err != nil {
			return err
		}
		current = 13
	}
	if current < 14 {
		if err := applyMigration(db, 14, giteeSyncConflictDetailsSchema); err != nil {
			return err
		}
		current = 14
	}
	if current < 15 {
		if err := applyMigration(db, 15, giteeSyncProvisioningSchema); err != nil {
			return err
		}
	}
	if current < 16 {
		if err := applyMigration(db, 16, resumeLibrarySchema); err != nil {
			return err
		}
		current = 16
	}
	if current < 17 {
		if err := applyMigration(db, 17, giteeSyncCursorSchema); err != nil {
			return err
		}
	}
	if err := ensurePositionSourceURL(db); err != nil {
		return err
	}
	return ensureSystemStageTypes(db)
}

func applyMigration(db *sql.DB, version int, statements string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(statements); err != nil {
		return err
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`, version, nowString()); err != nil {
		return err
	}
	return tx.Commit()
}

const initialSchema = `
	CREATE TABLE companies (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL COLLATE NOCASE UNIQUE,
		industry TEXT NOT NULL DEFAULT '',
		homepage TEXT NOT NULL DEFAULT '',
		notes TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);

	CREATE TABLE campaigns (
		id TEXT PRIMARY KEY,
		company_id TEXT NOT NULL REFERENCES companies(id) ON DELETE RESTRICT,
		name TEXT NOT NULL,
		opens_on TEXT,
		closes_on TEXT,
		source_url TEXT NOT NULL DEFAULT '',
		last_verified_on TEXT,
		notes TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		UNIQUE(company_id, name),
		CHECK(opens_on IS NULL OR closes_on IS NULL OR opens_on <= closes_on)
	);

	CREATE TABLE positions (
		id TEXT PRIMARY KEY,
		campaign_id TEXT NOT NULL REFERENCES campaigns(id) ON DELETE RESTRICT,
		title TEXT NOT NULL,
		location TEXT NOT NULL DEFAULT '',
		track TEXT NOT NULL DEFAULT '',
		priority INTEGER NOT NULL DEFAULT 3 CHECK(priority BETWEEN 1 AND 5),
		notes TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		UNIQUE(campaign_id, title, location)
	);

	CREATE TABLE applications (
		id TEXT PRIMARY KEY,
		position_id TEXT NOT NULL REFERENCES positions(id) ON DELETE RESTRICT UNIQUE,
		submitted_on TEXT,
		resume_name TEXT NOT NULL DEFAULT '',
		next_action TEXT NOT NULL DEFAULT '',
		next_action_on TEXT,
		notes TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);

	CREATE TABLE events (
		id TEXT PRIMARY KEY,
		campaign_id TEXT NOT NULL REFERENCES campaigns(id) ON DELETE RESTRICT,
		application_id TEXT REFERENCES applications(id) ON DELETE RESTRICT,
		type TEXT NOT NULL,
		title TEXT NOT NULL,
		starts_at TEXT NOT NULL,
		ends_at TEXT,
		participation TEXT NOT NULL DEFAULT 'planned',
		source_url TEXT NOT NULL DEFAULT '',
		last_verified_on TEXT,
		notes TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		CHECK(ends_at IS NULL OR starts_at <= ends_at)
	);

	CREATE INDEX idx_campaigns_company_id ON campaigns(company_id);
	CREATE INDEX idx_positions_campaign_id ON positions(campaign_id);
	CREATE INDEX idx_applications_next_action_on ON applications(next_action_on);
	CREATE INDEX idx_events_starts_at ON events(starts_at);
	CREATE INDEX idx_events_campaign_id ON events(campaign_id);
`

const applicationFlowSchema = `
	ALTER TABLE campaigns ADD COLUMN process_overview TEXT NOT NULL DEFAULT '';
	ALTER TABLE positions ADD COLUMN job_code TEXT NOT NULL DEFAULT '';
	ALTER TABLE positions ADD COLUMN department TEXT NOT NULL DEFAULT '';
	ALTER TABLE applications ADD COLUMN status TEXT NOT NULL DEFAULT 'active';
	ALTER TABLE applications ADD COLUMN channel TEXT NOT NULL DEFAULT '';

	CREATE TABLE application_stages (
		id TEXT PRIMARY KEY,
		application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE,
		sort_order INTEGER NOT NULL,
		name TEXT NOT NULL,
		type TEXT NOT NULL,
		status TEXT NOT NULL,
		scheduled_start TEXT,
		scheduled_end TEXT,
		result_at TEXT,
		source_url TEXT NOT NULL DEFAULT '',
		notes TEXT NOT NULL DEFAULT '',
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL,
		UNIQUE(application_id, sort_order),
		CHECK(scheduled_end IS NULL OR scheduled_start IS NULL OR scheduled_start <= scheduled_end)
	);

	INSERT INTO application_stages(
		id, application_id, sort_order, name, type, status,
		scheduled_start, scheduled_end, result_at, source_url, notes, created_at, updated_at
	)
	SELECT
		e.id,
		e.application_id,
		(SELECT COUNT(*) FROM events prior
		 WHERE prior.application_id = e.application_id
		 AND (prior.starts_at < e.starts_at OR (prior.starts_at = e.starts_at AND prior.id <= e.id))),
		e.title,
		CASE WHEN e.type IN ('written_test', 'interview') THEN e.type ELSE 'other' END,
		'scheduled',
		e.starts_at, e.ends_at,
		NULL, e.source_url, e.notes, e.created_at, e.updated_at
	FROM events e
	WHERE e.application_id IS NOT NULL;

	CREATE INDEX idx_applications_status ON applications(status);
	CREATE INDEX idx_application_stages_application_order ON application_stages(application_id, sort_order);
	CREATE INDEX idx_application_stages_scheduled_start ON application_stages(scheduled_start);
`

const stageTypeSchema = `
	CREATE TABLE stage_types (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL COLLATE NOCASE UNIQUE,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);

	INSERT INTO stage_types(id, name, created_at, updated_at) VALUES
		('written_test', '笔试', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
		('interview', '技术面', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
		('hr_interview', 'HR 面', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
		('offer', 'Offer', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
		('other', '其他', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
`

const simplifyStageStatusSchema = `
	UPDATE application_stages
	SET status = CASE
		WHEN status IN ('scheduled', 'passed', 'failed') THEN status
		WHEN status IN ('skipped', 'cancelled') THEN 'failed'
		ELSE 'scheduled'
	END;

	UPDATE applications AS application
	SET status = COALESCE((
		SELECT CASE
			WHEN stage.status = 'failed' THEN 'rejected'
			WHEN stage.type = 'offer' AND stage.status = 'passed' THEN 'offer'
			ELSE 'active'
		END
		FROM application_stages stage
		WHERE stage.application_id = application.id
		ORDER BY stage.sort_order DESC
		LIMIT 1
	), 'active');
`

const positionSourceURLSchema = `
	ALTER TABLE positions ADD COLUMN source_url TEXT NOT NULL DEFAULT '';
`

const positionAttachmentSchema = `
	CREATE TABLE position_attachments (
		id TEXT PRIMARY KEY,
		position_id TEXT NOT NULL REFERENCES positions(id) ON DELETE CASCADE,
		original_name TEXT NOT NULL,
		stored_name TEXT NOT NULL,
		mime_type TEXT NOT NULL DEFAULT 'application/octet-stream',
		size_bytes INTEGER NOT NULL CHECK(size_bytes >= 0),
		created_at TEXT NOT NULL,
		UNIQUE(position_id, stored_name)
	);

	CREATE INDEX idx_position_attachments_position_id ON position_attachments(position_id, created_at DESC);
`

// Existing terminal and scheduled records keep their meaning. This migration
// records the introduction of the explicit attended/waiting-result state.
const attendedStageStatusSchema = `
	UPDATE application_stages
	SET status = 'scheduled'
	WHERE status NOT IN ('scheduled', 'attended', 'passed', 'failed');
`

// Normalize the earlier broad "技术面" category into the first interview
// round, then establish the stable product-owned type catalog.
const normalizedSystemStageTypesSchema = `
	UPDATE application_stages SET type='first_interview' WHERE type='interview';
	DELETE FROM stage_types WHERE id='interview';
	UPDATE stage_types
	SET name = name || '（自定义-' || substr(id, 1, 8) || '）'
	WHERE id NOT IN ('written_test', 'assessment', 'ai_interview', 'first_interview', 'second_interview', 'third_interview', 'fourth_interview', 'hr_interview', 'offer')
	  AND name IN ('笔试', '测评', 'AI 面', '一面', '二面', '三面', '四面', 'HR 面', 'Offer');

	INSERT OR IGNORE INTO stage_types(id, name, created_at, updated_at) VALUES
		('written_test', '笔试', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
		('assessment', '测评', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
		('ai_interview', 'AI 面', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
		('first_interview', '一面', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
		('second_interview', '二面', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
		('third_interview', '三面', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
		('fourth_interview', '四面', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
		('hr_interview', 'HR 面', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now')),
		('offer', 'Offer', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'), strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
`

// A node now records only its actionable state: scheduled, passed, or failed.
// Older attended nodes remain active and are normalized to scheduled, while a
// configured result notification continues to represent the waiting phase.
const simplifyNodeStatusesSchema = `
	UPDATE application_stages SET status='scheduled' WHERE status='attended';
`

// An application can retain exactly one managed copy of the resume that was
// submitted for it. Its file is stored independently from position materials.
const applicationResumeSchema = `
	CREATE TABLE application_resumes (
		id TEXT PRIMARY KEY,
		application_id TEXT NOT NULL REFERENCES applications(id) ON DELETE CASCADE UNIQUE,
		original_name TEXT NOT NULL,
		stored_name TEXT NOT NULL,
		mime_type TEXT NOT NULL DEFAULT 'application/octet-stream',
		size_bytes INTEGER NOT NULL CHECK(size_bytes >= 0),
		created_at TEXT NOT NULL,
		UNIQUE(application_id, stored_name)
	);

	CREATE INDEX idx_application_resumes_application_id ON application_resumes(application_id);
`

// The Gitee token deliberately does not live in this schema. It is protected
// separately with Windows DPAPI, while this table set only keeps non-secret
// synchronization metadata and immutable object operations.
const giteeSyncSchema = `
	CREATE TABLE sync_config (
		id INTEGER PRIMARY KEY CHECK(id = 1),
		device_id TEXT NOT NULL DEFAULT '',
		device_name TEXT NOT NULL DEFAULT '',
		owner TEXT NOT NULL DEFAULT '',
		primary_repo TEXT NOT NULL DEFAULT '',
		enabled INTEGER NOT NULL DEFAULT 0,
		state TEXT NOT NULL DEFAULT 'local_only',
		last_success_at TEXT NOT NULL DEFAULT '',
		last_check_at TEXT NOT NULL DEFAULT '',
		last_error TEXT NOT NULL DEFAULT '',
		next_sequence INTEGER NOT NULL DEFAULT 1,
		initial_sync_pending INTEGER NOT NULL DEFAULT 0,
		updated_at TEXT NOT NULL DEFAULT ''
	);
	INSERT INTO sync_config(id, device_id, device_name, updated_at)
	VALUES(1, '', '', strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));

	CREATE TABLE sync_object_states (
		object_type TEXT NOT NULL,
		object_id TEXT NOT NULL,
		object_version INTEGER NOT NULL,
		remote_version INTEGER NOT NULL DEFAULT 0,
		content_hash TEXT NOT NULL DEFAULT '',
		deleted INTEGER NOT NULL DEFAULT 0,
		dirty INTEGER NOT NULL DEFAULT 0,
		updated_at TEXT NOT NULL,
		PRIMARY KEY(object_type, object_id)
	);
	CREATE TABLE sync_operations (
		id TEXT PRIMARY KEY,
		device_id TEXT NOT NULL,
		sequence INTEGER NOT NULL,
		object_type TEXT NOT NULL,
		object_id TEXT NOT NULL,
		action TEXT NOT NULL,
		object_version INTEGER NOT NULL,
		base_version INTEGER NOT NULL,
		payload TEXT NOT NULL DEFAULT '{}',
		created_at TEXT NOT NULL,
		synced_at TEXT NOT NULL DEFAULT '',
		UNIQUE(device_id, sequence)
	);
	CREATE INDEX idx_sync_operations_pending ON sync_operations(synced_at, sequence);
	CREATE TABLE sync_applied_operations (
		operation_id TEXT PRIMARY KEY,
		applied_at TEXT NOT NULL
	);
	CREATE TABLE sync_conflicts (
		id TEXT PRIMARY KEY,
		object_type TEXT NOT NULL,
		object_id TEXT NOT NULL,
		local_payload TEXT NOT NULL DEFAULT '{}',
		remote_payload TEXT NOT NULL DEFAULT '{}',
		local_updated_at TEXT NOT NULL,
		remote_updated_at TEXT NOT NULL,
		remote_operation_id TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT 'open',
		created_at TEXT NOT NULL,
		resolved_at TEXT NOT NULL DEFAULT ''
	);
	CREATE UNIQUE INDEX idx_sync_conflicts_open_object
	ON sync_conflicts(object_type, object_id, status);
	CREATE TABLE sync_media_files (
		content_hash TEXT PRIMARY KEY,
		repo_name TEXT NOT NULL,
		remote_path TEXT NOT NULL,
		size_bytes INTEGER NOT NULL,
		updated_at TEXT NOT NULL
	);
`

const giteeSyncConflictDetailsSchema = `
	ALTER TABLE sync_conflicts ADD COLUMN remote_action TEXT NOT NULL DEFAULT 'upsert';
	ALTER TABLE sync_conflicts ADD COLUMN remote_version INTEGER NOT NULL DEFAULT 0;
`

// A repository can be created successfully while the first marker write is
// interrupted. Remember only that non-sensitive recovery state so a retry
// repairs the same dedicated repository instead of creating another suffix.
const giteeSyncProvisioningSchema = `
	CREATE TABLE sync_repo_provisioning (
		owner TEXT NOT NULL,
		repo_name TEXT NOT NULL,
		kind TEXT NOT NULL CHECK(kind IN ('primary', 'media')),
		created_at TEXT NOT NULL,
		PRIMARY KEY(owner, repo_name)
	);
	CREATE INDEX idx_sync_repo_provisioning_lookup
	ON sync_repo_provisioning(owner, kind, created_at DESC);
`

// Each remote device writes immutable operation files with a monotonically
// increasing sequence. These cursors avoid downloading historical operation
// JSON on every check while remaining recoverable: a cursor is advanced only
// after the corresponding batch has been applied successfully.
const giteeSyncCursorSchema = `
	CREATE TABLE sync_remote_cursors (
		device_id TEXT PRIMARY KEY,
		last_sequence INTEGER NOT NULL DEFAULT 0,
		updated_at TEXT NOT NULL
	);
`

// Resume versions are first-class reusable documents. The former
// application_resumes table is retained only long enough for an on-disk
// migration to move existing files into this library.
const resumeLibrarySchema = `
	CREATE TABLE resumes (
		id TEXT PRIMARY KEY,
		name TEXT NOT NULL,
		original_name TEXT NOT NULL,
		stored_name TEXT NOT NULL,
		mime_type TEXT NOT NULL DEFAULT 'application/octet-stream',
		size_bytes INTEGER NOT NULL CHECK(size_bytes >= 0),
		content_hash TEXT NOT NULL DEFAULT '',
		archived INTEGER NOT NULL DEFAULT 0,
		created_at TEXT NOT NULL,
		updated_at TEXT NOT NULL
	);
	CREATE INDEX idx_resumes_archived_updated_at ON resumes(archived, updated_at DESC);
	CREATE INDEX idx_resumes_content_hash ON resumes(content_hash);
	ALTER TABLE applications ADD COLUMN resume_id TEXT REFERENCES resumes(id);
	CREATE INDEX idx_applications_resume_id ON applications(resume_id);
`

func ensureSystemStageTypes(db *sql.DB) error {
	labels := map[domain.StageType]string{
		domain.StageWrittenTest:     "笔试",
		domain.StageAssessment:      "测评",
		domain.StageAIInterview:     "AI 面",
		domain.StageFirstInterview:  "一面",
		domain.StageSecondInterview: "二面",
		domain.StageThirdInterview:  "三面",
		domain.StageFourthInterview: "四面",
		domain.StageHRInterview:     "HR 面",
		domain.StageOffer:           "Offer",
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	now := nowString()
	if _, err := tx.Exec(`
		UPDATE stage_types
		SET name = name || '（自定义-' || substr(id, 1, 8) || '）'
		WHERE id NOT IN ('written_test', 'assessment', 'ai_interview', 'first_interview', 'second_interview', 'third_interview', 'fourth_interview', 'hr_interview', 'offer')
		  AND name IN ('笔试', '测评', 'AI 面', '一面', '二面', '三面', '四面', 'HR 面', 'Offer')
	`); err != nil {
		return err
	}
	for _, id := range domain.SystemStageTypes() {
		name := labels[id]
		if _, err := tx.Exec(`INSERT INTO stage_types(id, name, created_at, updated_at) VALUES (?, ?, ?, ?) ON CONFLICT(id) DO UPDATE SET name=excluded.name, updated_at=excluded.updated_at`, id, name, now, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// A missing source_url after a previously-recorded migration means an upgrade
// was interrupted or manually repaired. Reconcile this small, additive field
// before normal queries run so the database remains recoverable.
func ensurePositionSourceURL(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	exists, err := tableColumnExists(tx, "positions", "source_url")
	if err != nil {
		return err
	}
	if !exists {
		if _, err := tx.Exec(positionSourceURLSchema); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func removeLegacyPositionColumns(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DROP INDEX IF EXISTS idx_positions_status; DROP INDEX IF EXISTS idx_positions_decision_status;`); err != nil {
		return err
	}
	for _, column := range []string{"status", "decision_status"} {
		exists, err := tableColumnExists(tx, "positions", column)
		if err != nil {
			return err
		}
		if exists {
			if _, err := tx.Exec(`ALTER TABLE positions DROP COLUMN ` + column); err != nil {
				return err
			}
		}
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`, 3, nowString()); err != nil {
		return err
	}
	return tx.Commit()
}

func removeCompletedStageTime(db *sql.DB) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	exists, err := tableColumnExists(tx, "application_stages", "completed_at")
	if err != nil {
		return err
	}
	if exists {
		if _, err := tx.Exec(`ALTER TABLE application_stages DROP COLUMN completed_at`); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO schema_migrations(version, applied_at) VALUES (?, ?)`, 4, nowString()); err != nil {
		return err
	}
	return tx.Commit()
}

func tableColumnExists(tx *sql.Tx, table, target string) (bool, error) {
	rows, err := tx.Query(`PRAGMA table_info(` + table + `)`)
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return false, err
		}
		if name == target {
			return true, nil
		}
	}
	return false, rows.Err()
}
