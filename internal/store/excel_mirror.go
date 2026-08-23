package store

import (
	"database/sql"
	"hash/fnv"
	"strconv"
	"strings"
	"time"

	"github.com/dingyu/offer-atlas/internal/domain"
	"github.com/xuri/excelize/v2"
)

const readableMirrorWorkbookName = "投递信息表.xlsx"

var (
	applicationMirrorHeaders = []string{"公司", "招聘批次", "岗位", "岗位编号", "部门/地点", "优先级", "投递日期", "投递渠道", "简历版本", "当前节点", "节点状态", "流程进度", "岗位链接", "投递备注"}
	pendingMirrorHeaders     = []string{"公司", "招聘批次", "岗位", "岗位编号", "部门/地点", "优先级", "批次截止日", "岗位链接", "岗位备注"}
)

type applicationMirrorRow struct {
	ApplicationID string
	Company       string
	Campaign      string
	Position      string
	JobCode       string
	Department    string
	Location      string
	Priority      int
	SubmittedOn   string
	Channel       string
	ResumeName    string
	PositionURL   string
	Notes         string
	Stages        []applicationMirrorStage
}

type applicationMirrorStage struct {
	Type     domain.StageType
	Content  string
	Status   domain.StageStatus
	StartsAt *time.Time
}

type pendingPositionMirrorRow struct {
	Company     string
	Campaign    string
	Position    string
	JobCode     string
	Department  string
	Location    string
	Priority    int
	ClosesOn    string
	PositionURL string
	Notes       string
}

