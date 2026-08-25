package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dingyu/offer-atlas/internal/domain"
	"github.com/google/uuid"
	"github.com/xuri/excelize/v2"
)

func TestApplicationFlowAndSafetyMirror(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "offer-atlas.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	company, err := store.SaveCompany(domain.CompanyInput{Name: "Example Cloud", Industry: "Cloud computing"})
	if err != nil {
		t.Fatalf("save company: %v", err)
	}
	campaign, err := store.SaveCampaign(domain.CampaignInput{
		CompanyID: company.ID, Name: "2027 Autumn Campus Recruitment", OpensOn: "2026-08-01", ClosesOn: "2026-09-30",
		ProcessOverview: "The official guide lists one written test and an unfixed number of interviews.",
	})
	if err != nil {
		t.Fatalf("save campaign: %v", err)
	}
	position, err := store.SavePosition(domain.PositionInput{
		CampaignID: campaign.ID, Title: "Platform Engineer", JobCode: "ENG-2027-01", Department: "Cloud Platform", Location: "Hangzhou", Track: "Backend", SourceURL: "https://jobs.example.com/platform-engineer", Priority: 5,
	})
	if err != nil {
		t.Fatalf("save position: %v", err)
	}
	pendingPosition, err := store.SavePosition(domain.PositionInput{
		CampaignID: campaign.ID, Title: "Data Engineer", JobCode: "ENG-2027-02", Department: "Cloud Platform", Location: "Shanghai", SourceURL: "https://jobs.example.com/data-engineer", Priority: 4,
	})
	if err != nil || pendingPosition.ID == "" {
		t.Fatalf("save pending position: %#v, %v", pendingPosition, err)
	}

	application, err := store.SaveApplication(domain.ApplicationInput{PositionID: position.ID, SubmittedOn: "2026-08-20", Channel: "Official campus site", ResumeName: "backend-v3"})
	if err != nil {
		t.Fatalf("save application: %v", err)
	}
	writtenTest, err := store.SaveApplicationStage(domain.ApplicationStageInput{ApplicationID: application.ID, Name: "Written test", Type: domain.StageWrittenTest, Status: domain.StageScheduled, ScheduledStart: "2026-08-25T19:00"})
	if err != nil {
		t.Fatalf("save written test stage: %v", err)
	}
	firstInterview, err := store.SaveApplicationStage(domain.ApplicationStageInput{ApplicationID: application.ID, Name: "Technical interview 1", Type: domain.StageInterview, Status: domain.StageScheduled, ScheduledStart: "2026-08-28T14:00"})
	if err != nil {
		t.Fatalf("save first interview stage: %v", err)
	}
	hrInterview, err := store.SaveApplicationStage(domain.ApplicationStageInput{ApplicationID: application.ID, Name: "HR interview", Type: domain.StageHRInterview, Status: domain.StageScheduled})
	if err != nil {
		t.Fatalf("save dynamically added HR stage: %v", err)
	}

	stages, err := store.ReorderApplicationStages(application.ID, []string{writtenTest.ID, firstInterview.ID, hrInterview.ID})
	if err != nil {
		t.Fatalf("reorder stages: %v", err)
	}
	if len(stages) != 3 || stages[1].Name != "Technical interview 1" || stages[2].SortOrder != 3 {
		t.Fatalf("unexpected stages: %#v", stages)
	}

	detail, err := store.GetPositionDetail(position.ID)
	if err != nil {
		t.Fatalf("get position detail: %v", err)
	}
	if detail.Application == nil || detail.Application.Channel != "Official campus site" || len(detail.Stages) != 3 || detail.Position.SourceURL != "https://jobs.example.com/platform-engineer" {
		t.Fatalf("unexpected position detail: %#v", detail)
	}
	if detail.Campaign.ProcessOverview == "" {
		t.Fatal("process overview was not stored")
	}

	summaries, err := store.ListPositions(domain.PositionFilter{Status: string(domain.PositionApplied)})
	if err != nil {
		t.Fatalf("list positions: %v", err)
	}
	if len(summaries) != 1 || summaries[0].Status != domain.PositionApplied || summaries[0].CurrentStageName != "HR interview" || summaries[0].StageCount != 3 {
		t.Fatalf("unexpected position summary: %#v", summaries)
	}

	dashboard, err := store.Dashboard(time.Date(2026, 8, 26, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("dashboard: %v", err)
	}
	if dashboard.ActiveApplications != 1 || dashboard.TodoCount != 2 {
		t.Fatalf("unexpected dashboard: %#v", dashboard)
	}

	health, err := store.Health()
	if err != nil {
		t.Fatalf("health: %v", err)
	}
	if health.SchemaVersion != schemaVersion || health.Safety.LatestSnapshot == "" || health.Safety.LastError != "" {
		t.Fatalf("unexpected safety status: %#v", health.Safety)
	}
	if filepath.Base(health.Safety.LatestSnapshot) != "latest" {
		t.Fatalf("latest mirror should use a stable directory: %s", health.Safety.LatestSnapshot)
	}
	workbookPath := filepath.Join(health.Safety.LatestSnapshot, readableMirrorWorkbookName)
	workbook, err := excelize.OpenFile(workbookPath)
	if err != nil {
		t.Fatalf("open readable mirror workbook: %v", err)
	}
	defer workbook.Close()
	if sheets := workbook.GetSheetList(); len(sheets) != 2 || sheets[0] != "投递记录" || sheets[1] != "待投递岗位" {
		t.Fatalf("unexpected mirror sheets: %#v", sheets)
	}
	if headers, err := workbook.GetRows("投递记录"); err != nil || len(headers) < 2 || strings.Join(headers[0], "|") != strings.Join(applicationMirrorHeaders, "|") {
		t.Fatalf("unexpected application mirror headers: %#v, %v", headers, err)
	}
	if value, err := workbook.GetCellValue("投递记录", "A2"); err != nil || value != "Example Cloud" {
		t.Fatalf("application mirror company was not written: %q, %v", value, err)
	}
	if value, err := workbook.GetCellValue("投递记录", "L2"); err != nil || !strings.Contains(value, "HR interview") || !strings.Contains(value, "（08-25 19:00）") {
		t.Fatalf("application mirror progress was not written: %q, %v", value, err)
	}
	if linked, target, err := workbook.GetCellHyperLink("投递记录", "M2"); err != nil || !linked || target != "https://jobs.example.com/platform-engineer" {
		t.Fatalf("application mirror position link was not written: linked=%t target=%q err=%v", linked, target, err)
	}
	if headers, err := workbook.GetRows("待投递岗位"); err != nil || len(headers) < 2 || strings.Join(headers[0], "|") != strings.Join(pendingMirrorHeaders, "|") || headers[1][2] != "Data Engineer" {
		t.Fatalf("unexpected pending-position mirror contents: %#v, %v", headers, err)
	}
	if _, err := os.Stat(filepath.Join(health.Safety.MirrorDirectory, "journal.jsonl")); err != nil {
		t.Fatalf("journal was not written: %v", err)
	}
	history, err := os.ReadDir(filepath.Join(health.Safety.MirrorDirectory, "history"))
	if err != nil || len(history) != 1 {
		t.Fatalf("expected one daily archive, got %#v, %v", history, err)
	}
	if _, err := os.Stat(filepath.Join(health.Safety.MirrorDirectory, "history", history[0].Name(), readableMirrorWorkbookName)); err != nil {
		t.Fatalf("daily archive workbook was not written: %v", err)
	}
	if entries, err := os.ReadDir(health.Safety.LatestSnapshot); err != nil || len(entries) != 1 || entries[0].Name() != readableMirrorWorkbookName {
		t.Fatalf("latest mirror should contain only the readable workbook: %#v, %v", entries, err)
	}
	if _, err := os.Stat(filepath.Join(health.Safety.MirrorDirectory, "snapshots")); !os.IsNotExist(err) {
		t.Fatalf("per-write snapshot directories should not exist: %v", err)
	}

	backup, err := store.CreateBackup()
	if err != nil {
		t.Fatalf("create SQLite backup: %v", err)
	}
	info, err := os.Stat(backup)
	if err != nil || info.Size() == 0 {
		t.Fatalf("backup was not written: %v, %#v", err, info)
	}
}

func TestApplicationStageBelongsToApplicationAndCanBeDeleted(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "offer-atlas.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	company, _ := store.SaveCompany(domain.CompanyInput{Name: "Company A"})
	campaign, _ := store.SaveCampaign(domain.CampaignInput{CompanyID: company.ID, Name: "Autumn 2027"})
	position, _ := store.SavePosition(domain.PositionInput{CampaignID: campaign.ID, Title: "Backend", Priority: 3})
	application, _ := store.SaveApplication(domain.ApplicationInput{PositionID: position.ID})
	stage, err := store.SaveApplicationStage(domain.ApplicationStageInput{ApplicationID: application.ID, Name: "Interview", Type: domain.StageInterview, Status: domain.StageScheduled})
	if err != nil {
		t.Fatalf("save stage: %v", err)
	}
	if err := store.DeleteApplicationStage(stage.ID); err != nil {
		t.Fatalf("delete stage: %v", err)
	}
	stages, err := store.ListApplicationStages(application.ID)
	if err != nil || len(stages) != 0 {
		t.Fatalf("stage deletion failed: %#v, %v", stages, err)
	}
}

func TestApplicationStageRejectsCrossDaySchedule(t *testing.T) {
	fixture := newDeletionFixture(t)
	_, err := fixture.store.SaveApplicationStage(domain.ApplicationStageInput{
		ApplicationID:  fixture.application.ID,
		Name:           "跨天笔试",
		Type:           domain.StageWrittenTest,
		Status:         domain.StageScheduled,
		ScheduledStart: "2026-08-18T23:00",
		ScheduledEnd:   "2026-08-19T00:30",
	})
	if err == nil || !strings.Contains(err.Error(), "same day") {
		t.Fatalf("cross-day stage should be rejected, got %v", err)
	}
	if _, err := fixture.store.SaveApplicationStage(domain.ApplicationStageInput{
		ApplicationID:  fixture.application.ID,
		Name:           "同日笔试",
		Type:           domain.StageWrittenTest,
		Status:         domain.StageScheduled,
		ScheduledStart: "2026-08-18T09:00",
		ScheduledEnd:   "2026-08-18T11:00",
	}); err != nil {
		t.Fatalf("same-day stage should be saved: %v", err)
	}
}

func TestPositionAttachmentsAreMirroredBackedUpAndCleaned(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "offer-atlas.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	company, err := store.SaveCompany(domain.CompanyInput{Name: "Attachment Company"})
	if err != nil {
		t.Fatalf("save company: %v", err)
	}
	campaign, err := store.SaveCampaign(domain.CampaignInput{CompanyID: company.ID, Name: "Attachment Campaign"})
	if err != nil {
		t.Fatalf("save campaign: %v", err)
	}
	position, err := store.SavePosition(domain.PositionInput{CampaignID: campaign.ID, Title: "Attachment Position", Priority: 3})
	if err != nil {
		t.Fatalf("save position: %v", err)
	}
	sourcePath := filepath.Join(t.TempDir(), "job-description.png")
	if err := os.WriteFile(sourcePath, []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}, 0o600); err != nil {
		t.Fatalf("write source attachment: %v", err)
	}

	attachments, err := store.ImportPositionAttachments(position.ID, []string{sourcePath})
	if err != nil || len(attachments) != 1 {
		t.Fatalf("import attachment: %#v, %v", attachments, err)
	}
	attachment := attachments[0]
	if attachment.OriginalName != "job-description.png" || attachment.MIMEType != "image/png" {
		t.Fatalf("unexpected attachment metadata: %#v", attachment)
	}
	dataURL, err := store.PositionAttachmentDataURL(attachment.ID)
	if err != nil || !strings.HasPrefix(dataURL, "data:image/png;base64,") {
		t.Fatalf("image preview was not produced: %v, %q", err, dataURL)
	}

	health, err := store.Health()
	if err != nil {
		t.Fatalf("read health: %v", err)
	}
	if _, err := os.Stat(filepath.Join(health.Safety.LatestSnapshot, "attachments")); !os.IsNotExist(err) {
		t.Fatalf("attachments should not be copied into the readable mirror: %v", err)
	}

	backup, err := store.CreateBackup()
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(backup), "attachments", "positions", position.ID, attachment.StoredName)); err != nil {
		t.Fatalf("attachment was not copied to backup: %v", err)
	}
	storedPath, err := store.PositionAttachmentPath(attachment.ID)
	if err != nil {
		t.Fatalf("resolve attachment path: %v", err)
	}
	if err := store.DeletePositionAttachment(attachment.ID); err != nil {
		t.Fatalf("delete attachment: %v", err)
	}
	if _, err := os.Stat(storedPath); !os.IsNotExist(err) {
		t.Fatalf("attachment file remains after deletion: %v", err)
	}
	remaining, err := store.ListPositionAttachments(position.ID)
	if err != nil || len(remaining) != 0 {
		t.Fatalf("attachment metadata remains after deletion: %#v, %v", remaining, err)
	}

	attachments, err = store.ImportPositionAttachments(position.ID, []string{sourcePath})
	if err != nil || len(attachments) != 1 {
		t.Fatalf("re-import attachment: %#v, %v", attachments, err)
	}
	attachmentDirectory, err := store.attachmentPositionDirectory(position.ID)
	if err != nil {
		t.Fatalf("resolve attachment directory: %v", err)
	}
	if err := store.DeleteEntity(domain.DeleteInput{EntityType: domain.DeletionTargetPosition, ID: position.ID, ConfirmationText: position.Title}); err != nil {
		t.Fatalf("delete position: %v", err)
	}
	if _, err := os.Stat(attachmentDirectory); !os.IsNotExist(err) {
		t.Fatalf("position attachment directory remains after position deletion: %v", err)
	}
}

