import {
  CSSProperties,
  ClipboardEvent,
  FormEvent,
  ReactNode,
  createContext,
  useContext,
  useEffect,
  useRef,
  useState,
} from "react";
import {
  ArrowDown,
  ArrowLeft,
  ArrowRight,
  ArrowUp,
  BadgeCheck,
  Bot,
  BriefcaseBusiness,
  Building2,
  CalendarDays,
  CalendarRange,
  CheckCircle2,
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  CircleAlert,
  CircleHelp,
  CirclePlus,
  ClipboardCheck,
  ClipboardList,
  ClipboardPaste,
  Cloud,
  CloudOff,
  Compass,
  Copy,
  DatabaseBackup,
  ExternalLink,
  FileStack,
  FileText,
  FolderOpen,
  ImagePlus,
  LayoutDashboard,
  ListFilter,
  ListTodo,
	LoaderCircle,
  MessageSquare,
  Pencil,
  Plus,
  RotateCcw,
  Search,
  Settings2,
  ShieldCheck,
  Trash2,
  Trophy,
  UsersRound,
  X,
  Zap,
} from "lucide-react";
import {
  api,
  Application,
	AppUpdate,
  ApplicationDetail,
  ApplicationInput,
  ApplicationPage,
  ApplicationStage,
  ApplicationStageInput,
  ApplicationStatus,
  BackupCenter,
  BackupRecord,
  Campaign,
  CampaignInput,
  CloudSyncStatus,
  Company,
  CompanyInput,
  Dashboard,
  DeleteInput,
  DeletionPreview,
  DeletionTargetType,
  Health,
	GiteeConnectionPreview,
	SyncConflict,
  Position,
  PositionAttachment,
  PositionDetail,
  PositionInput,
  PositionPage,
  PositionStatus,
  PositionSummary,
  QuickCapturePositionInput,
	Resume,
	ResumeInput,
  ScheduleItem,
  SortField,
  SortOrder,
  StageStatus,
  StageType,
  StageTypeDefinition,
} from "./api";
import { BrowserOpenURL, ClipboardSetText, EventsOn } from "../wailsjs/runtime/runtime";

type View =
  | "dashboard"
  | "positions"
  | "applications"
	| "resumes"
  | "calendar"
  | "todos"
  | "directory"
  | "search";
type DialogName =
  | "company"
  | "campaign"
  | "position"
  | "quick-position"
  | "application"
	| "resume"
  | "stage"
  | "delete"
	| "update"
  | null;
type DeletionTarget = Pick<DeleteInput, "entityType" | "id"> & { name: string };
type ProtectedAction = {
  title: string;
  subject: string;
  description: string;
  confirmationText: string;
  confirmLabel: string;
  tone?: "danger" | "caution";
  action: () => Promise<void> | void;
};

const positionLabels: Record<PositionStatus, string> = {
  unapplied: "未投递",
  applied: "已投递",
};
const applicationLabels: Record<ApplicationStatus, string> = {
  active: "进行中",
  offer: "已通过",
  rejected: "未通过",
};
const defaultStageTypeLabels: Record<string, string> = {
  written_test: "笔试",
  assessment: "测评",
  ai_interview: "AI 面",
  first_interview: "一面",
  second_interview: "二面",
  third_interview: "三面",
  fourth_interview: "四面",
  hr_interview: "HR 面",
  offer: "Offer",
  other: "其他",
};
const stageStatusLabels: Record<StageStatus, string> = {
  scheduled: "已预约",
  passed: "通过",
  failed: "未通过",
};
const deletionLabels: Record<DeletionTargetType, string> = {
  company: "公司",
  campaign: "招聘批次",
  position: "岗位",
  application: "投递记录",
};
const emptyDashboard: Dashboard = {
  statusCounts: [],
  totalApplications: 0,
  activeApplications: 0,
  offerApplications: 0,
  rejectedApplications: 0,
  writtenTestStats: { type: "written_test", entered: 0, passed: 0, failed: 0 },
  assessmentStats: { type: "assessment", entered: 0, passed: 0, failed: 0 },
  interviewedApplications: 0,
  interviewStats: [],
  todoCount: 0,
};
const StageTypeCatalogContext = createContext<StageTypeDefinition[]>([]);

function textDate(value?: string) {
  if (!value) return "未设置";
  const date = new Date(value);
  return Number.isNaN(date.getTime())
    ? value
    : new Intl.DateTimeFormat("zh-CN", {
        month: "2-digit",
        day: "2-digit",
      }).format(date);
}
function textDateTime(value?: string) {
  if (!value) return "未设置";
  const date = new Date(value);
  return Number.isNaN(date.getTime())
    ? value
    : new Intl.DateTimeFormat("zh-CN", {
        month: "2-digit",
        day: "2-digit",
        hour: "2-digit",
        minute: "2-digit",
      }).format(date);
}
function textColumnWidth(values: string[], minimum: number, maximum: number) {
  const widest = values.reduce(
    (current, value) =>
      Math.max(
        current,
        Array.from(value).reduce(
          (sum, character) => sum + (/[^\x00-\xff]/.test(character) ? 1 : 0.62),
          0,
        ),
      ),
    0,
  );
  return `${Math.min(maximum, Math.max(minimum, Math.ceil(widest * 12 + 20)))}px`;
}
function applicationColumnStyle(
  items: ApplicationPage["items"],
): CSSProperties {
  return {
    "--application-company-width": textColumnWidth(
      ["公司", ...items.map((item) => item.companyName)],
      82,
      150,
    ),
    "--application-campaign-width": textColumnWidth(
      ["招聘批次", ...items.map((item) => item.campaignName)],
      94,
      170,
    ),
    "--application-position-width": textColumnWidth(
      ["岗位", ...items.map((item) => item.positionTitle)],
      88,
      180,
    ),
    "--application-submitted-width": textColumnWidth(
      [
        "投递时间",
        ...items.map(
          (item) => (item.submittedOn ? textDate(item.submittedOn) : "日期未记录"),
        ),
      ],
      92,
      118,
    ),
    "--application-state-width": textColumnWidth(
      ["投递状态", ...items.map((item) => applicationLabels[item.status])],
      88,
      110,
    ),
  } as CSSProperties;
}
function dateTimeRange(start: string, end?: string) {
  return end
    ? `${textDateTime(start)} 至 ${textDateTime(end)}`
    : textDateTime(start);
}
function stageTimeText(stage: ApplicationStage) {
  if (stage.scheduledStart)
    return dateTimeRange(stage.scheduledStart, stage.scheduledEnd);
  if (stage.resultAt) return `结果 ${textDateTime(stage.resultAt)}`;
  return "时间未安排";
}
function scheduleTimeText(item: ScheduleItem) {
  return dateTimeRange(item.startsAt, item.endsAt);
}
function attachmentSize(size: number) {
  if (size < 1024) return `${size} B`;
  if (size < 1024 * 1024) return `${(size / 1024).toFixed(1)} KB`;
  return `${(size / (1024 * 1024)).toFixed(1)} MB`;
}
function backupSize(size: number) {
  if (size < 1024 * 1024) return `${Math.max(1, Math.ceil(size / 1024))} KB`;
  return `${(size / (1024 * 1024)).toFixed(1)} MB`;
}
function attachmentIsImage(item: PositionAttachment) {
  return [
    "image/png",
    "image/jpeg",
    "image/gif",
    "image/webp",
    "image/bmp",
  ].includes(item.mimeType.toLowerCase());
}
function stageTypeLabel(type: StageType, catalog: StageTypeDefinition[]) {
  return (
    catalog.find((item) => item.id === type)?.name ||
    defaultStageTypeLabels[type] ||
    type ||
    "未分类"
  );
}
function useStageTypeLabel() {
  const catalog = useContext(StageTypeCatalogContext);
  return (type: StageType) => stageTypeLabel(type, catalog);
}
const inputDate = (value?: string) =>
  value ? new Date(value).toISOString().slice(0, 10) : "";
const inputDateTime = (value?: string) => {
  if (!value) return "";
  const date = new Date(value);
  return new Date(date.getTime() - date.getTimezoneOffset() * 60000)
    .toISOString()
    .slice(0, 16);
};

