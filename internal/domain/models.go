package domain

import "time"

type PositionStatus string

const (
	PositionUnapplied PositionStatus = "unapplied"
	PositionApplied   PositionStatus = "applied"
)

func ValidPositionStatus(value PositionStatus) bool {
	return value == PositionUnapplied || value == PositionApplied
}

type ApplicationStatus string

const (
	ApplicationActive   ApplicationStatus = "active"
	ApplicationOffer    ApplicationStatus = "offer"
	ApplicationRejected ApplicationStatus = "rejected"
)

func ValidApplicationStatus(value ApplicationStatus) bool {
	switch value {
	case ApplicationActive, ApplicationOffer, ApplicationRejected:
		return true
	default:
		return false
	}
}

type StageType string

const (
	StageResumeScreening StageType = "resume_screening"
	StageWrittenTest     StageType = "written_test"
	StageAssessment      StageType = "assessment"
	StageAIInterview     StageType = "ai_interview"
	StageFirstInterview  StageType = "first_interview"
	StageSecondInterview StageType = "second_interview"
	StageThirdInterview  StageType = "third_interview"
	StageFourthInterview StageType = "fourth_interview"
	StageHRInterview     StageType = "hr_interview"
	StageOffer           StageType = "offer"
	// Deprecated aliases kept while development fixtures move to the stable
	// system type catalog. They are not exposed as selectable system types.
	StageInterview StageType = StageFirstInterview
	StageOther     StageType = "other"
)

// SystemStageTypes are stable, product-owned types. They are used consistently
// in recording, filtering, and dashboard statistics.
func SystemStageTypes() []StageType {
	return []StageType{
		StageResumeScreening, StageWrittenTest, StageAssessment, StageAIInterview,
		StageFirstInterview, StageSecondInterview, StageThirdInterview,
		StageFourthInterview, StageHRInterview, StageOffer,
	}
}

// IsUnscheduledStageType identifies record-only nodes. These nodes carry a
// manually maintained result but never create calendar or todo occurrences.
func IsUnscheduledStageType(value StageType) bool {
	return value == StageResumeScreening
}

func IsSystemStageType(value StageType) bool {
	for _, stageType := range SystemStageTypes() {
		if value == stageType {
			return true
		}
	}
	return false
}

func IsCountedStageType(value StageType) bool { return IsSystemStageType(value) }

func IsInterviewStageType(value StageType) bool {
	switch value {
	case StageAIInterview, StageFirstInterview, StageSecondInterview, StageThirdInterview, StageFourthInterview, StageHRInterview:
		return true
	default:
		return false
	}
}

func ValidStageType(value StageType) bool {
	return value != ""
}

type StageStatus string

const (
	StageScheduled StageStatus = "scheduled"
	// StageAttended is retained only to decode pre-v11 records during migration.
	// It is no longer a valid user-facing node state.
	StageAttended StageStatus = "attended"
	StagePassed   StageStatus = "passed"
	StageFailed   StageStatus = "failed"
)

func ValidStageStatus(value StageStatus) bool {
	switch value {
	case StageScheduled, StagePassed, StageFailed:
		return true
	default:
		return false
	}
}

// ResourceOwnerType identifies an object that can carry user-managed related
// links. Supplemental attachments are available on every level except a
// position, which retains its established position-attachment collection.
type ResourceOwnerType string

const (
	ResourceOwnerCompany     ResourceOwnerType = "company"
	ResourceOwnerCampaign    ResourceOwnerType = "campaign"
	ResourceOwnerPosition    ResourceOwnerType = "position"
	ResourceOwnerApplication ResourceOwnerType = "application"
	ResourceOwnerStage       ResourceOwnerType = "stage"
)

func ValidResourceOwnerType(value ResourceOwnerType) bool {
	switch value {
	case ResourceOwnerCompany, ResourceOwnerCampaign, ResourceOwnerPosition, ResourceOwnerApplication, ResourceOwnerStage:
		return true
	default:
		return false
	}
}

func SupportsSupplementalAttachments(value ResourceOwnerType) bool {
	return value == ResourceOwnerCompany || value == ResourceOwnerCampaign || value == ResourceOwnerApplication || value == ResourceOwnerStage
}