func TestPastedPositionImageIsStoredAsAttachment(t *testing.T) {
	fixture := newDeletionFixture(t)
	image := []byte{0x89, 0x50, 0x4e, 0x47, 0x0d, 0x0a, 0x1a, 0x0a}
	attachments, err := fixture.store.ImportPastedPositionImage(fixture.position.ID, "clipboard-shot.png", "image/png", image)
	if err != nil || len(attachments) != 1 {
		t.Fatalf("paste image: %#v, %v", attachments, err)
	}
	attachment := attachments[0]
	if attachment.OriginalName != "clipboard-shot.png" || attachment.MIMEType != "image/png" || attachment.SizeBytes != int64(len(image)) {
		t.Fatalf("unexpected pasted image metadata: %#v", attachment)
	}
	path, err := fixture.store.PositionAttachmentPath(attachment.ID)
	if err != nil {
		t.Fatalf("resolve pasted image path: %v", err)
	}
	stored, err := os.ReadFile(path)
	if err != nil || string(stored) != string(image) {
		t.Fatalf("pasted image was not stored: %v, %x", err, stored)
	}
}

func TestAttachmentListsFollowAddedOrder(t *testing.T) {
	fixture := newDeletionFixture(t)
	earlier := datetimeString(time.Date(2026, time.August, 25, 10, 0, 0, 0, time.UTC))
	later := datetimeString(time.Date(2026, time.August, 25, 10, 1, 0, 0, time.UTC))

	for _, item := range []struct {
		id        string
		name      string
		createdAt string
	}{
		{id: "position-later", name: "later.png", createdAt: later},
		{id: "position-earlier", name: "earlier.png", createdAt: earlier},
	} {
		if _, err := fixture.store.db.Exec(`
			INSERT INTO position_attachments(id, position_id, original_name, stored_name, mime_type, size_bytes, created_at)
			VALUES (?, ?, ?, ?, 'image/png', 1, ?)
		`, item.id, fixture.position.ID, item.name, item.name, item.createdAt); err != nil {
			t.Fatalf("insert position attachment: %v", err)
		}
	}
	positionAttachments, err := fixture.store.ListPositionAttachments(fixture.position.ID)
	if err != nil || len(positionAttachments) != 2 || positionAttachments[0].ID != "position-earlier" || positionAttachments[1].ID != "position-later" {
		t.Fatalf("position attachments must follow added order: %#v, %v", positionAttachments, err)
	}

	for _, item := range []struct {
		id        string
		name      string
		createdAt string
	}{
		{id: "resource-later", name: "later.png", createdAt: later},
		{id: "resource-earlier", name: "earlier.png", createdAt: earlier},
	} {
		if _, err := fixture.store.db.Exec(`
			INSERT INTO supplemental_attachments(id, owner_type, owner_id, original_name, stored_name, mime_type, size_bytes, created_at)
			VALUES (?, 'application', ?, ?, ?, 'image/png', 1, ?)
		`, item.id, fixture.application.ID, item.name, item.name, item.createdAt); err != nil {
			t.Fatalf("insert supplemental attachment: %v", err)
		}
	}
	resourceAttachments, err := fixture.store.ListSupplementalAttachments(domain.ResourceOwnerApplication, fixture.application.ID)
	if err != nil || len(resourceAttachments) != 2 || resourceAttachments[0].ID != "resource-earlier" || resourceAttachments[1].ID != "resource-later" {
		t.Fatalf("supplemental attachments must follow added order: %#v, %v", resourceAttachments, err)
	}
}