export default function App() {
  const [view, setView] = useState<View>("dashboard");
  const [dialog, setDialog] = useState<DialogName>(null);
  const [dashboard, setDashboard] = useState<Dashboard>(emptyDashboard);
  const [health, setHealth] = useState<Health | null>(null);
  const [cloudSync, setCloudSync] = useState<CloudSyncStatus | null>(null);
	const [appUpdate, setAppUpdate] = useState<AppUpdate | null>(null);
  const [cloudSyncRevision, setCloudSyncRevision] = useState(0);
  const cloudSyncSnapshot = useRef<Pick<CloudSyncStatus, "activity" | "state" | "lastSuccessAt"> | null>(null);
  const [companies, setCompanies] = useState<Company[]>([]);
  const [campaigns, setCampaigns] = useState<Campaign[]>([]);
  const [positions, setPositions] = useState<PositionPage>({
    items: [],
    page: 1,
    pageSize: 20,
    total: 0,
  });
  const [applications, setApplications] = useState<ApplicationPage>({
    items: [],
    page: 1,
    pageSize: 20,
    total: 0,
  });
	const [resumes, setResumes] = useState<Resume[]>([]);
  const [scheduleItems, setScheduleItems] = useState<ScheduleItem[]>([]);
  const [stageTypes, setStageTypes] = useState<StageTypeDefinition[]>([]);
  const [positionFilter, setPositionFilter] = useState("all");
  const [applicationFilter, setApplicationFilter] = useState("all");
  const [applicationStageType, setApplicationStageType] = useState("");
  const [applicationStageStatus, setApplicationStageStatus] = useState("");
  const [applicationResumeID, setApplicationResumeID] = useState("");
  const [positionSortBy, setPositionSortBy] = useState<SortField>("priority");
  const [positionSortOrder, setPositionSortOrder] = useState<SortOrder>("desc");
  const [applicationSortBy, setApplicationSortBy] =
    useState<SortField>("priority");
  const [applicationSortOrder, setApplicationSortOrder] =
    useState<SortOrder>("desc");
  const [searchDraft, setSearchDraft] = useState("");
  const [searchQuery, setSearchQuery] = useState("");
  const [applicationPage, setApplicationPage] = useState(1);
  const [positionPage, setPositionPage] = useState(1);
  const [selectedPosition, setSelectedPosition] =
    useState<PositionDetail | null>(null);
  const [selectedApplication, setSelectedApplication] =
    useState<ApplicationDetail | null>(null);
  const [editingCompany, setEditingCompany] = useState<Company | null>(null);
  const [editingCampaign, setEditingCampaign] = useState<Campaign | null>(null);
  const [editingPosition, setEditingPosition] = useState<Position | null>(null);
  const [editingApplication, setEditingApplication] =
    useState<Application | null>(null);
	const [editingResume, setEditingResume] = useState<Resume | null>(null);
  const [editingStage, setEditingStage] = useState<ApplicationStage | null>(
    null,
  );
  const [deletionTarget, setDeletionTarget] = useState<DeletionTarget | null>(
    null,
  );
  const [protectedAction, setProtectedAction] =
    useState<ProtectedAction | null>(null);
  const [managingStageTypes, setManagingStageTypes] = useState(false);
  const [backupCenterOpen, setBackupCenterOpen] = useState(false);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [toast, setToast] = useState("");
  const [closingForSync, setClosingForSync] = useState(false);
  const [closeSyncMessage, setCloseSyncMessage] = useState("");
	const [closingForUpdate, setClosingForUpdate] = useState(false);

  useEffect(() => {
    const removeClosing = EventsOn("cloud-sync:closing", (message: string) => {
      setClosingForSync(true);
      setCloseSyncMessage(message);
    });
    const removeFailure = EventsOn("cloud-sync:close-failed", (message: string) => {
      setClosingForSync(false);
		setClosingForUpdate(false);
      setCloseSyncMessage("");
      setToast(message);
    });
		const removeUpdateClosing = EventsOn("app-update:closing", (message: string) => {
			setClosingForSync(true);
			setClosingForUpdate(true);
			setCloseSyncMessage(message);
		});
		const removeUpdateFailure = EventsOn("app-update:install-failed", (message: string) => {
			setClosingForSync(false);
			setClosingForUpdate(false);
			setCloseSyncMessage("");
			setToast(message);
		});
    return () => {
      removeClosing();
      removeFailure();
		removeUpdateClosing();
		removeUpdateFailure();
    };
  }, []);

  const loadBase = async () => {
    const [
      nextDashboard,
      nextHealth,
      nextCloudSync,
      nextCompanies,
      nextCampaigns,
      nextStageTypes,
			nextResumes,
    ] = await Promise.all([
      api.dashboard(),
      api.health(),
      api.cloudSyncStatus(),
      api.companies(),
      api.campaigns(),
      api.stageTypes(),
			api.resumes(true),
    ]);
    setDashboard(nextDashboard);
    setHealth(nextHealth);
    setCloudSync(nextCloudSync);
		// Keep a baseline from the initial read so a fast background cloud pull
		// still causes the visible lists to refresh when it completes.
		cloudSyncSnapshot.current = {
			activity: nextCloudSync.activity,
			state: nextCloudSync.state,
			lastSuccessAt: nextCloudSync.lastSuccessAt,
		};
    setCompanies(nextCompanies);
    setCampaigns(nextCampaigns);
    setStageTypes(nextStageTypes);
		setResumes(nextResumes);
  };
  const loadPositions = async (
    query = "",
    status = positionFilter,
    sortBy = positionSortBy,
    sortOrder = positionSortOrder,
  ) =>
    setPositions(
      await api.positions(status, query, positionPage, 20, sortBy, sortOrder),
    );
  const loadApplications = async (
    query = "",
    status = applicationFilter,
    sortBy = applicationSortBy,
    sortOrder = applicationSortOrder,
    stageType = applicationStageType,
    stageStatus = applicationStageStatus,
    resumeID = applicationResumeID,
  ) =>
    setApplications(
      await api.applications({
        status,
        query,
        page: applicationPage,
        pageSize: 20,
        sortBy,
        sortOrder,
        stageType,
        stageStatus,
        resumeId: resumeID,
      }),
    );
  const loadSchedule = async () =>
    setScheduleItems(await api.schedule({ from: "", to: "" }));
  const load = async () => {
    setLoading(true);
    try {
	  await loadBase();
	  if (view === "positions") await loadPositions();
	  if (view === "applications") await loadApplications();
	  if (view === "search" && searchQuery)
        await Promise.all([
          loadPositions(searchQuery, "all"),
          loadApplications(searchQuery, "all"),
        ]);
      if (view === "calendar" || view === "todos") await loadSchedule();
      setError("");
    } catch (reason) {
      setError(messageOf(reason));
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => {
    void load();
  }, [
    view,
    positionFilter,
    applicationFilter,
    applicationStageType,
    applicationStageStatus,
    applicationResumeID,
    positionSortBy,
    positionSortOrder,
    applicationSortBy,
    applicationSortOrder,
    applicationPage,
    positionPage,
    searchQuery,
    cloudSyncRevision,
  ]);

  useEffect(() => {
    if (cloudSyncRevision === 0) return;
    if (selectedPosition) {
      void api.positionDetail(selectedPosition.position.id)
        .then(setSelectedPosition)
        .catch((reason) => setError(messageOf(reason)));
    }
    if (selectedApplication) {
      void api.applicationDetail(selectedApplication.application.id)
        .then(setSelectedApplication)
        .catch((reason) => setError(messageOf(reason)));
    }
  }, [cloudSyncRevision]);

  useEffect(() => {
    const refreshCloudSync = () => {
      void api.cloudSyncStatus().then((next) => {
        const previous = cloudSyncSnapshot.current;
        const successfulCheckCompleted = Boolean(
          previous &&
          (next.state === "synced" || next.state === "pending" || next.state === "conflict") &&
          ((next.lastSuccessAt && next.lastSuccessAt !== previous.lastSuccessAt) ||
            (previous.activity !== "" && next.activity === "")),
        );
        cloudSyncSnapshot.current = {
          activity: next.activity,
          state: next.state,
          lastSuccessAt: next.lastSuccessAt,
        };
        setCloudSync(next);
        if (successfulCheckCompleted) setCloudSyncRevision((value) => value + 1);
      }).catch(() => undefined);
    };
		// A fast periodic check can complete between two polls. Keep this at one
		// second so the live checking state is observable without adding network
		// delay to the actual sync operation.
		const timer = window.setInterval(refreshCloudSync, 1_000);
    return () => window.clearInterval(timer);
  }, [closingForSync]);

	useEffect(() => {
		let active = true;
		const refreshUpdate = () => {
			void api.appUpdateStatus().then((next) => {
				if (active) setAppUpdate(next);
			}).catch(() => undefined);
		};
		refreshUpdate();
		const timer = window.setInterval(refreshUpdate, 600);
		return () => {
			active = false;
			window.clearInterval(timer);
		};
	}, []);

  const notify = (message: string) => {
    setToast(message);
    window.setTimeout(() => setToast(""), 2600);
  };
  const refresh = async (message: string) => {
    setDialog(null);
    setEditingCompany(null);
    setEditingCampaign(null);
    setEditingPosition(null);
    setEditingApplication(null);
		setEditingResume(null);
    setEditingStage(null);
    await load();
    notify(message);
  };
  const openNew = (name: DialogName) => {
    setEditingCompany(null);
    setEditingCampaign(null);
    setEditingPosition(null);
    setEditingApplication(null);
		setEditingResume(null);
    setEditingStage(null);
    setDialog(name);
  };
  const openPosition = async (id: string) => {
    try {
      setSelectedApplication(null);
      setSelectedPosition(await api.positionDetail(id));
    } catch (reason) {
      setError(messageOf(reason));
    }
  };
  const openApplication = async (id: string) => {
    try {
      setSelectedPosition(null);
      setSelectedApplication(await api.applicationDetail(id));
    } catch (reason) {
      setError(messageOf(reason));
    }
  };
  const openDeletion = (target: DeletionTarget) => {
    setDeletionTarget(target);
    setDialog("delete");
  };
  const finishDeletion = async () => {
    const target = deletionTarget;
    setDialog(null);
    setDeletionTarget(null);
    setSelectedPosition(null);
    setSelectedApplication(null);
    await load();
    if (target) notify(`${deletionLabels[target.entityType]}已删除`);
  };
  const runSearch = () => {
    setSelectedPosition(null);
    setSelectedApplication(null);
    setPositionPage(1);
    setApplicationPage(1);
    setSearchQuery(searchDraft.trim());
    setView("search");
  };
  const clearSearch = () => {
    setSearchDraft("");
    setSearchQuery("");
    setPositionPage(1);
    setApplicationPage(1);
  };
  const openApplicationStatistics = (
    status = "all",
    stageType = "",
    stageStatus = "",
  ) => {
    setSelectedPosition(null);
    setSelectedApplication(null);
    setApplicationFilter(status);
    setApplicationStageType(stageType);
    setApplicationStageStatus(stageStatus);
    setApplicationResumeID("");
    setApplicationPage(1);
    setView("applications");
  };

  const title = selectedPosition
    ? selectedPosition.position.title
    : selectedApplication
      ? `${selectedApplication.position.title} · 投递详情`
      : view === "dashboard"
        ? "总览"
        : view === "positions"
          ? "岗位管理"
            : view === "applications"
              ? "投递记录"
				: view === "resumes"
					? "简历库"
            : view === "calendar"
              ? "日历"
              : view === "todos"
                ? "待办"
                : view === "directory"
                  ? "公司与批次"
                  : "搜索结果";
  const sourceBackLabel =
    view === "calendar"
      ? "返回日历"
      : view === "todos"
        ? "返回待办"
        : view === "search"
          ? "返回搜索结果"
          : view === "positions"
            ? "返回岗位管理"
            : view === "dashboard"
              ? "返回总览"
              : "返回投递记录";
  return (
    <StageTypeCatalogContext.Provider value={stageTypes}>
      <div className="app-shell">
        <aside className="sidebar">
          <div className="brand">
            <div className="brand-mark">
              <Compass size={19} />
            </div>
            <div>
              <strong>OfferAtlas</strong>
            </div>
          </div>
          <nav className="nav-list" aria-label="主导航">
            <Nav
              active={
                view === "dashboard" &&
                !selectedPosition &&
                !selectedApplication
              }
              icon={<LayoutDashboard size={17} />}
              onClick={() => {
                setSelectedPosition(null);
                setSelectedApplication(null);
                setView("dashboard");
              }}
            >
              总览
            </Nav>
            <Nav
              active={view === "positions" || Boolean(selectedPosition)}
              icon={<BriefcaseBusiness size={17} />}
              onClick={() => {
                setSelectedPosition(null);
                setSelectedApplication(null);
                setView("positions");
              }}
            >
              岗位管理
            </Nav>
            <Nav
              active={view === "applications" || Boolean(selectedApplication)}
              icon={<ClipboardList size={17} />}
              onClick={() => {
                setSelectedPosition(null);
                setSelectedApplication(null);
                setView("applications");
              }}
            >
              投递记录
            </Nav>
				<Nav
					active={view === "resumes"}
					icon={<FileStack size={17} />}
					onClick={() => {
						setSelectedPosition(null);
						setSelectedApplication(null);
						setView("resumes");
					}}
				>
					简历库
				</Nav>
            <Nav
              active={view === "calendar"}
              icon={<CalendarRange size={17} />}
              onClick={() => {
                setSelectedPosition(null);
                setSelectedApplication(null);
                setView("calendar");
              }}
            >
              日历
            </Nav>
            <Nav
              active={view === "todos"}
              icon={<ListTodo size={17} />}
              onClick={() => {
                setSelectedPosition(null);
                setSelectedApplication(null);
                setView("todos");
              }}
            >
              待办
            </Nav>
            <Nav
              active={view === "directory"}
              icon={<Building2 size={17} />}
              onClick={() => {
                setSelectedPosition(null);
                setSelectedApplication(null);
                setView("directory");
              }}
            >
              公司与批次
            </Nav>
          </nav>
          <div className="sidebar-footer">
            <button
              type="button"
              className={`safety-sync-entry ${cloudSync?.state || "local_only"} ${health?.safety.lastError ? "mirror-danger" : ""}`}
              title={`云同步：${cloudSync?.message || "正在读取状态"}\n本地镜像：${health?.safety.lastError || "已同步，可恢复"}`}
              onClick={() => setBackupCenterOpen(true)}
            >
              <span className="safety-sync-icon" aria-hidden="true">
                <Cloud size={16} />
                <i />
              </span>
              <span className="safety-sync-copy">
                <strong>数据安全与同步</strong>
                <small className="safety-sync-cloud">
                  <b>云端</b>
                  <span>{cloudSyncCompactLabel(cloudSync)}</span>
                </small>
                <small className="safety-sync-detail">
                  <span>{cloudSyncProgressDetail(cloudSync)}</span>
                </small>
                <small className={health?.safety.lastError ? "safety-sync-mirror danger" : "safety-sync-mirror"}>
                  <b>本地</b>
                  <span>{health?.safety.lastError ? "镜像待检查" : "镜像已保护"}</span>
                </small>
              </span>
              <ChevronRight size={14} aria-hidden="true" />
            </button>
          </div>
        </aside>
        <main
          className={`main-content ${view === "calendar" && !selectedPosition && !selectedApplication ? "calendar-workspace" : ""} ${view === "dashboard" && !selectedPosition && !selectedApplication ? "dashboard-viewport" : ""}`}
        >
          <header className="topbar">
            <div className="topbar-leading">
              <span className="topbar-mark">
                <Compass size={15} />
              </span>
              <div className="breadcrumb">
                <span>OfferAtlas</span>
                <i />
                <strong>{title}</strong>
              </div>
            </div>
            <div className="topbar-actions">
				<button
					className={`app-update-entry ${appUpdate?.available ? "available" : ""} ${appUpdate?.state === "failed" ? "failed" : ""} ${appUpdate?.state === "checking" || appUpdate?.state === "downloading" ? "busy" : ""}`}
					type="button"
					title={appUpdate?.state === "failed" ? (appUpdate.message || "新版本检测超时，请检查网络后重试") : appUpdate?.available ? `发现 ${appUpdate.latestVersion}，查看更新` : "检查应用更新"}
					onClick={() => {
						setDialog("update");
						if (appUpdate?.state === "downloading" || appUpdate?.state === "installing" || appUpdate?.state === "downloaded") return;
						void api.checkForAppUpdate().then(setAppUpdate).catch(() => {
							void api.appUpdateStatus().then(setAppUpdate).catch(() => undefined);
						});
					}}
				>
					{appUpdate?.state === "failed" ? <CircleAlert size={14} /> : <RotateCcw size={14} />}
					<span>{appUpdate?.state === "failed" ? "检查超时" : appUpdate?.available ? `发现 v${appUpdate.latestVersion}` : "应用更新"}</span>
				</button>
              <div className="search-box" role="search">
                <Search className="search-leading" size={15} />
                <input
                  value={searchDraft}
                  onChange={(event) => setSearchDraft(event.target.value)}
                  onKeyDown={(event) => {
                    if (event.key === "Enter") {
                      event.preventDefault();
                      runSearch();
                    }
                    if (event.key === "Escape") clearSearch();
                  }}
                  placeholder="搜索公司、批次、岗位或编号"
                  aria-label="全局搜索：公司、招聘批次、岗位名称或岗位编号"
                />
                <button
                  type="button"
                  title="全局搜索（Enter）"
                  onClick={runSearch}
                >
                  <Search size={15} />
                </button>
              </div>
            </div>
          </header>
          {error && (
            <div className="error-banner">
              <span>{error}</span>
              <button title="关闭提示" onClick={() => setError("")}>
                <X size={15} />
              </button>
            </div>
          )}
          {loading && <div className="loading-line" />}
          {selectedPosition ? (
            <PositionDetailView
              detail={selectedPosition}
              backLabel={sourceBackLabel}
              onNotify={notify}
              onBack={() => setSelectedPosition(null)}
              onEditPosition={() => {
                setEditingPosition(selectedPosition.position);
                setDialog("position");
              }}
              onDeletePosition={() =>
                openDeletion({
                  entityType: "position",
                  id: selectedPosition.position.id,
                  name: selectedPosition.position.title,
                })
              }
              onCreateApplication={() => {
                setEditingApplication(null);
                setDialog("application");
              }}
              onOpenApplication={(id) => void openApplication(id)}
              onAddAttachments={async () => {
                try {
                  await api.addPositionAttachments(
                    selectedPosition.position.id,
                  );
                  await openPosition(selectedPosition.position.id);
                  notify("岗位附件已添加");
                } catch (reason) {
                  setError(messageOf(reason));
                }
              }}
              onDeleteAttachment={async (attachment) => {
                setProtectedAction({
                  title: "删除岗位附件",
                  subject: attachment.originalName,
                  description:
                    "该附件会从本机岗位资料中移除，并在后续云同步时一并删除。此操作无法在应用内恢复。",
                  confirmationText: attachment.originalName,
                  confirmLabel: "确认删除附件",
                  action: async () => {
                    await api.deletePositionAttachment(attachment.id);
                    await openPosition(selectedPosition.position.id);
                    notify("岗位附件已删除");
                  },
                });
              }}
              onOpenAttachment={async (attachment) => {
                try {
                  await api.openPositionAttachment(attachment.id);
                } catch (reason) {
                  setError(messageOf(reason));
                }
              }}
            />
          ) : selectedApplication ? (
            <ApplicationDetailView
              detail={selectedApplication}
              backLabel={sourceBackLabel}
			  onOpenResume={async (resumeID) => {
                try {
				  await api.openResume(resumeID);
                } catch (reason) {
                  setError(messageOf(reason));
                }
              }}
				onClearResume={async () => {
					if (!selectedApplication?.resume) return;
                  setProtectedAction({
                    title: "清除投递简历关联",
                    subject: selectedApplication.resume.name,
                    description:
                      "这条投递将不再关联该简历版本，简历库中的原始文件和其他投递关联会保留。",
                    confirmationText: "清除关联",
                    confirmLabel: "确认清除关联",
                    tone: "caution",
                    action: async () => {
                      if (selectedApplication.application.resumeId) {
                        await api.clearApplicationResume(selectedApplication.application.id);
                      } else {
                        await api.deleteApplicationResume(selectedApplication.application.id);
                      }
                      await openApplication(selectedApplication.application.id);
                      await load();
                      notify("已清除投递简历关联");
                    },
                  });
				}}
              onBack={() => setSelectedApplication(null)}
              onOpenPosition={(id) => void openPosition(id)}
              onEditApplication={() => {
                setEditingApplication(selectedApplication.application);
                setDialog("application");
              }}
              onDeleteApplication={() =>
                openDeletion({
                  entityType: "application",
                  id: selectedApplication.application.id,
                  name: selectedApplication.position.title,
                })
              }
              onCreateStage={() => {
                setEditingStage(null);
                setDialog("stage");
              }}
              onEditStage={(stage) => {
                setEditingStage(stage);
                setDialog("stage");
              }}
              onDeleteStage={async (stage) => {
                const name = `${stageTypeLabel(stage.type, stageTypes)}${stage.content ? ` · ${stage.content}` : ""}`;
                setProtectedAction({
                  title: "删除流程节点",
                  subject: name,
                  description:
                    "该节点的预约时间、结果和备注会一起删除，投递状态将据剩余流程自动重新计算。",
                  confirmationText: name,
                  confirmLabel: "确认删除节点",
                  action: async () => {
                    await api.deleteStage(stage.id);
                    await openApplication(selectedApplication.application.id);
                    notify("流程节点已删除");
                  },
                });
              }}
              onMoveStage={async (stage, direction) => {
                const next = [...selectedApplication.stages];
                const index = next.findIndex((item) => item.id === stage.id);
                const target = index + direction;
                if (target < 0 || target >= next.length) return;
                [next[index], next[target]] = [next[target], next[index]];
                try {
                  await api.reorderStages(
                    selectedApplication.application.id,
                    next.map((item) => item.id),
                  );
                  await openApplication(selectedApplication.application.id);
                } catch (reason) {
                  setError(messageOf(reason));
                }
              }}
            />
          ) : view === "dashboard" ? (
            <DashboardView
              dashboard={dashboard}
              onOpenApplications={openApplicationStatistics}
              onOpenTodos={() => setView("todos")}
              onQuickCapture={() => openNew("quick-position")}
            />
          ) : view === "positions" ? (
            <PositionsView
              page={positions}
              filter={positionFilter}
              sortBy={positionSortBy}
              sortOrder={positionSortOrder}
              onFilter={(value) => {
                setPositionFilter(value);
                setPositionPage(1);
              }}
              onSort={(field, order) => {
                setPositionSortBy(field);
                setPositionSortOrder(order);
                setPositionPage(1);
              }}
              onPage={setPositionPage}
              onSelect={(id) => void openPosition(id)}
              onDelete={(item) =>
                openDeletion({
                  entityType: "position",
                  id: item.id,
                  name: item.title,
                })
              }
              onNew={() => openNew("position")}
              onQuickCapture={() => openNew("quick-position")}
            />
          ) : view === "applications" ? (
            <ApplicationsView
              page={applications}
              filter={applicationFilter}
              stageType={applicationStageType}
              stageStatus={applicationStageStatus}
              resumeID={applicationResumeID}
              resumes={resumes}
              stageTypes={stageTypes}
              sortBy={applicationSortBy}
              sortOrder={applicationSortOrder}
              onApplyFilter={(status, type, nodeStatus, resumeID) => {
                setApplicationFilter(status);
                setApplicationStageType(type);
                setApplicationStageStatus(nodeStatus);
                setApplicationResumeID(resumeID);
                setApplicationPage(1);
              }}
              onSort={(field, order) => {
                setApplicationSortBy(field);
                setApplicationSortOrder(order);
                setApplicationPage(1);
              }}
              onPage={setApplicationPage}
              onSelect={(id) => void openApplication(id)}
            />
			) : view === "resumes" ? (
				<ResumeLibraryView
					items={resumes}
					onNew={() => openNew("resume")}
					onOpen={async (id) => {
						try {
							await api.openResume(id);
						} catch (reason) {
							setError(messageOf(reason));
						}
					}}
					onOpenApplications={(resumeID) => {
						setSelectedPosition(null);
						setSelectedApplication(null);
						setApplicationFilter("all");
						setApplicationStageType("");
						setApplicationStageStatus("");
						setApplicationResumeID(resumeID);
						setApplicationPage(1);
						setView("applications");
					}}
					onEdit={(item) => {
						setEditingResume(item);
						setDialog("resume");
					}}
					onArchive={async (item, archived) => {
						try {
							await api.saveResume({ id: item.id, name: item.name, archived });
							await load();
							notify(archived ? "简历版本已归档" : "简历版本已恢复使用");
						} catch (reason) {
							setError(messageOf(reason));
						}
					}}
					onDelete={async (item) => {
                    setProtectedAction({
                      title: "删除简历版本",
                      subject: item.name,
                      description:
                        "该未使用简历版本及其原始文件会从本机移除，并在后续云同步时一并删除。",
                      confirmationText: item.name,
                      confirmLabel: "确认删除简历",
                      action: async () => {
                        await api.deleteResume(item.id);
                        await load();
                        notify("简历版本已删除");
                      },
                    });
					}}
				/>
          ) : view === "search" ? (
            <SearchView
              query={searchQuery}
              positions={positions}
              applications={applications}
              onClear={clearSearch}
              onOpenPosition={(id) => void openPosition(id)}
              onOpenApplication={(id) => void openApplication(id)}
              onPositionPage={setPositionPage}
              onApplicationPage={setApplicationPage}
            />
          ) : view === "calendar" ? (
            <CalendarView
              items={scheduleItems}
              onOpenApplication={(id) => void openApplication(id)}
            />
          ) : view === "todos" ? (
            <TodosView
              items={scheduleItems}
              onOpenApplication={(id) => void openApplication(id)}
            />
          ) : (
            <DirectoryView
              companies={companies}
              campaigns={campaigns}
              onNewCompany={() => openNew("company")}
              onNewCampaign={() => openNew("campaign")}
              onEditCompany={(item) => {
                setEditingCompany(item);
                setDialog("company");
              }}
              onDeleteCompany={(item) =>
                openDeletion({
                  entityType: "company",
                  id: item.id,
                  name: item.name,
                })
              }
              onEditCampaign={(item) => {
                setEditingCampaign(item);
                setDialog("campaign");
              }}
              onDeleteCampaign={(item) =>
                openDeletion({
                  entityType: "campaign",
                  id: item.id,
                  name: item.name,
                })
              }
            />
          )}
        </main>
        {dialog === "company" && (
          <CompanyDialog
            initial={editingCompany}
            onClose={() => setDialog(null)}
            onSaved={() => void refresh("公司已保存")}
          />
        )}
        {dialog === "campaign" && (
          <CampaignDialog
            companies={companies}
            initial={editingCampaign}
            onClose={() => setDialog(null)}
            onSaved={() => void refresh("招聘批次已保存")}
          />
        )}
        {dialog === "position" && (
          <PositionDialog
            companies={companies}
            campaigns={campaigns}
            initial={editingPosition}
            onClose={() => setDialog(null)}
            onSaved={async () => {
              const positionID = selectedPosition?.position.id;
              await refresh("岗位已保存");
              if (positionID) await openPosition(positionID);
            }}
          />
        )}
        {dialog === "quick-position" && (
          <QuickCapturePositionDialog
            companies={companies}
            campaigns={campaigns}
            onClose={() => setDialog(null)}
            onSaved={async (position) => {
              await refresh("岗位已快速收录");
              await openPosition(position.id);
            }}
          />
        )}
        {dialog === "application" &&
          (selectedPosition || selectedApplication) && (
            <ApplicationDialog
              positionID={
                selectedPosition?.position.id ||
                selectedApplication?.position.id ||
                ""
              }
				resumes={resumes}
              initial={
                editingApplication ||
                selectedPosition?.application ||
                selectedApplication?.application ||
                null
              }
              onClose={() => setDialog(null)}
				onManageResumes={() => {
					setDialog(null);
					setSelectedPosition(null);
					setSelectedApplication(null);
					setView("resumes");
				}}
              onSaved={async () => {
                const id = selectedPosition?.position.id;
                const appID = selectedApplication?.application.id;
                await refresh("投递记录已保存");
                if (id) await openPosition(id);
                if (appID) await openApplication(appID);
              }}
            />
          )}
		{dialog === "resume" && (
			<ResumeDialog
				initial={editingResume}
				onClose={() => setDialog(null)}
				onSaved={() => void refresh(editingResume ? "简历版本已保存" : "简历版本已添加")}
			/>
		)}
        {dialog === "stage" &&
          (selectedPosition?.application ||
            selectedApplication?.application) && (
            <StageDialog
              applicationID={
                selectedPosition?.application?.id ||
                selectedApplication?.application.id ||
                ""
              }
              initial={editingStage}
              onClose={() => setDialog(null)}
              onManageTypes={() => setManagingStageTypes(true)}
              onSaved={async () => {
                const id = selectedPosition?.position.id;
                const appID = selectedApplication?.application.id;
                await refresh("流程节点已保存");
                if (id) await openPosition(id);
                if (appID) await openApplication(appID);
              }}
            />
          )}
        {dialog === "delete" && deletionTarget && (
          <DeleteDialog
            target={deletionTarget}
            onClose={() => {
              setDialog(null);
              setDeletionTarget(null);
            }}
            onDeleted={() => void finishDeletion()}
          />
        )}
		{dialog === "update" && (
			<UpdateDialog
				status={appUpdate}
				onClose={() => setDialog(null)}
				onChanged={setAppUpdate}
			/>
		)}
        {managingStageTypes && (
          <StageTypesDialog
            items={stageTypes}
            onClose={() => setManagingStageTypes(false)}
            onRequestProtectedAction={setProtectedAction}
            onChanged={async (message) => {
              try {
                setStageTypes(await api.stageTypes());
                notify(message);
              } catch (reason) {
                setError(messageOf(reason));
              }
            }}
          />
        )}
        {backupCenterOpen && (
          <BackupCenterDialog
            onClose={() => setBackupCenterOpen(false)}
            onCloudDataChanged={() => setCloudSyncRevision((value) => value + 1)}
            onRequestProtectedAction={setProtectedAction}
            onRestored={async (result) => {
              await load();
              if (selectedPosition) await openPosition(selectedPosition.position.id);
              if (selectedApplication) await openApplication(selectedApplication.application.id);
              notify(
                `已恢复 ${textDateTime(result.restoredBackup.createdAt)} 的备份；恢复前版本已自动留存`,
              );
            }}
          />
        )}
        {protectedAction && (
          <ProtectedActionDialog
            action={protectedAction}
            onClose={() => setProtectedAction(null)}
          />
        )}
		{closingForSync && (
          <section className="safe-exit-overlay" role="status" aria-live="assertive" aria-label="正在安全退出">
            <div className="safe-exit-panel">
              <span className="safe-exit-icon" aria-hidden="true"><Cloud size={25} /></span>
              <div>
                <p>{closingForUpdate ? "应用更新" : "数据安全"}</p>
                <h2>{closingForUpdate ? "正在完成更新" : "正在安全退出"}</h2>
                <strong>{closingForUpdate ? "正在确认云端同步状态" : cloudSyncCompactLabel(cloudSync)}</strong>
                <span>{closingForUpdate ? (closeSyncMessage || "正在准备安全重启") : (cloudSync?.message || closeSyncMessage || "正在确认本机更改是否已同步到 Gitee")}</span>
                <small>{closingForUpdate ? "同步完成后将自动启动新版本，请保持网络连接。" : "完成后应用将自动关闭，请保持网络连接。"}</small>
              </div>
            </div>
          </section>
        )}
        {toast && (
          <div className="toast">
            <CheckCircle2 size={16} />
            {toast}
          </div>
        )}
      </div>
    </StageTypeCatalogContext.Provider>
  );
}

function Nav({
  active,
  icon,
  children,
  onClick,
}: {
  active: boolean;
  icon: ReactNode;
  children: ReactNode;
  onClick: () => void;
}) {
  return (
    <button
      className={`nav-button ${active ? "active" : ""}`}
      aria-current={active ? "page" : undefined}
      onClick={onClick}
    >
      {icon}
      <span>{children}</span>
    </button>
  );
}
function Badge({ tone, text }: { tone: string; text: string }) {
  return (
    <span className={`status-badge ${tone}`} aria-label={text}>
      <i aria-hidden="true" />
      {text}
    </span>
  );
}
function HelpTip({
  children,
  label = "查看说明",
}: {
  children: ReactNode;
  label?: string;
}) {
  return (
    <span className="help-tip">
      <button type="button" className="help-tip-trigger" aria-label={label}>
        <CircleHelp size={12} aria-hidden="true" />
      </button>
      <span className="help-tip-popover" role="tooltip">
        {children}
      </span>
    </span>
  );
}
function ApplicationStatusHelp() {
  return (
    <HelpTip label="了解投递状态的计算规则">
      <strong>投递状态如何计算</strong>
      <span>
        系统以最后一个流程节点为准，并在保存节点或调整节点顺序后自动更新。
      </span>
      <span>
        最后节点“未通过”时，投递显示“未通过”；最后节点为 Offer
        且“通过”时，投递显示“已通过”；其他情况均为“进行中”。
      </span>
    </HelpTip>
  );
}
function PositionSubmissionHelp() {
  return (
    <HelpTip label="了解岗位投递时间的含义">
      <strong>投递时间</strong>
      <span>
        未创建投递记录时显示“未投递”；创建后记录实际投递日期。
      </span>
    </HelpTip>
  );
}
function PositionStatusHelp() {
  return (
    <HelpTip label="了解岗位投递状态的计算规则">
      <strong>投递状态如何计算</strong>
      <span>
        岗位创建投递记录后，这里显示该投递的自动状态；未投递岗位不显示投递状态。
      </span>
      <span>
        投递状态会随最后一个流程节点更新：最后节点未通过时显示“未通过”，Offer
        节点通过时显示“已通过”，其他情况显示“进行中”。
      </span>
    </HelpTip>
  );
}
function CurrentStageHelp() {
  return (
    <HelpTip label="了解当前流程的含义">
      <strong>当前流程</strong>
      <span>
        这里展示该投递最后一个流程节点。可在投递详情中新增、编辑或调整节点顺序。
      </span>
    </HelpTip>
  );
}
function ApplicationFilterHelp() {
  return (
    <HelpTip label="了解筛选条件的组合方式">
      <strong>筛选条件如何组合</strong>
      <span>投递状态筛选的是整条投递的自动状态。</span>
      <span>节点类型与节点结果筛选的是流程中的节点；同时选择两项时，须由同一个节点同时满足。</span>
    </HelpTip>
  );
}
function StageStatusHelp() {
  return (
    <HelpTip label="了解节点状态的含义">
      <strong>节点状态</strong>
      <span>“已预约”表示尚未完成；“通过”和“未通过”表示该节点的结果。</span>
    </HelpTip>
  );
}
function PriorityBadge({ value }: { value: number }) {
  return (
    <span
      className={`priority-badge level-${value}`}
      aria-label={`优先级 ${value}`}
    >
      <small>优先级</small>
      <strong>{value}</strong>
    </span>
  );
}
function ActiveFilterChip({
  label,
  tone = "neutral",
  onRemove,
  title,
}: {
  label: string;
  tone?: string;
  onRemove: () => void;
  title: string;
}) {
  return (
    <button
      type="button"
      className={`active-filter-chip tone-${tone}`}
      onClick={onRemove}
      title={title}
    >
      <span>{label}</span>
      <X size={12} aria-hidden="true" />
    </button>
  );
}
function SortableTableHeader({
  label,
  help,
  field,
  sortBy,
  sortOrder,
  onSort,
}: {
  label: string;
  help?: ReactNode;
  field: SortField;
  sortBy: SortField;
  sortOrder: SortOrder;
  onSort: (field: SortField, order: SortOrder) => void;
}) {
  const active = sortBy === field;
  const subject = field === "priority" ? "优先级" : "投递时间";
  return (
    <span className={`sortable-table-heading ${active ? "active" : ""}`}>
      <span className={help ? "table-heading-with-help" : undefined}>
        {label}
        {help}
      </span>
      <span className="table-sort-arrows" aria-label={`${subject}排序`}>
        <button
          type="button"
          className={active && sortOrder === "asc" ? "selected" : ""}
          title={`${subject}升序`}
          aria-label={`${subject}升序`}
          aria-pressed={active && sortOrder === "asc"}
          onClick={() => onSort(field, "asc")}
        >
          <ArrowUp size={11} />
        </button>
        <button
          type="button"
          className={active && sortOrder === "desc" ? "selected" : ""}
          title={`${subject}降序`}
          aria-label={`${subject}降序`}
          aria-pressed={active && sortOrder === "desc"}
          onClick={() => onSort(field, "desc")}
        >
          <ArrowDown size={11} />
        </button>
      </span>
    </span>
  );
}
function StageGlyph({ type }: { type: StageType }) {
  return (
    <span className={`stage-glyph ${type}`}>
      {type === "written_test" ? (
        <FileText size={15} />
      ) : type === "assessment" ? (
        <ClipboardCheck size={15} />
      ) : type === "ai_interview" ? (
        <Bot size={15} />
      ) : [
          "first_interview",
          "second_interview",
          "third_interview",
          "fourth_interview",
        ].includes(type) ? (
        <MessageSquare size={15} />
      ) : type === "hr_interview" ? (
        <UsersRound size={15} />
      ) : type === "offer" ? (
        <BadgeCheck size={15} />
      ) : (
        <ClipboardCheck size={15} />
      )}
    </span>
  );
}
function StageStrip({ stages }: { stages: ApplicationStage[] }) {
  const typeLabel = useStageTypeLabel();
  return (
    <div className="stage-strip" aria-label="投递流程">
      {stages.length ? (
        stages.map((stage, index) => (
          <div className="stage-chip-wrap" key={stage.id}>
            <span
              className={`stage-chip ${stage.status}`}
              title={`${typeLabel(stage.type)}${stage.content ? ` · ${stage.content}` : ""} · ${stageStatusLabels[stage.status]} · ${stageTimeText(stage)}`}
            >
              <StageGlyph type={stage.type} />
              <span>
                <strong>
                  {typeLabel(stage.type)}
                  {stage.content ? ` · ${stage.content}` : ""}
                </strong>
                <small>
                  {stageStatusLabels[stage.status]} · {stageTimeText(stage)}
                </small>
              </span>
            </span>
            {index < stages.length - 1 && (
              <i
                className={`stage-chip-line ${stage.status}`}
                aria-hidden="true"
              />
            )}
          </div>
        ))
      ) : (
        <span className="stage-strip-empty">尚未添加流程节点</span>
      )}
    </div>
  );
}
function Empty({ text }: { text: string }) {
  return (
    <div className="empty-state">
      <ClipboardList size={21} />
      <span>{text}</span>
    </div>
  );
}
function messageOf(reason: unknown) {
  return reason instanceof Error ? reason.message : "操作未完成，请稍后重试";
}
function Metric({
  label,
  value,
  hint,
  icon,
  accent = "blue",
}: {
  label: string;
  value: number;
  hint: string;
  icon: ReactNode;
  accent?: string;
}) {
  return (
    <div className={`metric metric-${accent}`}>
      <div className="metric-top">
        <span>{label}</span>
        <i>{icon}</i>
      </div>
      <strong>{value}</strong>
      <small>{hint}</small>
    </div>
  );
}
function PanelHeader({
  title,
  action,
  onClick,
}: {
  title: string;
  action: string;
  onClick: () => void;
}) {
  return (
    <div className="panel-header">
      <h2>{title}</h2>
      {action && (
        <button className="text-button" onClick={onClick}>
          {action}
        </button>
      )}
    </div>
  );
}

function DashboardView({
  dashboard,
  onOpenApplications,
  onOpenTodos,
  onQuickCapture,
}: {
  dashboard: Dashboard;
  onOpenApplications: (
    status?: string,
    stageType?: string,
    stageStatus?: string,
  ) => void;
  onOpenTodos: () => void;
  onQuickCapture: () => void;
}) {
  const typeLabel = useStageTypeLabel();
  const openStat = (stageType = "", stageStatus = "") =>
    onOpenApplications("all", stageType, stageStatus);
  const StatValue = ({
    value,
    type,
    status,
  }: {
    value: number;
    type?: string;
    status?: string;
  }) => (
    <button
      className={`analytics-number-button metric-${status || "entered"}`}
      disabled={!value}
      onClick={() =>
        type || status ? openStat(type, status) : onOpenApplications()
      }
    >
      {value}
    </button>
  );
  const StageStats = ({ stats }: { stats: Dashboard["writtenTestStats"] }) => (
    <div className="stage-stat-card">
      <div className="stage-stat-head">
        <span className={`stage-type ${stats.type}`}>
          <StageGlyph type={stats.type} />
          {typeLabel(stats.type)}
        </span>
      </div>
      <div className="stage-stat-values">
        <span>
          进入
          <StatValue value={stats.entered} type={stats.type} />
        </span>
        <span>
          通过
          <StatValue value={stats.passed} type={stats.type} status="passed" />
        </span>
        <span>
          未通过
          <StatValue value={stats.failed} type={stats.type} status="failed" />
        </span>
      </div>
    </div>
  );
  return (
    <div className="page-content dashboard-page dashboard-analytics">
      <section className="page-heading compact">
        <div>
          <h1>投递总览</h1>
        </div>
      </section>
      <section className="dashboard-summary-grid">
        <button
          className="dashboard-summary-card total"
          onClick={() => onOpenApplications()}
        >
          <span>总投递</span>
          <strong>{dashboard.totalApplications}</strong>
          <small>全部投递记录</small>
          <ClipboardList size={17} />
        </button>
        <button
          className="dashboard-summary-card active"
          onClick={() => onOpenApplications("active")}
        >
          <span>推进中</span>
          <strong>{dashboard.activeApplications}</strong>
          <small>仍在流程中</small>
          <CalendarRange size={17} />
        </button>
        <button
          className="dashboard-summary-card offer"
          onClick={() => onOpenApplications("offer")}
        >
          <span>Offer</span>
          <strong>{dashboard.offerApplications}</strong>
          <small>已获得录用</small>
          <Trophy size={17} />
        </button>
        <button
          className="dashboard-summary-card rejected"
          onClick={() => onOpenApplications("rejected")}
        >
          <span>未通过</span>
          <strong>{dashboard.rejectedApplications}</strong>
          <small>最终未通过</small>
          <CircleAlert size={17} />
        </button>
      </section>
      <section className="dashboard-lower-grid">
        <div className="dashboard-action-stack">
          <button
            className="dashboard-capture dashboard-capture-large"
            type="button"
            onClick={onQuickCapture}
          >
            <span className="dashboard-capture-mark">
              <Zap size={20} />
            </span>
            <span className="dashboard-capture-copy">
              <small>岗位管理</small>
              <strong>快速收录</strong>
              <em>一次记录公司、招聘批次和岗位</em>
            </span>
            <ArrowRight size={18} aria-hidden="true" />
          </button>
          <button
            type="button"
            className={`dashboard-todo-reminder ${dashboard.todoCount ? "has-todos" : "clear"}`}
            onClick={onOpenTodos}
          >
            <span className="dashboard-todo-mark">
              <ListTodo size={19} />
            </span>
            <span className="dashboard-todo-copy">
              <small>待办提醒</small>
              <strong>
                {dashboard.todoCount
                  ? `还有 ${dashboard.todoCount} 项待办`
                  : "当前没有待办"}
              </strong>
              <em>
                {dashboard.todoCount
                  ? "前往待办，集中安排笔试、面试与结果确认"
                  : "后续安排会自动汇总到待办"}
              </em>
            </span>
            <span className="dashboard-todo-count" aria-hidden="true">
              {dashboard.todoCount}
            </span>
            <ArrowRight size={18} aria-hidden="true" />
          </button>
          <section className="panel dashboard-metrics-panel dashboard-screening-panel">
            <div className="analytics-panel-heading">
              <div>
                <h2>笔试与测评</h2>
                <span>按投递记录去重</span>
              </div>
            </div>
            <div className="stage-stat-grid">
              <StageStats stats={dashboard.writtenTestStats} />
              <StageStats stats={dashboard.assessmentStats} />
            </div>
          </section>
        </div>
        <section className="panel dashboard-metrics-panel dashboard-interview-panel">
          <div className="analytics-panel-heading">
            <div>
              <h2>面试推进</h2>
              <span>按流程节点统计</span>
            </div>
            <div className="interview-overview-stat" aria-label={`进面 ${dashboard.interviewedApplications} 条投递`}>
              <span>进面</span>
              <strong>{dashboard.interviewedApplications}</strong>
              <em>条投递</em>
            </div>
          </div>
          <div className="interview-matrix">
            <div className="interview-matrix-head">
              <span>环节</span>
              <span>进入</span>
              <span>通过</span>
              <span>未通过</span>
            </div>
            {dashboard.interviewStats.map((stats) => (
              <div
                className={`interview-matrix-row ${stats.entered === 0 ? "empty" : ""}`}
                key={stats.type}
              >
                <span className={`stage-type ${stats.type}`}>
                  <StageGlyph type={stats.type} />
                  {typeLabel(stats.type)}
                </span>
                <StatValue value={stats.entered} type={stats.type} />
                <StatValue
                  value={stats.passed}
                  type={stats.type}
                  status="passed"
                />
                <StatValue
                  value={stats.failed}
                  type={stats.type}
                  status="failed"
                />
              </div>
            ))}
          </div>
        </section>
      </section>
    </div>
  );
}

function PositionFilterPopover({
  status,
  onClose,
  onApply,
}: {
  status: string;
  onClose: () => void;
  onApply: (status: string) => void;
}) {
  const [nextStatus, setNextStatus] = useState(status);
  return (
    <div
      className="toolbar-popover filter-popover"
      role="dialog"
      aria-label="筛选岗位"
    >
      <div className="toolbar-popover-heading">
        <strong>筛选岗位</strong>
        <span>按投递状态定位岗位</span>
      </div>
      <div className="toolbar-popover-section">
        <span>投递状态</span>
        <div className="toolbar-choice-grid">
          {[
            ["all", "全部岗位"],
            ["unapplied", "未投递"],
            ["applied", "已投递"],
          ].map(([value, label]) => (
            <button
              type="button"
              key={value}
              className={nextStatus === value ? "selected" : ""}
              onClick={() => setNextStatus(value)}
            >
              {label}
            </button>
          ))}
        </div>
      </div>
      <div className="toolbar-popover-actions">
        <button
          type="button"
          className="ghost-button"
          onClick={() => setNextStatus("all")}
        >
          清空条件
        </button>
        <button
          type="button"
          className="primary-button"
          onClick={() => {
            onApply(nextStatus);
            onClose();
          }}
        >
          应用筛选
        </button>
      </div>
    </div>
  );
}

function ApplicationFilterPopover({
  status,
  stageType,
  stageStatus,
  resumeID,
  resumes,
  stageTypes,
  onClose,
  onApply,
}: {
  status: string;
  stageType: string;
  stageStatus: string;
  resumeID: string;
  resumes: Resume[];
  stageTypes: StageTypeDefinition[];
  onClose: () => void;
  onApply: (
    status: string,
    stageType: string,
    stageStatus: string,
    resumeID: string,
  ) => void;
}) {
  const [nextStatus, setNextStatus] = useState(status);
  const [nextType, setNextType] = useState(stageType);
  const [nextStageStatus, setNextStageStatus] = useState(stageStatus);
  const [nextResumeID, setNextResumeID] = useState(resumeID);
  const systemTypes = stageTypes.filter((item) => item.system);
  const customTypes = stageTypes.filter((item) => !item.system);
  return (
    <div
      className="toolbar-popover filter-popover application-filter-popover"
      role="dialog"
      aria-label="筛选投递记录"
    >
      <div className="toolbar-popover-heading">
        <strong>筛选投递记录</strong>
        <span className="popover-heading-with-help">
          组合投递结果与流程条件 <ApplicationFilterHelp />
        </span>
      </div>
      <div className="toolbar-popover-section">
        <span className="popover-heading-with-help">
          投递状态 <ApplicationStatusHelp />
        </span>
        <div className="toolbar-choice-grid">
          {[
            ["all", "全部"],
            ["active", "进行中"],
            ["offer", "Offer"],
            ["rejected", "未通过"],
          ].map(([value, label]) => (
            <button
              type="button"
              key={value}
              className={nextStatus === value ? "selected" : ""}
              onClick={() => setNextStatus(value)}
            >
              {label}
            </button>
          ))}
        </div>
      </div>
      <div className="toolbar-popover-field-grid">
        <label>
          包含节点类型
          <select
            value={nextType}
            onChange={(event) => setNextType(event.target.value)}
          >
            <option value="">不限节点类型</option>
            <optgroup label="系统类型（纳入统计）">
              {systemTypes.map((item) => (
                <option key={item.id} value={item.id}>
                  {item.name}
                </option>
              ))}
            </optgroup>
            {customTypes.length > 0 && (
              <optgroup label="自定义类型">
                {customTypes.map((item) => (
                  <option key={item.id} value={item.id}>
                    {item.name}
                  </option>
                ))}
              </optgroup>
            )}
          </select>
        </label>
        <div className="toolbar-popover-field">
          <span className="popover-heading-with-help">
            <label htmlFor="application-stage-status-filter">节点结果</label>
            <StageStatusHelp />
          </span>
          <select
            id="application-stage-status-filter"
            value={nextStageStatus}
            onChange={(event) => setNextStageStatus(event.target.value)}
          >
            <option value="">不限节点状态</option>
            <option value="scheduled">已预约</option>
            <option value="passed">通过</option>
            <option value="failed">未通过</option>
          </select>
        </div>
        <label className="application-resume-filter">
          简历版本
          <select
            value={nextResumeID}
            onChange={(event) => setNextResumeID(event.target.value)}
          >
            <option value="">不限简历版本</option>
            <option value="__none__">未关联简历</option>
            {resumes.length > 0 && (
              <optgroup label="简历库">
                {resumes.map((item) => (
                  <option key={item.id} value={item.id}>
                    {item.name}{item.archived ? "（已归档）" : ""}
                  </option>
                ))}
              </optgroup>
            )}
          </select>
        </label>
      </div>
      <div className="toolbar-popover-actions">
        <button
          type="button"
          className="ghost-button"
          onClick={() => {
            setNextStatus("all");
            setNextType("");
            setNextStageStatus("");
            setNextResumeID("");
          }}
        >
          清空条件
        </button>
        <button
          type="button"
          className="primary-button"
          onClick={() => {
            onApply(nextStatus, nextType, nextStageStatus, nextResumeID);
            onClose();
          }}
        >
          应用筛选
        </button>
      </div>
    </div>
  );
}

function PositionsView({
  page,
  filter,
  sortBy,
  sortOrder,
  onFilter,
  onSort,
  onPage,
  onSelect,
  onDelete,
  onNew,
  onQuickCapture,
}: {
  page: PositionPage;
  filter: string;
  sortBy: SortField;
  sortOrder: SortOrder;
  onFilter: (value: string) => void;
  onSort: (field: SortField, order: SortOrder) => void;
  onPage: (value: number) => void;
  onSelect: (id: string) => void;
  onDelete: (item: PositionSummary) => void;
  onNew: () => void;
  onQuickCapture: () => void;
}) {
  const typeLabel = useStageTypeLabel();
  const totalPages = Math.max(1, Math.ceil(page.total / page.pageSize));
  const [openMenu, setOpenMenu] = useState<"filter" | null>(null);
  return (
    <div className="page-content">
      <section className="page-heading compact">
        <div>
          <h1>岗位管理</h1>
          <p className="muted">
            记录目标岗位；是否投递及后续状态由投递记录管理。
          </p>
        </div>
        <div className="heading-buttons">
          <button className="secondary-button" onClick={onNew}>
            <Plus size={16} />
            完整新增
          </button>
          <button className="primary-button" onClick={onQuickCapture}>
            <Zap size={16} />
            快速收录
          </button>
        </div>
      </section>
      <section className="panel opportunity-panel">
        <div className="table-toolbar list-toolbar">
          <div className="toolbar-control-group">
            <div className="toolbar-popover-anchor">
              <button
                type="button"
                className={`toolbar-action-button ${filter !== "all" ? "active" : ""}`}
                onClick={() =>
                  setOpenMenu(openMenu === "filter" ? null : "filter")
                }
              >
                <ListFilter size={14} />
                筛选{filter !== "all" && <b>1</b>}
              </button>
              {openMenu === "filter" && (
                <PositionFilterPopover
                  status={filter}
                  onClose={() => setOpenMenu(null)}
                  onApply={onFilter}
                />
              )}
            </div>
          </div>
          <span className="result-count">共 {page.total} 个岗位</span>
        </div>
        {filter !== "all" && (
          <div className="active-filter-bar">
            <span>筛选条件</span>
            <ActiveFilterChip
              label={positionLabels[filter as PositionStatus]}
              tone={filter}
              title="移除投递状态条件"
              onRemove={() => onFilter("all")}
            />
            <button
              type="button"
              className="clear-filter-button"
              onClick={() => onFilter("all")}
            >
              清除全部
            </button>
          </div>
        )}
        {page.items.length ? (
          <div className="table-scroll">
            <div className="position-column-labels">
              <span>公司</span>
              <span>招聘批次</span>
              <span>岗位</span>
              <SortableTableHeader
                label="优先级"
                field="priority"
                sortBy={sortBy}
                sortOrder={sortOrder}
                onSort={onSort}
              />
              <SortableTableHeader
                label="投递时间"
                help={<PositionSubmissionHelp />}
                field="submitted_on"
                sortBy={sortBy}
                sortOrder={sortOrder}
                onSort={onSort}
              />
              <span className="table-heading-with-help">
                投递状态 <PositionStatusHelp />
              </span>
              <span className="table-heading-with-help">
                当前流程 <CurrentStageHelp />
              </span>
              <span />
            </div>
            <div className="opportunity-list">
              {page.items.map((item) => (
                <div className={`opportunity-row ${item.status}`} key={item.id}>
                  <button
                    type="button"
                    className="opportunity-open"
                    onClick={() => onSelect(item.id)}
                  >
                    <strong className="position-company">
                      {item.companyName}
                    </strong>
                    <span className="position-campaign">
                      {item.campaignName}
                    </span>
                    <span className="opportunity-title">
                      <strong>{item.title}</strong>
                      <small>
                        {[item.jobCode, item.department, item.location]
                          .filter(Boolean)
                          .join(" · ") || "岗位信息待补充"}
                      </small>
                    </span>
                    <PriorityBadge value={item.priority} />
                    <span className="opportunity-submission-date">
                      {item.applicationId
                        ? item.submittedOn
                          ? textDate(item.submittedOn)
                          : "日期未记录"
                        : "未投递"}
                    </span>
                    <span className="opportunity-status">
                      {item.applicationId ? (
                        <Badge
                          tone={item.applicationStatus}
                          text={
                            applicationLabels[
                              item.applicationStatus as ApplicationStatus
                            ]
                          }
                        />
                      ) : (
                        <span className="opportunity-status-empty">—</span>
                      )}
                    </span>
                    <span className="opportunity-stage">
                      <strong>
                        {item.currentStageType
                          ? `${typeLabel(item.currentStageType)}${item.currentStageName ? ` · ${item.currentStageName}` : ""}`
                          : item.applicationId
                            ? "等待添加流程"
                            : "尚未投递"}
                      </strong>
                      <small>
                        {item.currentStageType
                          ? stageStatusLabels[
                              item.currentStageStatus as StageStatus
                            ]
                          : item.applicationId
                            ? "可随时新增节点"
                            : "创建投递后启用"}
                      </small>
                    </span>
                  </button>
                  <button
                    className="icon-button small danger-button"
                    title={`删除岗位 ${item.title}`}
                    onClick={() => onDelete(item)}
                  >
                    <Trash2 size={14} />
                  </button>
                </div>
              ))}
            </div>
          </div>
        ) : (
          <Empty text="还没有符合条件的岗位。" />
        )}
        <div className="pagination">
          <button
            className="icon-button small"
            disabled={page.page <= 1}
            onClick={() => onPage(page.page - 1)}
            title="上一页"
          >
            <ArrowLeft size={15} />
          </button>
          <span>
            第 {page.page} / {totalPages} 页
          </span>
          <button
            className="icon-button small"
            disabled={page.page >= totalPages}
            onClick={() => onPage(page.page + 1)}
            title="下一页"
          >
            <ArrowRight size={15} />
          </button>
        </div>
      </section>
    </div>
  );
}

function ApplicationsView({
  page,
  filter,
  stageType,
  stageStatus,
  resumeID,
  resumes,
  stageTypes,
  sortBy,
  sortOrder,
  onApplyFilter,
  onSort,
  onPage,
  onSelect,
}: {
  page: ApplicationPage;
  filter: string;
  stageType: string;
  stageStatus: string;
  resumeID: string;
  resumes: Resume[];
  stageTypes: StageTypeDefinition[];
  sortBy: SortField;
  sortOrder: SortOrder;
  onApplyFilter: (
    status: string,
    stageType: string,
    stageStatus: string,
    resumeID: string,
  ) => void;
  onSort: (field: SortField, order: SortOrder) => void;
  onPage: (value: number) => void;
  onSelect: (id: string) => void;
}) {
  const totalPages = Math.max(1, Math.ceil(page.total / page.pageSize));
  const columnStyle = applicationColumnStyle(page.items);
  const [openMenu, setOpenMenu] = useState<"filter" | null>(null);
  const activeFilterCount =
    Number(filter !== "all") +
    Number(Boolean(stageType)) +
    Number(Boolean(stageStatus)) +
    Number(Boolean(resumeID));
  const selectedResume = resumes.find((item) => item.id === resumeID);
  const resumeFilterLabel =
    resumeID === "__none__"
      ? "未关联简历"
      : selectedResume
        ? selectedResume.name
        : "简历版本";
  const clearAll = () => onApplyFilter("all", "", "", "");
  return (
    <div className="page-content applications-page">
      <section className="page-heading compact">
        <div>
          <h1>投递记录</h1>
        </div>
      </section>
      <section className="panel application-list-panel" style={columnStyle}>
        <div className="table-toolbar list-toolbar">
          <div className="toolbar-control-group">
            <div className="toolbar-popover-anchor">
              <button
                type="button"
                className={`toolbar-action-button ${activeFilterCount ? "active" : ""}`}
                onClick={() =>
                  setOpenMenu(openMenu === "filter" ? null : "filter")
                }
              >
                <ListFilter size={14} />
                筛选{activeFilterCount > 0 && <b>{activeFilterCount}</b>}
              </button>
              {openMenu === "filter" && (
                <ApplicationFilterPopover
                  status={filter}
                  stageType={stageType}
                  stageStatus={stageStatus}
                  resumeID={resumeID}
                  resumes={resumes}
                  stageTypes={stageTypes}
                  onClose={() => setOpenMenu(null)}
                  onApply={onApplyFilter}
                />
              )}
            </div>
          </div>
          <span className="result-count">{page.total} 条投递</span>
        </div>
        {activeFilterCount > 0 && (
          <div className="active-filter-bar">
            <span>筛选条件</span>
            {filter !== "all" && (
              <ActiveFilterChip
                label={applicationLabels[filter as ApplicationStatus]}
                tone={filter}
                title="移除投递状态条件"
                onRemove={() => onApplyFilter("all", stageType, stageStatus, resumeID)}
              />
            )}
            {stageType && (
              <ActiveFilterChip
                label={stageTypeLabel(stageType, stageTypes)}
                tone={stageType}
                title="移除节点类型条件"
                onRemove={() => onApplyFilter(filter, "", stageStatus, resumeID)}
              />
            )}
            {stageStatus && (
              <ActiveFilterChip
                label={stageStatusLabels[stageStatus as StageStatus]}
                tone={stageStatus}
                title="移除节点结果条件"
                onRemove={() => onApplyFilter(filter, stageType, "", resumeID)}
              />
            )}
            {resumeID && (
              <ActiveFilterChip
                label={resumeFilterLabel}
                tone="resume"
                title="移除简历版本条件"
                onRemove={() => onApplyFilter(filter, stageType, stageStatus, "")}
              />
            )}
            <button
              type="button"
              className="clear-filter-button"
              onClick={clearAll}
            >
              清除全部
            </button>
          </div>
        )}
        {page.items.length ? (
          <div className="table-scroll">
            <div className="application-column-labels">
              <span>公司</span>
              <span>招聘批次</span>
              <span>岗位</span>
              <SortableTableHeader
                label="优先级"
                field="priority"
                sortBy={sortBy}
                sortOrder={sortOrder}
                onSort={onSort}
              />
              <SortableTableHeader
                label="投递时间"
                field="submitted_on"
                sortBy={sortBy}
                sortOrder={sortOrder}
                onSort={onSort}
              />
              <span className="table-heading-with-help">
                投递状态 <ApplicationStatusHelp />
              </span>
              <span className="table-heading-with-help">
                流程进度 <CurrentStageHelp />
              </span>
              <span />
            </div>
            <div className="application-list">
              {page.items.map((item) => (
                <article className="application-row" key={item.id}>
                  <strong className="application-company">
                    {item.companyName}
                  </strong>
                  <span className="application-campaign">
                    {item.campaignName}
                  </span>
                  <b className="application-position">{item.positionTitle}</b>
                  <PriorityBadge value={item.positionPriority} />
                  <time className="application-submitted">
                    {item.submittedOn
                      ? textDate(item.submittedOn)
                      : "日期未记录"}
                  </time>
                  <div className="application-row-state">
                    <Badge
                      tone={item.status}
                      text={applicationLabels[item.status]}
                    />
                  </div>
                  <StageStrip stages={item.stages} />
                  <button
                    className="secondary-button compact-button"
                    onClick={() => onSelect(item.id)}
                  >
                    <span>详情</span>
                    <ArrowRight size={14} />
                  </button>
                </article>
              ))}
            </div>
          </div>
        ) : (
          <Empty text="还没有符合条件的投递记录。" />
        )}
        <div className="pagination">
          <button
            className="icon-button small"
            disabled={page.page <= 1}
            onClick={() => onPage(page.page - 1)}
            title="上一页"
          >
            <ArrowLeft size={15} />
          </button>
          <span>
            第 {page.page} / {totalPages} 页
          </span>
          <button
            className="icon-button small"
            disabled={page.page >= totalPages}
            onClick={() => onPage(page.page + 1)}
            title="下一页"
          >
            <ArrowRight size={15} />
          </button>
        </div>
      </section>
    </div>
  );
}

function SearchPager({
  page,
  pageSize,
  total,
  onPage,
}: {
  page: number;
  pageSize: number;
  total: number;
  onPage: (page: number) => void;
}) {
  const totalPages = Math.max(1, Math.ceil(total / pageSize));
  if (totalPages <= 1) return null;
  return (
    <div className="search-pager">
      <button
        className="icon-button small"
        disabled={page <= 1}
        title="上一页"
        onClick={() => onPage(page - 1)}
      >
        <ArrowLeft size={15} />
      </button>
      <span>
        第 {page} / {totalPages} 页
      </span>
      <button
        className="icon-button small"
        disabled={page >= totalPages}
        title="下一页"
        onClick={() => onPage(page + 1)}
      >
        <ArrowRight size={15} />
      </button>
    </div>
  );
}

function SearchView({
  query,
  positions,
  applications,
  onClear,
  onOpenPosition,
  onOpenApplication,
  onPositionPage,
  onApplicationPage,
}: {
  query: string;
  positions: PositionPage;
  applications: ApplicationPage;
  onClear: () => void;
  onOpenPosition: (id: string) => void;
  onOpenApplication: (id: string) => void;
  onPositionPage: (page: number) => void;
  onApplicationPage: (page: number) => void;
}) {
  const typeLabel = useStageTypeLabel();
  if (!query)
    return (
      <div className="page-content search-page">
        <section className="page-heading compact">
          <div>
            <h1>搜索</h1>
            <p className="muted">搜索公司、招聘批次、岗位名称或岗位编号。</p>
          </div>
        </section>
        <section className="panel">
          <Empty text="输入关键词后按 Enter，即可同时检索岗位与投递记录。" />
        </section>
      </div>
    );
  const total = positions.total + applications.total;
  return (
    <div className="page-content search-page">
      <section className="page-heading compact">
        <div>
          <h1>搜索结果</h1>
          <p className="muted">
            “{query}”匹配公司、招聘批次、岗位名称与岗位编号。
          </p>
        </div>
        <button className="secondary-button" onClick={onClear}>
          清除搜索
        </button>
      </section>
      <div className="search-summary">
        <strong>{total}</strong>
        <span>个匹配结果</span>
        <small>
          岗位 {positions.total} 个 · 投递 {applications.total} 条
        </small>
      </div>
      <section className="panel search-result-panel">
        <div className="search-result-heading">
          <h2>岗位</h2>
          <span>{positions.total} 个</span>
        </div>
        {positions.items.length ? (
          <>
            <div className="search-column-labels">
              <span>公司</span>
              <span>招聘批次</span>
              <span>岗位</span>
              <span>投递状态</span>
              <span />
            </div>
            <div className="search-result-list">
              {positions.items.map((item) => (
                <button
                  className="search-result-row"
                  key={item.id}
                  onClick={() => onOpenPosition(item.id)}
                >
                  <strong>{item.companyName}</strong>
                  <span>{item.campaignName}</span>
                  <b title={item.title}>{item.title}</b>
                  <div>
                    <Badge
                      tone={item.status}
                      text={positionLabels[item.status]}
                    />
                    {item.applicationStatus && (
                      <Badge
                        tone={item.applicationStatus as ApplicationStatus}
                        text={
                          applicationLabels[
                            item.applicationStatus as ApplicationStatus
                          ]
                        }
                      />
                    )}
                  </div>
                  <ArrowRight size={15} />
                </button>
              ))}
            </div>
            <SearchPager
              page={positions.page}
              pageSize={positions.pageSize}
              total={positions.total}
              onPage={onPositionPage}
            />
          </>
        ) : (
          <Empty text="没有匹配的岗位。" />
        )}
      </section>
      <section className="panel search-result-panel">
        <div className="search-result-heading">
          <h2>投递记录</h2>
          <span>{applications.total} 条</span>
        </div>
        {applications.items.length ? (
          <>
            <div className="search-column-labels applications">
              <span>公司</span>
              <span>招聘批次</span>
              <span>岗位</span>
              <span>当前流程</span>
              <span>投递状态</span>
              <span />
            </div>
            <div className="search-result-list">
              {applications.items.map((item) => (
                <button
                  className="search-result-row applications"
                  key={item.id}
                  onClick={() => onOpenApplication(item.id)}
                >
                  <strong>{item.companyName}</strong>
                  <span>{item.campaignName}</span>
                  <b title={item.positionTitle}>{item.positionTitle}</b>
                  <span className="search-current-stage">
                    {item.currentStageType
                      ? `${typeLabel(item.currentStageType)}${item.currentStageName ? ` · ${item.currentStageName}` : ""}`
                      : "尚未添加流程"}
                    {item.currentStageStatus && (
                      <small>
                        {stageStatusLabels[item.currentStageStatus]}
                      </small>
                    )}
                  </span>
                  <Badge
                    tone={item.status}
                    text={applicationLabels[item.status]}
                  />
                  <ArrowRight size={15} />
                </button>
              ))}
            </div>
            <SearchPager
              page={applications.page}
              pageSize={applications.pageSize}
              total={applications.total}
              onPage={onApplicationPage}
            />
          </>
        ) : (
          <Empty text="没有匹配的投递记录。" />
        )}
      </section>
    </div>
  );
}

type CalendarMode = "month" | "week";
type CalendarOccurrence = {
  item: ScheduleItem;
  day: Date;
  segmentStart: Date;
  segmentEnd: Date;
  startsOnDay: boolean;
  endsOnDay: boolean;
};

function localDayKey(value: Date | string) {
  const date = value instanceof Date ? value : new Date(value);
  return `${date.getFullYear()}-${String(date.getMonth() + 1).padStart(2, "0")}-${String(date.getDate()).padStart(2, "0")}`;
}
function startOfDay(value: Date) {
  return new Date(value.getFullYear(), value.getMonth(), value.getDate());
}
function addDays(value: Date, amount: number) {
  const date = new Date(value);
  date.setDate(date.getDate() + amount);
  return date;
}
function mondayOf(value: Date) {
  const date = startOfDay(value);
  date.setDate(date.getDate() - ((date.getDay() + 6) % 7));
  return date;
}
function monthTitle(value: Date) {
  return new Intl.DateTimeFormat("zh-CN", {
    year: "numeric",
    month: "long",
  }).format(value);
}
function weekTitle(value: Date) {
  const end = addDays(value, 6);
  return `${textDate(value.toISOString())} - ${textDate(end.toISOString())}`;
}
function timeText(value: Date) {
  return new Intl.DateTimeFormat("zh-CN", {
    hour: "2-digit",
    minute: "2-digit",
    hour12: false,
  }).format(value);
}
function scheduleEnd(item: ScheduleItem) {
  return item.endsAt
    ? new Date(item.endsAt)
    : new Date(new Date(item.startsAt).getTime() + 45 * 60 * 1000);
}
function projectOccurrences(items: ScheduleItem[], days: Date[]) {
  const occurrences = new Map<string, CalendarOccurrence[]>();
  days.forEach((day) => occurrences.set(localDayKey(day), []));
  items.forEach((item) => {
    const startsAt = new Date(item.startsAt);
    const key = localDayKey(startsAt);
    const day = days.find((candidate) => localDayKey(candidate) === key);
    if (!day) return;
    const endsAt = scheduleEnd(item);
    occurrences
      .get(key)
      ?.push({
        item,
        day,
        segmentStart: startsAt,
        segmentEnd: endsAt,
        startsOnDay: true,
        endsOnDay: localDayKey(endsAt) === key,
      });
  });
  occurrences.forEach((value) =>
    value.sort(
      (left, right) =>
        left.segmentStart.getTime() - right.segmentStart.getTime(),
    ),
  );
  return occurrences;
}
function scheduleKindLabel(
  item: ScheduleItem,
  typeLabel: (type: StageType) => string,
) {
  return item.kind === "result" ? "结果通知" : typeLabel(item.type);
}
function occurrenceTimeText(occurrence: CalendarOccurrence) {
  const { item } = occurrence;
  if (!item.endsAt) return textDateTime(item.startsAt);
  return scheduleTimeText(item);
}
function ScheduleChip({
  occurrence,
  onOpenApplication,
}: {
  occurrence: CalendarOccurrence;
  onOpenApplication: (id: string) => void;
}) {
  const typeLabel = useStageTypeLabel();
  const { item } = occurrence;
  return (
    <button
      type="button"
      className={`schedule-chip ${item.status} ${item.isCompleted ? "completed" : item.isOverdue ? "overdue" : "todo"} ${item.kind}`}
      title={`${item.companyName} · ${item.campaignName} · ${item.positionTitle} · ${item.name} · ${scheduleTimeText(item)}`}
      onClick={() => onOpenApplication(item.applicationId)}
    >
      <span className="schedule-identity">
        <b>{item.companyName}</b>
        <em>{item.positionTitle}</em>
      </span>
      <span className="schedule-stage-line">
        <span className={`schedule-kind ${item.type}`}>
          {scheduleKindLabel(item, typeLabel)}
        </span>
        <strong className="schedule-node">{item.name}</strong>
      </span>
      <small className="schedule-chip-meta">
        <time>{occurrenceTimeText(occurrence)}</time>
      </small>
    </button>
  );
}
const weekHourHeight = 52;
const weekDayHeight = 24 * weekHourHeight;
type WeekOccurrenceLayout = CalendarOccurrence & {
  lane: number;
  laneCount: number;
};

function minuteOfDay(value: Date) {
  return value.getHours() * 60 + value.getMinutes();
}
function weekSegmentMinutes(occurrence: CalendarOccurrence) {
  const startsAt = minuteOfDay(occurrence.segmentStart);
  const endsAt =
    localDayKey(occurrence.segmentStart) === localDayKey(occurrence.segmentEnd)
      ? minuteOfDay(occurrence.segmentEnd)
      : startsAt + 45;
  return {
    startsAt,
    endsAt: Math.min(24 * 60, Math.max(endsAt, startsAt + 1)),
  };
}
function layoutWeekOccurrences(occurrences: CalendarOccurrence[]) {
  const sorted = [...occurrences].sort((left, right) => {
    const leftTime = weekSegmentMinutes(left);
    const rightTime = weekSegmentMinutes(right);
    return (
      leftTime.startsAt - rightTime.startsAt ||
      leftTime.endsAt - rightTime.endsAt
    );
  });
  const layouts: WeekOccurrenceLayout[] = [];
  let group: CalendarOccurrence[] = [];
  let groupEnd = -1;
  const flushGroup = () => {
    if (!group.length) return;
    const active: { endsAt: number; lane: number }[] = [];
    const placed: { occurrence: CalendarOccurrence; lane: number }[] = [];
    let laneCount = 1;
    group.forEach((occurrence) => {
      const { startsAt, endsAt } = weekSegmentMinutes(occurrence);
      for (let index = active.length - 1; index >= 0; index -= 1)
        if (active[index].endsAt <= startsAt) active.splice(index, 1);
      const occupied = new Set(active.map((entry) => entry.lane));
      let lane = 0;
      while (occupied.has(lane)) lane += 1;
      active.push({ endsAt, lane });
      placed.push({ occurrence, lane });
      laneCount = Math.max(laneCount, lane + 1);
    });
    layouts.push(
      ...placed.map(({ occurrence, lane }) => ({
        ...occurrence,
        lane,
        laneCount,
      })),
    );
    group = [];
    groupEnd = -1;
  };
  sorted.forEach((occurrence) => {
    const { startsAt, endsAt } = weekSegmentMinutes(occurrence);
    if (group.length && startsAt >= groupEnd) flushGroup();
    group.push(occurrence);
    groupEnd = Math.max(groupEnd, endsAt);
  });
  flushGroup();
  return layouts;
}
function weekSegmentTimeText(occurrence: CalendarOccurrence) {
  const { startsAt, endsAt } = weekSegmentMinutes(occurrence);
  const formatMinutes = (value: number) =>
    value === 24 * 60
      ? "24:00"
      : `${String(Math.floor(value / 60)).padStart(2, "0")}:${String(value % 60).padStart(2, "0")}`;
  return `${formatMinutes(startsAt)} - ${formatMinutes(endsAt)}`;
}
function WeekScheduleBlock({
  occurrence,
  onOpenApplication,
}: {
  occurrence: WeekOccurrenceLayout;
  onOpenApplication: (id: string) => void;
}) {
  const typeLabel = useStageTypeLabel();
  const { item } = occurrence;
  const { startsAt, endsAt } = weekSegmentMinutes(occurrence);
  const top = (startsAt / 60) * weekHourHeight;
  const height = Math.min(
    Math.max(((endsAt - startsAt) / 60) * weekHourHeight, 50),
    weekDayHeight - top,
  );
  const compact = height < 44;
  return (
    <button
      type="button"
      className={`week-schedule-block ${compact ? "short" : ""} ${item.status} ${item.isCompleted ? "completed" : item.isOverdue ? "overdue" : "todo"} ${item.kind}`}
      style={{
        top: `${top}px`,
        height: `${height}px`,
        left: `calc(5px + (${occurrence.lane} * ((100% - 10px) / ${occurrence.laneCount})))`,
        width: `calc((100% - 10px) / ${occurrence.laneCount} - 4px)`,
        right: "auto",
      }}
      title={`${item.companyName} · ${item.campaignName} · ${item.positionTitle} · ${scheduleTimeText(item)}`}
      onClick={() => onOpenApplication(item.applicationId)}
    >
      {!compact && (
        <span className="week-schedule-identity">
          {item.companyName} · {item.positionTitle}
        </span>
      )}
      <strong>{item.name}</strong>
      {!compact && (
        <small>
          <span className={`schedule-kind ${item.type}`}>
            {scheduleKindLabel(item, typeLabel)}
          </span>
          {weekSegmentTimeText(occurrence)}
        </small>
      )}
    </button>
  );
}
function WeekCalendar({
  days,
  occurrences,
  onOpenApplication,
}: {
  days: Date[];
  occurrences: Map<string, CalendarOccurrence[]>;
  onOpenApplication: (id: string) => void;
}) {
  const hours = Array.from({ length: 24 }, (_, hour) => hour);
  const today = localDayKey(new Date());
  const daySections = days.map((day) => ({
    day,
    timed: layoutWeekOccurrences(occurrences.get(localDayKey(day)) || []),
  }));
  return (
    <div className="week-calendar">
      <div className="week-calendar-head">
        <span />
        {days.map((day) => (
          <div
            className={localDayKey(day) === today ? "today" : ""}
            key={localDayKey(day)}
          >
            <small>
              周
              {
                ["一", "二", "三", "四", "五", "六", "日"][
                  (day.getDay() + 6) % 7
                ]
              }
            </small>
            <strong>{textDate(day.toISOString())}</strong>
          </div>
        ))}
      </div>
      <div className="week-calendar-body">
        <div className="week-time-axis">
          {hours.map((hour) => (
            <span key={hour}>{String(hour).padStart(2, "0")}:00</span>
          ))}
        </div>
        {daySections.map(({ day, timed }) => (
          <div
            className={`week-day-column ${localDayKey(day) === today ? "today" : ""}`}
            key={localDayKey(day)}
          >
            {hours.map((hour) => (
              <i
                className="week-hour-line"
                key={hour}
                style={{ top: `${hour * weekHourHeight}px` }}
              />
            ))}
            {timed.map((occurrence) => (
              <WeekScheduleBlock
                key={`${occurrence.item.id}:${localDayKey(day)}`}
                occurrence={occurrence}
                onOpenApplication={onOpenApplication}
              />
            ))}
          </div>
        ))}
      </div>
    </div>
  );
}
function CalendarView({
  items,
  onOpenApplication,
}: {
  items: ScheduleItem[];
  onOpenApplication: (id: string) => void;
}) {
  const [mode, setMode] = useState<CalendarMode>("month");
  const [anchorDate, setAnchorDate] = useState(() => new Date());
  const [expandedDay, setExpandedDay] = useState<string | null>(null);
  const first = new Date(anchorDate.getFullYear(), anchorDate.getMonth(), 1);
  const monthStart = addDays(first, -((first.getDay() + 6) % 7));
  const weekStart = mondayOf(anchorDate);
  const days =
    mode === "month"
      ? Array.from({ length: 42 }, (_, index) => addDays(monthStart, index))
      : Array.from({ length: 7 }, (_, index) => addDays(weekStart, index));
  const occurrences = projectOccurrences(items, days);
  const calendarTitle =
    mode === "month" ? monthTitle(anchorDate) : weekTitle(weekStart);
  const move = (direction: number) => {
    setExpandedDay(null);
    setAnchorDate((current) =>
      mode === "month"
        ? new Date(current.getFullYear(), current.getMonth() + direction, 1)
        : addDays(current, direction * 7),
    );
  };

  return (
    <div className="page-content calendar-page">
      <section className="page-heading compact">
        <div>
          <h1>日历</h1>
          <p className="muted">
            笔试、面试和结果通知均来自投递流程节点，点击可直达详情。
          </p>
        </div>
        <div className="calendar-legend">
          <span className="legend-dot todo" />
          待完成
          <span className="legend-dot completed" />
          已完成
        </div>
      </section>
      <section className="panel calendar-panel">
        <div className="calendar-toolbar">
          <button
            className="icon-button small"
            title={mode === "month" ? "上个月" : "上一周"}
            onClick={() => move(-1)}
          >
            <ChevronLeft size={15} />
          </button>
          <strong>{calendarTitle}</strong>
          <button
            className="icon-button small"
            title={mode === "month" ? "下个月" : "下一周"}
            onClick={() => move(1)}
          >
            <ChevronRight size={15} />
          </button>
          <div
            className="calendar-view-switch"
            role="tablist"
            aria-label="日历视图"
          >
            <button
              className={mode === "month" ? "selected" : ""}
              role="tab"
              aria-selected={mode === "month"}
              onClick={() => {
                setExpandedDay(null);
                setMode("month");
              }}
            >
              <CalendarDays size={14} />月
            </button>
            <button
              className={mode === "week" ? "selected" : ""}
              role="tab"
              aria-selected={mode === "week"}
              onClick={() => {
                setExpandedDay(null);
                setMode("week");
              }}
            >
              <CalendarRange size={14} />周
            </button>
          </div>
          <button
            className="text-button calendar-today"
            onClick={() => {
              setExpandedDay(null);
              setAnchorDate(new Date());
            }}
          >
            回到今天
          </button>
        </div>
        <div className="calendar-scroll">
          {mode === "week" ? (
            <WeekCalendar
              days={days}
              occurrences={occurrences}
              onOpenApplication={onOpenApplication}
            />
          ) : (
            <>
              <div className="calendar-weekdays">
                {["一", "二", "三", "四", "五", "六", "日"].map((day) => (
                  <span key={day}>周{day}</span>
                ))}
              </div>
              <div className="calendar-grid">
                {days.map((day) => {
                  const key = localDayKey(day);
                  const dayOccurrences = occurrences.get(key) || [];
                  const isToday = key === localDayKey(new Date());
                  const isExpanded = expandedDay === key;
                  const visibleItems = isExpanded
                    ? dayOccurrences
                    : dayOccurrences.slice(0, 2);
                  const hiddenCount =
                    dayOccurrences.length - visibleItems.length;
                  return (
                    <div
                      className={`calendar-day ${day.getMonth() !== anchorDate.getMonth() ? "outside" : ""} ${isToday ? "today" : ""} ${isExpanded ? "expanded" : ""}`}
                      key={key}
                    >
                      <div className="calendar-day-header">
                        <strong>{day.getDate()}</strong>
                        {isToday && <span>今</span>}
                      </div>
                      <div className="calendar-day-items">
                        {visibleItems.map((occurrence) => (
                          <ScheduleChip
                            key={`${occurrence.item.id}:${key}`}
                            occurrence={occurrence}
                            onOpenApplication={onOpenApplication}
                          />
                        ))}
                        {hiddenCount > 0 && (
                          <button
                            type="button"
                            className="calendar-more"
                            onClick={() => setExpandedDay(key)}
                          >
                            还有 {hiddenCount} 项
                          </button>
                        )}
                        {isExpanded && dayOccurrences.length > 2 && (
                          <button
                            type="button"
                            className="calendar-more collapse"
                            onClick={() => setExpandedDay(null)}
                          >
                            收起
                          </button>
                        )}
                      </div>
                    </div>
                  );
                })}
              </div>
            </>
          )}
        </div>
      </section>
    </div>
  );
}
type TodoEntry = { item: ScheduleItem; source: "time" | "waiting" };
function todoAction(item: ScheduleItem, source: TodoEntry["source"]) {
  if (source === "waiting" || item.isWaitingResult)
    return { tone: "waiting-result", text: "等待结果" };
  if (item.isOverdue) return { tone: "todo-overdue", text: "已逾期" };
  if (item.kind === "result")
    return { tone: "todo-upcoming", text: "待确认结果" };
  return { tone: "todo-upcoming", text: "待参加" };
}
function TodoRow({
  entry,
  onOpenApplication,
}: {
  entry: TodoEntry;
  onOpenApplication: (id: string) => void;
}) {
  const typeLabel = useStageTypeLabel();
  const { item, source } = entry;
  const action = todoAction(item, source);
  const timeText =
    source === "waiting" ? "未设置通知时间" : scheduleTimeText(item);
  return (
    <button
      type="button"
      className={`todo-row ${item.status} ${item.isOverdue ? "overdue" : ""} ${action.tone} ${source === "waiting" ? "waiting-without-time" : ""}`}
      onClick={() => onOpenApplication(item.applicationId)}
    >
      <span
        className={`todo-state ${item.isOverdue ? "overdue" : action.tone === "waiting-result" ? "waiting" : "todo"}`}
      />
      <strong className="todo-company">{item.companyName}</strong>
      <span className="todo-campaign">{item.campaignName}</span>
      <b className="todo-position">{item.positionTitle}</b>
      <span className="todo-stage-name">{item.name}</span>
      <span className={`stage-type ${item.type}`}>
        <StageGlyph type={item.type} />
        {scheduleKindLabel(item, typeLabel)}
      </span>
      <Badge tone={action.tone} text={action.text} />
      <div className="todo-time" title={timeText}>
        <span>{timeText}</span>
      </div>
      <ArrowRight size={15} />
    </button>
  );
}
type TodoScope = "all" | "today" | "week" | "overdue" | "waiting";
function executableTodos(items: ScheduleItem[]): TodoEntry[] {
  const timed = items
    .filter((item) => !item.isCompleted)
    .map((item) => ({ item, source: "time" as const }));
  const timedResultStages = new Set(
    timed
      .filter(({ item }) => item.kind === "result" && item.isWaitingResult)
      .map(({ item }) => item.stageId),
  );
  const waitingWithoutTime = items
    .filter(
      (item) =>
        item.kind === "stage" &&
        item.isWaitingResult &&
        !timedResultStages.has(item.stageId),
    )
    .map((item) => ({ item, source: "waiting" as const }));
  return [...timed, ...waitingWithoutTime].sort((left, right) => {
    if (left.source !== right.source) return left.source === "time" ? -1 : 1;
    return (
      new Date(left.item.startsAt).getTime() -
      new Date(right.item.startsAt).getTime()
    );
  });
}
function TodosView({
  items,
  onOpenApplication,
}: {
  items: ScheduleItem[];
  onOpenApplication: (id: string) => void;
}) {
  const [scope, setScope] = useState<TodoScope>("all");
  const todoItems = executableTodos(items);
  const now = new Date();
  const today = localDayKey(now);
  const tomorrow = startOfDay(addDays(now, 1));
  const weekEnd = addDays(tomorrow, 7);
  const scopes: { key: TodoScope; label: string; count: number }[] = [
    { key: "all", label: "全部", count: todoItems.length },
    {
      key: "today",
      label: "今天",
      count: todoItems.filter(
        ({ item, source }) =>
          source === "time" && localDayKey(item.startsAt) === today,
      ).length,
    },
    {
      key: "week",
      label: "未来 7 天",
      count: todoItems.filter(({ item, source }) => {
        const date = new Date(item.startsAt);
        return source === "time" && date >= tomorrow && date < weekEnd;
      }).length,
    },
    {
      key: "overdue",
      label: "逾期",
      count: todoItems.filter(
        ({ item, source }) => source === "time" && item.isOverdue,
      ).length,
    },
    {
      key: "waiting",
      label: "等待结果",
      count: todoItems.filter(({ item }) => item.isWaitingResult).length,
    },
  ];
  const visible = todoItems.filter((item) => {
    if (scope === "today")
      return (
        item.source === "time" && localDayKey(item.item.startsAt) === today
      );
    if (scope === "week") {
      const date = new Date(item.item.startsAt);
      return item.source === "time" && date >= tomorrow && date < weekEnd;
    }
    if (scope === "overdue")
      return item.source === "time" && item.item.isOverdue;
    if (scope === "waiting") return item.item.isWaitingResult;
    return true;
  });
  return (
    <div className="page-content todos-page">
      <section className="page-heading compact">
        <div>
          <h1>待办</h1>
          <p className="muted">
            收录未结束的时间事项；已参加但未出结果的节点单独归入等待结果。
          </p>
        </div>
        <div className="todo-summary">
          <strong>{todoItems.length}</strong>
          <span>项待处理</span>
        </div>
      </section>
      <section className="panel todo-panel">
        <div className="todo-panel-header">
          <div>
            <h2>待办事项</h2>
            <p>总览中的“即将到来”仅展示这里的未来时间事项。</p>
          </div>
          <div
            className="filter-tabs todo-scope-tabs"
            role="tablist"
            aria-label="待办筛选"
          >
            {scopes.map((item) => (
              <button
                key={item.key}
                className={scope === item.key ? "selected" : ""}
                role="tab"
                aria-selected={scope === item.key}
                onClick={() => setScope(item.key)}
              >
                {item.label}
                <span>{item.count}</span>
              </button>
            ))}
          </div>
        </div>
        {visible.length ? (
          <div className="todo-list-scroll">
            <div className="todo-column-labels" aria-hidden="true">
              <span />
              <span>公司</span>
              <span>招聘批次</span>
              <span>岗位</span>
              <span>流程节点</span>
              <span>类型</span>
              <span>待办状态</span>
              <span>处理时间</span>
              <span />
            </div>
            <div className="todo-list">
              {visible.map((entry) => (
                <TodoRow
                  key={entry.item.id}
                  entry={entry}
                  onOpenApplication={onOpenApplication}
                />
              ))}
            </div>
          </div>
        ) : (
          <Empty text="此视图下没有待处理事项。" />
        )}
      </section>
    </div>
  );
}

function DirectoryView({
  companies,
  campaigns,
  onNewCompany,
  onNewCampaign,
  onEditCompany,
  onDeleteCompany,
  onEditCampaign,
  onDeleteCampaign,
}: {
  companies: Company[];
  campaigns: Campaign[];
  onNewCompany: () => void;
  onNewCampaign: () => void;
  onEditCompany: (item: Company) => void;
  onDeleteCompany: (item: Company) => void;
  onEditCampaign: (item: Campaign) => void;
  onDeleteCampaign: (item: Campaign) => void;
}) {
  return (
    <div className="page-content">
      <section className="page-heading compact">
        <div>
          <h1>公司与招聘批次</h1>
          <p className="muted">保存官方公告与流程参考，不预设实际面试轮数。</p>
        </div>
        <div className="heading-buttons">
          <button className="secondary-button" onClick={onNewCampaign}>
            <FileText size={17} />
            新增批次
          </button>
          <button className="primary-button" onClick={onNewCompany}>
            <Plus size={17} />
            新增公司
          </button>
        </div>
      </section>
      <section className="directory-list">
        {companies.map((company) => {
          const companyCampaigns = campaigns.filter(
            (campaign) => campaign.companyId === company.id,
          );
          return (
            <section className="panel company-block" key={company.id}>
              <div className="company-block-header">
                <div>
                  <span className="company-monogram">
                    {company.name.slice(0, 1)}
                  </span>
                  <div>
                    <strong>{company.name}</strong>
                    <small>
                      {company.industry || "未分类"} · {companyCampaigns.length}{" "}
                      个招聘批次
                    </small>
                  </div>
                </div>
                <div className="row-actions">
                  <button
                    className="icon-button small"
                    title="编辑公司"
                    onClick={() => onEditCompany(company)}
                  >
                    <Pencil size={14} />
                  </button>
                  <button
                    className="icon-button small danger-button"
                    title="删除公司"
                    onClick={() => onDeleteCompany(company)}
                  >
                    <Trash2 size={14} />
                  </button>
                </div>
              </div>
              <div className="campaign-list">
                {companyCampaigns.map((campaign) => (
                  <div className="campaign-row" key={campaign.id}>
                    <div>
                      <strong>{campaign.name}</strong>
                      <span>
                        {campaign.processOverview || "尚未记录官方流程参考"}
                      </span>
                    </div>
                    <div>
                      <small>
                        {campaign.closesOn
                          ? `截止 ${textDate(campaign.closesOn)}`
                          : "未设置截止日期"}
                      </small>
                      <div className="row-actions">
                        <button
                          className="icon-button small"
                          title="编辑招聘批次"
                          onClick={() => onEditCampaign(campaign)}
                        >
                          <Pencil size={14} />
                        </button>
                        <button
                          className="icon-button small danger-button"
                          title="删除招聘批次"
                          onClick={() => onDeleteCampaign(campaign)}
                        >
                          <Trash2 size={14} />
                        </button>
                      </div>
                    </div>
                  </div>
                ))}
                {!companyCampaigns.length && <Empty text="尚未添加招聘批次" />}
              </div>
            </section>
          );
        })}
        {!companies.length && (
          <section className="panel">
            <Empty text="先创建公司，再建立招聘批次和岗位。" />
          </section>
        )}
      </section>
    </div>
  );
}

function PositionDetailView({
  detail,
  backLabel,
  onNotify,
  onBack,
  onEditPosition,
  onDeletePosition,
  onCreateApplication,
  onOpenApplication,
  onAddAttachments,
  onDeleteAttachment,
  onOpenAttachment,
}: {
  detail: PositionDetail;
  backLabel: string;
  onNotify: (message: string) => void;
  onBack: () => void;
  onEditPosition: () => void;
  onDeletePosition: () => void;
  onCreateApplication: () => void;
  onOpenApplication: (id: string) => void;
  onAddAttachments: () => Promise<void>;
  onDeleteAttachment: (item: PositionAttachment) => Promise<void>;
  onOpenAttachment: (item: PositionAttachment) => Promise<void>;
}) {
  return (
    <div className="page-content position-detail-page">
      <button className="back-button" onClick={onBack}>
        <ArrowLeft size={16} />
        {backLabel}
      </button>
      <section className="detail-heading">
        <div>
          <p className="eyebrow">
            {detail.company.name} · {detail.campaign.name}
          </p>
          <h1>{detail.position.title}</h1>
          <p className="muted">
            {[
              detail.position.jobCode,
              detail.position.department,
              detail.position.location,
              detail.position.track,
            ]
              .filter(Boolean)
              .join(" · ") || "岗位基础信息待补充"}
          </p>
        </div>
        <div className="heading-buttons">
          <Badge tone={detail.status} text={positionLabels[detail.status]} />
          <button className="secondary-button" onClick={onEditPosition}>
            <Pencil size={16} />
            编辑岗位
          </button>
          <button
            className="icon-button danger-button"
            title="删除岗位"
            onClick={onDeletePosition}
          >
            <Trash2 size={16} />
          </button>
        </div>
      </section>
      <div className="detail-grid">
        <section className="panel detail-panel">
          <PanelHeader title="岗位信息" action="" onClick={() => undefined} />
          <div className="detail-fields">
            <Info
              label="岗位编号"
              value={detail.position.jobCode || "未设置"}
            />
            <Info
              label="部门 / 事业群"
              value={detail.position.department || "未设置"}
            />
            <Info label="城市" value={detail.position.location || "未设置"} />
            <Info label="岗位方向" value={detail.position.track || "未设置"} />
            <Info label="优先级" value={`${detail.position.priority} 级`} />
            <Info
              label="岗位招聘链接"
              value={detail.position.sourceUrl || "未设置"}
              link={detail.position.sourceUrl}
              onNotify={onNotify}
            />
          </div>
          <div className="process-overview">
            <span>岗位备注</span>
            <p>{detail.position.notes || "暂无岗位备注。"}</p>
          </div>
        </section>
        <section className="panel detail-panel">
          <PanelHeader
            title="所属招聘批次"
            action=""
            onClick={() => undefined}
          />
          <div className="detail-fields">
            <Info label="公司" value={detail.company.name} />
            <Info label="招聘批次" value={detail.campaign.name} />
            <Info label="开放日期" value={textDate(detail.campaign.opensOn)} />
            <Info label="截止日期" value={textDate(detail.campaign.closesOn)} />
            <Info
              label="批次官网链接"
              value={detail.campaign.sourceUrl || "未设置"}
              link={detail.campaign.sourceUrl}
              onNotify={onNotify}
            />
            <Info
              label="最后核验"
              value={textDate(detail.campaign.lastVerifiedOn)}
            />
          </div>
          <div className="process-overview">
            <span>批次官方流程参考</span>
            <p>
              {detail.campaign.processOverview ||
                "尚未记录该招聘批次的官方流程参考。它只用于了解公告信息，不会生成个人流程节点。"}
            </p>
          </div>
        </section>
        <section className="panel detail-panel application-panel position-application-panel">
          <div className="panel-header position-application-header">
            <h2>我的投递</h2>
            {detail.application ? (
              <div className="position-application-meta">
                <span>
                  <small>投递时间</small>
                  <strong>
                    {detail.application.submittedOn
                      ? textDate(detail.application.submittedOn)
                      : "未记录"}
                  </strong>
                </span>
                <span>
                  <small>投递状态</small>
                  <Badge
                    tone={detail.application.status}
                    text={applicationLabels[detail.application.status]}
                  />
                </span>
              </div>
            ) : (
              <button className="text-button" onClick={onCreateApplication}>
                创建投递
              </button>
            )}
          </div>
          {detail.application ? (
            <div
              className="position-application-flow"
              role="button"
              tabIndex={0}
              title="打开投递详情"
              onClick={() => onOpenApplication(detail.application!.id)}
              onKeyDown={(event) => {
                if (event.key === "Enter" || event.key === " ") {
                  event.preventDefault();
                  onOpenApplication(detail.application!.id);
                }
              }}
            >
              <StageStrip stages={detail.stages} />
            </div>
          ) : (
            <Empty text="创建投递后，才会出现笔试和面试流程节点。" />
          )}
        </section>
      </div>
      <PositionAttachmentsPanel
        positionID={detail.position.id}
        items={detail.attachments || []}
        onAdd={onAddAttachments}
        onDelete={onDeleteAttachment}
        onOpen={onOpenAttachment}
      />
    </div>
  );
}

function PositionAttachmentsPanel({
  positionID,
  items,
  onAdd,
  onDelete,
  onOpen,
}: {
  positionID: string;
  items: PositionAttachment[];
  onAdd: () => Promise<void>;
  onDelete: (item: PositionAttachment) => Promise<void>;
  onOpen: (item: PositionAttachment) => Promise<void>;
}) {
  const [displayedItems, setDisplayedItems] = useState(items);
  const [preview, setPreview] = useState<{ name: string; url: string } | null>(
    null,
  );
  const [previewing, setPreviewing] = useState("");
  const [previewError, setPreviewError] = useState("");
  const [pasting, setPasting] = useState(false);
  const [pasteTargetActive, setPasteTargetActive] = useState(false);
  const [pasteMessage, setPasteMessage] = useState("");
  useEffect(() => {
    setDisplayedItems(items);
  }, [items]);
  const images = displayedItems.filter(attachmentIsImage);
  const files = displayedItems.filter((item) => !attachmentIsImage(item));
  const showPreview = async (item: PositionAttachment) => {
    setPreviewing(item.id);
    setPreviewError("");
    try {
      setPreview({
        name: item.originalName,
        url: await api.positionAttachmentDataURL(item.id),
      });
    } catch (reason) {
      setPreviewError(messageOf(reason));
    } finally {
      setPreviewing("");
    }
  };
  const pasteImage = async (image: File) => {
    if (!attachmentIsClipboardImage(image.type)) {
      setPreviewError("剪贴板中没有可保存的图片");
      return;
    }
    setPasting(true);
    setPreviewError("");
    setPasteMessage("");
    try {
      const attachments = await api.pastePositionImage(
        positionID,
        clipboardImageName(image.type),
        await fileDataURL(image),
      );
      setDisplayedItems(attachments);
      setPasteMessage("截图已添加");
    } catch (reason) {
      setPreviewError(messageOf(reason));
    } finally {
      setPasting(false);
    }
  };
  const receivePaste = (event: ClipboardEvent<HTMLDivElement>) => {
    if (pasting) return;
    const image = Array.from(event.clipboardData.files).find((file) =>
      attachmentIsClipboardImage(file.type),
    );
    if (!image) {
      setPreviewError("剪贴板中没有图片，请复制截图后重试");
      return;
    }
    event.preventDefault();
    void pasteImage(image);
  };
  return (
    <section className="panel position-attachments">
      <div className="panel-header attachment-header">
        <div>
          <h2>岗位附件</h2>
          <span>
            {displayedItems.length
              ? `${displayedItems.length} 个文件`
              : "用于保存 JD 截图、公告、文档或其他参考资料"}
          </span>
        </div>
        <div className="attachment-header-actions">
          <button
            className="secondary-button attachment-add-button"
            onClick={() => void onAdd()}
          >
            <ImagePlus size={16} />
            添加附件
          </button>
        </div>
      </div>
      {previewError && <p className="attachment-error">{previewError}</p>}
      {pasteMessage && <p className="attachment-feedback">{pasteMessage}</p>}
      <div
        className={`attachment-paste-zone ${pasteTargetActive ? "is-active" : ""}`}
        role="button"
        tabIndex={0}
        aria-label="将鼠标移至此处后按 Ctrl+V 粘贴截图"
        onMouseEnter={(event) => event.currentTarget.focus()}
        onMouseLeave={(event) => {
          if (document.activeElement === event.currentTarget)
            event.currentTarget.blur();
        }}
        onMouseDown={(event) => event.currentTarget.focus()}
        onFocus={() => setPasteTargetActive(true)}
        onBlur={() => setPasteTargetActive(false)}
        onPaste={receivePaste}
      >
        <ClipboardPaste size={17} />
        <div>
          <strong>{pasting ? "正在保存截图" : "粘贴截图"}</strong>
          <span>将鼠标移至此区域后按 Ctrl+V；页面其他位置不会接收粘贴</span>
        </div>
        <kbd>Ctrl+V</kbd>
      </div>
      {!displayedItems.length ? (
        <div className="attachment-empty">
          <ImagePlus size={20} />
          <span>暂未添加附件</span>
          <small>图片会在此处预览，其他文件可直接打开。</small>
        </div>
      ) : (
        <div className="attachment-body">
          {images.length > 0 && (
            <div className="attachment-gallery">
              {images.map((item) => (
                <AttachmentImageCard
                  item={item}
                  key={item.id}
                  loading={previewing === item.id}
                  onPreview={showPreview}
                  onOpen={onOpen}
                  onDelete={onDelete}
                />
              ))}
            </div>
          )}
          {files.length > 0 && (
            <div className="attachment-file-list">
              {files.map((item) => (
                <article className="attachment-file" key={item.id}>
                  <span className="attachment-file-icon">
                    <FileText size={18} />
                  </span>
                  <div>
                    <strong title={item.originalName}>
                      {item.originalName}
                    </strong>
                    <small>
                      {item.mimeType || "未知类型"} ·{" "}
                      {attachmentSize(item.sizeBytes)} ·{" "}
                      {textDate(item.createdAt)}
                    </small>
                  </div>
                  <div className="attachment-actions">
                    <button
                      className="icon-button small"
                      title="用默认程序打开"
                      onClick={() => void onOpen(item)}
                    >
                      <ExternalLink size={14} />
                    </button>
                    <button
                      className="icon-button small danger-button"
                      title="删除附件"
                      onClick={() => void onDelete(item)}
                    >
                      <Trash2 size={14} />
                    </button>
                  </div>
                </article>
              ))}
            </div>
          )}
        </div>
      )}
      {preview && (
        <div
          className="attachment-lightbox"
          role="dialog"
          aria-modal="true"
          aria-label={preview.name}
        >
          <div className="attachment-lightbox-content">
            <div>
              <strong>{preview.name}</strong>
              <button
                className="icon-button small"
                title="关闭图片预览"
                onClick={() => setPreview(null)}
              >
                <X size={15} />
              </button>
            </div>
            <img src={preview.url} alt={preview.name} />
          </div>
        </div>
      )}
    </section>
  );
}

function attachmentIsClipboardImage(mimeType: string) {
  return [
    "image/png",
    "image/jpeg",
    "image/gif",
    "image/webp",
    "image/bmp",
  ].includes(mimeType.toLowerCase());
}
function clipboardImageName(mimeType: string) {
  const extension =
    mimeType === "image/jpeg" ? "jpg" : mimeType.split("/")[1] || "png";
  const timestamp = new Date().toISOString().replace(/[:.]/g, "-");
  return `截图-${timestamp}.${extension}`;
}
const maxStagedAttachmentBytes = 25 * 1024 * 1024;
function formatFileSize(size: number) {
  if (size < 1024 * 1024) return `${Math.max(1, Math.round(size / 1024))} KB`;
  return `${(size / (1024 * 1024)).toFixed(size >= 10 * 1024 * 1024 ? 0 : 1)} MB`;
}
function fileDataURL(file: Blob) {
  return new Promise<string>((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () =>
      typeof reader.result === "string"
        ? resolve(reader.result)
        : reject(new Error("无法读取文件"));
    reader.onerror = () => reject(reader.error || new Error("无法读取文件"));
    reader.readAsDataURL(file);
  });
}

function AttachmentImageCard({
  item,
  loading,
  onPreview,
  onOpen,
  onDelete,
}: {
  item: PositionAttachment;
  loading: boolean;
  onPreview: (item: PositionAttachment) => Promise<void>;
  onOpen: (item: PositionAttachment) => Promise<void>;
  onDelete: (item: PositionAttachment) => Promise<void>;
}) {
  const [thumbnail, setThumbnail] = useState("");
  useEffect(() => {
    let active = true;
    void api
      .positionAttachmentDataURL(item.id)
      .then((url) => {
        if (active) setThumbnail(url);
      })
      .catch(() => {
        if (active) setThumbnail("");
      });
    return () => {
      active = false;
    };
  }, [item.id]);
  return (
    <article className="attachment-image">
      <button
        type="button"
        className="attachment-image-preview"
        title={`查看 ${item.originalName}`}
        onClick={() => void onPreview(item)}
      >
        {thumbnail ? (
          <img src={thumbnail} alt="" />
        ) : (
          <>
            <ImagePlus size={22} />
            <span>图片不可预览</span>
          </>
        )}
        <span className="attachment-image-overlay">
          {loading ? "正在加载" : "点击查看"}
        </span>
      </button>
      <div className="attachment-caption">
        <strong title={item.originalName}>{item.originalName}</strong>
        <small>
          {attachmentSize(item.sizeBytes)} · {textDate(item.createdAt)}
        </small>
        <div>
          <button
            className="icon-button small"
            title="用默认程序打开"
            onClick={() => void onOpen(item)}
          >
            <ExternalLink size={14} />
          </button>
          <button
            className="icon-button small danger-button"
            title="删除附件"
            onClick={() => void onDelete(item)}
          >
            <Trash2 size={14} />
          </button>
        </div>
      </div>
    </article>
  );
}

function QuickCaptureAttachmentRow({
  file,
  disabled,
  onRemove,
}: {
  file: File;
  disabled: boolean;
  onRemove: () => void;
}) {
  const isImage = attachmentIsClipboardImage(file.type);
  const [thumbnail, setThumbnail] = useState("");
  useEffect(() => {
    if (!isImage) {
      setThumbnail("");
      return;
    }
    const url = URL.createObjectURL(file);
    setThumbnail(url);
    return () => URL.revokeObjectURL(url);
  }, [file, isImage]);
  return (
    <article>
      <span
        className={`attachment-file-icon quick-attachment-icon ${isImage ? "image" : ""}`}
      >
        {isImage && thumbnail ? (
          <img src={thumbnail} alt="" />
        ) : isImage ? (
          <ImagePlus size={16} />
        ) : (
          <FileText size={16} />
        )}
      </span>
      <div>
        <strong title={file.name}>{file.name}</strong>
        <small>
          {file.type || "未知类型"} · {attachmentSize(file.size)}
        </small>
      </div>
      <button
        className="icon-button small danger-button"
        type="button"
        disabled={disabled}
        title={`移除 ${file.name}`}
        onClick={onRemove}
      >
        <X size={14} />
      </button>
    </article>
  );
}

function ApplicationDetailView({
  detail,
  backLabel,
  onOpenResume,
	onClearResume,
  onBack,
  onOpenPosition,
  onEditApplication,
  onDeleteApplication,
  onCreateStage,
  onEditStage,
  onDeleteStage,
  onMoveStage,
}: {
  detail: ApplicationDetail;
  backLabel: string;
  onOpenResume: (resumeID: string) => Promise<void>;
	onClearResume: () => void;
  onBack: () => void;
  onOpenPosition: (id: string) => void;
  onEditApplication: () => void;
  onDeleteApplication: () => void;
  onCreateStage: () => void;
  onEditStage: (item: ApplicationStage) => void;
  onDeleteStage: (item: ApplicationStage) => void;
  onMoveStage: (item: ApplicationStage, direction: number) => void;
}) {
  return (
    <div className="page-content application-detail-page">
      <button className="back-button" onClick={onBack}>
        <ArrowLeft size={16} />
        {backLabel}
      </button>
      <section className="detail-heading">
        <div>
          <p className="eyebrow">
            {detail.company.name} · {detail.campaign.name}
          </p>
          <h1>{detail.position.title}</h1>
          <p className="muted">
            投递详情 · {detail.application.channel || "未记录渠道"}
          </p>
        </div>
        <div className="heading-buttons">
          <Badge
            tone={detail.application.status}
            text={applicationLabels[detail.application.status]}
          />
          <button className="secondary-button" onClick={onEditApplication}>
            <Pencil size={16} />
            编辑投递
          </button>
          <button
            className="icon-button danger-button"
            title="删除投递记录"
            onClick={onDeleteApplication}
          >
            <Trash2 size={16} />
          </button>
        </div>
      </section>
      <section className="panel application-context">
        <div>
          <span>所属公司</span>
          <strong>{detail.company.name}</strong>
        </div>
        <div>
          <span>招聘批次</span>
          <strong>{detail.campaign.name}</strong>
        </div>
        <div>
          <span>岗位</span>
          <button
            className="link-button"
            onClick={() => onOpenPosition(detail.position.id)}
          >
            {detail.position.title}
            <ArrowRight size={14} />
          </button>
        </div>
        <div>
          <span>投递日期</span>
          <strong>{textDate(detail.application.submittedOn)}</strong>
        </div>
        <div>
          <span>关联简历</span>
          {detail.resume ? (
			<div className="application-resume-actions">
			  <button
				className="application-resume-link"
				title={`打开 ${detail.resume.originalName}`}
				onClick={() => void onOpenResume(detail.resume!.id)}
			  >
				<FileText size={14} />
				<strong>{detail.resume.name || detail.resume.originalName}</strong>
				<ExternalLink size={13} />
			  </button>
			  {detail.resume && (
				<button className="icon-button tiny" title="清除简历关联" onClick={onClearResume}>
				  <X size={12} />
				</button>
			  )}
			</div>
          ) : (
			<strong>未关联简历</strong>
          )}
			{detail.resume && (
            <small className="application-resume-version">
			  {detail.resume.originalName} · {attachmentSize(detail.resume.sizeBytes)}
            </small>
          )}
        </div>
        <div>
          <span className="detail-label-with-help">
            状态来源 <ApplicationStatusHelp />
          </span>
          <strong>最新流程节点自动计算</strong>
        </div>
      </section>
      <section className="panel detail-notes">
        <span>投递备注</span>
        <p>{detail.application.notes || "暂无投递备注"}</p>
      </section>
      <Timeline
        stages={detail.stages}
        onCreateStage={onCreateStage}
        onEditStage={onEditStage}
        onDeleteStage={onDeleteStage}
        onMoveStage={onMoveStage}
      />
    </div>
  );
}

function Timeline({
  stages,
  onCreateStage,
  onEditStage,
  onDeleteStage,
  onMoveStage,
}: {
  stages: ApplicationStage[];
  onCreateStage: () => void;
  onEditStage: (item: ApplicationStage) => void;
  onDeleteStage: (item: ApplicationStage) => void;
  onMoveStage: (item: ApplicationStage, direction: number) => void;
}) {
  const typeLabel = useStageTypeLabel();
  return (
    <section className="panel timeline-panel">
      <div className="timeline-panel-header">
        <div>
          <h2>投递流程</h2>
          <p className="timeline-status-rule">
            投递状态由最后一个流程节点自动计算。
            <ApplicationStatusHelp />
          </p>
        </div>
        <button className="primary-button" onClick={onCreateStage}>
          <CirclePlus size={16} />
          新增流程节点
        </button>
      </div>
      {stages.length ? (
        <div className="stage-timeline">
          {stages.map((stage, index) => (
            <article className={`timeline-item ${stage.status}`} key={stage.id}>
              <div className="timeline-rail">
                <StageGlyph type={stage.type} />
              </div>
              <div className="timeline-content">
                <div className="timeline-title">
                  <strong>
                    {typeLabel(stage.type)}
                    {stage.content ? ` · ${stage.content}` : ""}
                  </strong>
                  <span className={`stage-type ${stage.type}`}>
                    <StageGlyph type={stage.type} />
                    {typeLabel(stage.type)}
                  </span>
                  <Badge
                    tone={stage.status}
                    text={stageStatusLabels[stage.status]}
                  />
                </div>
                <div className="timeline-meta">
                  <span>{stageTimeText(stage)}</span>
                  {stage.resultAt && (
                    <span>结果通知 {textDateTime(stage.resultAt)}</span>
                  )}
                  {stage.sourceUrl && (
                    <a href={stage.sourceUrl} target="_blank" rel="noreferrer">
                      查看来源
                    </a>
                  )}
                </div>
                {stage.notes && <p>{stage.notes}</p>}
              </div>
              <div className="stage-actions">
                <button
                  className="icon-button small"
                  title="上移节点"
                  disabled={index === 0}
                  onClick={() => onMoveStage(stage, -1)}
                >
                  <ArrowUp size={14} />
                </button>
                <button
                  className="icon-button small"
                  title="下移节点"
                  disabled={index === stages.length - 1}
                  onClick={() => onMoveStage(stage, 1)}
                >
                  <ArrowDown size={14} />
                </button>
                <button
                  className="icon-button small"
                  title="编辑节点"
                  onClick={() => onEditStage(stage)}
                >
                  <Pencil size={14} />
                </button>
                <button
                  className="icon-button small danger-button"
                  title="删除节点"
                  onClick={() => onDeleteStage(stage)}
                >
                  <Trash2 size={14} />
                </button>
              </div>
            </article>
          ))}
        </div>
      ) : (
        <Empty text="暂未添加节点。收到笔试、面试或录用通知安排后可随时新增。" />
      )}
    </section>
  );
}
function Info({
  label,
  value,
  link,
  onNotify,
}: {
  label: string;
  value: string;
  link?: string;
  onNotify?: (message: string) => void;
}) {
  const externalURL = validExternalURL(link);
  const copyLink = async () => {
    if (!externalURL) return;
    try {
      const copied = await ClipboardSetText(externalURL);
      if (!copied) throw new Error("clipboard unavailable");
      onNotify?.("链接已复制");
    } catch {
      try {
        await navigator.clipboard.writeText(externalURL);
        onNotify?.("链接已复制");
      } catch {
        onNotify?.("无法复制链接，请手动选择后复制");
      }
    }
  };
  const openLink = () => {
    if (!externalURL) return;
    BrowserOpenURL(externalURL);
  };
  return (
    <div>
      <span>{label}</span>
      {externalURL ? (
        <div className="detail-link-value">
          <button
            className="detail-link"
            type="button"
            title={`在浏览器打开：${externalURL}`}
            onClick={openLink}
          >
            <span>{value}</span>
            <ExternalLink size={13} aria-hidden="true" />
          </button>
          <button
            className="icon-button small detail-link-copy"
            type="button"
            title="复制链接"
            aria-label={`复制${label}`}
            onClick={() => void copyLink()}
          >
            <Copy size={13} />
          </button>
        </div>
      ) : (
        <strong title={value}>{value}</strong>
      )}
    </div>
  );
}

function validExternalURL(value?: string) {
  const url = value?.trim();
  if (!url) return "";
  try {
    const parsed = new URL(url);
    return ["http:", "https:"].includes(parsed.protocol) ? parsed.href : "";
  } catch {
    return "";
  }
}

function Dialog({
  title,
  subtitle,
  children,
  onClose,
  kicker = "手动录入",
  closeDisabled = false,
}: {
  title: string;
  subtitle: string;
  children: ReactNode;
  onClose: () => void;
  kicker?: string;
  closeDisabled?: boolean;
}) {
  return (
    <div className="dialog-backdrop" role="presentation">
      <section
        className="dialog"
        role="dialog"
        aria-modal="true"
        aria-label={title}
      >
        <div className="dialog-header">
          <div>
            <p className="eyebrow">{kicker}</p>
            <h2>{title}</h2>
            <p>{subtitle}</p>
          </div>
          <button className="icon-button" title={closeDisabled ? "操作进行中" : "关闭"} disabled={closeDisabled} onClick={onClose}>
            <X size={18} />
          </button>
        </div>
        {children}
      </section>
    </div>
  );
}

function updateSize(value: number) {
  if (!value) return "";
  if (value < 1024 * 1024) return `${Math.max(1, Math.ceil(value / 1024))} KB`;
  return `${(value / (1024 * 1024)).toFixed(1)} MB`;
}

function UpdateDialog({
  status,
  onClose,
  onChanged,
}: {
  status: AppUpdate | null;
  onClose: () => void;
  onChanged: (next: AppUpdate) => void;
}) {
  const [error, setError] = useState("");
  const current = status || {
    currentVersion: "",
    latestVersion: "",
    available: false,
    state: "idle" as const,
    releaseNotes: "",
    publishedAt: "",
    releaseUrl: "",
    assetName: "",
    assetSize: 0,
    downloadedBytes: 0,
    message: "正在读取更新状态",
    networkStatus: "",
    checkedAt: "",
  };
  const busy = current.state === "checking" || current.state === "downloading" || current.state === "installing";
  const progress = current.assetSize > 0
    ? Math.min(100, Math.round((current.downloadedBytes / current.assetSize) * 100))
    : 0;
  const check = async () => {
    if (busy) return;
    setError("");
    try {
      onChanged(await api.checkForAppUpdate());
    } catch {
      void api.appUpdateStatus().then(onChanged).catch(() => undefined);
    }
  };
  const download = async () => {
    if (busy) return;
    setError("");
    try {
      onChanged(await api.downloadAppUpdate());
    } catch (reason) {
      setError(messageOf(reason));
      void api.appUpdateStatus().then(onChanged).catch(() => undefined);
    }
  };
  const install = async () => {
    if (busy) return;
    setError("");
    try {
      await api.installDownloadedUpdate();
    } catch (reason) {
      setError(messageOf(reason));
      void api.appUpdateStatus().then(onChanged).catch(() => undefined);
    }
  };
  return (
    <Dialog
      title="应用更新"
      subtitle="通过 GitHub Release 获取已校验的新版本。更新只替换应用程序，不会改动本地数据、附件、备份或云同步配置。"
      kicker="Offer Atlas"
      onClose={onClose}
      closeDisabled={busy}
    >
      <div className="update-dialog-body">
        <section className={`update-summary update-${current.state}`}>
          <span className="update-summary-icon" aria-hidden="true">
            {busy ? <LoaderCircle size={20} /> : <BadgeCheck size={20} />}
          </span>
          <div>
            <small>当前版本 v{current.currentVersion || "-"}</small>
            <strong>
              {current.available || current.state === "downloaded" || current.state === "installing"
                ? `新版本 v${current.latestVersion}`
                : current.state === "failed" ? "暂时无法检查更新" : "已是最新版本"}
            </strong>
            <span>{current.message}</span>
            {current.networkStatus && <b className="update-network-status">{current.networkStatus}</b>}
            <em>{current.checkedAt ? `最近检查 ${textDateTime(current.checkedAt)}` : "尚未检查更新"}</em>
          </div>
          {current.publishedAt && <time>{textDate(current.publishedAt)} 发布</time>}
        </section>

        {current.state === "downloading" && (
          <section className="update-download-progress" aria-live="polite">
            <div><span>正在下载并校验</span><strong>{progress}%</strong></div>
            <i><b style={{ width: `${progress}%` }} /></i>
            {current.networkStatus && <small>{current.networkStatus}</small>}
            <small>{updateSize(current.downloadedBytes)} / {updateSize(current.assetSize) || "正在获取大小"}</small>
          </section>
        )}

        {current.available && (
          <section className="update-release-notes">
            <div className="update-section-heading">
              <span>本次更新</span>
              {current.releaseUrl && (
                <button className="inline-text-button" type="button" onClick={() => void BrowserOpenURL(current.releaseUrl)}>
                  <ExternalLink size={13} />
                  完整发布说明
                </button>
              )}
            </div>
            <pre>{current.releaseNotes || "发布说明将在 GitHub Release 中提供。"}</pre>
          </section>
        )}

        {current.state === "downloaded" && (
          <section className="update-ready-note">
            <ShieldCheck size={17} />
            <div><strong>新版本已完成完整性校验</strong><span>开始更新后，会先等待正在进行的云同步和待同步修改安全完成，再自动启动新版本。</span></div>
          </section>
        )}
        {error && <p className="form-error update-error">{error}</p>}
        <div className="update-dialog-actions">
          <button className="secondary-button" type="button" disabled={busy} onClick={() => void check()}>
            {current.state === "checking" ? <LoaderCircle className="button-spinner" size={15} /> : <RotateCcw size={15} />}
            检查更新
          </button>
          {current.state === "downloaded" ? (
            <button className="primary-button" type="button" onClick={() => void install()}>
              <ArrowDown size={15} />
              安全重启并更新
            </button>
          ) : current.available && (
            <button className="primary-button" type="button" disabled={busy} onClick={() => void download()}>
              {current.state === "downloading" ? <LoaderCircle className="button-spinner" size={15} /> : <ArrowDown size={15} />}
              下载新版本{current.assetSize ? `（${updateSize(current.assetSize)}）` : ""}
            </button>
          )}
        </div>
      </div>
    </Dialog>
  );
}

function ProtectedActionDialog({
  action,
  onClose,
}: {
  action: ProtectedAction;
  onClose: () => void;
}) {
  const [confirmation, setConfirmation] = useState("");
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const caution = action.tone === "caution";
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (confirmation !== action.confirmationText || saving) return;
    setSaving(true);
    setError("");
    try {
      await action.action();
      onClose();
    } catch (reason) {
      setError(messageOf(reason));
      setSaving(false);
    }
  };
  return (
    <div className="dialog-backdrop protected-action-backdrop" role="presentation">
      <section
        className={`dialog protected-action-dialog ${caution ? "caution" : "danger"}`}
        role="dialog"
        aria-modal="true"
        aria-label={action.title}
      >
        <form className="form-grid protected-action-form" onSubmit={submit}>
          <section className="protected-action-intro">
            <span className="protected-action-icon" aria-hidden="true">
              {caution ? <CircleAlert size={18} /> : <Trash2 size={18} />}
            </span>
            <div>
              <p>{caution ? "需要确认" : "受保护删除"}</p>
              <h2>{action.title}</h2>
              <span>{action.description}</span>
            </div>
            <button
              className="icon-button small"
              type="button"
              title="关闭"
              disabled={saving}
              onClick={onClose}
            >
              <X size={15} />
            </button>
          </section>
          <section className="protected-action-subject">
            <span>即将处理</span>
            <strong title={action.subject}>{action.subject}</strong>
          </section>
          <Field wide label={`输入“${action.confirmationText}”以确认`}>
            <input
              autoFocus
              value={confirmation}
              onChange={(event) => setConfirmation(event.target.value)}
              placeholder={action.confirmationText}
              disabled={saving}
            />
          </Field>
          <FormError value={error} />
          <div className="dialog-buttons protected-action-buttons">
            <button type="button" className="ghost-button" disabled={saving} onClick={onClose}>
              取消
            </button>
            <button
              className={caution ? "secondary-button protected-caution-button" : "danger-primary-button"}
              disabled={saving || confirmation !== action.confirmationText}
              type="submit"
            >
              {caution ? <CloudOff size={15} /> : <Trash2 size={15} />}
              {saving ? "处理中" : action.confirmLabel}
            </button>
          </div>
        </form>
      </section>
    </div>
  );
}