func readApplicationMirrorRows(tx *sql.Tx) ([]applicationMirrorRow, error) {
	rows, err := tx.Query(`
		SELECT a.id, c.name, ca.name, p.title, p.job_code, p.department, p.location, p.priority,
		       COALESCE(a.submitted_on, ''), a.channel, a.resume_name, p.source_url, a.notes
		FROM applications a
		JOIN positions p ON p.id=a.position_id
		JOIN campaigns ca ON ca.id=p.campaign_id
		JOIN companies c ON c.id=ca.company_id
		ORDER BY p.priority DESC, a.updated_at DESC, c.name COLLATE NOCASE, ca.name COLLATE NOCASE, p.title COLLATE NOCASE
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]applicationMirrorRow, 0)
	indices := make(map[string]int)
	for rows.Next() {
		var item applicationMirrorRow
		if err := rows.Scan(&item.ApplicationID, &item.Company, &item.Campaign, &item.Position, &item.JobCode, &item.Department, &item.Location, &item.Priority, &item.SubmittedOn, &item.Channel, &item.ResumeName, &item.PositionURL, &item.Notes); err != nil {
			return nil, err
		}
		indices[item.ApplicationID] = len(items)
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	stageRows, err := tx.Query(`
		SELECT application_id, type, name, status, scheduled_start
		FROM application_stages
		ORDER BY application_id, sort_order
	`)
	if err != nil {
		return nil, err
	}
	defer stageRows.Close()
	for stageRows.Next() {
		var applicationID, content string
		var stageType domain.StageType
		var status domain.StageStatus
		var startsAt sql.NullString
		if err := stageRows.Scan(&applicationID, &stageType, &content, &status, &startsAt); err != nil {
			return nil, err
		}
		start, err := parseNullableStoredDateTime(startsAt)
		if err != nil {
			return nil, err
		}
		if index, ok := indices[applicationID]; ok {
			items[index].Stages = append(items[index].Stages, applicationMirrorStage{Type: stageType, Content: content, Status: status, StartsAt: start})
		}
	}
	return items, stageRows.Err()
}

func readPendingPositionMirrorRows(tx *sql.Tx) ([]pendingPositionMirrorRow, error) {
	rows, err := tx.Query(`
		SELECT c.name, ca.name, p.title, p.job_code, p.department, p.location, p.priority,
		       COALESCE(ca.closes_on, ''), p.source_url, p.notes
		FROM positions p
		JOIN campaigns ca ON ca.id=p.campaign_id
		JOIN companies c ON c.id=ca.company_id
		LEFT JOIN applications a ON a.position_id=p.id
		WHERE a.id IS NULL
		ORDER BY p.priority DESC, CASE WHEN ca.closes_on IS NULL THEN 1 ELSE 0 END, ca.closes_on ASC, p.updated_at DESC, c.name COLLATE NOCASE
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	items := make([]pendingPositionMirrorRow, 0)
	for rows.Next() {
		var item pendingPositionMirrorRow
		if err := rows.Scan(&item.Company, &item.Campaign, &item.Position, &item.JobCode, &item.Department, &item.Location, &item.Priority, &item.ClosesOn, &item.PositionURL, &item.Notes); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func writeReadableMirrorWorkbook(path string, applications []applicationMirrorRow, pendingPositions []pendingPositionMirrorRow) error {
	workbook := excelize.NewFile()
	defer workbook.Close()
	if err := workbook.SetSheetName("Sheet1", "投递记录"); err != nil {
		return err
	}
	if _, err := workbook.NewSheet("待投递岗位"); err != nil {
		return err
	}
	styles, err := newReadableMirrorStyles(workbook)
	if err != nil {
		return err
	}
	if err := writeApplicationMirrorSheet(workbook, styles, applications); err != nil {
		return err
	}
	if err := writePendingPositionMirrorSheet(workbook, styles, pendingPositions); err != nil {
		return err
	}
	workbook.SetActiveSheet(0)
	contents, err := workbook.WriteToBuffer()
	if err != nil {
		return err
	}
	if err := writeSyncedFile(path, contents.Bytes()); err != nil {
		return err
	}
	return nil
}

type readableMirrorStyles struct {
	header       int
	body         int
	bodyEven     int
	wrap         int
	wrapEven     int
	progress     int
	progressEven int
	hyperlink    int
	priority     map[int]int
	stageStatus  map[domain.StageStatus]int
	labelPalette []int
}

func newReadableMirrorStyles(workbook *excelize.File) (readableMirrorStyles, error) {
	styles := readableMirrorStyles{priority: map[int]int{}, stageStatus: map[domain.StageStatus]int{}}
	var err error
	if styles.header, err = newMirrorCellStyle(workbook, "0E6B62", "FFFFFF", true, false, "center", false); err != nil {
		return styles, err
	}
	if styles.body, err = newMirrorCellStyle(workbook, "", "29483F", false, false, "left", false); err != nil {
		return styles, err
	}
	if styles.bodyEven, err = newMirrorCellStyle(workbook, "F7FAF8", "29483F", false, false, "left", false); err != nil {
		return styles, err
	}
	if styles.wrap, err = newMirrorCellStyle(workbook, "", "29483F", false, true, "left", false); err != nil {
		return styles, err
	}
	if styles.wrapEven, err = newMirrorCellStyle(workbook, "F7FAF8", "29483F", false, true, "left", false); err != nil {
		return styles, err
	}
	if styles.progress, err = newMirrorProgressStyle(workbook, ""); err != nil {
		return styles, err
	}
	if styles.progressEven, err = newMirrorProgressStyle(workbook, "F7FAF8"); err != nil {
		return styles, err
	}
	if styles.hyperlink, err = newMirrorCellStyle(workbook, "", "1769AA", false, false, "left", true); err != nil {
		return styles, err
	}

	priorityColors := map[int][2]string{
		1: {"F1F3F3", "667872"},
		2: {"EAF2FB", "3E6C9B"},
		3: {"E7F4EF", "137563"},
		4: {"FEF0DE", "A3651A"},
		5: {"FBEAEC", "A83D48"},
	}
	for priority, colors := range priorityColors {
		if styles.priority[priority], err = newMirrorCellStyle(workbook, colors[0], colors[1], true, false, "center", false); err != nil {
			return styles, err
		}
	}
	statusColors := map[domain.StageStatus][2]string{
		domain.StageScheduled: {"E2F3EE", "0D7569"},
		domain.StageAttended:  {"EAF1FA", "3C6D9E"},
		domain.StagePassed:    {"E6F4EA", "2E814C"},
		domain.StageFailed:    {"FBEAEC", "AE3D47"},
	}
	for status, colors := range statusColors {
		if styles.stageStatus[status], err = newMirrorCellStyle(workbook, colors[0], colors[1], true, false, "center", false); err != nil {
			return styles, err
		}
	}
	for _, colors := range [][2]string{{"EAF3FB", "3A6796"}, {"E9F5EF", "28755A"}, {"FFF2DE", "98631D"}, {"F7ECF5", "8A4B7C"}, {"EFF0FA", "5E6397"}, {"E7F4F4", "176F71"}} {
		style, styleErr := newMirrorCellStyle(workbook, colors[0], colors[1], true, false, "left", false)
		if styleErr != nil {
			return styles, styleErr
		}
		styles.labelPalette = append(styles.labelPalette, style)
	}
	return styles, nil
}

func newMirrorCellStyle(workbook *excelize.File, fill, fontColor string, bold, wrap bool, horizontal string, underline bool) (int, error) {
	font := &excelize.Font{Family: "Microsoft YaHei", Size: 10, Color: fontColor, Bold: bold}
	if underline {
		font.Underline = "single"
	}
	style := &excelize.Style{
		Font:      font,
		Alignment: &excelize.Alignment{Horizontal: horizontal, Vertical: "center", WrapText: wrap},
		Border:    []excelize.Border{{Type: "bottom", Color: "DDE7E1", Style: 1}},
	}
	if fill != "" {
		style.Fill = excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{fill}}
	}
	return workbook.NewStyle(style)
}

func newMirrorProgressStyle(workbook *excelize.File, fill string) (int, error) {
	style := &excelize.Style{
		Font: &excelize.Font{Family: "Microsoft YaHei", Size: 10, Color: "29483F"},
		Alignment: &excelize.Alignment{
			Horizontal: "left",
			Vertical:   "center",
		},
		Border: []excelize.Border{{Type: "bottom", Color: "DDE7E1", Style: 1}},
	}
	if fill != "" {
		style.Fill = excelize.Fill{Type: "pattern", Pattern: 1, Color: []string{fill}}
	}
	return workbook.NewStyle(style)
}

func writeApplicationMirrorSheet(workbook *excelize.File, styles readableMirrorStyles, items []applicationMirrorRow) error {
	const sheet = "投递记录"
	// Flow histories vary by company, so reserve enough room for a normal
	// multi-round process and let the worksheet scroll horizontally for outliers.
	widths := []float64{15, 20, 22, 15, 22, 9, 13, 14, 14, 20, 11, 160, 15, 34}
	if err := prepareReadableMirrorSheet(workbook, sheet, applicationMirrorHeaders, widths, len(items), styles); err != nil {
		return err
	}
	for index, item := range items {
		row := index + 2
		if err := writeReadableMirrorRow(workbook, sheet, row, []string{
			item.Company, item.Campaign, item.Position, item.JobCode, mirrorDepartmentLocation(item.Department, item.Location), mirrorPriority(item.Priority),
			item.SubmittedOn, item.Channel, item.ResumeName, mirrorCurrentStageName(item.Stages), mirrorCurrentStageStatus(item.Stages), "", "", item.Notes,
		}, styles, []int{14}); err != nil {
			return err
		}
		if err := workbook.SetCellStyle(sheet, mirrorCell(1, row), mirrorCell(1, row), styles.labelStyle(item.Company)); err != nil {
			return err
		}
		if err := workbook.SetCellStyle(sheet, mirrorCell(2, row), mirrorCell(2, row), styles.labelStyle(item.Company+"/"+item.Campaign)); err != nil {
			return err
		}
		if err := workbook.SetCellStyle(sheet, mirrorCell(6, row), mirrorCell(6, row), styles.priorityStyle(item.Priority)); err != nil {
			return err
		}
		if status := mirrorCurrentStageStatusValue(item.Stages); status != "" {
			if err := workbook.SetCellStyle(sheet, mirrorCell(11, row), mirrorCell(11, row), styles.stageStatusStyle(status)); err != nil {
				return err
			}
		}
		if err := workbook.SetCellRichText(sheet, mirrorCell(12, row), mirrorProgressRuns(item.Stages)); err != nil {
			return err
		}
		progressStyle := styles.progress
		if row%2 == 0 {
			progressStyle = styles.progressEven
		}
		if err := workbook.SetCellStyle(sheet, mirrorCell(12, row), mirrorCell(12, row), progressStyle); err != nil {
			return err
		}
		if err := writeMirrorHyperlink(workbook, sheet, mirrorCell(13, row), item.PositionURL, styles.hyperlink); err != nil {
			return err
		}
	}
	return nil
}

func writePendingPositionMirrorSheet(workbook *excelize.File, styles readableMirrorStyles, items []pendingPositionMirrorRow) error {
	const sheet = "待投递岗位"
	widths := []float64{16, 22, 24, 16, 24, 10, 14, 16, 42}
	if err := prepareReadableMirrorSheet(workbook, sheet, pendingMirrorHeaders, widths, len(items), styles); err != nil {
		return err
	}
	for index, item := range items {
		row := index + 2
		if err := writeReadableMirrorRow(workbook, sheet, row, []string{
			item.Company, item.Campaign, item.Position, item.JobCode, mirrorDepartmentLocation(item.Department, item.Location), mirrorPriority(item.Priority), item.ClosesOn, "", item.Notes,
		}, styles, []int{9}); err != nil {
			return err
		}
		if err := workbook.SetCellStyle(sheet, mirrorCell(1, row), mirrorCell(1, row), styles.labelStyle(item.Company)); err != nil {
			return err
		}
		if err := workbook.SetCellStyle(sheet, mirrorCell(2, row), mirrorCell(2, row), styles.labelStyle(item.Company+"/"+item.Campaign)); err != nil {
			return err
		}
		if err := workbook.SetCellStyle(sheet, mirrorCell(6, row), mirrorCell(6, row), styles.priorityStyle(item.Priority)); err != nil {
			return err
		}
		if err := writeMirrorHyperlink(workbook, sheet, mirrorCell(8, row), item.PositionURL, styles.hyperlink); err != nil {
			return err
		}
	}
	return nil
}

func prepareReadableMirrorSheet(workbook *excelize.File, sheet string, headers []string, widths []float64, rowCount int, styles readableMirrorStyles) error {
	if err := workbook.SetSheetRow(sheet, "A1", &headers); err != nil {
		return err
	}
	lastColumn := mirrorColumn(len(headers))
	if err := workbook.SetCellStyle(sheet, "A1", lastColumn+"1", styles.header); err != nil {
		return err
	}
	if err := workbook.SetRowHeight(sheet, 1, 28); err != nil {
		return err
	}
	for index, width := range widths {
		column := mirrorColumn(index + 1)
		if err := workbook.SetColWidth(sheet, column, column, width); err != nil {
			return err
		}
	}
	if err := workbook.AutoFilter(sheet, "A1:"+lastColumn+mirrorRow(rowCount+1), nil); err != nil {
		return err
	}
	return workbook.SetPanes(sheet, &excelize.Panes{
		Freeze:      true,
		YSplit:      1,
		TopLeftCell: "A2",
		ActivePane:  "bottomLeft",
		Selection:   []excelize.Selection{{SQRef: "A2", ActiveCell: "A2", Pane: "bottomLeft"}},
	})
}

func writeReadableMirrorRow(workbook *excelize.File, sheet string, row int, values []string, styles readableMirrorStyles, wrapColumns []int) error {
	wraps := make(map[int]bool, len(wrapColumns))
	for _, column := range wrapColumns {
		wraps[column] = true
	}
	baseStyle, wrapStyle := styles.body, styles.wrap
	if row%2 == 0 {
		baseStyle, wrapStyle = styles.bodyEven, styles.wrapEven
	}
	for column, value := range values {
		cell := mirrorCell(column+1, row)
		if err := workbook.SetCellValue(sheet, cell, value); err != nil {
			return err
		}
		style := baseStyle
		if wraps[column+1] {
			style = wrapStyle
		}
		if err := workbook.SetCellStyle(sheet, cell, cell, style); err != nil {
			return err
		}
	}
	return workbook.SetRowHeight(sheet, row, 34)
}

func writeMirrorHyperlink(workbook *excelize.File, sheet, cell, url string, style int) error {
	if strings.TrimSpace(url) == "" {
		return nil
	}
	if err := workbook.SetCellValue(sheet, cell, "打开岗位页"); err != nil {
		return err
	}
	if err := workbook.SetCellHyperLink(sheet, cell, url, "External"); err != nil {
		return err
	}
	return workbook.SetCellStyle(sheet, cell, cell, style)
}

func (styles readableMirrorStyles) priorityStyle(priority int) int {
	if style, ok := styles.priority[priority]; ok {
		return style
	}
	return styles.priority[3]
}

func (styles readableMirrorStyles) stageStatusStyle(status domain.StageStatus) int {
	if style, ok := styles.stageStatus[status]; ok {
		return style
	}
	return styles.body
}

func (styles readableMirrorStyles) labelStyle(value string) int {
	hash := fnv.New32a()
	_, _ = hash.Write([]byte(strings.ToLower(strings.TrimSpace(value))))
	return styles.labelPalette[int(hash.Sum32())%len(styles.labelPalette)]
}

func mirrorDepartmentLocation(department, location string) string {
	parts := make([]string, 0, 2)
	if value := strings.TrimSpace(department); value != "" {
		parts = append(parts, value)
	}
	if value := strings.TrimSpace(location); value != "" {
		parts = append(parts, value)
	}
	return strings.Join(parts, " · ")
}

func mirrorPriority(priority int) string {
	if priority < 1 || priority > 5 {
		return ""
	}
	return "P" + string(rune('0'+priority))
}

func mirrorCurrentStageName(stages []applicationMirrorStage) string {
	if len(stages) == 0 {
		return "未添加流程"
	}
	return mirrorStageDisplayName(stages[len(stages)-1])
}

func mirrorCurrentStageStatus(stages []applicationMirrorStage) string {
	if len(stages) == 0 {
		return "未开始"
	}
	return mirrorStageStatusLabel(stages[len(stages)-1].Status)
}

func mirrorCurrentStageStatusValue(stages []applicationMirrorStage) domain.StageStatus {
	if len(stages) == 0 {
		return ""
	}
	return stages[len(stages)-1].Status
}

func mirrorProgressRuns(stages []applicationMirrorStage) []excelize.RichTextRun {
	if len(stages) == 0 {
		return []excelize.RichTextRun{{Text: "尚未添加流程节点", Font: &excelize.Font{Family: "Microsoft YaHei", Color: "8B9B94", Italic: true}}}
	}
	runs := make([]excelize.RichTextRun, 0, len(stages)*5)
	for index, stage := range stages {
		isCurrent := index == len(stages)-1
		runs = append(runs, excelize.RichTextRun{Text: mirrorStageDisplayName(stage), Font: &excelize.Font{Family: "Microsoft YaHei", Color: "29483F", Bold: isCurrent}})
		if timeLabel := mirrorStageStartLabel(stage.StartsAt); timeLabel != "" {
			runs = append(runs,
				excelize.RichTextRun{Text: "（" + timeLabel + "）", Font: &excelize.Font{Family: "Microsoft YaHei", Color: "64766F"}},
			)
		}
		runs = append(runs,
			excelize.RichTextRun{Text: " · ", Font: &excelize.Font{Family: "Microsoft YaHei", Color: "899A93"}},
			excelize.RichTextRun{Text: mirrorStageStatusLabel(stage.Status), Font: &excelize.Font{Family: "Microsoft YaHei", Color: mirrorStageStatusColor(stage.Status), Bold: true}},
		)
		if !isCurrent {
			runs = append(runs, excelize.RichTextRun{Text: "   →   ", Font: &excelize.Font{Family: "Microsoft YaHei", Color: "A7B3AE"}})
		}
	}
	return runs
}

func mirrorStageDisplayName(stage applicationMirrorStage) string {
	label := systemStageTypeLabel(stage.Type)
	if label == "自定义流程" {
		label = "自定义流程"
	}
	if content := strings.TrimSpace(stage.Content); content != "" {
		return label + " · " + content
	}
	return label
}

func mirrorStageStartLabel(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.In(time.Local).Format("01-02 15:04")
}

func mirrorStageStatusLabel(status domain.StageStatus) string {
	switch status {
	case domain.StageScheduled:
		return "已预约"
	case domain.StageAttended:
		return "已参加"
	case domain.StagePassed:
		return "通过"
	case domain.StageFailed:
		return "未通过"
	default:
		return "未开始"
	}
}

func mirrorStageStatusColor(status domain.StageStatus) string {
	switch status {
	case domain.StageScheduled:
		return "0D7569"
	case domain.StageAttended:
		return "3C6D9E"
	case domain.StagePassed:
		return "2E814C"
	case domain.StageFailed:
		return "AE3D47"
	default:
		return "8B9B94"
	}
}

func mirrorColumn(column int) string {
	name, err := excelize.ColumnNumberToName(column)
	if err != nil {
		panic(err)
	}
	return name
}

func mirrorCell(column, row int) string { return mirrorColumn(column) + mirrorRow(row) }

func mirrorRow(row int) string { return strconv.Itoa(row) }