type ResourceLink struct {
	ID        string            `json:"id"`
	OwnerType ResourceOwnerType `json:"ownerType"`
	OwnerID   string            `json:"ownerId"`
	Name      string            `json:"name"`
	URL       string            `json:"url"`
	SortOrder int               `json:"sortOrder"`
	CreatedAt time.Time         `json:"createdAt"`
	UpdatedAt time.Time         `json:"updatedAt"`
}

type ResourceLinkInput struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	URL  string `json:"url"`
}

// SupplementalAttachment stores screenshots and files attached to a company,
// campaign, application, or process stage. Position attachments deliberately
// remain in the legacy position collection for full data compatibility.
type SupplementalAttachment struct {
	ID           string            `json:"id"`
	OwnerType    ResourceOwnerType `json:"ownerType"`
	OwnerID      string            `json:"ownerId"`
	OriginalName string            `json:"originalName"`
	StoredName   string            `json:"storedName"`
	MIMEType     string            `json:"mimeType"`
	SizeBytes    int64             `json:"sizeBytes"`
	CreatedAt    time.Time         `json:"createdAt"`
}

type Company struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Industry  string    `json:"industry"`
	Homepage  string    `json:"homepage"`
	Notes     string    `json:"notes"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type CompanyInput struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Industry string `json:"industry"`
	Homepage string `json:"homepage"`
	Notes    string `json:"notes"`
}

type Campaign struct {
	ID              string     `json:"id"`
	CompanyID       string     `json:"companyId"`
	Name            string     `json:"name"`
	OpensOn         *time.Time `json:"opensOn"`
	ClosesOn        *time.Time `json:"closesOn"`
	SourceURL       string     `json:"sourceUrl"`
	LastVerifiedOn  *time.Time `json:"lastVerifiedOn"`
	ProcessOverview string     `json:"processOverview"`
	Notes           string     `json:"notes"`
	CreatedAt       time.Time  `json:"createdAt"`
	UpdatedAt       time.Time  `json:"updatedAt"`
}

type CampaignInput struct {
	ID              string `json:"id"`
	CompanyID       string `json:"companyId"`
	Name            string `json:"name"`
	OpensOn         string `json:"opensOn"`
	ClosesOn        string `json:"closesOn"`
	SourceURL       string `json:"sourceUrl"`
	LastVerifiedOn  string `json:"lastVerifiedOn"`
	ProcessOverview string `json:"processOverview"`
	Notes           string `json:"notes"`
}

type Position struct {
	ID         string    `json:"id"`
	CampaignID string    `json:"campaignId"`
	Title      string    `json:"title"`
	JobCode    string    `json:"jobCode"`
	Department string    `json:"department"`
	Location   string    `json:"location"`
	Track      string    `json:"track"`
	SourceURL  string    `json:"sourceUrl"`
	Priority   int       `json:"priority"`
	Notes      string    `json:"notes"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

type PositionInput struct {
	ID         string `json:"id"`
	CampaignID string `json:"campaignId"`
	Title      string `json:"title"`
	JobCode    string `json:"jobCode"`
	Department string `json:"department"`
	Location   string `json:"location"`
	Track      string `json:"track"`
	SourceURL  string `json:"sourceUrl"`
	Priority   int    `json:"priority"`
	Notes      string `json:"notes"`
}

// QuickCapturePositionInput records the company, recruitment campaign, and
// position in one operation. Exact company/campaign names reuse existing data.
type QuickCapturePositionInput struct {
	CompanyName             string `json:"companyName"`
	CompanyIndustry         string `json:"companyIndustry"`
	CompanyHomepage         string `json:"companyHomepage"`
	CompanyNotes            string `json:"companyNotes"`
	CampaignName            string `json:"campaignName"`
	CampaignOpensOn         string `json:"campaignOpensOn"`
	CampaignClosesOn        string `json:"campaignClosesOn"`
	CampaignSourceURL       string `json:"campaignSourceUrl"`
	CampaignLastVerifiedOn  string `json:"campaignLastVerifiedOn"`
	CampaignProcessOverview string `json:"campaignProcessOverview"`
	CampaignNotes           string `json:"campaignNotes"`
	Title                   string `json:"title"`
	JobCode                 string `json:"jobCode"`
	Department              string `json:"department"`
	Location                string `json:"location"`
	Track                   string `json:"track"`
	SourceURL               string `json:"sourceUrl"`
	Priority                int    `json:"priority"`
	Notes                   string `json:"notes"`
}