func TestUploadedPositionAttachmentDataIsStoredAndMirrored(t *testing.T) {
	fixture := newDeletionFixture(t)
	contents := []byte("position reference")
	attachments, err := fixture.store.ImportPositionAttachmentData(fixture.position.ID, "reference", "text/plain", contents)
	if err != nil || len(attachments) != 1 {
		t.Fatalf("upload attachment data: %#v, %v", attachments, err)
	}
	attachment := attachments[0]
	if attachment.OriginalName != "reference" || attachment.MIMEType != "text/plain" || attachment.SizeBytes != int64(len(contents)) {
		t.Fatalf("unexpected uploaded attachment metadata: %#v", attachment)
	}
	if filepath.Ext(attachment.StoredName) != ".txt" {
		t.Fatalf("uploaded attachment should receive a MIME-derived extension: %q", attachment.StoredName)
	}
	path, err := fixture.store.PositionAttachmentPath(attachment.ID)
	if err != nil {
		t.Fatalf("resolve uploaded attachment: %v", err)
	}
	if actual, err := os.ReadFile(path); err != nil || string(actual) != string(contents) {
		t.Fatalf("uploaded attachment contents: %v, %q", err, actual)
	}
	health, err := fixture.store.Health()
	if err != nil {
		t.Fatalf("read health: %v", err)
	}
	if _, err := os.Stat(filepath.Join(health.Safety.LatestSnapshot, "attachments")); !os.IsNotExist(err) {
		t.Fatalf("uploaded attachment should not be copied into readable mirror: %v", err)
	}
}

func TestStageStatusDefaultsToScheduledAndRejectsLegacyStates(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "offer-atlas.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	company, _ := store.SaveCompany(domain.CompanyInput{Name: "Status Company"})
	campaign, _ := store.SaveCampaign(domain.CampaignInput{CompanyID: company.ID, Name: "Status Campaign"})
	position, _ := store.SavePosition(domain.PositionInput{CampaignID: campaign.ID, Title: "Status Position", Priority: 3})
	application, _ := store.SaveApplication(domain.ApplicationInput{PositionID: position.ID})

	stage, err := store.SaveApplicationStage(domain.ApplicationStageInput{ApplicationID: application.ID, Name: "Interview", Type: domain.StageInterview})
	if err != nil || stage.Status != domain.StageScheduled {
		t.Fatalf("new stages must default to scheduled: %#v, %v", stage, err)
	}
	if _, err := store.SaveApplicationStage(domain.ApplicationStageInput{ApplicationID: application.ID, Name: "Legacy", Type: domain.StageInterview, Status: domain.StageStatus("pending")}); err == nil {
		t.Fatal("legacy stage status must be rejected")
	}
}

func TestScheduleItemsAreDerivedFromApplicationStages(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "offer-atlas.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	company, _ := store.SaveCompany(domain.CompanyInput{Name: "Schedule Company"})
	campaign, _ := store.SaveCampaign(domain.CampaignInput{CompanyID: company.ID, Name: "Schedule Campaign"})
	position, _ := store.SavePosition(domain.PositionInput{CampaignID: campaign.ID, Title: "Schedule Position", Priority: 3})
	application, _ := store.SaveApplication(domain.ApplicationInput{PositionID: position.ID})
	// Keep the scheduled range within one calendar day regardless of the time
	// at which the test suite happens to run.
	now := time.Now().UTC()
	scheduledDay := now.AddDate(0, 0, 2)
	startTime := time.Date(scheduledDay.Year(), scheduledDay.Month(), scheduledDay.Day(), 14, 0, 0, 0, time.UTC)
	endTime := startTime.Add(time.Hour)
	resultTime := startTime.AddDate(0, 0, 1)
	start := startTime.Format("2006-01-02T15:04")
	end := endTime.Format("2006-01-02T15:04")
	result := resultTime.Format("2006-01-02T15:04")
	if _, err := store.SaveApplicationStage(domain.ApplicationStageInput{ApplicationID: application.ID, Name: "Interview", Type: domain.StageInterview, Status: domain.StageScheduled, ScheduledStart: start, ScheduledEnd: end}); err != nil {
		t.Fatalf("save scheduled stage: %v", err)
	}
	if _, err := store.SaveApplicationStage(domain.ApplicationStageInput{ApplicationID: application.ID, Name: "Written test", Type: domain.StageWrittenTest, Status: domain.StageScheduled, ResultAt: result}); err != nil {
		t.Fatalf("save result notification stage: %v", err)
	}

	items, err := store.ListScheduleItems(domain.ScheduleFilter{})
	if err != nil {
		t.Fatalf("list schedule items: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("expected two schedule occurrences, got %#v", items)
	}
	if items[0].ApplicationID != application.ID || items[0].PositionID != position.ID || items[0].CompanyName != company.Name {
		t.Fatalf("schedule occurrence lost its navigation context: %#v", items[0])
	}
	if items[0].Kind != "stage" || items[0].EndsAt == nil || items[0].IsCompleted {
		t.Fatalf("scheduled interval was not represented correctly: %#v", items[0])
	}
	if items[1].Kind != "result" || items[1].IsCompleted {
		t.Fatalf("result notification should remain an open Todo: %#v", items[1])
	}

	rows, err := store.db.Query(`PRAGMA table_info(application_stages)`)
	if err != nil {
		t.Fatalf("inspect application stages: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan application stage column: %v", err)
		}
		if name == "completed_at" {
			t.Fatal("completed_at should not remain in the stage model")
		}
	}
}

func TestScheduleItemUsesEndTimeAsOverdueCutoff(t *testing.T) {
	start := time.Date(2026, time.August, 18, 9, 0, 0, 0, time.UTC)
	end := time.Date(2026, time.August, 18, 18, 0, 0, 0, time.UTC)
	stage := domain.ApplicationStage{ID: "stage", ApplicationID: "application", Status: domain.StageScheduled}

	withinWindow := makeScheduleItem(stage, scheduleStageMeta{}, "stage", start, &end, time.Date(2026, time.August, 18, 12, 0, 0, 0, time.UTC))
	if withinWindow.IsOverdue {
		t.Fatal("an active availability window should not be overdue before its end time")
	}

	afterWindow := makeScheduleItem(stage, scheduleStageMeta{}, "stage", start, &end, time.Date(2026, time.August, 18, 18, 1, 0, 0, time.UTC))
	if !afterWindow.IsOverdue {
		t.Fatal("a scheduled availability window should be overdue after its end time")
	}

	pointInTime := makeScheduleItem(stage, scheduleStageMeta{}, "stage", start, nil, time.Date(2026, time.August, 18, 9, 1, 0, 0, time.UTC))
	if !pointInTime.IsOverdue {
		t.Fatal("a scheduled point-in-time schedule should be overdue after its start time")
	}
}

func TestFutureAppointmentCannotHaveARecordedOutcome(t *testing.T) {
	future := time.Now().UTC().Add(2 * time.Hour)
	end := future.Add(time.Hour)
	if err := validateStageStatusTiming(domain.StageScheduled, &future, &end, time.Now().UTC()); err != nil {
		t.Fatalf("scheduled future appointment should be valid: %v", err)
	}
	for _, status := range []domain.StageStatus{domain.StagePassed, domain.StageFailed} {
		if err := validateStageStatusTiming(status, &future, &end, time.Now().UTC()); err == nil {
			t.Fatalf("future appointment must reject status %q", status)
		}
	}
	past := time.Now().UTC().Add(-2 * time.Hour)
	if err := validateStageStatusTiming(domain.StagePassed, &past, nil, time.Now().UTC()); err != nil {
		t.Fatalf("past appointment should allow its recorded result: %v", err)
	}
}

func TestResultNotificationCannotPredateScheduledAppointment(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "offer-atlas.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	company, _ := store.SaveCompany(domain.CompanyInput{Name: "Result Timing Company"})
	campaign, _ := store.SaveCampaign(domain.CampaignInput{CompanyID: company.ID, Name: "2027 Autumn"})
	position, _ := store.SavePosition(domain.PositionInput{CampaignID: campaign.ID, Title: "Result Timing Position", Priority: 3})
	application, _ := store.SaveApplication(domain.ApplicationInput{PositionID: position.ID})
	start := time.Now().UTC().Truncate(24 * time.Hour).Add(12 * time.Hour)
	end := start.Add(time.Hour)
	if _, err := store.SaveApplicationStage(domain.ApplicationStageInput{
		ApplicationID:  application.ID,
		Name:           "Technical interview",
		Type:           domain.StageInterview,
		Status:         domain.StageScheduled,
		ScheduledStart: start.Format("2006-01-02T15:04"),
		ScheduledEnd:   end.Format("2006-01-02T15:04"),
		ResultAt:       start.Add(30 * time.Minute).Format("2006-01-02T15:04"),
	}); err == nil || !strings.Contains(err.Error(), "result time") {
		t.Fatalf("result notification before the appointment end must be rejected, got %v", err)
	}
	if _, err := store.SaveApplicationStage(domain.ApplicationStageInput{
		ApplicationID:  application.ID,
		Name:           "Technical interview",
		Type:           domain.StageInterview,
		Status:         domain.StageScheduled,
		ScheduledStart: start.Format("2006-01-02T15:04"),
		ScheduledEnd:   end.Format("2006-01-02T15:04"),
		ResultAt:       end.Format("2006-01-02T15:04"),
	}); err != nil {
		t.Fatalf("result notification at the appointment end should be valid: %v", err)
	}
}