function Field({
  label,
  children,
  wide = false,
}: {
  label: string;
  children: ReactNode;
  wide?: boolean;
}) {
  return (
    <label className={`field ${wide ? "wide" : ""}`}>
      <span>{label}</span>
      {children}
    </label>
  );
}
function FormError({ value }: { value: string }) {
  return value ? <p className="form-error">{value}</p> : null;
}
function FormHint({ children }: { children: ReactNode }) {
  return <p className="form-hint">{children}</p>;
}

type ReferenceOption = {
  id: string;
  label: string;
  detail?: string;
};

function ReferenceAutocomplete({
  value,
  options,
  placeholder,
  emptyText,
  autoFocus = false,
  disabled = false,
  onChange,
  onSelect,
}: {
  value: string;
  options: ReferenceOption[];
  placeholder: string;
  emptyText: string;
  autoFocus?: boolean;
  disabled?: boolean;
  onChange: (value: string) => void;
  onSelect: (option: ReferenceOption) => void;
}) {
  const rootRef = useRef<HTMLDivElement>(null);
  const [open, setOpen] = useState(false);
  const [activeIndex, setActiveIndex] = useState(-1);
  const normalizedValue = value.trim().toLocaleLowerCase();
  const matches = options.filter((option) =>
    option.label.toLocaleLowerCase().includes(normalizedValue),
  );
  useEffect(() => {
    const closeOnOutsidePress = (event: MouseEvent) => {
      if (!rootRef.current?.contains(event.target as Node)) setOpen(false);
    };
    window.addEventListener("mousedown", closeOnOutsidePress);
    return () => window.removeEventListener("mousedown", closeOnOutsidePress);
  }, []);
  useEffect(() => {
    setActiveIndex(-1);
  }, [value]);
  const select = (option: ReferenceOption) => {
    onSelect(option);
    setOpen(false);
    setActiveIndex(-1);
  };
  const listID = `reference-options-${options[0]?.id || "empty"}`;
  return (
    <div className={`reference-autocomplete ${open && options.length ? "open" : ""}`} ref={rootRef}>
      <input
        autoFocus={autoFocus}
        required
        disabled={disabled}
        role="combobox"
        aria-autocomplete="list"
        aria-expanded={open && options.length > 0}
        aria-controls={listID}
        value={value}
        onFocus={() => {
          if (options.length) setOpen(true);
        }}
        onChange={(event) => {
          onChange(event.target.value);
          if (options.length) setOpen(true);
        }}
        onKeyDown={(event) => {
          if (event.key === "Escape") {
            setOpen(false);
            return;
          }
          if (!matches.length) return;
          if (event.key === "ArrowDown") {
            event.preventDefault();
            setOpen(true);
            setActiveIndex((current) => Math.min(current + 1, matches.length - 1));
          }
          if (event.key === "ArrowUp") {
            event.preventDefault();
            setOpen(true);
            setActiveIndex((current) => Math.max(current - 1, 0));
          }
          if (event.key === "Enter" && open && activeIndex >= 0) {
            event.preventDefault();
            select(matches[activeIndex]);
          }
        }}
        placeholder={placeholder}
      />
      {options.length > 0 && (
        <button
          type="button"
          className="reference-autocomplete-trigger"
          title={open ? "收起已有记录" : "选择已有记录"}
          aria-label={open ? "收起已有记录" : "选择已有记录"}
          disabled={disabled}
          onMouseDown={(event) => event.preventDefault()}
          onClick={() => setOpen((current) => !current)}
        >
          <ChevronDown size={15} />
        </button>
      )}
      {open && options.length > 0 && (
        <div className="reference-autocomplete-menu" id={listID} role="listbox">
          {matches.length ? (
            matches.map((option, index) => (
              <button
                className={activeIndex === index ? "active" : ""}
                type="button"
                role="option"
                aria-selected={activeIndex === index}
                key={option.id}
                onMouseDown={(event) => event.preventDefault()}
                onClick={() => select(option)}
              >
                <span>{option.label}</span>
                <small>{option.detail || "已有记录"}</small>
              </button>
            ))
          ) : (
            <p>{emptyText}</p>
          )}
        </div>
      )}
    </div>
  );
}