type Application struct {
	ID          string            `json:"id"`
	PositionID  string            `json:"positionId"`
	Status      ApplicationStatus `json:"status"`
	SubmittedOn *time.Time        `json:"submittedOn"`
	Channel     string            `json:"channel"`
	ResumeID    string            `json:"resumeId"`
	ResumeName  string            `json:"resumeName"`
	Notes       string            `json:"notes"`
	CreatedAt   time.Time         `json:"createdAt"`
	UpdatedAt   time.Time         `json:"updatedAt"`
}

type ApplicationInput struct {
	ID          string `json:"id"`
	PositionID  string `json:"positionId"`
	SubmittedOn string `json:"submittedOn"`
	Channel     string `json:"channel"`
	ResumeID    string `json:"resumeId"`
	ResumeName  string `json:"resumeName"`
	Notes       string `json:"notes"`
}

// Resume is a reusable resume version. One version may be associated with
// many applications, while an archived version remains available to preserve
// historical application records.
type Resume struct {
	ID           string    `json:"id"`
	Name         string    `json:"name"`
	OriginalName string    `json:"originalName"`
	StoredName   string    `json:"storedName"`
	MIMEType     string    `json:"mimeType"`
	SizeBytes    int64     `json:"sizeBytes"`
	ContentHash  string    `json:"contentHash"`
	Archived     bool      `json:"archived"`
	UsageCount   int       `json:"usageCount"`
	CreatedAt    time.Time `json:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt"`
}

type ResumeInput struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Archived bool   `json:"archived"`
}

// ApplicationResume is the single, managed copy of the resume used for one
// application. It intentionally belongs to the application rather than the
// position, so the source document remains unambiguous in later reviews.
type ApplicationResume struct {
	ID            string    `json:"id"`
	ApplicationID string    `json:"applicationId"`
	OriginalName  string    `json:"originalName"`
	StoredName    string    `json:"storedName"`
	MIMEType      string    `json:"mimeType"`
	SizeBytes     int64     `json:"sizeBytes"`
	CreatedAt     time.Time `json:"createdAt"`
}

type ApplicationStage struct {
	ID             string                   `json:"id"`
	ApplicationID  string                   `json:"applicationId"`
	SortOrder      int                      `json:"sortOrder"`
	Content        string                   `json:"content"`
	Name           string                   `json:"-"`
	Type           StageType                `json:"type"`
	Status         StageStatus              `json:"status"`
	ScheduledStart *time.Time               `json:"scheduledStart"`
	ScheduledEnd   *time.Time               `json:"scheduledEnd"`
	ResultAt       *time.Time               `json:"resultAt"`
	SourceURL      string                   `json:"sourceUrl"`
	Notes          string                   `json:"notes"`
	CreatedAt      time.Time                `json:"createdAt"`
	UpdatedAt      time.Time                `json:"updatedAt"`
	Links          []ResourceLink           `json:"links"`
	Attachments    []SupplementalAttachment `json:"attachments"`
}

type ApplicationStageInput struct {
	ID             string      `json:"id"`
	ApplicationID  string      `json:"applicationId"`
	SortOrder      int         `json:"sortOrder"`
	Content        string      `json:"content"`
	Name           string      `json:"-"`
	Type           StageType   `json:"type"`
	Status         StageStatus `json:"status"`
	ScheduledStart string      `json:"scheduledStart"`
	ScheduledEnd   string      `json:"scheduledEnd"`
	ResultAt       string      `json:"resultAt"`
	SourceURL      string      `json:"sourceUrl"`
	Notes          string      `json:"notes"`
}