func TestPositionPriorityOrdersPositionAndApplicationLists(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "offer-atlas.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	company, _ := store.SaveCompany(domain.CompanyInput{Name: "Priority Company"})
	campaign, _ := store.SaveCampaign(domain.CampaignInput{CompanyID: company.ID, Name: "2027 Autumn"})
	low, _ := store.SavePosition(domain.PositionInput{CampaignID: campaign.ID, Title: "Low priority", Priority: 1})
	high, _ := store.SavePosition(domain.PositionInput{CampaignID: campaign.ID, Title: "High priority", Priority: 5})
	if _, err := store.SaveApplication(domain.ApplicationInput{PositionID: low.ID, SubmittedOn: "2026-08-10"}); err != nil {
		t.Fatalf("save low priority application: %v", err)
	}
	if _, err := store.SaveApplication(domain.ApplicationInput{PositionID: high.ID, SubmittedOn: "2026-08-20"}); err != nil {
		t.Fatalf("save high priority application: %v", err)
	}

	positions, err := store.ListPositionPage(domain.PositionFilter{SortBy: "priority", SortOrder: "asc", Page: 1, PageSize: 20})
	if err != nil || len(positions.Items) != 2 || positions.Items[0].ID != low.ID || positions.Items[1].ID != high.ID {
		t.Fatalf("position list should support ascending priority order: %#v, %v", positions, err)
	}
	applications, err := store.ListApplications(domain.ApplicationFilter{SortBy: "priority", SortOrder: "desc", Page: 1, PageSize: 20})
	if err != nil || len(applications.Items) != 2 || applications.Items[0].PositionID != high.ID || applications.Items[0].PositionPriority != 5 || applications.Items[1].PositionID != low.ID {
		t.Fatalf("application list should follow the position priority order: %#v, %v", applications, err)
	}
	positions, err = store.ListPositionPage(domain.PositionFilter{SortBy: "submitted_on", SortOrder: "desc", Page: 1, PageSize: 20})
	if err != nil || len(positions.Items) != 2 || positions.Items[0].ID != high.ID || positions.Items[1].ID != low.ID {
		t.Fatalf("position list should support descending submitted date order: %#v, %v", positions, err)
	}
	applications, err = store.ListApplications(domain.ApplicationFilter{SortBy: "submitted_on", SortOrder: "asc", Page: 1, PageSize: 20})
	if err != nil || len(applications.Items) != 2 || applications.Items[0].PositionID != low.ID || applications.Items[1].PositionID != high.ID {
		t.Fatalf("application list should support ascending submitted date order: %#v, %v", applications, err)
	}
	if _, err := store.ListApplications(domain.ApplicationFilter{SortOrder: "invalid", Page: 1, PageSize: 20}); err == nil {
		t.Fatal("unknown sort direction must be rejected")
	}
	if _, err := store.ListApplications(domain.ApplicationFilter{SortBy: "invalid", Page: 1, PageSize: 20}); err == nil {
		t.Fatal("unknown sort field must be rejected")
	}
}

func TestScheduledStageWithResultTimeCreatesWaitingResultReminder(t *testing.T) {
	stage := domain.ApplicationStage{ID: "stage", ApplicationID: "application", Status: domain.StageScheduled}
	start := time.Date(2026, time.August, 18, 9, 0, 0, 0, time.UTC)
	end := time.Date(2026, time.August, 18, 10, 0, 0, 0, time.UTC)
	now := time.Date(2026, time.August, 19, 9, 0, 0, 0, time.UTC)
	resultAt := now.Add(24 * time.Hour)
	stage.ScheduledStart, stage.ScheduledEnd, stage.ResultAt = &start, &end, &resultAt
	appointment := makeScheduleItem(stage, scheduleStageMeta{}, "stage", start, &end, now)
	if !appointment.IsCompleted || appointment.IsOverdue || !appointment.IsWaitingResult {
		t.Fatalf("past scheduled appointment with a result time should wait without becoming overdue: %#v", appointment)
	}
	result := makeScheduleItem(stage, scheduleStageMeta{}, "result", resultAt, nil, now)
	if result.IsCompleted || result.IsOverdue || !result.IsWaitingResult {
		t.Fatalf("result notification should remain open while waiting: %#v", result)
	}
	if domain.ValidStageStatus(domain.StageAttended) {
		t.Fatal("legacy attended status should no longer be accepted for new nodes")
	}
}

func TestQuickCaptureAndRestoreBackup(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "offer-atlas.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	position, err := store.QuickCapturePosition(domain.QuickCapturePositionInput{
		CompanyName: "Quick Company", CompanyIndustry: "Software", CompanyHomepage: "https://company.example", CompanyNotes: "Company note",
		CampaignName: "2027 Autumn", CampaignOpensOn: "2026-08-01", CampaignClosesOn: "2026-09-30", CampaignSourceURL: "https://company.example/campus", CampaignLastVerifiedOn: "2026-08-02", CampaignProcessOverview: "Written test and interviews", CampaignNotes: "Campaign note",
		Title: "Backend", JobCode: "Q-01", Department: "Platform", Location: "Hangzhou", Track: "Infrastructure", SourceURL: "https://company.example/backend", Priority: 4, Notes: "Position note",
	})
	if err != nil || position.Title != "Backend" {
		t.Fatalf("quick capture position: %#v, %v", position, err)
	}
	detail, err := store.GetPositionDetail(position.ID)
	if err != nil || detail.Company.Industry != "Software" || detail.Company.Homepage != "https://company.example" || detail.Company.Notes != "Company note" || detail.Campaign.OpensOn == nil || detail.Campaign.ClosesOn == nil || detail.Campaign.SourceURL != "https://company.example/campus" || detail.Campaign.ProcessOverview != "Written test and interviews" || detail.Campaign.Notes != "Campaign note" || detail.Position.Department != "Platform" || detail.Position.Track != "Infrastructure" || detail.Position.Notes != "Position note" {
		t.Fatalf("quick capture did not persist the complete object hierarchy: %#v, %v", detail, err)
	}
	attachmentSource := filepath.Join(t.TempDir(), "quick-jd.txt")
	if err := os.WriteFile(attachmentSource, []byte("job description"), 0o600); err != nil {
		t.Fatalf("write attachment source: %v", err)
	}
	attachments, err := store.ImportPositionAttachments(position.ID, []string{attachmentSource})
	if err != nil || len(attachments) != 1 {
		t.Fatalf("attach quick position before backup: %#v, %v", attachments, err)
	}
	backupAttachment := attachments[0]
	second, err := store.QuickCapturePosition(domain.QuickCapturePositionInput{CompanyName: "Quick Company", CompanyIndustry: "Overwritten", CampaignName: "2027 Autumn", CampaignSourceURL: "https://overwrite.example", Title: "Platform", Priority: 3})
	if err != nil || second.CampaignID != position.CampaignID {
		t.Fatalf("quick capture should reuse existing campaign: %#v, %v", second, err)
	}
	detail, err = store.GetPositionDetail(position.ID)
	if err != nil || detail.Company.Industry != "Software" || detail.Campaign.SourceURL != "https://company.example/campus" {
		t.Fatalf("quick capture must not overwrite reused shared records: %#v, %v", detail, err)
	}

	backupPath, err := store.CreateBackup()
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}
	backupID := filepath.Base(filepath.Dir(backupPath))
	if _, err := store.SaveCompany(domain.CompanyInput{Name: "After Backup"}); err != nil {
		t.Fatalf("save later company: %v", err)
	}
	center, err := store.BackupCenter()
	if err != nil || len(center.Backups) != 1 || center.Backups[0].ID != backupID || center.ArchivesCount == 0 {
		t.Fatalf("backup center should show complete backup and daily archive: %#v, %v", center, err)
	}
	if _, err := store.RestoreBackup(backupID, "wrong"); err == nil {
		t.Fatal("restore must require exact backup confirmation")
	}
	result, err := store.RestoreBackup(backupID, backupID)
	if err != nil || result.RestoredBackup.ID != backupID || result.SafetyBackup.ID == "" {
		t.Fatalf("restore backup: %#v, %v", result, err)
	}
	companies, err := store.ListCompanies()
	if err != nil || len(companies) != 1 || companies[0].Name != "Quick Company" {
		t.Fatalf("restore did not return database to backup state: %#v, %v", companies, err)
	}
	restoredAttachmentPath, err := store.PositionAttachmentPath(backupAttachment.ID)
	if err != nil {
		t.Fatalf("restore did not return attachment metadata: %v", err)
	}
	if contents, err := os.ReadFile(restoredAttachmentPath); err != nil || string(contents) != "job description" {
		t.Fatalf("restore did not return attachment file: %v, %q", err, contents)
	}
}