function dateFieldText(value: string, withTime: boolean, placeholder: string) {
  if (!value) return placeholder;
  const [date, time] = value.split("T");
  const [year, month, day] = date.split("-");
  if (!year || !month || !day) return value;
  const formatted = `${year}年${Number(month)}月${Number(day)}日`;
  return withTime && time ? `${formatted} ${time.slice(0, 5)}` : formatted;
}
type PickerInputType = "date" | "datetime-local";

function PickerFieldInput({
  type,
  value,
  onChange,
  min,
  placeholder,
  disabled = false,
}: {
  type: PickerInputType;
  value: string;
  onChange: (value: string) => void;
  min?: string;
  placeholder: string;
  disabled?: boolean;
}) {
  return (
    <span className={`date-field-input ${value ? "has-value" : ""} ${disabled ? "is-disabled" : ""}`}>
      <span className="date-field-display">
        {dateFieldText(value, type === "datetime-local", placeholder)}
      </span>
      <CalendarDays className="date-field-icon" size={16} aria-hidden="true" />
      <input
        className="date-field-native"
        type={type}
        value={value}
        min={min}
        aria-label={placeholder}
        disabled={disabled}
        onChange={(event) => onChange(event.target.value)}
      />
    </span>
  );
}

function DateFieldInput({
  value,
  onChange,
  min,
  placeholder = "选择日期",
  disabled = false,
}: {
  value: string;
  onChange: (value: string) => void;
  min?: string;
  placeholder?: string;
  disabled?: boolean;
}) {
  return (
    <PickerFieldInput
      type="date"
      value={value}
      min={min}
      placeholder={placeholder}
      disabled={disabled}
      onChange={onChange}
    />
  );
}
function DateTimeFieldInput({
  value,
  onChange,
  min,
  placeholder = "选择日期和时间",
}: {
  value: string;
  onChange: (value: string) => void;
  min?: string;
  placeholder?: string;
}) {
  return (
    <PickerFieldInput
      type="datetime-local"
      value={value}
      min={min}
      placeholder={placeholder}
      onChange={onChange}
    />
  );
}
function Buttons({
  onClose,
  saving,
  label,
}: {
  onClose: () => void;
  saving: boolean;
  label: string;
}) {
  return (
    <div className="dialog-buttons">
      <button type="button" className="ghost-button" onClick={onClose}>
        取消
      </button>
      <button className="primary-button" disabled={saving} type="submit">
        <CheckCircle2 size={16} />
        {saving ? "保存中" : label}
      </button>
    </div>
  );
}