// StageTypeDefinition keeps the stable type ID separate from its editable
// display name, so renaming a type never invalidates existing stage records.
type StageTypeDefinition struct {
	ID        StageType `json:"id"`
	Name      string    `json:"name"`
	System    bool      `json:"system"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type StageTypeInput struct {
	ID   StageType `json:"id"`
	Name string    `json:"name"`
}

type ScheduleFilter struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// ScheduleItem is one scheduled occurrence derived from an application stage.
// A result notification is deliberately represented as its own occurrence so it
// can appear in the calendar and Todo list while remaining linked to the stage.
type ScheduleItem struct {
	ID                string            `json:"id"`
	StageID           string            `json:"stageId"`
	ApplicationID     string            `json:"applicationId"`
	PositionID        string            `json:"positionId"`
	CompanyName       string            `json:"companyName"`
	CampaignName      string            `json:"campaignName"`
	PositionTitle     string            `json:"positionTitle"`
	ApplicationStatus ApplicationStatus `json:"applicationStatus"`
	Name              string            `json:"name"`
	Type              StageType         `json:"type"`
	Status            StageStatus       `json:"status"`
	Kind              string            `json:"kind"`
	StartsAt          time.Time         `json:"startsAt"`
	EndsAt            *time.Time        `json:"endsAt"`
	IsCompleted       bool              `json:"isCompleted"`
	IsOverdue         bool              `json:"isOverdue"`
	IsWaitingResult   bool              `json:"isWaitingResult"`
}

type PositionFilter struct {
	Status    string `json:"status"`
	Query     string `json:"query"`
	SortBy    string `json:"sortBy"`
	SortOrder string `json:"sortOrder"`
	Page      int    `json:"page"`
	PageSize  int    `json:"pageSize"`
}

type PositionSummary struct {
	Position
	CompanyName        string            `json:"companyName"`
	CampaignName       string            `json:"campaignName"`
	Status             PositionStatus    `json:"status"`
	ApplicationID      string            `json:"applicationId"`
	ApplicationStatus  ApplicationStatus `json:"applicationStatus"`
	SubmittedOn        string            `json:"submittedOn"`
	CurrentStageName   string            `json:"currentStageName"`
	CurrentStageType   StageType         `json:"currentStageType"`
	CurrentStageStatus StageStatus       `json:"currentStageStatus"`
	StageCount         int               `json:"stageCount"`
}

type PositionDetail struct {
	Position    Position             `json:"position"`
	Status      PositionStatus       `json:"status"`
	Company     Company              `json:"company"`
	Campaign    Campaign             `json:"campaign"`
	Application *Application         `json:"application"`
	Resume      *Resume              `json:"resume"`
	Stages      []ApplicationStage   `json:"stages"`
	Attachments []PositionAttachment `json:"attachments"`
	Links       []ResourceLink       `json:"links"`
}

type CompanyDetail struct {
	Company          Company                  `json:"company"`
	Campaigns        []Campaign               `json:"campaigns"`
	CampaignCount    int                      `json:"campaignCount"`
	PositionCount    int                      `json:"positionCount"`
	ApplicationCount int                      `json:"applicationCount"`
	Links            []ResourceLink           `json:"links"`
	Attachments      []SupplementalAttachment `json:"attachments"`
}

type CampaignDetail struct {
	Campaign         Campaign                 `json:"campaign"`
	Company          Company                  `json:"company"`
	PositionCount    int                      `json:"positionCount"`
	ApplicationCount int                      `json:"applicationCount"`
	Links            []ResourceLink           `json:"links"`
	Attachments      []SupplementalAttachment `json:"attachments"`
}

type DirectoryStats struct {
	CompanyCount     int `json:"companyCount"`
	CampaignCount    int `json:"campaignCount"`
	PositionCount    int `json:"positionCount"`
	ApplicationCount int `json:"applicationCount"`
}

// PositionAttachment is a user-provided file associated with one position.
// The original filename is presentation-only; StoredName is an internal UUID
// filename and deliberately never exposes an arbitrary local path to the UI.
type PositionAttachment struct {
	ID           string    `json:"id"`
	PositionID   string    `json:"positionId"`
	OriginalName string    `json:"originalName"`
	StoredName   string    `json:"storedName"`
	MIMEType     string    `json:"mimeType"`
	SizeBytes    int64     `json:"sizeBytes"`
	CreatedAt    time.Time `json:"createdAt"`
}

type PositionPage struct {
	Items    []PositionSummary `json:"items"`
	Page     int               `json:"page"`
	PageSize int               `json:"pageSize"`
	Total    int               `json:"total"`
}

type StatusCount struct {
	Status PositionStatus `json:"status"`
	Count  int            `json:"count"`
}

type Dashboard struct {
	// StatusCounts remains available for older desktop bundles during an
	// in-place upgrade. The current dashboard uses application statistics.
	StatusCounts            []StatusCount        `json:"statusCounts"`
	TotalApplications       int                  `json:"totalApplications"`
	ActiveApplications      int                  `json:"activeApplications"`
	OfferApplications       int                  `json:"offerApplications"`
	RejectedApplications    int                  `json:"rejectedApplications"`
	ResumeScreeningStats    StageProgressStats   `json:"resumeScreeningStats"`
	WrittenTestStats        StageProgressStats   `json:"writtenTestStats"`
	AssessmentStats         StageProgressStats   `json:"assessmentStats"`
	InterviewedApplications int                  `json:"interviewedApplications"`
	InterviewStats          []StageProgressStats `json:"interviewStats"`
	TodoCount               int                  `json:"todoCount"`
}

// StageProgressStats counts distinct applications, not process-node totals.
type StageProgressStats struct {
	Type    StageType `json:"type"`
	Entered int       `json:"entered"`
	Passed  int       `json:"passed"`
	Failed  int       `json:"failed"`
}

type ApplicationSummary struct {
	Application
	CompanyName        string             `json:"companyName"`
	CampaignName       string             `json:"campaignName"`
	PositionTitle      string             `json:"positionTitle"`
	PositionPriority   int                `json:"positionPriority"`
	CurrentStageName   string             `json:"currentStageName"`
	CurrentStageType   StageType          `json:"currentStageType"`
	CurrentStageStatus StageStatus        `json:"currentStageStatus"`
	StageCount         int                `json:"stageCount"`
	Stages             []ApplicationStage `json:"stages"`
}

type ApplicationFilter struct {
	Status      string `json:"status"`
	Query       string `json:"query"`
	SortBy      string `json:"sortBy"`
	SortOrder   string `json:"sortOrder"`
	StageType   string `json:"stageType"`
	StageStatus string `json:"stageStatus"`
	ResumeID    string `json:"resumeId"`
	Page        int    `json:"page"`
	PageSize    int    `json:"pageSize"`
}

type ApplicationPage struct {
	Items    []ApplicationSummary `json:"items"`
	Page     int                  `json:"page"`
	PageSize int                  `json:"pageSize"`
	Total    int                  `json:"total"`
}

type ApplicationDetail struct {
	Application Application              `json:"application"`
	Position    Position                 `json:"position"`
	Company     Company                  `json:"company"`
	Campaign    Campaign                 `json:"campaign"`
	Resume      *Resume                  `json:"resume"`
	Stages      []ApplicationStage       `json:"stages"`
	Links       []ResourceLink           `json:"links"`
	Attachments []SupplementalAttachment `json:"attachments"`
}

type SafetyStatus struct {
	MirrorDirectory string `json:"mirrorDirectory"`
	LatestSnapshot  string `json:"latestSnapshot"`
	LastSyncedAt    string `json:"lastSyncedAt"`
	LastError       string `json:"lastError"`
}

type Health struct {
	DatabasePath  string       `json:"databasePath"`
	SchemaVersion int          `json:"schemaVersion"`
	Safety        SafetyStatus `json:"safety"`
}

type BackupRecord struct {
	ID              string `json:"id"`
	CreatedAt       string `json:"createdAt"`
	DatabaseSize    int64  `json:"databaseSize"`
	AttachmentCount int    `json:"attachmentCount"`
	AttachmentSize  int64  `json:"attachmentSize"`
}

type BackupCenter struct {
	DataDirectory   string          `json:"dataDirectory"`
	BackupDirectory string          `json:"backupDirectory"`
	MirrorDirectory string          `json:"mirrorDirectory"`
	LastSyncedAt    string          `json:"lastSyncedAt"`
	ArchivesCount   int             `json:"archivesCount"`
	Backups         []BackupRecord  `json:"backups"`
	CloudSync       CloudSyncStatus `json:"cloudSync"`
}

// CloudSyncStatus deliberately contains no credential material. The Gitee
// personal token is held outside SQLite in a Windows DPAPI protected file.
type CloudSyncStatus struct {
	State          string `json:"state"`
	Activity       string `json:"activity"`
	Message        string `json:"message"`
	Owner          string `json:"owner"`
	PrimaryRepo    string `json:"primaryRepo"`
	DeviceName     string `json:"deviceName"`
	LastSuccessAt  string `json:"lastSuccessAt"`
	LastCheckedAt  string `json:"lastCheckedAt"`
	PendingChanges int    `json:"pendingChanges"`
	ActiveChanges  int    `json:"activeChanges"`
	QueuedChanges  int    `json:"queuedChanges"`
	ConflictCount  int    `json:"conflictCount"`
	CanSync        bool   `json:"canSync"`
	ProgressDone   int    `json:"progressDone"`
	ProgressTotal  int    `json:"progressTotal"`
	FilesDone      int    `json:"filesDone"`
	FilesTotal     int    `json:"filesTotal"`
	RetryAttempt   int    `json:"retryAttempt"`
	RetryMax       int    `json:"retryMax"`
	RetryError     string `json:"retryError"`
	RetryAfter     int    `json:"retryAfter"`
}

type SyncDataSummary struct {
	Companies    int `json:"companies"`
	Campaigns    int `json:"campaigns"`
	Positions    int `json:"positions"`
	Applications int `json:"applications"`
	Stages       int `json:"stages"`
	Attachments  int `json:"attachments"`
	Resumes      int `json:"resumes"`
}

type GiteeConnectionPreview struct {
	Account      string          `json:"account"`
	PrimaryRepo  string          `json:"primaryRepo"`
	Local        SyncDataSummary `json:"local"`
	Cloud        SyncDataSummary `json:"cloud"`
	Recommended  string          `json:"recommended"`
	NeedsConfirm bool            `json:"needsConfirm"`
}

type SyncConflict struct {
	ID                string `json:"id"`
	ObjectType        string `json:"objectType"`
	ObjectID          string `json:"objectId"`
	LocalUpdatedAt    string `json:"localUpdatedAt"`
	RemoteUpdatedAt   string `json:"remoteUpdatedAt"`
	LocalDescription  string `json:"localDescription"`
	RemoteDescription string `json:"remoteDescription"`
}

type RestoreResult struct {
	RestoredBackup BackupRecord `json:"restoredBackup"`
	SafetyBackup   BackupRecord `json:"safetyBackup"`
}

type DeletionTargetType string

const (
	DeletionTargetCompany     DeletionTargetType = "company"
	DeletionTargetCampaign    DeletionTargetType = "campaign"
	DeletionTargetPosition    DeletionTargetType = "position"
	DeletionTargetApplication DeletionTargetType = "application"
)

func ValidDeletionTargetType(value DeletionTargetType) bool {
	switch value {
	case DeletionTargetCompany, DeletionTargetCampaign, DeletionTargetPosition, DeletionTargetApplication:
		return true
	default:
		return false
	}
}

type DeleteInput struct {
	EntityType       DeletionTargetType `json:"entityType"`
	ID               string             `json:"id"`
	ConfirmationText string             `json:"confirmationText"`
}

type DeletionPreview struct {
	EntityType       DeletionTargetType `json:"entityType"`
	EntityName       string             `json:"entityName"`
	ConfirmationText string             `json:"confirmationText"`
	CampaignCount    int                `json:"campaignCount"`
	PositionCount    int                `json:"positionCount"`
	ApplicationCount int                `json:"applicationCount"`
	StageCount       int                `json:"stageCount"`
}