func TestApplicationStatusIsDerivedFromTheLatestStage(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "offer-atlas.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	company, _ := store.SaveCompany(domain.CompanyInput{Name: "Flow Company"})
	campaign, _ := store.SaveCampaign(domain.CampaignInput{CompanyID: company.ID, Name: "2027 Autumn"})
	position, _ := store.SavePosition(domain.PositionInput{CampaignID: campaign.ID, Title: "Backend", Priority: 3})
	application, err := store.SaveApplication(domain.ApplicationInput{PositionID: position.ID})
	if err != nil {
		t.Fatalf("save application: %v", err)
	}
	if application.Status != domain.ApplicationActive {
		t.Fatalf("application without stages should be active: %#v", application)
	}

	interview, err := store.SaveApplicationStage(domain.ApplicationStageInput{ApplicationID: application.ID, Name: "Technical interview", Type: domain.StageInterview, Status: domain.StageFailed})
	if err != nil {
		t.Fatalf("save failed stage: %v", err)
	}
	application, _ = store.applicationByID(application.ID)
	if application.Status != domain.ApplicationRejected {
		t.Fatalf("latest failed stage should reject application: %#v", application)
	}

	if _, err := store.SaveApplicationStage(domain.ApplicationStageInput{ID: interview.ID, ApplicationID: application.ID, Name: interview.Name, Type: interview.Type, Status: domain.StagePassed}); err != nil {
		t.Fatalf("update interview stage: %v", err)
	}
	application, _ = store.applicationByID(application.ID)
	if application.Status != domain.ApplicationActive {
		t.Fatalf("passing a non-offer stage should keep application active: %#v", application)
	}

	offer, err := store.SaveApplicationStage(domain.ApplicationStageInput{ApplicationID: application.ID, Name: "Offer", Type: domain.StageOffer, Status: domain.StagePassed})
	if err != nil {
		t.Fatalf("save offer stage: %v", err)
	}
	application, _ = store.applicationByID(application.ID)
	if application.Status != domain.ApplicationOffer {
		t.Fatalf("passed offer should complete application: %#v", application)
	}

	if _, err := store.ReorderApplicationStages(application.ID, []string{offer.ID, interview.ID}); err != nil {
		t.Fatalf("reorder stages: %v", err)
	}
	application, _ = store.applicationByID(application.ID)
	if application.Status != domain.ApplicationActive {
		t.Fatalf("reordering should recalculate from the new final stage: %#v", application)
	}
	if err := store.DeleteApplicationStage(interview.ID); err != nil {
		t.Fatalf("delete latest stage: %v", err)
	}
	application, _ = store.applicationByID(application.ID)
	if application.Status != domain.ApplicationOffer {
		t.Fatalf("deleting latest stage should restore offer status: %#v", application)
	}

	page, err := store.ListApplications(domain.ApplicationFilter{Status: string(domain.ApplicationOffer), Page: 1, PageSize: 20})
	if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].CurrentStageName != "Offer" {
		t.Fatalf("application list should expose the derived state and final node: %#v, %v", page, err)
	}
	positionPage, err := store.ListPositionPage(domain.PositionFilter{Status: string(domain.PositionApplied), Page: 1, PageSize: 20})
	if err != nil || positionPage.Total != 1 || len(positionPage.Items) != 1 || positionPage.Items[0].Status != domain.PositionApplied {
		t.Fatalf("position list should paginate and expose the derived applied state: %#v, %v", positionPage, err)
	}
}

func TestStageTypeCatalogSupportsCustomTypesSafely(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "offer-atlas.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	defaults, err := store.ListStageTypes()
	if err != nil {
		t.Fatalf("list default stage types: %v", err)
	}
	if len(defaults) != len(domain.SystemStageTypes())+1 {
		t.Fatalf("expected stable system types plus legacy other, got %#v", defaults)
	}
	defaultNames := make(map[domain.StageType]string, len(defaults))
	for _, item := range defaults {
		defaultNames[item.ID] = item.Name
	}
	if defaultNames[domain.StageResumeScreening] != "简历筛选" || defaultNames[domain.StageWrittenTest] != "笔试" || defaultNames[domain.StageAssessment] != "测评" || defaultNames[domain.StageAIInterview] != "AI 面" || defaultNames[domain.StageFirstInterview] != "一面" || defaultNames[domain.StageSecondInterview] != "二面" || defaultNames[domain.StageThirdInterview] != "三面" || defaultNames[domain.StageFourthInterview] != "四面" || defaultNames[domain.StageHRInterview] != "HR 面" || defaultNames[domain.StageOffer] != "Offer" || defaultNames[domain.StageOther] != "其他" {
		t.Fatalf("unexpected default stage type catalog: %#v", defaultNames)
	}
	for _, item := range defaults {
		if domain.IsSystemStageType(item.ID) && !item.System {
			t.Fatalf("system stage type must be marked as system: %#v", item)
		}
	}
	if _, err := store.SaveStageType(domain.StageTypeInput{ID: domain.StageWrittenTest, Name: "改名"}); err == nil {
		t.Fatal("system stage type should not be editable")
	}
	if err := store.DeleteStageType(domain.StageWrittenTest); err == nil {
		t.Fatal("system stage type should not be deletable")
	}

	groupInterview, err := store.SaveStageType(domain.StageTypeInput{Name: "群面"})
	if err != nil {
		t.Fatalf("create custom stage type: %v", err)
	}
	if groupInterview.ID == "" || groupInterview.Name != "群面" {
		t.Fatalf("custom stage type was not created correctly: %#v", groupInterview)
	}

	company, err := store.SaveCompany(domain.CompanyInput{Name: "Stage Type Company"})
	if err != nil {
		t.Fatalf("save company: %v", err)
	}
	campaign, err := store.SaveCampaign(domain.CampaignInput{CompanyID: company.ID, Name: "Stage Type Campaign"})
	if err != nil {
		t.Fatalf("save campaign: %v", err)
	}
	position, err := store.SavePosition(domain.PositionInput{CampaignID: campaign.ID, Title: "Stage Type Position", Priority: 3})
	if err != nil {
		t.Fatalf("save position: %v", err)
	}
	application, err := store.SaveApplication(domain.ApplicationInput{PositionID: position.ID})
	if err != nil {
		t.Fatalf("save application: %v", err)
	}
	stage, err := store.SaveApplicationStage(domain.ApplicationStageInput{ApplicationID: application.ID, Name: "群面", Type: groupInterview.ID, Status: domain.StageScheduled})
	if err != nil {
		t.Fatalf("save stage with custom type: %v", err)
	}
	if stage.Type != groupInterview.ID {
		t.Fatalf("custom stage type was not retained by stage: %#v", stage)
	}

	renamed, err := store.SaveStageType(domain.StageTypeInput{ID: groupInterview.ID, Name: "小组面试"})
	if err != nil {
		t.Fatalf("rename custom stage type: %v", err)
	}
	if renamed.ID != groupInterview.ID || renamed.Name != "小组面试" {
		t.Fatalf("renaming must retain the stable type ID: before=%#v after=%#v", groupInterview, renamed)
	}
	storedStage, err := store.stageByID(stage.ID)
	if err != nil || storedStage.Type != groupInterview.ID {
		t.Fatalf("renaming a type must not invalidate existing stages: %#v, %v", storedStage, err)
	}
	if err := store.DeleteStageType(groupInterview.ID); err == nil {
		t.Fatal("deleting a stage type still used by an application stage should fail")
	}

	unused, err := store.SaveStageType(domain.StageTypeInput{Name: "交叉面"})
	if err != nil {
		t.Fatalf("create unused stage type: %v", err)
	}
	if err := store.DeleteStageType(unused.ID); err != nil {
		t.Fatalf("delete unused stage type: %v", err)
	}
	typesAfterDeletion, err := store.ListStageTypes()
	if err != nil {
		t.Fatalf("list stage types after deletion: %v", err)
	}
	for _, item := range typesAfterDeletion {
		if item.ID == unused.ID {
			t.Fatalf("unused stage type should have been removed: %#v", typesAfterDeletion)
		}
	}
}