function CompanyDialog({
  initial,
  onClose,
  onSaved,
}: {
  initial: Company | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [form, setForm] = useState<CompanyInput>({
    id: initial?.id,
    name: initial?.name || "",
    industry: initial?.industry || "",
    homepage: initial?.homepage || "",
    notes: initial?.notes || "",
  });
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setSaving(true);
    try {
      await api.saveCompany(form);
      onSaved();
    } catch (reason) {
      setError(messageOf(reason));
      setSaving(false);
    }
  };
  return (
    <Dialog
      title={initial ? "编辑公司" : "新增公司"}
      subtitle="公司只保存主体信息，具体秋招信息归属于招聘批次。"
      onClose={onClose}
    >
      <form className="form-grid" onSubmit={submit}>
        <Field label="公司名称">
          <input
            autoFocus
            required
            value={form.name}
            onChange={(event) => setForm({ ...form, name: event.target.value })}
          />
        </Field>
        <Field label="行业">
          <input
            value={form.industry}
            onChange={(event) =>
              setForm({ ...form, industry: event.target.value })
            }
          />
        </Field>
        <Field wide label="招聘官网">
          <input
            type="url"
            value={form.homepage}
            onChange={(event) =>
              setForm({ ...form, homepage: event.target.value })
            }
            placeholder="https://"
          />
        </Field>
        <Field wide label="备注">
          <textarea
            rows={3}
            value={form.notes}
            onChange={(event) =>
              setForm({ ...form, notes: event.target.value })
            }
          />
        </Field>
        <FormError value={error} />
        <Buttons onClose={onClose} saving={saving} label="保存公司" />
      </form>
    </Dialog>
  );
}
function CampaignDialog({
  companies,
  initial,
  onClose,
  onSaved,
}: {
  companies: Company[];
  initial: Campaign | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const [form, setForm] = useState<CampaignInput>({
    id: initial?.id,
    companyId: initial?.companyId || "",
    name: initial?.name || "",
    opensOn: inputDate(initial?.opensOn),
    closesOn: inputDate(initial?.closesOn),
    sourceUrl: initial?.sourceUrl || "",
    lastVerifiedOn: inputDate(initial?.lastVerifiedOn),
    processOverview: initial?.processOverview || "",
    notes: initial?.notes || "",
  });
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setSaving(true);
    try {
      await api.saveCampaign(form);
      onSaved();
    } catch (reason) {
      setError(messageOf(reason));
      setSaving(false);
    }
  };
  return (
    <Dialog
      title={initial ? "编辑招聘批次" : "新增招聘批次"}
      subtitle="官方流程只记录概述；实际笔试和面试在具体投递中手动添加。"
      onClose={onClose}
    >
      <form className="form-grid" onSubmit={submit}>
        <Field label="所属公司">
          <select
            required
            value={form.companyId}
            onChange={(event) =>
              setForm({ ...form, companyId: event.target.value })
            }
          >
            <option value="">选择公司</option>
            {companies.map((item) => (
              <option key={item.id} value={item.id}>
                {item.name}
              </option>
            ))}
          </select>
        </Field>
        <Field label="批次名称">
          <input
            required
            value={form.name}
            onChange={(event) => setForm({ ...form, name: event.target.value })}
          />
        </Field>
        <Field label="开放日期">
          <DateFieldInput
            value={form.opensOn}
            onChange={(value) => setForm({ ...form, opensOn: value })}
          />
        </Field>
        <Field label="截止日期">
          <DateFieldInput
            value={form.closesOn}
            onChange={(value) => setForm({ ...form, closesOn: value })}
          />
        </Field>
        <Field wide label="批次官网链接">
          <input
            type="url"
            value={form.sourceUrl}
            onChange={(event) =>
              setForm({ ...form, sourceUrl: event.target.value })
            }
            placeholder="本次招聘公告或批次主页"
          />
        </Field>
        <Field wide label="最后核验">
          <DateFieldInput
            value={form.lastVerifiedOn}
            onChange={(value) => setForm({ ...form, lastVerifiedOn: value })}
          />
        </Field>
        <Field wide label="官方流程参考">
          <textarea
            rows={4}
            value={form.processOverview}
            onChange={(event) =>
              setForm({ ...form, processOverview: event.target.value })
            }
            placeholder="例如：公告提及两场笔试，面试轮次视业务部门安排而定。"
          />
        </Field>
        <Field wide label="备注">
          <textarea
            rows={2}
            value={form.notes}
            onChange={(event) =>
              setForm({ ...form, notes: event.target.value })
            }
          />
        </Field>
        <FormError value={error} />
        <Buttons onClose={onClose} saving={saving} label="保存批次" />
      </form>
    </Dialog>
  );
}
function PositionDialog({
  companies,
  campaigns,
  initial,
  onClose,
  onSaved,
}: {
  companies: Company[];
  campaigns: Campaign[];
  initial: Position | null;
  onClose: () => void;
  onSaved: () => void;
}) {
  const initialCompanyID =
    campaigns.find((campaign) => campaign.id === initial?.campaignId)
      ?.companyId || "";
  const [companyID, setCompanyID] = useState(initialCompanyID);
  const [form, setForm] = useState<PositionInput>({
    id: initial?.id,
    campaignId: initial?.campaignId || "",
    title: initial?.title || "",
    jobCode: initial?.jobCode || "",
    department: initial?.department || "",
    location: initial?.location || "",
    track: initial?.track || "",
    sourceUrl: initial?.sourceUrl || "",
    priority: initial?.priority || 3,
    notes: initial?.notes || "",
  });
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const companyCampaigns = campaigns.filter(
    (campaign) => campaign.companyId === companyID,
  );
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setSaving(true);
    try {
      await api.savePosition(form);
      onSaved();
    } catch (reason) {
      setError(messageOf(reason));
      setSaving(false);
    }
  };
  return (
    <Dialog
      title={initial ? "编辑岗位" : "新增岗位"}
      subtitle="先定位公司，再从该公司的招聘批次中选择岗位所属批次。"
      onClose={onClose}
    >
      <form className="form-grid" onSubmit={submit}>
        <Field label="先选择公司">
          <select
            required
            value={companyID}
            onChange={(event) => {
              setCompanyID(event.target.value);
              setForm({ ...form, campaignId: "" });
            }}
          >
            <option value="">选择公司</option>
            {companies.map((item) => (
              <option key={item.id} value={item.id}>
                {item.name}
              </option>
            ))}
          </select>
        </Field>
        <Field label="再选择招聘批次">
          <select
            required
            disabled={!companyID}
            value={form.campaignId}
            onChange={(event) =>
              setForm({ ...form, campaignId: event.target.value })
            }
          >
            <option value="">
              {companyID ? "选择招聘批次" : "请先选择公司"}
            </option>
            {companyCampaigns.map((item) => (
              <option key={item.id} value={item.id}>
                {item.name}
              </option>
            ))}
          </select>
        </Field>
        {companyID && !companyCampaigns.length && (
          <FormHint>
            该公司尚未建立招聘批次，请先到“公司与批次”中新建。
          </FormHint>
        )}
        <Field label="岗位名称">
          <input
            autoFocus
            required
            value={form.title}
            onChange={(event) =>
              setForm({ ...form, title: event.target.value })
            }
          />
        </Field>
        <Field label="岗位编号">
          <input
            value={form.jobCode}
            onChange={(event) =>
              setForm({ ...form, jobCode: event.target.value })
            }
          />
        </Field>
        <Field label="部门 / 事业群">
          <input
            value={form.department}
            onChange={(event) =>
              setForm({ ...form, department: event.target.value })
            }
          />
        </Field>
        <Field label="城市">
          <input
            value={form.location}
            onChange={(event) =>
              setForm({ ...form, location: event.target.value })
            }
          />
        </Field>
        <Field label="方向">
          <input
            value={form.track}
            onChange={(event) =>
              setForm({ ...form, track: event.target.value })
            }
          />
        </Field>
        <Field wide label="岗位招聘链接">
          <input
            type="url"
            value={form.sourceUrl}
            onChange={(event) =>
              setForm({ ...form, sourceUrl: event.target.value })
            }
            placeholder="该岗位的招聘页或职位详情页"
          />
        </Field>
        <Field label="优先级 1-5">
          <input
            type="number"
            min={1}
            max={5}
            value={form.priority}
            onChange={(event) =>
              setForm({ ...form, priority: Number(event.target.value) })
            }
          />
        </Field>
        <Field wide label="岗位备注">
          <textarea
            rows={3}
            value={form.notes}
            onChange={(event) =>
              setForm({ ...form, notes: event.target.value })
            }
          />
        </Field>
        <FormError value={error} />
        <Buttons onClose={onClose} saving={saving} label="保存岗位" />
      </form>
    </Dialog>
  );
}
function QuickCapturePositionDialog({
  companies,
  campaigns,
  onClose,
  onSaved,
}: {
  companies: Company[];
  campaigns: Campaign[];
  onClose: () => void;
  onSaved: (position: Position) => Promise<void> | void;
}) {
  const [form, setForm] = useState<QuickCapturePositionInput>({
    companyName: "",
    companyIndustry: "",
    companyHomepage: "",
    companyNotes: "",
    campaignName: "",
    campaignOpensOn: "",
    campaignClosesOn: "",
    campaignSourceUrl: "",
    campaignLastVerifiedOn: "",
    campaignProcessOverview: "",
    campaignNotes: "",
    title: "",
    jobCode: "",
    department: "",
    location: "",
    track: "",
    sourceUrl: "",
    priority: 3,
    notes: "",
  });
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const [pendingAttachments, setPendingAttachments] = useState<File[]>([]);
  const [createdPosition, setCreatedPosition] = useState<Position | null>(null);
  const [uploadingName, setUploadingName] = useState("");
  const fileInputRef = useRef<HTMLInputElement>(null);
  const matchingCompany = companies.find(
    (item) =>
      item.name.trim().toLocaleLowerCase() ===
      form.companyName.trim().toLocaleLowerCase(),
  );
  const campaignOptions = matchingCompany
    ? campaigns.filter((item) => item.companyId === matchingCompany.id)
    : [];
  const companyReferenceOptions = companies.map((item) => ({
    id: item.id,
    label: item.name,
    detail: item.industry || "已有公司资料",
  }));
  const campaignReferenceOptions = campaignOptions.map((item) => ({
    id: item.id,
    label: item.name,
    detail: item.processOverview ? "已保存流程参考" : "已有招聘批次",
  }));
  const matchingCampaign = matchingCompany
    ? campaignOptions.find(
        (item) =>
          item.name.trim().toLocaleLowerCase() ===
          form.campaignName.trim().toLocaleLowerCase(),
      )
    : undefined;
  const reuseCompany = (company: Company, current: QuickCapturePositionInput) => ({
    ...current,
    companyIndustry: company.industry,
    companyHomepage: company.homepage,
    companyNotes: company.notes,
  });
  const reuseCampaign = (campaign: Campaign, current: QuickCapturePositionInput) => ({
    ...current,
    campaignOpensOn: campaign.opensOn || "",
    campaignClosesOn: campaign.closesOn || "",
    campaignSourceUrl: campaign.sourceUrl,
    campaignLastVerifiedOn: campaign.lastVerifiedOn || "",
    campaignProcessOverview: campaign.processOverview,
    campaignNotes: campaign.notes,
  });
  const clearCompanyFields = (current: QuickCapturePositionInput) => ({
    ...current,
    companyIndustry: "",
    companyHomepage: "",
    companyNotes: "",
  });
  const clearCampaignDetails = (current: QuickCapturePositionInput) => ({
    ...current,
    campaignOpensOn: "",
    campaignClosesOn: "",
    campaignSourceUrl: "",
    campaignLastVerifiedOn: "",
    campaignProcessOverview: "",
    campaignNotes: "",
  });
  const clearCampaignFields = (current: QuickCapturePositionInput) => ({
    ...clearCampaignDetails(current),
    campaignName: "",
  });
  const changeCompanyName = (companyName: string) => {
    const nextCompany = companies.find(
      (item) =>
        item.name.trim().toLocaleLowerCase() ===
        companyName.trim().toLocaleLowerCase(),
    );
    setForm((current) => {
      const companyChanged = Boolean(
        matchingCompany && matchingCompany.id !== nextCompany?.id,
      );
      let next = { ...current, companyName };
      if (nextCompany) {
        next = reuseCompany(nextCompany, next);
      } else if (matchingCompany) {
        next = clearCompanyFields(next);
      }
      return companyChanged ? clearCampaignFields(next) : next;
    });
  };
  const changeCampaignName = (campaignName: string) => {
    const nextCampaign = matchingCompany
      ? campaigns.find(
          (item) =>
            item.companyId === matchingCompany.id &&
            item.name.trim().toLocaleLowerCase() ===
              campaignName.trim().toLocaleLowerCase(),
        )
      : undefined;
    setForm((current) => {
      const next = { ...current, campaignName };
      if (nextCampaign) return reuseCampaign(nextCampaign, next);
      return matchingCampaign ? clearCampaignDetails(next) : next;
    });
  };
  useEffect(() => {
    if (!matchingCompany) return;
    setForm((current) => {
      const next = reuseCompany(matchingCompany, current);
      return next.companyIndustry === current.companyIndustry &&
        next.companyHomepage === current.companyHomepage &&
        next.companyNotes === current.companyNotes
        ? current
        : next;
    });
  }, [matchingCompany?.id, matchingCompany?.industry, matchingCompany?.homepage, matchingCompany?.notes]);
  useEffect(() => {
    if (!matchingCampaign) return;
    setForm((current) => {
      const next = reuseCampaign(matchingCampaign, current);
      return next.campaignOpensOn === current.campaignOpensOn &&
        next.campaignClosesOn === current.campaignClosesOn &&
        next.campaignSourceUrl === current.campaignSourceUrl &&
        next.campaignLastVerifiedOn === current.campaignLastVerifiedOn &&
        next.campaignProcessOverview === current.campaignProcessOverview &&
        next.campaignNotes === current.campaignNotes
        ? current
        : next;
    });
  }, [matchingCampaign?.id, matchingCampaign?.opensOn, matchingCampaign?.closesOn, matchingCampaign?.sourceUrl, matchingCampaign?.lastVerifiedOn, matchingCampaign?.processOverview, matchingCampaign?.notes]);
  const stageFiles = (files: Iterable<File>) => {
    const selected = Array.from(files);
    const oversized = selected.find(
      (file) => file.size > maxStagedAttachmentBytes,
    );
    if (oversized) {
      setError(`“${oversized.name}”超过 25 MB，快速收录暂不支持上传此文件。`);
      return;
    }
    setError("");
    setPendingAttachments((current) => [...current, ...selected]);
  };
  const receivePaste = (event: ClipboardEvent<HTMLDivElement>) => {
    if (saving) return;
    const image = Array.from(event.clipboardData.files).find((file) =>
      attachmentIsClipboardImage(file.type),
    );
    if (!image) {
      setError("剪贴板中没有图片，请复制截图后重试。");
      return;
    }
    event.preventDefault();
    stageFiles([
      new File([image], clipboardImageName(image.type), { type: image.type }),
    ]);
  };
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setSaving(true);
    setError("");
    try {
      const position =
        createdPosition || (await api.quickCapturePosition(form));
      if (!createdPosition) setCreatedPosition(position);
      for (const file of pendingAttachments) {
        setUploadingName(file.name);
        try {
          await api.uploadPositionAttachment(
            position.id,
            file.name,
            await fileDataURL(file),
          );
          setPendingAttachments((current) =>
            current.filter((item) => item !== file),
          );
        } catch (reason) {
          setError(
            `岗位已收录，但附件“${file.name}”上传失败：${messageOf(reason)}。请再次点击“完成附件上传”重试剩余文件。`,
          );
          return;
        }
      }
      await onSaved(position);
    } catch (reason) {
      setError(messageOf(reason));
    } finally {
      setSaving(false);
      setUploadingName("");
    }
  };
  return (
    <Dialog
      title="快速收录岗位"
      subtitle="一次录入公司、招聘批次和岗位；同名记录会自动复用。"
      kicker="岗位管理"
      onClose={onClose}
    >
      <form className="form-grid quick-capture-form" onSubmit={submit}>
        <FormHint>
          {createdPosition
            ? "岗位已收录。附件将继续上传至该岗位；无需重复填写或创建。"
            : matchingCompany && matchingCampaign
              ? "已复用公司与招聘批次；已保存资料会同步显示，并在此处保持只读。"
              : matchingCompany
                ? "已复用公司资料；已保存资料会同步显示，并在此处保持只读。"
                : "同名公司或该公司下同名招聘批次会直接复用；首次创建时会保存本次填写的完整信息。"}
        </FormHint>
        <section className="quick-capture-section">
          <header>
            <span>01</span>
            <div>
              <strong>公司信息</strong>
              <small>{matchingCompany ? "已复用 · 资料只读" : "主体资料"}</small>
            </div>
          </header>
          <div className="quick-capture-section-fields">
            <Field label="公司名称">
              <ReferenceAutocomplete
                autoFocus
                value={form.companyName}
                placeholder="例如：阿里巴巴"
                emptyText="没有匹配的已有公司，可直接创建新公司"
                options={companyReferenceOptions}
                onChange={changeCompanyName}
                onSelect={(item) => changeCompanyName(item.label)}
              />
            </Field>
            <Field label="行业">
              <input
                className={matchingCompany ? "quick-capture-readonly" : ""}
                readOnly={Boolean(matchingCompany)}
                value={form.companyIndustry}
                onChange={(event) =>
                  setForm({ ...form, companyIndustry: event.target.value })
                }
                placeholder="例如：互联网"
              />
            </Field>
            <Field wide label="招聘官网">
              <input
                type="url"
                className={matchingCompany ? "quick-capture-readonly" : ""}
                readOnly={Boolean(matchingCompany)}
                value={form.companyHomepage}
                onChange={(event) =>
                  setForm({ ...form, companyHomepage: event.target.value })
                }
                placeholder="https://"
              />
            </Field>
            <Field wide label="公司备注">
              <textarea
                className={matchingCompany ? "quick-capture-readonly" : ""}
                readOnly={Boolean(matchingCompany)}
                rows={2}
                value={form.companyNotes}
                onChange={(event) =>
                  setForm({ ...form, companyNotes: event.target.value })
                }
              />
            </Field>
          </div>
        </section>
        <section className="quick-capture-section">
          <header>
            <span>02</span>
            <div>
              <strong>招聘批次</strong>
              <small>{matchingCampaign ? "已复用 · 资料只读" : "本轮秋招信息"}</small>
            </div>
          </header>
          <div className="quick-capture-section-fields">
            <Field label="招聘批次">
              <ReferenceAutocomplete
                value={form.campaignName}
                placeholder="例如：2027 届秋招"
                emptyText="没有匹配的已有批次，可直接创建新批次"
                options={campaignReferenceOptions}
                onChange={changeCampaignName}
                onSelect={(item) => changeCampaignName(item.label)}
              />
            </Field>
            <Field label="最后核验">
              <DateFieldInput
                value={form.campaignLastVerifiedOn}
                disabled={Boolean(matchingCampaign)}
                onChange={(value) =>
                  setForm({ ...form, campaignLastVerifiedOn: value })
                }
              />
            </Field>
            <Field label="开放日期">
              <DateFieldInput
                value={form.campaignOpensOn}
                disabled={Boolean(matchingCampaign)}
                onChange={(value) =>
                  setForm({ ...form, campaignOpensOn: value })
                }
              />
            </Field>
            <Field label="截止日期">
              <DateFieldInput
                value={form.campaignClosesOn}
                disabled={Boolean(matchingCampaign)}
                onChange={(value) =>
                  setForm({ ...form, campaignClosesOn: value })
                }
              />
            </Field>
            <Field wide label="批次官网链接">
              <input
                type="url"
                className={matchingCampaign ? "quick-capture-readonly" : ""}
                readOnly={Boolean(matchingCampaign)}
                value={form.campaignSourceUrl}
                onChange={(event) =>
                  setForm({ ...form, campaignSourceUrl: event.target.value })
                }
                placeholder="招聘公告或批次主页"
              />
            </Field>
            <Field wide label="官方流程参考">
              <textarea
                className={matchingCampaign ? "quick-capture-readonly" : ""}
                readOnly={Boolean(matchingCampaign)}
                rows={3}
                value={form.campaignProcessOverview}
                onChange={(event) =>
                  setForm({
                    ...form,
                    campaignProcessOverview: event.target.value,
                  })
                }
                placeholder="例如：公告提及笔试与若干轮面试"
              />
            </Field>
            <Field wide label="批次备注">
              <textarea
                className={matchingCampaign ? "quick-capture-readonly" : ""}
                readOnly={Boolean(matchingCampaign)}
                rows={2}
                value={form.campaignNotes}
                onChange={(event) =>
                  setForm({ ...form, campaignNotes: event.target.value })
                }
              />
            </Field>
          </div>
        </section>
        <section className="quick-capture-section">
          <header>
            <span>03</span>
            <div>
              <strong>岗位信息</strong>
              <small>目标职位资料</small>
            </div>
          </header>
          <div className="quick-capture-section-fields">
            <Field label="岗位名称">
              <input
                required
                value={form.title}
                onChange={(event) =>
                  setForm({ ...form, title: event.target.value })
                }
                placeholder="例如：后端开发工程师"
              />
            </Field>
            <Field label="岗位编号">
              <input
                value={form.jobCode}
                onChange={(event) =>
                  setForm({ ...form, jobCode: event.target.value })
                }
              />
            </Field>
            <Field label="部门 / 事业群">
              <input
                value={form.department}
                onChange={(event) =>
                  setForm({ ...form, department: event.target.value })
                }
              />
            </Field>
            <Field label="城市">
              <input
                value={form.location}
                onChange={(event) =>
                  setForm({ ...form, location: event.target.value })
                }
              />
            </Field>
            <Field label="岗位方向">
              <input
                value={form.track}
                onChange={(event) =>
                  setForm({ ...form, track: event.target.value })
                }
              />
            </Field>
            <Field label="优先级 1-5">
              <input
                type="number"
                min={1}
                max={5}
                value={form.priority}
                onChange={(event) =>
                  setForm({ ...form, priority: Number(event.target.value) })
                }
              />
            </Field>
            <Field wide label="岗位招聘链接">
              <input
                type="url"
                value={form.sourceUrl}
                onChange={(event) =>
                  setForm({ ...form, sourceUrl: event.target.value })
                }
                placeholder="该岗位的职位详情页"
              />
            </Field>
            <Field wide label="岗位备注">
              <textarea
                rows={3}
                value={form.notes}
                onChange={(event) =>
                  setForm({ ...form, notes: event.target.value })
                }
              />
            </Field>
          </div>
        </section>
        <section className="quick-capture-section quick-attachment-section">
          <header>
            <span>04</span>
            <div>
              <strong>岗位附件</strong>
              <small>JD 截图、公告或其他参考资料</small>
            </div>
          </header>
          <div className="quick-attachment-body">
            <input
              ref={fileInputRef}
              className="quick-capture-file-input"
              type="file"
              multiple
              onChange={(event) => {
                stageFiles(event.target.files || []);
                event.target.value = "";
              }}
            />
            <button
              className="secondary-button quick-attachment-picker"
              type="button"
              onClick={() => fileInputRef.current?.click()}
            >
              <ImagePlus size={16} />
              选择图片或文件
            </button>
            <div
              className="quick-paste-zone"
              role="button"
              tabIndex={0}
              aria-label="将鼠标移至此处后按 Ctrl+V 粘贴截图"
              onMouseEnter={(event) => event.currentTarget.focus()}
              onMouseLeave={(event) => {
                if (document.activeElement === event.currentTarget)
                  event.currentTarget.blur();
              }}
              onMouseDown={(event) => event.currentTarget.focus()}
              onPaste={receivePaste}
            >
              <ClipboardPaste size={17} />
              <div>
                <strong>粘贴截图</strong>
                <span>将鼠标移至此处后按 Ctrl+V；页面其他位置不会接收粘贴</span>
              </div>
              <kbd>Ctrl+V</kbd>
            </div>
          </div>
          {pendingAttachments.length > 0 && (
            <div className="quick-attachment-list">
              {pendingAttachments.map((file, index) => (
                <QuickCaptureAttachmentRow
                  file={file}
                  key={`${file.name}-${file.size}-${file.lastModified}-${index}`}
                  disabled={saving && uploadingName === file.name}
                  onRemove={() =>
                    setPendingAttachments((current) =>
                      current.filter((item) => item !== file),
                    )
                  }
                />
              ))}
            </div>
          )}
        </section>
        <FormError value={error} />
        <Buttons
          onClose={onClose}
          saving={saving}
          label={
            createdPosition
              ? uploadingName
                ? `上传 ${uploadingName}`
                : "完成附件上传"
              : "收录岗位"
          }
        />
      </form>
    </Dialog>
  );
}
function ApplicationDialog({
  positionID,
	resumes,
  initial,
  onClose,
	onManageResumes,
  onSaved,
}: {
  positionID: string;
	resumes: Resume[];
  initial: Application | null;
  onClose: () => void;
	onManageResumes: () => void;
  onSaved: () => void;
}) {
  const [form, setForm] = useState<ApplicationInput>({
    id: initial?.id,
    positionId: positionID,
    submittedOn: inputDate(initial?.submittedOn),
    channel: initial?.channel || "",
		resumeId: initial?.resumeId || "",
    resumeName: initial?.resumeName || "",
    notes: initial?.notes || "",
  });
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
	const selectedResume = resumes.find((item) => item.id === form.resumeId);
	const availableResumes = resumes.filter((item) => !item.archived || item.id === form.resumeId);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setSaving(true);
    try {
		const application = await api.saveApplication(form);
      if (!form.id) {
        setForm((current) => ({ ...current, id: application.id }));
      }
      onSaved();
    } catch (reason) {
      setError(messageOf(reason));
      setSaving(false);
    }
  };
  return (
    <Dialog
      title={initial ? "编辑投递记录" : "创建投递记录"}
      subtitle="投递状态由最新流程节点自动计算，不需要手动维护。"
      onClose={onClose}
    >
      <form className="form-grid" onSubmit={submit}>
        <Field wide label="自动状态">
          <div className="derived-field">
            {initial ? (
              <Badge
                tone={initial.status}
                text={applicationLabels[initial.status]}
              />
            ) : (
              <span>创建后显示为“进行中”</span>
            )}
            <small>修改流程节点状态后会自动更新</small>
          </div>
        </Field>
        <Field label="投递日期">
          <DateFieldInput
            value={form.submittedOn}
            onChange={(value) => setForm({ ...form, submittedOn: value })}
          />
        </Field>
        <Field label="投递渠道">
          <input
            value={form.channel}
            onChange={(event) =>
              setForm({ ...form, channel: event.target.value })
            }
            placeholder="官网、内推、牛客等"
          />
        </Field>
		<Field wide label="投递简历">
          <div className="application-resume-picker">
            <div className="application-resume-summary">
              <FileText size={17} />
              <div className="application-resume-copy">
				<strong>{selectedResume ? selectedResume.name : availableResumes.length ? "选择简历库中的版本" : "简历库还没有可用版本"}</strong>
                <span>
				  {selectedResume
					? `${selectedResume.originalName} · ${attachmentSize(selectedResume.sizeBytes)}${selectedResume.archived ? " · 已归档" : ""}`
					: availableResumes.length
						? "先选择一个版本；关联后可在投递详情直接打开。"
						: "先在简历库添加文件，再为本次投递绑定对应版本。"}
                </span>
              </div>
            </div>
			<div className="application-resume-controls">
			  <select
				className="application-resume-select"
				aria-label="选择投递简历"
				value={form.resumeId}
				disabled={saving || availableResumes.length === 0}
				onChange={(event) => {
				  const resumeID = event.target.value;
				  const resume = resumes.find((item) => item.id === resumeID);
				  setForm({ ...form, resumeId: resumeID, resumeName: resume?.name || "" });
				}}
			  >
				<option value="">{availableResumes.length ? "暂不关联" : "暂无可选版本"}</option>
				{availableResumes.map((item) => (
				  <option value={item.id} key={item.id}>
					{item.name}{item.archived ? "（已归档）" : ""}
				  </option>
				))}
			  </select>
			  <button className="secondary-button application-resume-manage" type="button" disabled={saving} onClick={onManageResumes}>
				<FileStack size={15} />
				{availableResumes.length ? "管理简历库" : "添加简历版本"}
			  </button>
			</div>
          </div>
        </Field>
        <Field wide label="投递备注">
          <textarea
            rows={4}
            value={form.notes}
            onChange={(event) =>
              setForm({ ...form, notes: event.target.value })
            }
          />
        </Field>
        <FormError value={error} />
        <Buttons onClose={onClose} saving={saving} label="保存投递记录" />
      </form>
    </Dialog>
  );
}

