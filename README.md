# OfferAtlas

OfferAtlas 是一个本地优先的 Windows 秋招投递记录工具。它把“我在关注的岗位”和“我已经投递后的实际流程”分开管理，避免在一开始就为每家公司预设不可靠的面试轮数。

## 产品概览

[查看产品架构、状态规则与流程节点说明](docs/product-overview-zh.html)。这是一份独立的静态 HTML，不依赖应用运行环境、截图或网络，双击即可在普通浏览器打开。

## 使用教程

建议首次使用时先阅读下面两份教程。它们面向实际使用者，按应用中的真实功能说明操作步骤和注意事项：

- [OfferAtlas 快速上手](docs/offer-atlas-quick-start.md)：从录入公司、招聘批次和岗位开始，介绍投递记录、流程节点、日历、待办、简历库以及数据备份等核心功能。
- [Gitee 云同步使用指南](docs/cloud-sync-guide-zh.md)：介绍如何连接 Gitee、完成首次上传或下载、在多台电脑间同步，以及查看同步状态、处理失败和冲突。

如果只想先了解日常使用流程，请从“快速上手”开始；准备在多台电脑之间使用时，再阅读“Gitee 云同步使用指南”。

## 数据安全

SQLite 是应用的主数据库，启用了迁移、外键校验和 WAL 日志。数据默认保存在：

```text
%AppData%\OfferAtlas\offer-atlas.db
```

每次成功保存公司、招聘批次、岗位、投递记录或流程节点后，应用都会尝试在同一目录更新最新安全镜像，并在当天第一次有数据变化时建立一份日归档：

```text
%AppData%\OfferAtlas\safety-mirror\
  LATEST.txt
  journal.jsonl
  latest\
    投递信息表.xlsx
  history\YYYY-MM-DD\
    投递信息表.xlsx
```

- `投递信息表.xlsx` 可以直接用 Excel、WPS 或 LibreOffice 打开，包含“投递记录”和“待投递岗位”两个工作表；表头固定、支持筛选，并以统一颜色标识优先级和流程状态。
- `latest` 始终是最新数据，应用通过临时目录和原子替换更新它；镜像目录不复制附件，只保留便于查阅的工作簿。
- `history` 每天最多生成一份完整归档，默认保留最近 30 天；`journal.jsonl` 仍记录每次镜像操作，但不会为每次操作复制完整数据文件。
- 应用启动时也会生成一次镜像。若镜像失败，界面会显示“安全镜像需要检查”，主数据库仍可继续使用。
- 侧栏中的“备份 SQLite”可额外创建可直接用于完整恢复的数据库副本，保存在 `%AppData%\OfferAtlas\backups\`。

Excel 镜像能在数据库损坏时尽量保留可读、可整理的投递信息；岗位附件仅由完整备份保存。它们都不能防御整块磁盘损坏、勒索软件或系统级误删。对特别重要的数据，定期把整个 `%AppData%\OfferAtlas\` 目录复制到 OneDrive、移动硬盘或其他独立位置。

## Gitee 多设备同步（Windows V1）

云同步默认关闭，单机模式不受影响。请在应用的“数据安全与备份”中主动连接：

1. 在 Gitee 的私人令牌页面创建名为 `Offer Atlas`、具备 `project` 权限的令牌。
2. 将令牌粘贴到应用的“Gitee 云同步”区域。令牌仅使用当前 Windows 账户的 DPAPI 加密保存在 `%AppData%\OfferAtlas\gitee-token.dpapi`，不会写入数据库、Excel 镜像、完整备份或远端仓库。
3. 应用会找到已有的 Offer Atlas 私有同步仓库，或创建 `offer-atlas-sync`。首次接入会展示本机与云端摘要：云端为空时确认上传，本机为空时确认下载，两端都有数据时确认合并；下载和合并前自动创建本地完整备份。

同步上传业务对象、岗位附件和投递简历，但不会上传 SQLite、Excel 镜像或完整备份。主仓库保存按设备分开的不可变更操作记录；附件按 SHA-256 去重，保存到 `offer-atlas-media-001` 起的私有媒体仓库。单个媒体仓库接近 400 MiB 时会自动创建下一个仓库，避免触及 Gitee 单仓库限制。

每次本地修改会立即保存；第一次修改会开启约 10 秒的同步窗口，窗口内的修改会合并进本轮同步。启动、每次成功同步后的 30 秒检查及“立即同步”都会先拉取云端更新。两个计时器不会并发：同步期间的新修改会进入下一批，并在本次成功后重新等待 10 秒。网络临时失败会按 10、30、90 秒重试，失败后会明确提示“本地数据已保存，云同步未完成”。两台设备从相同旧版本修改同一对象，或删除与修改相撞时，应用保留两边数据并要求选择“保留本机”或“使用云端”。断开 Gitee 只删除本机令牌和同步配置，不删除本地数据、备份或远端仓库。

## 开发

环境要求：Go 1.25+、Node.js 24+、Wails v2。

```powershell
go install github.com/wailsapp/wails/v2/cmd/wails@latest
cd frontend
npm install
cd ..
wails dev
```

## 验证与打包

```powershell
cd D:\Project\Go_Project\offer-atlas
go test ./...
go vet ./...
wails build -platform windows/amd64
```

Windows 可执行文件输出到 `build\bin\OfferAtlas.exe`。