func TestDashboardStatisticsAndStageFiltersUseDistinctApplications(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "offer-atlas.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	company, _ := store.SaveCompany(domain.CompanyInput{Name: "统计公司"})
	campaign, _ := store.SaveCampaign(domain.CampaignInput{CompanyID: company.ID, Name: "2027 秋招"})
	newApplication := func(title string) domain.Application {
		position, saveErr := store.SavePosition(domain.PositionInput{CampaignID: campaign.ID, Title: title, Priority: 3})
		if saveErr != nil {
			t.Fatalf("save position %s: %v", title, saveErr)
		}
		application, saveErr := store.SaveApplication(domain.ApplicationInput{PositionID: position.ID})
		if saveErr != nil {
			t.Fatalf("save application %s: %v", title, saveErr)
		}
		return application
	}
	first := newApplication("后端开发")
	second := newApplication("平台开发")
	third := newApplication("基础架构")
	fourth := newApplication("研发工程师")
	for _, input := range []domain.ApplicationStageInput{
		{ApplicationID: first.ID, Type: domain.StageResumeScreening, Status: domain.StagePassed},
		{ApplicationID: first.ID, Type: domain.StageWrittenTest, Status: domain.StagePassed},
		{ApplicationID: first.ID, Type: domain.StageFirstInterview, Status: domain.StagePassed},
		{ApplicationID: first.ID, Type: domain.StageSecondInterview, Status: domain.StageScheduled},
		{ApplicationID: second.ID, Type: domain.StageAssessment, Status: domain.StageFailed},
		{ApplicationID: third.ID, Type: domain.StageFirstInterview, Status: domain.StageFailed},
		{ApplicationID: fourth.ID, Type: domain.StageOffer, Status: domain.StagePassed},
	} {
		if _, err := store.SaveApplicationStage(input); err != nil {
			t.Fatalf("save process node %#v: %v", input, err)
		}
	}
	dashboard, err := store.Dashboard(time.Now().UTC())
	if err != nil {
		t.Fatalf("load dashboard: %v", err)
	}
	if dashboard.TotalApplications != 4 || dashboard.ActiveApplications != 1 || dashboard.OfferApplications != 1 || dashboard.RejectedApplications != 2 {
		t.Fatalf("unexpected application totals: %#v", dashboard)
	}
	if dashboard.ResumeScreeningStats.Entered != 1 || dashboard.ResumeScreeningStats.Passed != 1 || dashboard.WrittenTestStats.Entered != 1 || dashboard.WrittenTestStats.Passed != 1 || dashboard.AssessmentStats.Entered != 1 || dashboard.AssessmentStats.Failed != 1 {
		t.Fatalf("unexpected resume/written/assessment stats: %#v %#v %#v", dashboard.ResumeScreeningStats, dashboard.WrittenTestStats, dashboard.AssessmentStats)
	}
	if dashboard.InterviewedApplications != 2 {
		t.Fatalf("interviewed applications must be deduplicated, got %#v", dashboard)
	}
	firstRound := dashboard.InterviewStats[1]
	if firstRound.Type != domain.StageFirstInterview || firstRound.Entered != 2 || firstRound.Passed != 1 || firstRound.Failed != 1 {
		t.Fatalf("unexpected first interview stats: %#v", firstRound)
	}
	filtered, err := store.ListApplications(domain.ApplicationFilter{StageType: string(domain.StageFirstInterview), StageStatus: string(domain.StageFailed), Page: 1, PageSize: 20})
	if err != nil || filtered.Total != 1 || filtered.Items[0].ID != third.ID {
		t.Fatalf("stage filter should locate first-interview failures: %#v, %v", filtered, err)
	}
	entered, err := store.ListApplications(domain.ApplicationFilter{StageType: string(domain.StageFirstInterview), Page: 1, PageSize: 20})
	if err != nil || entered.Total != 2 {
		t.Fatalf("stage type filter should locate each entered application: %#v, %v", entered, err)
	}
	resumePassed, err := store.ListApplications(domain.ApplicationFilter{StageType: string(domain.StageResumeScreening), StageStatus: string(domain.StagePassed), Page: 1, PageSize: 20})
	if err != nil || resumePassed.Total != 1 || resumePassed.Items[0].ID != first.ID {
		t.Fatalf("resume screening filter should locate the selected application: %#v, %v", resumePassed, err)
	}
	if _, err := store.SaveApplicationStage(domain.ApplicationStageInput{ApplicationID: first.ID, Type: domain.StageResumeScreening, Status: domain.StageScheduled, ScheduledStart: "2026-08-25T19:00"}); err == nil {
		t.Fatal("resume screening must not accept calendar times")
	}
}

func TestPositionTableHasNoManualStatusColumns(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "offer-atlas.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()
	rows, err := store.db.Query(`PRAGMA table_info(positions)`)
	if err != nil {
		t.Fatalf("inspect positions table: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull, primaryKey int
		var defaultValue any
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			t.Fatalf("scan table info: %v", err)
		}
		if name == "status" || name == "decision_status" {
			t.Fatalf("manual position status column should not exist: %s", name)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("inspect positions table: %v", err)
	}
}

func TestPositionSourceURLMigrationPreservesExistingPositions(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "offer-atlas.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatalf("open current store: %v", err)
	}
	company, err := store.SaveCompany(domain.CompanyInput{Name: "Migration Company"})
	if err != nil {
		store.Close()
		t.Fatalf("save company: %v", err)
	}
	campaign, err := store.SaveCampaign(domain.CampaignInput{CompanyID: company.ID, Name: "Migration Campaign"})
	if err != nil {
		store.Close()
		t.Fatalf("save campaign: %v", err)
	}
	position, err := store.SavePosition(domain.PositionInput{CampaignID: campaign.ID, Title: "Existing Position"})
	if err != nil {
		store.Close()
		t.Fatalf("save position: %v", err)
	}
	if _, err := store.db.Exec(`ALTER TABLE positions DROP COLUMN source_url; DELETE FROM schema_migrations WHERE version=7`); err != nil {
		store.Close()
		t.Fatalf("downgrade fixture to version 6: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close version 6 fixture: %v", err)
	}

	upgraded, err := Open(databasePath)
	if err != nil {
		t.Fatalf("open version 6 fixture: %v", err)
	}
	defer upgraded.Close()
	updated, err := upgraded.SavePosition(domain.PositionInput{ID: position.ID, CampaignID: campaign.ID, Title: position.Title, SourceURL: "https://jobs.example.com/existing-position"})
	if err != nil {
		t.Fatalf("save position after migration: %v", err)
	}
	if updated.SourceURL != "https://jobs.example.com/existing-position" {
		t.Fatalf("position source URL was not stored after migration: %#v", updated)
	}
	if _, err := upgraded.GetPositionDetail(position.ID); err != nil {
		t.Fatalf("existing position was not preserved: %v", err)
	}
}

func TestReusableResumeLifecycleAndBackup(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "offer-atlas.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer store.Close()

	company, err := store.SaveCompany(domain.CompanyInput{Name: "简历附件公司"})
	if err != nil {
		t.Fatalf("save company: %v", err)
	}
	campaign, err := store.SaveCampaign(domain.CampaignInput{CompanyID: company.ID, Name: "2027 秋招"})
	if err != nil {
		t.Fatalf("save campaign: %v", err)
	}
	position, err := store.SavePosition(domain.PositionInput{CampaignID: campaign.ID, Title: "后端开发工程师", Priority: 4})
	if err != nil {
		t.Fatalf("save position: %v", err)
	}
	application, err := store.SaveApplication(domain.ApplicationInput{PositionID: position.ID, ResumeName: "后端 v3"})
	if err != nil {
		t.Fatalf("save application: %v", err)
	}

	first, err := store.ImportApplicationResumeData(application.ID, "后端-v3.pdf", "application/pdf", []byte("first resume"))
	if err != nil {
		t.Fatalf("save first resume: %v", err)
	}
	firstPath, err := store.ApplicationResumePath(application.ID)
	if err != nil {
		t.Fatalf("locate first resume: %v", err)
	}
	if contents, readErr := os.ReadFile(firstPath); readErr != nil || string(contents) != "first resume" {
		t.Fatalf("unexpected first resume contents: %q, %v", contents, readErr)
	}

	second, err := store.ImportApplicationResumeData(application.ID, "后端-v4.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", []byte("second resume"))
	if err != nil {
		t.Fatalf("select replacement resume: %v", err)
	}
	if second.OriginalName != "后端-v4.docx" || second.StoredName == first.StoredName {
		t.Fatalf("resume selection did not create the expected version: %#v", second)
	}
	if _, statErr := os.Stat(firstPath); statErr != nil {
		t.Fatalf("previous library version should remain available, got %v", statErr)
	}
	detail, err := store.GetApplicationDetail(application.ID)
	if err != nil || detail.Resume == nil || detail.Resume.OriginalName != second.OriginalName {
		t.Fatalf("application detail should expose its resume: %#v, %v", detail.Resume, err)
	}
	positionDetail, err := store.GetPositionDetail(position.ID)
	if err != nil || positionDetail.Resume == nil || positionDetail.Resume.ID != second.ID {
		t.Fatalf("position detail should expose its application's resume: %#v, %v", positionDetail.Resume, err)
	}
	secondPosition, err := store.SavePosition(domain.PositionInput{CampaignID: campaign.ID, Title: "后端开发工程师（第二个岗位）", Priority: 3})
	if err != nil {
		t.Fatal(err)
	}
	secondApplication, err := store.SaveApplication(domain.ApplicationInput{PositionID: secondPosition.ID, ResumeID: second.ID})
	if err != nil {
		t.Fatal(err)
	}
	versions, err := store.ListResumes(true)
	if err != nil {
		t.Fatal(err)
	}
	var shared *domain.Resume
	for index := range versions {
		if versions[index].ID == second.ID {
			shared = &versions[index]
			break
		}
	}
	if shared == nil || shared.UsageCount != 2 {
		t.Fatalf("one library version should be shared by both applications: %#v", shared)
	}
	if err := store.DeleteResume(second.ID); err == nil {
		t.Fatal("a resume used by applications must be archived instead of deleted")
	}
	if err := store.ClearApplicationResume(secondApplication.ID); err != nil {
		t.Fatal(err)
	}

	backupPath, err := store.CreateBackup()
	if err != nil {
		t.Fatalf("create backup: %v", err)
	}
	backupResume := filepath.Join(filepath.Dir(backupPath), "attachments", "resumes", second.StoredName)
	if contents, readErr := os.ReadFile(backupResume); readErr != nil || string(contents) != "second resume" {
		t.Fatalf("backup should contain the application resume: %q, %v", contents, readErr)
	}

	if err := store.DeleteEntity(domain.DeleteInput{EntityType: domain.DeletionTargetApplication, ID: application.ID, ConfirmationText: position.Title}); err != nil {
		t.Fatalf("delete application: %v", err)
	}
	if _, err := store.ApplicationResumePath(application.ID); err == nil {
		t.Fatal("deleted application resume should no longer be accessible")
	}
	versions, err = store.ListResumes(true)
	if err != nil || len(versions) != 2 {
		t.Fatalf("deleting an application must retain reusable resume versions: %#v, %v", versions, err)
	}
	if err := store.DeleteResume(second.ID); err != nil {
		t.Fatalf("unused library resume should be deletable: %v", err)
	}
	if _, err := store.ResumePath(second.ID); err == nil {
		t.Fatal("deleted unused resume should not remain accessible")
	}
}