function ResumeLibraryView({
	items,
	onNew,
	onOpen,
	onOpenApplications,
	onEdit,
	onArchive,
	onDelete,
}: {
	items: Resume[];
	onNew: () => void;
	onOpen: (id: string) => Promise<void>;
	onOpenApplications: (resumeID: string) => void;
	onEdit: (item: Resume) => void;
	onArchive: (item: Resume, archived: boolean) => Promise<void>;
	onDelete: (item: Resume) => Promise<void>;
}) {
	const active = items.filter((item) => !item.archived);
	const archived = items.filter((item) => item.archived);
	const usages = items.reduce((total, item) => total + item.usageCount, 0);
	const renderItem = (item: Resume) => (
		<article className={`resume-library-row ${item.archived ? "archived" : ""}`} key={item.id}>
			<span className="resume-file-mark"><FileText size={18} /></span>
			<div className="resume-library-main">
				<div>
					<strong>{item.name}</strong>
					{item.archived && <Badge tone="neutral" text="已归档" />}
				</div>
				<span title={item.originalName}>{item.originalName} · {attachmentSize(item.sizeBytes)}</span>
			</div>
			{item.usageCount > 0 ? (
				<button
					type="button"
					className="resume-library-usage"
					title={`查看使用“${item.name}”的 ${item.usageCount} 条投递`}
					onClick={() => onOpenApplications(item.id)}
				>
					<strong>{item.usageCount}</strong>
					<span>条投递关联</span>
				</button>
			) : (
				<div className="resume-library-usage">
					<strong>0</strong>
					<span>条投递关联</span>
				</div>
			)}
			<time title={textDateTime(item.updatedAt)}>更新 {textDate(item.updatedAt)}</time>
			<div className="resume-library-actions">
				<button className="icon-button small" title="打开简历" onClick={() => void onOpen(item.id)}><ExternalLink size={14} /></button>
				<button className="icon-button small" title="编辑版本名称" onClick={() => onEdit(item)}><Pencil size={14} /></button>
				<button className="icon-button small" title={item.archived ? "恢复使用" : "归档版本"} onClick={() => void onArchive(item, !item.archived)}>
					{item.archived ? <RotateCcw size={14} /> : <FolderOpen size={14} />}
				</button>
				{item.usageCount === 0 && <button className="icon-button small danger-button" title="删除未使用版本" onClick={() => void onDelete(item)}><Trash2 size={14} /></button>}
			</div>
		</article>
	);
	return (
		<div className="page-content resume-library-page">
			<section className="page-heading compact">
				<div>
					<h1>简历库</h1>
					<p className="muted">统一保存简历版本，投递时直接关联，历史使用始终可追溯。</p>
				</div>
				<button className="primary-button" onClick={onNew}><Plus size={16} />添加简历版本</button>
			</section>
			<section className="resume-library-summary" aria-label="简历库摘要">
				<div><span>可选版本</span><strong>{active.length}</strong></div>
				<div><span>已归档</span><strong>{archived.length}</strong></div>
				<div><span>投递关联</span><strong>{usages}</strong></div>
			</section>
			<section className="panel resume-library-panel">
				<div className="resume-library-heading"><span>版本</span><span>使用情况</span><span>最近更新</span><span>操作</span></div>
				{active.length ? active.map(renderItem) : <div className="resume-library-empty"><FileStack size={24} /><strong>还没有可用的简历版本</strong><span>添加后，可在创建投递时直接选择并重复使用。</span></div>}
			</section>
			{archived.length > 0 && (
				<section className="panel resume-library-panel archived-resume-panel">
					<div className="resume-library-section-title"><span>已归档版本</span><small>归档版本保留历史投递关联，不会出现在新投递选择中。</small></div>
					{archived.map(renderItem)}
				</section>
			)}
		</div>
	);
}

function ResumeDialog({
	initial,
	onClose,
	onSaved,
}: {
	initial: Resume | null;
	onClose: () => void;
	onSaved: () => void;
}) {
	const [name, setName] = useState(initial?.name || "");
	const [file, setFile] = useState<File | null>(null);
	const [saving, setSaving] = useState(false);
	const [error, setError] = useState("");
	const fileInputRef = useRef<HTMLInputElement>(null);
	const selectFile = (next?: File) => {
		if (!next) return;
		if (next.size > maxStagedAttachmentBytes) {
			setError(`“${next.name}”超过 25 MB，无法加入简历库。`);
			return;
		}
		setFile(next);
		if (!name.trim()) setName(next.name.replace(/\.[^.]+$/, ""));
		setError("");
	};
	const submit = async (event: FormEvent) => {
		event.preventDefault();
		setSaving(true);
		try {
			if (initial) {
				await api.saveResume({ id: initial.id, name, archived: initial.archived } satisfies ResumeInput);
			} else {
				if (!file) throw new Error("请选择要保存的简历文件");
				await api.uploadResume(name, file.name, await fileDataURL(file));
			}
			onSaved();
		} catch (reason) {
			setError(messageOf(reason));
			setSaving(false);
		}
	};
	return (
		<Dialog title={initial ? "编辑简历版本" : "添加简历版本"} subtitle={initial ? "修改名称不会影响已关联的投递记录。" : "文件由简历库统一保存，可关联到多条投递记录。"} onClose={onClose}>
			<form className="form-grid" onSubmit={submit}>
				<Field wide label="版本名称">
					<input value={name} onChange={(event) => setName(event.target.value)} placeholder="如：后端开发 v3" autoFocus />
				</Field>
				{initial ? (
					<Field wide label="已保存文件">
						<div className="resume-dialog-file"><FileText size={18} /><div><strong>{initial.originalName}</strong><span>{attachmentSize(initial.sizeBytes)} · 已关联 {initial.usageCount} 条投递</span></div></div>
					</Field>
				) : (
					<Field wide label="简历文件">
						<div className="resume-dialog-file">
							<input ref={fileInputRef} className="quick-capture-file-input" type="file" accept=".pdf,.doc,.docx,.txt,application/pdf,application/msword,application/vnd.openxmlformats-officedocument.wordprocessingml.document,text/plain" onChange={(event) => { selectFile(event.target.files?.[0]); event.target.value = ""; }} />
							<FileText size={18} />
							<div><strong>{file ? file.name : "选择 PDF、Word 或文本简历"}</strong><span>{file ? attachmentSize(file.size) : "最大 25 MB；相同文件会自动复用"}</span></div>
							<button type="button" className="secondary-button" disabled={saving} onClick={() => fileInputRef.current?.click()}><FolderOpen size={15} />选择文件</button>
						</div>
					</Field>
				)}
				<FormError value={error} />
				<Buttons onClose={onClose} saving={saving} label={initial ? "保存版本" : "添加到简历库"} />
			</form>
		</Dialog>
	);
}
function StageDialog({
  applicationID,
  initial,
  onClose,
  onManageTypes,
  onSaved,
}: {
  applicationID: string;
  initial: ApplicationStage | null;
  onClose: () => void;
  onManageTypes: () => void;
  onSaved: () => void;
}) {
  const catalog = useContext(StageTypeCatalogContext);
  const fallbackTypes = Object.entries(defaultStageTypeLabels)
    .filter(([id]) => id !== "other")
    .map(([id, name]) => ({ id, name, system: true }));
  const typeOptions = catalog.length ? catalog : fallbackTypes;
  const systemTypes = typeOptions.filter((item) => item.system);
  const customTypes = typeOptions.filter((item) => !item.system);
  const [form, setForm] = useState<ApplicationStageInput>({
    id: initial?.id,
    applicationId: applicationID,
    sortOrder: initial?.sortOrder || 0,
    content: initial?.content || "",
    type: initial?.type || "first_interview",
    status: initial?.status || "scheduled",
    scheduledStart: inputDateTime(initial?.scheduledStart),
    scheduledEnd: inputDateTime(initial?.scheduledEnd),
    resultAt: inputDateTime(initial?.resultAt),
    sourceUrl: initial?.sourceUrl || "",
    notes: initial?.notes || "",
  });
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (
      form.scheduledStart &&
      form.scheduledEnd &&
      form.scheduledStart.slice(0, 10) !== form.scheduledEnd.slice(0, 10)
    ) {
      setError("开始时间和结束时间必须在同一天");
      return;
    }
    const cutoff = form.scheduledEnd || form.scheduledStart;
    if (
      cutoff &&
      form.resultAt &&
      new Date(form.resultAt).getTime() < new Date(cutoff).getTime()
    ) {
      setError("结果通知时间不能早于笔试或面试结束时间");
      return;
    }
    if (
      cutoff &&
      new Date(cutoff).getTime() > Date.now() &&
      form.status !== "scheduled"
    ) {
      setError("未来的笔试或面试只能保持“已预约”，结束后再记录最终结果");
      return;
    }
    setSaving(true);
    try {
      await api.saveStage(form);
      onSaved();
    } catch (reason) {
      setError(messageOf(reason));
      setSaving(false);
    }
  };
  return (
    <Dialog
      title={initial ? "编辑流程节点" : "新增流程节点"}
      subtitle="系统类型用于统一统计；节点内容只记录本次流程的必要补充。"
      onClose={onClose}
    >
      <form className="form-grid" onSubmit={submit}>
        <div className="field">
          <span className="field-label-row">
            <span>节点类型</span>
            <button
              className="inline-text-button"
              type="button"
              onClick={onManageTypes}
            >
              <Settings2 size={13} />
              管理自定义类型
            </button>
          </span>
          <select
            autoFocus
            value={form.type}
            onChange={(event) => setForm({ ...form, type: event.target.value })}
          >
            <optgroup label="系统类型（纳入统计）">
              {systemTypes.map((item) => (
                <option key={item.id} value={item.id}>
                  {item.name}
                </option>
              ))}
            </optgroup>
            {customTypes.length > 0 && (
              <optgroup label="自定义类型（仅记录流程）">
                {customTypes.map((item) => (
                  <option key={item.id} value={item.id}>
                    {item.name}
                  </option>
                ))}
              </optgroup>
            )}
          </select>
        </div>
        <Field label="节点内容（可选）">
          <input
            value={form.content}
            onChange={(event) =>
              setForm({ ...form, content: event.target.value })
            }
            placeholder="例如：技术负责人沟通、第二场在线笔试"
          />
        </Field>
        <Field label="当前状态">
          <select
            value={form.status}
            onChange={(event) =>
              setForm({ ...form, status: event.target.value as StageStatus })
            }
          >
            <option value="scheduled">已预约</option>
            <option value="passed">通过</option>
            <option value="failed">未通过</option>
          </select>
        </Field>
        <Field label="开始时间">
          <DateTimeFieldInput
            value={form.scheduledStart}
            onChange={(value) => setForm({ ...form, scheduledStart: value })}
          />
        </Field>
        <Field label="结束时间（可选）">
          <DateTimeFieldInput
            value={form.scheduledEnd}
            min={form.scheduledStart || undefined}
            onChange={(value) => setForm({ ...form, scheduledEnd: value })}
          />
        </Field>
        <Field label="结果通知时间（可选）">
          <DateTimeFieldInput
            value={form.resultAt}
            min={form.scheduledEnd || form.scheduledStart || undefined}
            onChange={(value) => setForm({ ...form, resultAt: value })}
          />
        </Field>
        <FormHint>
          开始和结束时间必须在同一天；结果通知不得早于结束时间。填写开始时间或结果通知时间后，才会出现在日历和待办；未来节点只能记录为已预约。
        </FormHint>
        <Field wide label="信息来源">
          <input
            type="url"
            value={form.sourceUrl}
            onChange={(event) =>
              setForm({ ...form, sourceUrl: event.target.value })
            }
          />
        </Field>
        <Field wide label="节点备注">
          <textarea
            rows={4}
            value={form.notes}
            onChange={(event) =>
              setForm({ ...form, notes: event.target.value })
            }
          />
        </Field>
        <FormError value={error} />
        <Buttons onClose={onClose} saving={saving} label="保存流程节点" />
      </form>
    </Dialog>
  );
}

function StageTypesDialog({
  items,
  onClose,
  onChanged,
  onRequestProtectedAction,
}: {
  items: StageTypeDefinition[];
  onClose: () => void;
  onChanged: (message: string) => Promise<void> | void;
  onRequestProtectedAction: (action: ProtectedAction) => void;
}) {
  const [newName, setNewName] = useState("");
  const [drafts, setDrafts] = useState<Record<string, string>>(() =>
    Object.fromEntries(items.map((item) => [item.id, item.name])),
  );
	const [saving, setSaving] = useState("");
	const [error, setError] = useState("");
	useEffect(() => {
    setDrafts(Object.fromEntries(items.map((item) => [item.id, item.name])));
  }, [items]);
  const create = async (event: FormEvent) => {
    event.preventDefault();
    setSaving("create");
    setError("");
    try {
      await api.saveStageType({ name: newName });
      setNewName("");
      await onChanged("节点类型已添加");
    } catch (reason) {
      setError(messageOf(reason));
    } finally {
      setSaving("");
    }
  };
  const save = async (item: StageTypeDefinition) => {
    if (item.system) return;
    setSaving(item.id);
    setError("");
    try {
      await api.saveStageType({ id: item.id, name: drafts[item.id] || "" });
      await onChanged("自定义类型已更新");
    } catch (reason) {
      setError(messageOf(reason));
    } finally {
      setSaving("");
    }
  };
  const remove = (item: StageTypeDefinition) => {
    if (item.system) return;
    onRequestProtectedAction({
      title: "删除自定义节点类型",
      subject: item.name,
      description:
        "该类型会从流程配置中移除。已被流程节点使用的类型不能删除，请先调整相关节点。",
      confirmationText: item.name,
      confirmLabel: "确认删除类型",
      action: async () => {
        setSaving(item.id);
        setError("");
        try {
          await api.deleteStageType(item.id);
          await onChanged("自定义类型已删除");
        } finally {
          setSaving("");
        }
      },
    });
  };
  return (
    <Dialog
      title="管理节点类型"
      subtitle="系统类型用于统计且不可修改；自定义类型仅用于记录流程。"
      kicker="流程配置"
      onClose={onClose}
    >
      <div className="stage-type-manager">
        <form className="stage-type-create" onSubmit={create}>
          <input
            required
            value={newName}
            onChange={(event) => setNewName(event.target.value)}
            placeholder="新增类型，例如：群面"
          />
          <button
            className="primary-button"
            disabled={saving === "create"}
            type="submit"
          >
            <Plus size={15} />
            {saving === "create" ? "添加中" : "添加"}
          </button>
        </form>
        <div className="stage-type-list">
          {items.map((item) => (
            <div
              className={`stage-type-row ${item.system ? "system" : "custom"}`}
              key={item.id}
            >
              <input
                disabled={item.system}
                value={drafts[item.id] ?? item.name}
                onChange={(event) =>
                  setDrafts({ ...drafts, [item.id]: event.target.value })
                }
              />
              <span
                className={`stage-type-kind ${item.system ? "system" : "custom"}`}
              >
                {item.system ? "系统类型 · 统计" : "自定义类型"}
              </span>
              {!item.system && (
                <>
                  <button
                    className="icon-button small"
                    disabled={
                      saving === item.id ||
                      (drafts[item.id] ?? item.name).trim() === item.name
                    }
                    title={`保存 ${item.name}`}
                    onClick={() => void save(item)}
                  >
                    <CheckCircle2 size={14} />
                  </button>
                  <button
                    className="icon-button small danger-button"
                    disabled={saving === item.id}
                    title={`删除 ${item.name}`}
                    onClick={() => void remove(item)}
                  >
                    <Trash2 size={14} />
                  </button>
                </>
              )}
            </div>
          ))}
        </div>
        <FormError value={error} />
        <div className="dialog-buttons">
          <button type="button" className="ghost-button" onClick={onClose}>
            完成
          </button>
        </div>
      </div>
    </Dialog>
  );
}

function syncStateLabel(status?: CloudSyncStatus | null) {
  if (status?.activity === "checking") return "检查云端";
  if (status?.activity === "downloading") return "正在恢复";
  if (status?.activity === "uploading") return "正在上传";
  if (cloudSyncRecentlyCompleted(status)) return "刚刚完成";
  switch (status?.state) {
    case "pending": return "待同步";
    case "syncing": return "同步中";
    case "synced": return "已同步";
    case "failed": return "同步失败";
    case "conflict": return "需要处理冲突";
    case "pending_confirmation": return "等待确认";
    default: return "仅本机";
  }
}

function cloudSyncCompactLabel(status: CloudSyncStatus | null) {
  if (status?.activity === "checking") return "正在检查云端";
  if (status?.activity === "downloading") return "正在恢复云端数据";
  if (status?.activity === "uploading") return "正在上传本机数据";
  if (cloudSyncRecentlyCompleted(status)) return "刚刚完成云端同步";
  switch (status?.state) {
    case "synced":
      return "云端已同步";
    case "pending":
      return `待同步 ${Math.max(1, status.pendingChanges)} 项`;
    case "syncing":
      return "正在同步";
    case "failed":
      return "云同步异常";
    case "conflict":
      return `需处理冲突 ${Math.max(1, status.conflictCount)} 项`;
    case "pending_confirmation":
      return "等待同步确认";
    default:
      return "云同步未开启";
  }
}

function cloudSyncProgressDetail(status: CloudSyncStatus | null) {
  if (!status) return "正在读取状态";
  if (status.retryAttempt > 0) return `请求暂时失败，${status.retryAfter} 秒后重试`;
  if (status.activity === "downloading") {
    return syncTransferProgress(status, "正在恢复云端数据");
  }
  if (status.activity === "uploading") {
    return syncTransferProgress(status, "正在上传本机数据");
  }
  if (status.activity === "syncing") {
    if (status.queuedChanges > 0) return `下一批待上传 ${status.queuedChanges} 条`;
    return status.activeChanges > 0 ? `本次剩余 ${status.activeChanges} 条` : "正在完成本次同步";
  }
  if (status.activity === "checking") {
    return status.queuedChanges > 0 ? `检查完成后上传 ${status.queuedChanges} 条` : "正在核对其他设备的更新";
  }
  if (cloudSyncRecentlyCompleted(status)) return "刚刚完成云端检查，本机数据已保持最新";
  if (status.state === "pending") return `本轮约 10 秒后上传 ${Math.max(1, status.pendingChanges)} 条`;
  if (status.state === "failed") return "本地已保存，等待处理";
  if (status.state === "conflict") return `需处理 ${Math.max(1, status.conflictCount)} 项冲突`;
  if (status.state === "synced") return "已核对云端，本机镜像可恢复";
  return "可在此连接 Gitee 云同步";
}

// Periodic checks can finish before the next status poll. Keep a short-lived
// completion label so a successful fast check is still visible to the user.
function cloudSyncRecentlyCompleted(status: CloudSyncStatus | null | undefined) {
  if (!status || status.state !== "synced" || status.activity || !status.lastSuccessAt) return false;
  const completedAt = Date.parse(status.lastSuccessAt);
  if (!Number.isFinite(completedAt)) return false;
  const elapsed = Date.now() - completedAt;
  return elapsed >= 0 && elapsed < 4_000;
}