func TestListApplicationsFiltersByResume(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "offer-atlas.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	company, err := store.SaveCompany(domain.CompanyInput{Name: "简历筛选公司"})
	if err != nil {
		t.Fatal(err)
	}
	campaign, err := store.SaveCampaign(domain.CampaignInput{CompanyID: company.ID, Name: "2027 秋招"})
	if err != nil {
		t.Fatal(err)
	}
	resume, err := store.ImportResumeData("后端开发 v3", "backend-v3.pdf", "application/pdf", []byte("resume-v3"))
	if err != nil {
		t.Fatal(err)
	}
	createApplication := func(title string, resumeID string) domain.Application {
		position, saveErr := store.SavePosition(domain.PositionInput{CampaignID: campaign.ID, Title: title, Priority: 3})
		if saveErr != nil {
			t.Fatal(saveErr)
		}
		application, saveErr := store.SaveApplication(domain.ApplicationInput{PositionID: position.ID, ResumeID: resumeID})
		if saveErr != nil {
			t.Fatal(saveErr)
		}
		return application
	}
	first := createApplication("平台研发", resume.ID)
	second := createApplication("基础架构", resume.ID)
	third := createApplication("数据研发", "")
	if _, err := store.SaveApplicationStage(domain.ApplicationStageInput{ApplicationID: first.ID, Type: domain.StageFirstInterview, Status: domain.StagePassed}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveApplicationStage(domain.ApplicationStageInput{ApplicationID: second.ID, Type: domain.StageFirstInterview, Status: domain.StageFailed}); err != nil {
		t.Fatal(err)
	}

	page, err := store.ListApplications(domain.ApplicationFilter{ResumeID: resume.ID, Page: 1, PageSize: 20})
	if err != nil || page.Total != 2 {
		t.Fatalf("resume filter should return both linked applications: %#v, %v", page, err)
	}
	page, err = store.ListApplications(domain.ApplicationFilter{
		Status:      string(domain.ApplicationActive),
		StageType:   string(domain.StageFirstInterview),
		StageStatus: string(domain.StagePassed),
		ResumeID:    resume.ID,
		Page:        1,
		PageSize:    20,
	})
	if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != first.ID {
		t.Fatalf("resume and stage filters should combine as an intersection: %#v, %v", page, err)
	}
	page, err = store.ListApplications(domain.ApplicationFilter{ResumeID: "__none__", Page: 1, PageSize: 20})
	if err != nil || page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != third.ID {
		t.Fatalf("unlinked-resume filter should return only applications without a resume: %#v, %v", page, err)
	}
}

func TestLegacyApplicationResumeMigratesToReusableLibrary(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "legacy-resume.db")
	store, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	company, _ := store.SaveCompany(domain.CompanyInput{Name: "旧简历迁移公司"})
	campaign, _ := store.SaveCampaign(domain.CampaignInput{CompanyID: company.ID, Name: "2027 秋招"})
	position, _ := store.SavePosition(domain.PositionInput{CampaignID: campaign.ID, Title: "平台研发", Priority: 3})
	application, err := store.SaveApplication(domain.ApplicationInput{PositionID: position.ID, ResumeName: "后端 v3"})
	if err != nil {
		t.Fatal(err)
	}
	legacyPath, err := store.applicationResumePath(application.ID, "legacy-v3.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(legacyPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyPath, []byte("legacy-resume-contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`INSERT INTO application_resumes(id, application_id, original_name, stored_name, mime_type, size_bytes, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`, uuid.NewString(), application.ID, "legacy-v3.pdf", "legacy-v3.pdf", "application/pdf", len("legacy-resume-contents"), nowString()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	migrated, err := Open(databasePath)
	if err != nil {
		t.Fatal(err)
	}
	defer migrated.Close()
	detail, err := migrated.GetApplicationDetail(application.ID)
	if err != nil || detail.Resume == nil || detail.Application.ResumeID == "" || detail.Resume.Name != "后端 v3" {
		t.Fatalf("legacy resume was not migrated into the library: %#v, %v", detail, err)
	}
	path, err := migrated.ResumePath(detail.Resume.ID)
	if err != nil {
		t.Fatal(err)
	}
	if contents, err := os.ReadFile(path); err != nil || string(contents) != "legacy-resume-contents" {
		t.Fatalf("migrated resume file mismatch: %q, %v", contents, err)
	}
	var legacyRows int
	if err := migrated.db.QueryRow(`SELECT COUNT(*) FROM application_resumes WHERE application_id=?`, application.ID).Scan(&legacyRows); err != nil || legacyRows != 0 {
		t.Fatalf("legacy metadata should be removed after migration: %d, %v", legacyRows, err)
	}
}

func TestProtectedEntityDeletion(t *testing.T) {
	t.Run("application", func(t *testing.T) {
		fixture := newDeletionFixture(t)
		preview, err := fixture.store.PreviewDeletion(domain.DeleteInput{EntityType: domain.DeletionTargetApplication, ID: fixture.application.ID})
		if err != nil || preview.ApplicationCount != 1 || preview.StageCount != 1 {
			t.Fatalf("unexpected application deletion preview: %#v, %v", preview, err)
		}
		if err := fixture.store.DeleteEntity(domain.DeleteInput{EntityType: domain.DeletionTargetApplication, ID: fixture.application.ID, ConfirmationText: fixture.position.Title}); err != nil {
			t.Fatalf("delete application: %v", err)
		}
		detail, err := fixture.store.GetPositionDetail(fixture.position.ID)
		if err != nil || detail.Application != nil || detail.Status != domain.PositionUnapplied {
			t.Fatalf("position should return to unapplied after deleting application: %#v, %v", detail, err)
		}
	})

	t.Run("position", func(t *testing.T) {
		fixture := newDeletionFixture(t)
		preview, err := fixture.store.PreviewDeletion(domain.DeleteInput{EntityType: domain.DeletionTargetPosition, ID: fixture.position.ID})
		if err != nil || preview.PositionCount != 1 || preview.ApplicationCount != 1 || preview.StageCount != 1 {
			t.Fatalf("unexpected position deletion preview: %#v, %v", preview, err)
		}
		if err := fixture.store.DeleteEntity(domain.DeleteInput{EntityType: domain.DeletionTargetPosition, ID: fixture.position.ID, ConfirmationText: "wrong"}); err == nil {
			t.Fatal("position deletion should require the exact confirmation text")
		}
		if err := fixture.store.DeleteEntity(domain.DeleteInput{EntityType: domain.DeletionTargetPosition, ID: fixture.position.ID, ConfirmationText: fixture.position.Title}); err != nil {
			t.Fatalf("delete position: %v", err)
		}
		positions, err := fixture.store.ListPositions(domain.PositionFilter{})
		if err != nil || len(positions) != 0 {
			t.Fatalf("position was not deleted: %#v, %v", positions, err)
		}
	})

	t.Run("campaign", func(t *testing.T) {
		fixture := newDeletionFixture(t)
		preview, err := fixture.store.PreviewDeletion(domain.DeleteInput{EntityType: domain.DeletionTargetCampaign, ID: fixture.campaign.ID})
		if err != nil || preview.CampaignCount != 1 || preview.PositionCount != 1 || preview.ApplicationCount != 1 || preview.StageCount != 1 {
			t.Fatalf("unexpected campaign deletion preview: %#v, %v", preview, err)
		}
		if err := fixture.store.DeleteEntity(domain.DeleteInput{EntityType: domain.DeletionTargetCampaign, ID: fixture.campaign.ID, ConfirmationText: fixture.campaign.Name}); err != nil {
			t.Fatalf("delete campaign: %v", err)
		}
		campaigns, err := fixture.store.ListCampaigns(fixture.company.ID)
		if err != nil || len(campaigns) != 0 {
			t.Fatalf("campaign was not deleted: %#v, %v", campaigns, err)
		}
	})

	t.Run("company", func(t *testing.T) {
		fixture := newDeletionFixture(t)
		preview, err := fixture.store.PreviewDeletion(domain.DeleteInput{EntityType: domain.DeletionTargetCompany, ID: fixture.company.ID})
		if err != nil || preview.CampaignCount != 1 || preview.PositionCount != 1 || preview.ApplicationCount != 1 || preview.StageCount != 1 {
			t.Fatalf("unexpected company deletion preview: %#v, %v", preview, err)
		}
		if err := fixture.store.DeleteEntity(domain.DeleteInput{EntityType: domain.DeletionTargetCompany, ID: fixture.company.ID, ConfirmationText: fixture.company.Name}); err != nil {
			t.Fatalf("delete company: %v", err)
		}
		companies, err := fixture.store.ListCompanies()
		if err != nil || len(companies) != 0 {
			t.Fatalf("company tree was not deleted: %#v, %v", companies, err)
		}
		health, err := fixture.store.Health()
		if err != nil {
			t.Fatalf("read safety status after deletion: %v", err)
		}
		workbook, err := excelize.OpenFile(filepath.Join(health.Safety.LatestSnapshot, readableMirrorWorkbookName))
		if err != nil {
			t.Fatalf("open latest safety mirror after deletion: %v", err)
		}
		defer workbook.Close()
		if mirrorWorkbookContains(workbook, fixture.company.Name) {
			t.Fatalf("latest safety mirror still contains deleted company %q", fixture.company.Name)
		}
	})
}

func TestRelatedMaterialsPersistBackupAndFollowTheirOwners(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "related-materials.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	company, err := store.SaveCompany(domain.CompanyInput{Name: "资料测试公司"})
	if err != nil {
		t.Fatal(err)
	}
	campaign, err := store.SaveCampaign(domain.CampaignInput{CompanyID: company.ID, Name: "2027 秋招"})
	if err != nil {
		t.Fatal(err)
	}
	position, err := store.SavePosition(domain.PositionInput{CampaignID: campaign.ID, Title: "资料测试岗位", Priority: 3})
	if err != nil {
		t.Fatal(err)
	}
	application, err := store.SaveApplication(domain.ApplicationInput{PositionID: position.ID})
	if err != nil {
		t.Fatal(err)
	}
	stage, err := store.SaveApplicationStage(domain.ApplicationStageInput{ApplicationID: application.ID, Content: "技术面", Type: domain.StageFirstInterview, Status: domain.StageScheduled})
	if err != nil {
		t.Fatal(err)
	}

	for _, owner := range []struct {
		typeID domain.ResourceOwnerType
		id     string
	}{
		{domain.ResourceOwnerCompany, company.ID}, {domain.ResourceOwnerCampaign, campaign.ID}, {domain.ResourceOwnerPosition, position.ID}, {domain.ResourceOwnerApplication, application.ID}, {domain.ResourceOwnerStage, stage.ID},
	} {
		links, saveErr := store.SaveResourceLinks(owner.typeID, owner.id, []domain.ResourceLinkInput{{Name: "官方入口", URL: "https://jobs.example.com/process"}})
		if saveErr != nil || len(links) != 1 || links[0].OwnerType != owner.typeID {
			t.Fatalf("save links for %s: %#v, %v", owner.typeID, links, saveErr)
		}
	}
	if _, err := store.SaveResourceLinks(domain.ResourceOwnerCompany, company.ID, []domain.ResourceLinkInput{{Name: "bad", URL: "file:///local"}}); err == nil {
		t.Fatal("non-http related link must be rejected")
	}

	for _, owner := range []struct {
		typeID domain.ResourceOwnerType
		id     string
	}{
		{domain.ResourceOwnerCompany, company.ID}, {domain.ResourceOwnerCampaign, campaign.ID}, {domain.ResourceOwnerApplication, application.ID}, {domain.ResourceOwnerStage, stage.ID},
	} {
		items, importErr := store.ImportSupplementalAttachmentData(owner.typeID, owner.id, "reference.txt", "text/plain", []byte("related-material"))
		if importErr != nil || len(items) != 1 {
			t.Fatalf("save supplemental attachment for %s: %#v, %v", owner.typeID, items, importErr)
		}
	}
	if _, err := store.ImportSupplementalAttachmentData(domain.ResourceOwnerPosition, position.ID, "not-allowed.txt", "text/plain", []byte("x")); err == nil {
		t.Fatal("position must keep its existing attachment collection")
	}

	companyDetail, err := store.GetCompanyDetail(company.ID)
	if err != nil || companyDetail.CampaignCount != 1 || len(companyDetail.Campaigns) != 1 || len(companyDetail.Links) != 1 || len(companyDetail.Attachments) != 1 {
		t.Fatalf("unexpected company detail: %#v, %v", companyDetail, err)
	}
	applicationDetail, err := store.GetApplicationDetail(application.ID)
	if err != nil || len(applicationDetail.Links) != 1 || len(applicationDetail.Attachments) != 1 || len(applicationDetail.Stages) != 1 || len(applicationDetail.Stages[0].Links) != 1 || len(applicationDetail.Stages[0].Attachments) != 1 {
		t.Fatalf("unexpected application detail: %#v, %v", applicationDetail, err)
	}
	stats, err := store.DirectoryStats()
	if err != nil || stats.CompanyCount != 1 || stats.CampaignCount != 1 || stats.PositionCount != 1 || stats.ApplicationCount != 1 {
		t.Fatalf("unexpected directory stats: %#v, %v", stats, err)
	}
	backup, err := store.CreateBackup()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(backup), "attachments", "resources", "company", company.ID, companyDetail.Attachments[0].StoredName)); err != nil {
		t.Fatalf("company supplemental attachment was not copied to backup: %v", err)
	}

	applicationAttachment := applicationDetail.Attachments[0]
	applicationPath, err := store.SupplementalAttachmentPath(applicationAttachment.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.DeleteEntity(domain.DeleteInput{EntityType: domain.DeletionTargetApplication, ID: application.ID, ConfirmationText: position.Title}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(applicationPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("application supplemental attachment should be removed with its owner: %v", err)
	}
	var remaining int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM resource_links WHERE owner_type IN ('application', 'stage')`).Scan(&remaining); err != nil || remaining != 0 {
		t.Fatalf("application and stage links should be removed with the application: %d, %v", remaining, err)
	}
}

type deletionFixture struct {
	store       *Store
	company     domain.Company
	campaign    domain.Campaign
	position    domain.Position
	application domain.Application
}

func mirrorWorkbookContains(workbook *excelize.File, target string) bool {
	for _, sheet := range workbook.GetSheetList() {
		rows, err := workbook.GetRows(sheet)
		if err != nil {
			continue
		}
		for _, row := range rows {
			for _, value := range row {
				if strings.Contains(value, target) {
					return true
				}
			}
		}
	}
	return false
}

func newDeletionFixture(t *testing.T) deletionFixture {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "offer-atlas.db"))
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	company, err := store.SaveCompany(domain.CompanyInput{Name: "Deletion Company"})
	if err != nil {
		t.Fatalf("save company: %v", err)
	}
	campaign, err := store.SaveCampaign(domain.CampaignInput{CompanyID: company.ID, Name: "Deletion Campaign"})
	if err != nil {
		t.Fatalf("save campaign: %v", err)
	}
	position, err := store.SavePosition(domain.PositionInput{CampaignID: campaign.ID, Title: "Deletion Position", Priority: 3})
	if err != nil {
		t.Fatalf("save position: %v", err)
	}
	application, err := store.SaveApplication(domain.ApplicationInput{PositionID: position.ID})
	if err != nil {
		t.Fatalf("save application: %v", err)
	}
	if _, err := store.SaveApplicationStage(domain.ApplicationStageInput{ApplicationID: application.ID, Name: "Deletion Stage", Type: domain.StageInterview, Status: domain.StageScheduled}); err != nil {
		t.Fatalf("save stage: %v", err)
	}
	return deletionFixture{store: store, company: company, campaign: campaign, position: position, application: application}
}