function syncTransferProgress(status: CloudSyncStatus, fallback: string) {
  if (status.progressTotal > 0) {
    const files = status.filesTotal > 0 ? `，附件与简历已完成 ${status.filesDone} 份` : "";
    return `${status.progressDone}/${status.progressTotal} 条${files}`;
  }
  return fallback;
}

function SyncSummaryColumns({ local, cloud }: { local: import("./api").SyncDataSummary; cloud: import("./api").SyncDataSummary }) {
  const items: Array<[keyof import("./api").SyncDataSummary, string]> = [
    ["positions", "岗位"], ["applications", "投递"], ["stages", "流程节点"], ["attachments", "附件"], ["resumes", "简历"],
  ];
  return <div className="gitee-sync-summary">
    <div><strong>本机</strong>{items.map(([key, label]) => <span key={key}>{label} <b>{local[key]}</b></span>)}</div>
    <div><strong>云端</strong>{items.map(([key, label]) => <span key={key}>{label} <b>{cloud[key]}</b></span>)}</div>
  </div>;
}

function BackupCenterDialog({
  onClose,
  onCloudDataChanged,
  onRequestProtectedAction,
  onRestored,
}: {
  onClose: () => void;
  onCloudDataChanged: () => void;
  onRequestProtectedAction: (action: ProtectedAction) => void;
  onRestored: (result: {
    restoredBackup: BackupRecord;
    safetyBackup: BackupRecord;
  }) => Promise<void> | void;
}) {
  const [center, setCenter] = useState<BackupCenter | null>(null);
  const [selected, setSelected] = useState<BackupRecord | null>(null);
  const [confirmation, setConfirmation] = useState("");
	const [saving, setSaving] = useState("");
	const [error, setError] = useState("");
	const [notice, setNotice] = useState("");
	const [giteeToken, setGiteeToken] = useState("");
	const [connectionPreview, setConnectionPreview] =
	  useState<GiteeConnectionPreview | null>(null);
	const [initialSyncConfirming, setInitialSyncConfirming] = useState(false);
	const initialSyncConfirmingRef = useRef(false);
	const [conflicts, setConflicts] = useState<SyncConflict[]>([]);
	const [resumePreviewLoading, setResumePreviewLoading] = useState(false);
	const [resumePreviewError, setResumePreviewError] = useState("");
	const [resumePreviewRequest, setResumePreviewRequest] = useState(0);
	const [deleteCloudConfirming, setDeleteCloudConfirming] = useState(false);
  const loadCenter = async () => {
    try {
      setCenter(await api.backupCenter());
    } catch (reason) {
      setError(messageOf(reason));
    }
  };
	useEffect(() => {
		void loadCenter();
	}, []);
	useEffect(() => {
		if (initialSyncConfirming || center?.cloudSync.state !== "pending_confirmation" || connectionPreview) return;
		let active = true;
		setResumePreviewLoading(true);
		setResumePreviewError("");
		void api.pendingGiteeConnectionPreview().then((preview) => {
			if (active) setConnectionPreview(preview);
		}).catch((reason) => {
			if (active) setResumePreviewError(messageOf(reason));
		}).finally(() => {
			if (active) setResumePreviewLoading(false);
		});
		return () => {
			active = false;
		};
	}, [center?.cloudSync.state, connectionPreview, initialSyncConfirming, resumePreviewRequest]);
	useEffect(() => {
		// The backend may already have accepted confirmation while this dialog is
		// still rendering its cached preview. Never leave a stale second confirm
		// button visible after that durable state changes.
		if (!initialSyncConfirming && connectionPreview && center?.cloudSync.state !== "pending_confirmation") {
			setConnectionPreview(null);
		}
	}, [center?.cloudSync.state, connectionPreview, initialSyncConfirming]);
	useEffect(() => {
		let active = true;
		const refreshCloudState = () => {
			void api.cloudSyncStatus().then((cloudSync) => {
				if (!active) return;
				setCenter((current) => current ? { ...current, cloudSync } : current);
			}).catch(() => undefined);
		};
		refreshCloudState();
		const timer = window.setInterval(refreshCloudState, 1_000);
		return () => {
			active = false;
			window.clearInterval(timer);
		};
	}, []);
	useEffect(() => {
		if (!center?.cloudSync || center.cloudSync.conflictCount === 0) {
			setConflicts([]);
			return;
		}
		void api.syncConflicts().then(setConflicts).catch((reason) => setError(messageOf(reason)));
	}, [center?.cloudSync.conflictCount]);
  const openLocation = async (kind: "data" | "backups" | "mirror") => {
    try {
      await api.openBackupLocation(kind);
    } catch (reason) {
      setError(messageOf(reason));
    }
  };
	const createBackup = async () => {
		setSaving("create");
		setError("");
		setNotice("");
    try {
      await api.backup();
      await loadCenter();
    } catch (reason) {
      setError(messageOf(reason));
    } finally {
      setSaving("");
    }
  };
	const connectGitee = async () => {
		setSaving("connect-gitee");
		setError("");
		setNotice("");
		try {
			setConnectionPreview(await api.connectGitee(giteeToken));
			setGiteeToken("");
			await loadCenter();
		} catch (reason) {
			setError(messageOf(reason));
		} finally {
			setSaving("");
		}
	};
	const confirmGitee = async (mode: "upload" | "download" | "merge") => {
		if (initialSyncConfirmingRef.current) return;
		initialSyncConfirmingRef.current = true;
		setInitialSyncConfirming(true);
		setSaving("confirm-gitee");
		setError("");
		setNotice("");
		setConnectionPreview(null);
		try {
			await api.confirmGiteeConnection(mode);
			await loadCenter();
			onCloudDataChanged();
		} catch (reason) {
			setError(messageOf(reason));
			await loadCenter();
		} finally {
			initialSyncConfirmingRef.current = false;
			setInitialSyncConfirming(false);
			setSaving("");
		}
	};
	const syncNow = async () => {
		setSaving("sync-now");
		setError("");
		setNotice("");
		try {
			await api.syncGiteeNow();
			await loadCenter();
			onCloudDataChanged();
		} catch (reason) {
			setError(messageOf(reason));
			await loadCenter();
		} finally {
			setSaving("");
		}
	};
	const disconnectGitee = () => {
    onRequestProtectedAction({
      title: "断开 Gitee 云同步",
      subject: "本机的 Gitee 连接",
      description:
        "只会移除本机保存的令牌和同步配置。本地岗位、投递、附件、备份以及 Gitee 云端仓库都不会被删除。",
      confirmationText: "断开 Gitee",
      confirmLabel: "确认断开连接",
      tone: "caution",
      action: async () => {
        setSaving("disconnect-gitee");
        setError("");
        setNotice("");
        try {
          await api.disconnectGitee();
          setConnectionPreview(null);
          await loadCenter();
        } finally {
          setSaving("");
        }
      },
    });
	};
	const deleteCloudRepositories = async () => {
		if (deleteCloudConfirming) return;
    onRequestProtectedAction({
      title: "删除云端仓库",
      subject: "所有 Offer Atlas Gitee 仓库",
      description:
        "将删除所有带有 Offer Atlas 标识的主仓库和附件仓库。云端历史不可恢复，但本地岗位、附件、备份和令牌不会受到影响。",
      confirmationText: "删除云端仓库",
      confirmLabel: "确认删除云端仓库",
      action: async () => {
        setDeleteCloudConfirming(true);
        setSaving("delete-cloud");
        setError("");
        setNotice("");
        try {
          const deleted = await api.deleteGiteeSyncRepositories();
          setConnectionPreview(null);
          await loadCenter();
          setNotice(`已删除 ${deleted.length} 个 Offer Atlas 云端仓库。本地数据和备份已保留。`);
        } finally {
          setDeleteCloudConfirming(false);
          setSaving("");
        }
      },
    });
	};
	const resolveConflict = async (id: string, choice: "local" | "remote") => {
		setSaving(id);
		setError("");
		setNotice("");
		try {
			await api.resolveSyncConflict(id, choice);
			setConflicts(await api.syncConflicts());
			await loadCenter();
		} catch (reason) {
			setError(messageOf(reason));
		} finally {
			setSaving("");
		}
	};
  const restore = async () => {
    if (!selected) return;
    setSaving(selected.id);
    setError("");
    try {
      const result = await api.restoreBackup(selected.id, confirmation);
      await onRestored(result);
      onClose();
    } catch (reason) {
      setError(messageOf(reason));
      setSaving("");
    }
  };
	const copyBackupName = async (id: string) => {
    try {
      await navigator.clipboard.writeText(id);
    } catch {
      setError("无法复制备份名称，请手动选择并复制");
    }
	};
	const cloudStatus = center?.cloudSync;
	const cloudBusy = !center || initialSyncConfirming || cloudStatus?.activity === "checking" || cloudStatus?.activity === "syncing" || cloudStatus?.activity === "downloading" || cloudStatus?.activity === "uploading" || cloudStatus?.activity === "deleting" || saving === "connect-gitee" || saving === "sync-now" || saving === "disconnect-gitee" || saving === "delete-cloud";
	return (
    <Dialog
      title="数据安全与同步"
      subtitle="本地镜像便于查阅，完整备份可恢复数据；连接 Gitee 后可在多台电脑间同步。"
      kicker="本地数据"
      onClose={onClose}
    >
      <div className="backup-center">
        <section className="backup-overview">
          <article>
            <span>
              <ShieldCheck size={16} />
              自动镜像
            </span>
            <strong>
              {center?.lastSyncedAt
                ? `最近同步 ${textDateTime(center.lastSyncedAt)}`
                : "等待首次保存"}
            </strong>
            <small>
              每日归档 {center?.archivesCount ?? 0} 份，可直接查看投递信息表。
            </small>
            <button
              className="inline-text-button"
              onClick={() => void openLocation("mirror")}
            >
              <FolderOpen size={13} />
              打开镜像目录
            </button>
          </article>
          <article>
            <span>
              <DatabaseBackup size={16} />
              完整备份
            </span>
            <strong>
              {center ? `${center.backups.length} 份可恢复` : "正在读取"}
            </strong>
            <small>恢复前会自动保留当前版本。</small>
            <button
              className="inline-text-button"
              onClick={() => void openLocation("backups")}
            >
              <FolderOpen size={13} />
              打开备份目录
            </button>
          </article>
        </section>
		<section className={`gitee-sync ${cloudStatus?.state || "local_only"} ${cloudBusy ? "busy" : ""}`}>
          <div className="gitee-sync-heading">
			<span className={`gitee-sync-mark ${cloudBusy ? "busy" : ""}`}>
			  {cloudBusy ? <LoaderCircle size={16} /> : <ShieldCheck size={16} />}
			</span>
            <div>
              <strong>Gitee 云同步</strong>
			  <small>{cloudStatus?.message || "正在读取同步状态..."}</small>
            </div>
			<span className={`gitee-sync-state ${cloudBusy ? "busy" : ""}`}>
			  {cloudBusy && <LoaderCircle size={11} />}
			  {syncStateLabel(cloudStatus)}
			</span>
          </div>
		  {cloudStatus?.state === "local_only" ? (
            <div className="gitee-connect-form">
              <div className="gitee-connect-copy">
                <strong>跨电脑使用同一份投递记录</strong>
                <span>在 Gitee 创建名为 <b>Offer Atlas</b>、具备 project 权限的私人令牌。令牌仅以 Windows 账户加密方式保存在本机。</span>
              </div>
              <label>
                Gitee 私人令牌
                <input type="password" autoComplete="off" value={giteeToken} onChange={(event) => setGiteeToken(event.target.value)} placeholder="粘贴私人令牌" />
              </label>
              <button className="secondary-button" disabled={!giteeToken.trim() || saving === "connect-gitee"} onClick={() => void connectGitee()}>
				{saving === "connect-gitee" ? <LoaderCircle className="button-wait-icon" size={15} /> : <ShieldCheck size={15} />}
                {saving === "connect-gitee" ? "正在验证" : "连接 Gitee"}
              </button>
            </div>
		  ) : initialSyncConfirming ? (
			<div className="gitee-first-sync gitee-initial-syncing">
			  <div className="gitee-first-sync-copy">
				<strong>{cloudStatus?.activity === "uploading" ? "正在首次上传本机数据" : "正在恢复云端数据"}</strong>
				<span>{cloudStatus ? syncTransferProgress(cloudStatus, "正在准备首次同步，请保持窗口打开。") : "正在准备首次同步，请保持窗口打开。"}</span>
			  </div>
			  <div className={`gitee-initial-sync-progress ${cloudStatus?.progressTotal ? "determinate" : ""}`} aria-hidden="true"><i style={cloudStatus?.progressTotal ? { width: `${Math.max(6, Math.min(100, cloudStatus.progressDone / cloudStatus.progressTotal * 100))}%`, transform: "none" } : undefined} /></div>
			  {cloudStatus && cloudStatus.retryAttempt > 0 && <div className="gitee-retry-notice"><LoaderCircle size={12} />请求暂时失败，将在 {cloudStatus.retryAfter} 秒后重试（{cloudStatus.retryAttempt}/{Math.max(1, cloudStatus.retryMax - 1)}）<small>{cloudStatus.retryError}</small></div>}
			</div>
		  ) : connectionPreview ? (
            <div className="gitee-first-sync">
              <div className="gitee-first-sync-copy">
                <strong>已验证账号 {connectionPreview.account}</strong>
                <span>将使用私有仓库 {connectionPreview.primaryRepo}。首次下载或合并前会自动创建本地完整备份。</span>
              </div>
              <SyncSummaryColumns local={connectionPreview.local} cloud={connectionPreview.cloud} />
              <div className="gitee-first-sync-actions">
                <span>{connectionPreview.recommended === "upload" ? "云端为空，请确认上传本机数据。" : connectionPreview.recommended === "download" ? "本机为空，请确认下载云端数据。" : "两端均有数据，请确认合并；同一对象的版本分叉会保留为冲突。"}</span>
                <button className="secondary-button" disabled={saving === "confirm-gitee"} onClick={() => void confirmGitee(connectionPreview.recommended)}>
				  {saving === "confirm-gitee" && <LoaderCircle className="button-wait-icon" size={15} />}
                  {saving === "confirm-gitee" ? "正在初始化" : connectionPreview.recommended === "upload" ? "确认上传本机数据" : connectionPreview.recommended === "download" ? "确认下载云端数据" : "确认合并两端数据"}
                </button>
              </div>
            </div>
		  ) : center?.cloudSync.state === "pending_confirmation" ? (
		    <div className="gitee-confirmation-recovery">
		      <strong>首次同步等待确认</strong>
		      <span>{resumePreviewLoading ? "正在读取本机与云端摘要，完成后可选择上传、下载或合并。" : "请查看两端摘要后选择上传、下载或合并；本机数据不会被自动覆盖。"}</span>
		      {resumePreviewError && <p>{resumePreviewError}</p>}
		      <div>
		        <button className="secondary-button" disabled={resumePreviewLoading} onClick={() => setResumePreviewRequest((value) => value + 1)}>
				  {resumePreviewLoading && <LoaderCircle className="button-wait-icon" size={15} />}
		          {resumePreviewLoading ? "读取中" : "重新读取同步摘要"}
		        </button>
		        <button className="ghost-button danger-text-button" disabled={saving === "disconnect-gitee"} onClick={() => void disconnectGitee()}>
				  {saving === "disconnect-gitee" && <LoaderCircle className="button-wait-icon" size={14} />}
		          {saving === "disconnect-gitee" ? "断开中" : "重新连接 Gitee"}
		        </button>
		      </div>
		    </div>
		  ) : (
            <div className="gitee-connected">
              <div className="gitee-connected-meta">
                <span>账号 {center?.cloudSync.owner || "-"}</span>
                <span>仓库 {center?.cloudSync.primaryRepo || "-"}</span>
                <span>本机 {center?.cloudSync.deviceName || "-"}</span>
				{cloudStatus?.lastSuccessAt && <span>最近成功 {textDateTime(cloudStatus.lastSuccessAt)}</span>}
				{cloudStatus?.activity === "uploading" && <span className="gitee-active-count gitee-live-activity"><LoaderCircle size={12} />{syncTransferProgress(cloudStatus, "正在上传")}</span>}
				{cloudStatus?.activity === "downloading" && <span className="gitee-active-count gitee-live-activity"><LoaderCircle size={12} />{syncTransferProgress(cloudStatus, "正在恢复云端数据")}</span>}
				{cloudStatus?.activity === "syncing" && <span className="gitee-active-count gitee-live-activity"><LoaderCircle size={12} />本次同步 {cloudStatus.activeChanges} 条</span>}
				{cloudStatus?.activity === "checking" && <span className="gitee-active-count gitee-live-activity"><LoaderCircle size={12} />正在检查云端更新</span>}
				{cloudStatus?.activity === "deleting" && <span className="gitee-active-count gitee-live-activity"><LoaderCircle size={12} />正在删除云端仓库（{cloudStatus.progressDone}/{cloudStatus.progressTotal}）</span>}
				{(cloudStatus?.queuedChanges || 0) > 0 && <span className="gitee-pending-count">下一批待上传 {cloudStatus?.queuedChanges} 条</span>}
				{cloudStatus?.state === "pending" && (cloudStatus.pendingChanges || 0) > 0 && <span className="gitee-pending-count">本轮约 10 秒后上传 {cloudStatus.pendingChanges} 条</span>}
			  </div>
			  {cloudStatus && cloudStatus.retryAttempt > 0 && <div className="gitee-retry-notice"><LoaderCircle size={12} />请求暂时失败，将在 {cloudStatus.retryAfter} 秒后重试（{cloudStatus.retryAttempt}/{Math.max(1, cloudStatus.retryMax - 1)}）<small>{cloudStatus.retryError}</small></div>}
			  <div className="gitee-connected-actions">
				<div className="gitee-primary-actions">
				<button className="secondary-button" disabled={saving === "sync-now" || !cloudStatus?.canSync} onClick={() => void syncNow()}>
				  {saving === "sync-now" ? <LoaderCircle className="button-wait-icon" size={15} /> : <RotateCcw size={15} />}
                  {saving === "sync-now" ? "同步中" : "立即同步"}
                </button>
				</div>
				<div className="gitee-danger-actions" aria-label="危险操作">
				<button className="ghost-button disconnect-gitee-button" disabled={saving === "disconnect-gitee"} onClick={() => void disconnectGitee()}>
				  {saving === "disconnect-gitee" && <LoaderCircle className="button-wait-icon" size={14} />}
                  {saving === "disconnect-gitee" ? "断开中" : "断开 Gitee"}
				</button>
				<button className="ghost-button delete-cloud-button" disabled={cloudBusy || deleteCloudConfirming} onClick={() => void deleteCloudRepositories()}>
					{deleteCloudConfirming && <LoaderCircle className="button-wait-icon" size={14} />}
					{deleteCloudConfirming ? "删除中" : "删除云端仓库"}
				</button>
				<small>删除仅影响云端仓库，不会删除本机数据</small>
				</div>
              </div>
            </div>
          )}
		  {cloudStatus?.state !== "local_only" && !connectionPreview && (
            <p className="gitee-sync-rules">启动应用、每次成功同步后 30 秒及手动同步时都会先检查云端；首次修改后约 10 秒同步，窗口内后续修改会并入本轮。同步期间的新修改进入下一批，且始终先拉取、再推送。</p>
          )}
		  {cloudStatus?.state === "failed" && <div className="gitee-failure-notice"><strong>本次云同步未完成</strong><span>{cloudStatus.message}</span><small>本地数据和已恢复的数据均已保存。检查网络或 Gitee 状态后可再次同步，系统会从已完成的位置继续。</small></div>}
          {conflicts.length > 0 && (
            <div className="gitee-conflicts">
              <strong>需要处理的同步冲突</strong>
              {conflicts.map((conflict) => (
                <article key={conflict.id}>
                  <div><span>本机 · {textDateTime(conflict.localUpdatedAt)}</span><strong>{conflict.localDescription}</strong></div>
                  <div><span>云端 · {textDateTime(conflict.remoteUpdatedAt)}</span><strong>{conflict.remoteDescription}</strong></div>
                  <div className="gitee-conflict-actions">
                    <button className="secondary-button" disabled={saving === conflict.id} onClick={() => void resolveConflict(conflict.id, "local")}>保留本机</button>
                    <button className="ghost-button" disabled={saving === conflict.id} onClick={() => void resolveConflict(conflict.id, "remote")}>使用云端</button>
                  </div>
                </article>
              ))}
            </div>
          )}
        </section>
        <div className="backup-actions">
          <div>
            <strong>完整备份</strong>
            <span>需要在大批量修改、清理数据前手动创建一份。</span>
          </div>
		  <button
            className="secondary-button"
            disabled={saving === "create"}
            onClick={() => void createBackup()}
          >
			{saving === "create" ? <LoaderCircle className="button-wait-icon" size={16} /> : <DatabaseBackup size={16} />}
            {saving === "create" ? "创建中" : "立即备份"}
          </button>
        </div>
        <section className="backup-list">
          <div className="backup-list-heading">
            <strong>可恢复备份</strong>
            <button
              className="icon-button small"
              title="打开数据目录"
              onClick={() => void openLocation("data")}
            >
              <FolderOpen size={14} />
            </button>
          </div>
          {center ? (
            center.backups.length ? (
              center.backups.map((item) => (
                <article
                  className={`backup-row ${selected?.id === item.id ? "selected" : ""}`}
                  key={item.id}
                >
                  <button
                    type="button"
                    onClick={() => {
                      setSelected(item);
                      setConfirmation("");
                    }}
                  >
                    <strong>{textDateTime(item.createdAt)}</strong>
                    <span>备份名称：{item.id}</span>
                    <em>
                      数据库 {backupSize(item.databaseSize)} · 附件{" "}
                      {item.attachmentCount} 个 /{" "}
                      {backupSize(item.attachmentSize)}
                    </em>
                  </button>
                  <button
                    className="icon-button small"
                    title="复制备份名称"
                    aria-label={`复制备份名称 ${item.id}`}
                    onClick={() => void copyBackupName(item.id)}
                  >
                    <Copy size={14} />
                  </button>
                  <button
                    className="icon-button small"
                    title="选择此备份恢复"
                    onClick={() => {
                      setSelected(item);
                      setConfirmation("");
                    }}
                  >
                    <RotateCcw size={14} />
                  </button>
                </article>
              ))
            ) : (
              <div className="backup-empty">
                暂无完整备份。自动镜像已持续保护数据，建议在开始正式投递前创建第一份完整备份。
              </div>
            )
          ) : (
            <div className="backup-empty">正在读取备份信息...</div>
          )}
        </section>
        {selected && (
          <section className="restore-confirm">
            <div>
              <strong>恢复至 {textDateTime(selected.createdAt)}</strong>
              <span>
                备份名称：{selected.id}
                <br />
                会先创建当前版本的完整备份，再恢复该备份中的数据库与附件。
              </span>
            </div>
            <label>
              输入备份名称确认
              <input
                value={confirmation}
                onChange={(event) => setConfirmation(event.target.value)}
                placeholder={selected.id}
              />
            </label>
            <button
              className="danger-primary-button"
              disabled={confirmation !== selected.id || saving === selected.id}
              onClick={() => void restore()}
            >
			  {saving === selected.id ? <LoaderCircle className="button-wait-icon" size={16} /> : <RotateCcw size={16} />}
              {saving === selected.id ? "恢复中" : "确认恢复"}
            </button>
          </section>
        )}
		<FormError value={error} />
		{notice && <p className="form-success">{notice}</p>}
        <div className="dialog-buttons">
          <button className="ghost-button" onClick={onClose}>
            完成
          </button>
        </div>
      </div>
    </Dialog>
  );
}

function DeleteDialog({
  target,
  onClose,
  onDeleted,
}: {
  target: DeletionTarget;
  onClose: () => void;
  onDeleted: () => void;
}) {
  const [preview, setPreview] = useState<DeletionPreview | null>(null);
  const [confirmation, setConfirmation] = useState("");
  const [loading, setLoading] = useState(true);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    void api
      .previewDeletion(target.entityType, target.id)
      .then((result) => {
        if (active) setPreview(result);
      })
      .catch((reason) => {
        if (active) setError(messageOf(reason));
      })
      .finally(() => {
        if (active) setLoading(false);
      });
    return () => {
      active = false;
    };
  }, [target.entityType, target.id]);
  const submit = async (event: FormEvent) => {
    event.preventDefault();
    if (!preview) return;
    setSaving(true);
    try {
      await api.deleteEntity({
        entityType: target.entityType,
        id: target.id,
        confirmationText: confirmation,
      });
      onDeleted();
    } catch (reason) {
      setError(messageOf(reason));
      setSaving(false);
    }
  };
  const impact = preview
    ? [
        preview.campaignCount ? `${preview.campaignCount} 个招聘批次` : "",
        preview.positionCount ? `${preview.positionCount} 个岗位` : "",
        preview.applicationCount
          ? `${preview.applicationCount} 条投递记录`
          : "",
        preview.stageCount ? `${preview.stageCount} 个流程节点` : "",
      ].filter(Boolean)
    : [];
  return (
    <Dialog
      title={`删除${deletionLabels[target.entityType]}`}
      subtitle="此操作会同时删除其下的关联数据，且无法在应用内撤销。"
      kicker="受保护删除"
      onClose={onClose}
    >
      <form className="form-grid delete-form" onSubmit={submit}>
        <section className="delete-summary">
          <span>即将删除</span>
          <strong>{preview?.entityName || target.name}</strong>
          {loading ? (
            <p>正在核对关联数据...</p>
          ) : (
            <p>
              {impact.length
                ? `还将删除：${impact.join("、")}`
                : "该记录下没有其他关联数据。"}
            </p>
          )}
        </section>
        <Field
          wide
          label={`输入“${preview?.confirmationText || target.name}”以确认`}
        >
          <input
            autoFocus
            value={confirmation}
            onChange={(event) => setConfirmation(event.target.value)}
          />
        </Field>
        <FormError value={error} />
        <div className="dialog-buttons">
          <button type="button" className="ghost-button" onClick={onClose}>
            取消
          </button>
          <button
            className="danger-primary-button"
            disabled={
              loading || saving || confirmation !== preview?.confirmationText
            }
            type="submit"
          >
            <Trash2 size={16} />
            {saving ? "删除中" : "确认删除"}
          </button>
        </div>
      </form>
    </Dialog>
  );
}
