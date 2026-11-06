# WakeHub v0.2.0 验收报告（M8）

- **验收日期**：2026-11-06 凌晨（M8 实施，交付截止 06:30）
- **验收基线**：main 单 commit `f7e3df4` 之上的未提交工作区（M1–M7 + M8 补项）
- **验收方式**：全量测试（`go test` / `-race` / `go vet` / `gofmt` / 前端 vitest + vue-tsc + build）+ 真实二进制端到端冒烟（全新库、001 旧库升级、真 Agent WS 连接、reset-password CLI）+ 43 端点逐项核对 + 审计 28 项逐项销账 + README/install.sh/CHANGELOG 逐行复核

## 验收结论：有条件通过

**本机能验证的全部通过。** Go 后端 142 个顶层测试 0 失败（含 `-race` 全量），前端 22 个测试 + 类型检查 + 构建全部通过，二进制冒烟 74 项断言 0 失败，43 个契约端点全部注册且全部有测试或冒烟覆盖，审计 28 项新发现全部销账（25 已修复并有测试、1 部分修复、2 仅代码未实测）。

**“有条件”是因为以下 7 类验证本机沙箱做不了，必须到用户 VM / 真实局域网 / 真机上做**（清单见 §7，`docs/deployment.md` 末尾也有同一份清单）：Docker 容器构建与运行、真实网段扫描与多设备去重、真实 WOL 冷启动回检、多 Agent 跨网段、ARM64 构建、PWA 真机安装、移动端目视。**在这些验证完成前，不要把 v0.2.0 当作“已在生产验证”的版本发布**，先在 VM 上按 §7 逐项打勾。

## 1. 测试结果汇总

| 检查 | 命令 | 结果 | 耗时 |
|---|---|---|---|
| Go 全量测试 | `go test ./... -count=1` | 12 个包全部 ok，**142 个顶层测试，0 失败** | 约 37s |
| Go 全量 race | `go test -race ./... -count=1` | 12 个包全部 ok，无数据竞态 | 约 254s |
| 修复后 race（受影响包） | `go test -race ./internal/api/... ./internal/device/... ./internal/config/... -count=1` | 3 个包全部 ok | 约 161s |
| vet | `go vet ./...` | 通过 | — |
| gofmt | `gofmt -l internal/ cmd/` | 干净（无输出） | — |
| 前端测试 | `npm test`（vitest） | **22/22 通过**（client/validate/cron 三个文件） | 约 2.5s |
| 前端类型检查 | `npm run typecheck`（vue-tsc --noEmit） | 干净 | — |
| 前端构建 | `npm run build` | 通过，路由级懒加载分包 | 约 8s |
| 二进制冒烟 | `bash /tmp/m8-smoke/run.sh`（脚本见 §2） | **74 项断言，0 失败** | 约 12s |
| shell 语法 | `bash -n install.sh scripts/backup.sh` | 通过 | — |
| compose 校验 | python3 `yaml.safe_load`（M7；沙箱无 docker CLI，`docker compose config` 未跑） | 通过（仅 YAML 语法） | — |

> 说明：race 全量是在 M8 两个修复之前跑的；修复后对受影响的 api/device/config 三个包重跑了 race（全部通过），并重跑了非 race 全量（全部通过）。M8 修复只动了 `internal/api/router.go`、`internal/device/repository.go`、`internal/device/validate.go`、`internal/config/config.go`、`cmd/agent/main.go` 五个文件，未动其他包。

## 2. 二进制端到端冒烟（74 项断言）

用 `go build` 出的真实 `wakehub-server` / `wakehub-agent` 二进制跑通（脚本 `/tmp/m8-smoke/run.sh`，不在交付包内）：

- **启动拒绝**：Agent 无 env / 缺 NAME / 非法 CIDR / `local` 保留值均 fatal（M8 新增，§3）；Server 占位密码 fatal。
- **全新库**：health（`status ok / version 0.2.0 / db ok`）、DB 文件 0600、`schema_migrations` 1–5、种子 `local` 行已删。
- **认证**：未登录 401、错误密码 401、登录成功下发 `Set-Cookie: wakehub_session`（HttpOnly）、响应体无 `token` 字段、`me` 正常；改密后旧会话 401；退出后会话 401；`reset-password` CLI 成功（管道输入两次密码）。
- **Dashboard**：M5 新字段齐全（`pending_agents` / `server_status` / `db_status` / `uptime_seconds` / `version` / `upcoming_tasks`）。
- **设备**：创建/非法 MAC 400/MAC 重复 409/缺 `agent_id` 400/列表分页信封/更新返回重查行（MAC 未丢）/更新不存在 404/删除不存在 404。
- **唤醒**：单设备 `sent` + `boot_status: pending`（不虚报启动）；禁用设备唤醒 400；批量唤醒 `batch_id` + `skipped=3`（禁用/重复/不存在）。
- **检测/睡眠**：检测返回非空 state；`sleep_type=none` 时睡眠 400；畸形 JSON 睡眠 400（不默认执行）。
- **扫描（真 Agent WS 连接）**：预签发 `wha_` token → Agent 二进制真实连上（状态 `connected`）→ `POST /scan` 202 → 轮询至完成（`total=126 found=0 failed=0`，阴性不计 failed）→ 导入 best-effort（`imported=1 skipped=1`，已存在去重）；白名单外网段 400；取消已完成扫描 400；查询不存在扫描 404。
- **任务**：创建（`next_run_at` 已算）/非法 cron 400/`action` 非 wake 400/暂停/重复暂停 400/恢复/runs 空列表 `[]`/删除不存在 404。
- **Agent 管理**：白名单重复 409/新增/删除；吊销后会话断开（状态回 `approved`）；重签发新 token（旧 token 轮换）；禁用/启用；删除后 404。
- **审计**：日志按 `action` + `result` 筛选 + 分页；非法 `result` 400；含 `login_success` 记录。
- **001 旧库升级**：python 建的仅含 001 的库 + 一条 `agent_id='local'` 的遗留设备 → 启动后迁移到 005、种子 `local` 行已删、服务 health 200、遗留设备保留（`agent_id` 原样为 `local`）。
- **SPA**：`/` 与 `/devices` 均 200（fallback 正常）。

两处说明：
- **并发扫描 409**：冒烟里第二次 `POST /scan` 返回 202 而不是 409——因为沙箱内不可达网段的扫描约 10ms 即完成，第二次请求到达时首次扫描已结束。这不是守卫失效：守卫由 `internal/api/m4_test.go:552`（HTTP 级 409，用 fake Agent 使探测耗时 250ms，窗口确定）与 M8 手动复现（2026-11-06 01:40，第二次请求实测 409 且 body 含进行中的 `scan_id`）双重覆盖。
- **限流 429**：冒烟未暴力刷 600 次；通用限流逻辑由 `TestSessionRateLimit`（M5）与登录限流测试 `TestLoginRateLimited` / `TestRateLimitIPAndAccount` / `TestRateLimitKeysCapped` 覆盖。

## 3. M8 新增与修复（验收中发现并已修）

| # | 问题 | 修复 | 测试 |
|---|---|---|---|
| M8-1 | **Agent 未配 ID/名/网段时拒绝启动（契约 §12.1，M7 遗留）**：`LoadAgentConfig` 仍有旧默认值 `agent-lan-01` / `Home LAN Agent` / `192.168.1.0/24`，与 install.sh（`agent-home-lan` 等）不一致 | `internal/config/config.go`：取消三处代码默认值；新增 `AgentConfig.Validate()`（缺项/非法 CIDR/`local` 保留值/格式错误均拒绝，文案写清缺了哪项）；`cmd/agent/main.go` 启动时调用，失败即 `log.Fatalf` | `TestAgentConfigValidate`（10 个子用例）、`TestLoadAgentConfigNoDefaults`；冒烟 4 项断言 |
| M8-2 | **分组重名返回 400 且外泄原始 DB 错误**：`POST /api/v1/groups` 重名时返回 400 + `constraint failed: UNIQUE constraint failed: device_groups.name (2067)`；契约 §11 要求 409，§1.3 要求不外泄 DB 原文 | `internal/device/validate.go` 新增 `ErrGroupNameConflict`；`CreateGroup` 先查后插 + 唯一约束兜底；`handleCreateGroup` 映射为 409 `分组名称已存在`，其他错误不再返回原文 | `TestCreateGroupDuplicateReturns409`；冒烟“分组重名 409”通过 |
| M8-3 | **扫描详情空结果返回 `null`**：`snapshot()` 里 `append([]discoveredDevice(nil), ...)` 在零元素时返回 nil，空结果的扫描详情序列化为 `"results":null`，违反契约 §1.1“空列表永不返回 null” | `snapshot()` 末尾补 nil → `[]` 兜底 | `TestScanSnapshotEmptyResultsAreArray`；冒烟扫描完成断言 |
| M8-4 | **扫描列表摘要含 `"results":null`**：`listSummaries()` 复用 `scanTask` 并把 Results 置 nil，契约 §7.3 要求列表“不含 results” | 新增 `scanTaskSummary` struct（无 Results 字段），`listSummaries()` 返回该类型 | 同上 |
| M8-5 | README 与 install.sh 不一致（文档复核发现） | README 环境表默认值 `agent-lan-01` / `Home LAN Agent` 改为与 install.sh 一致的 `agent-home-lan` / `Home-HostNetwork-Agent`；`make docker-up` 的警告措辞改为与 Makefile 现状一致（无 `.env` 时直接拒绝） | 代码-文档逐行核对 |
| M8-6 | **OUI 条数写错**：代码表实为 27 条，README/CHANGELOG/`docs/security.md` 写 26 条 | 4 处统一改为 27 条（README ×2、CHANGELOG ×2、security.md ×1，含 CHANGELOG 部署节的一处） | 数表逐行计数确认 |

## 4. 43 端点验收矩阵

路由注册（`internal/api/router.go`）与 `docs/api.md` 均为 43 个（api.md：40 个小节 + 3 组并列端点）。覆盖证据分三档：**T**=单元/接口测试、**S**=二进制冒烟、**R**=仅路由注册 + 代码审查（无专用测试，见备注）。

| # | 端点 | 证据 |
|---|---|---|
| 1 | POST /api/v1/auth/login | T `TestLoginSuccessSetsCookie` / `TestLoginWrongPassword` / `TestLoginRateLimited`；S |
| 2 | POST /api/v1/auth/logout | T `TestLogoutRevokesSession`；S |
| 3 | POST /api/v1/auth/change-password | T `TestChangePasswordFlow` / `TestChangePasswordValidation` / `TestChangePasswordInvalidatesOldSession`；S |
| 4 | GET /api/v1/auth/me | T `TestMe`；S |
| 5 | GET /api/v1/health | T `TestHealthEndpoint`；S |
| 6 | GET /api/v1/ws/agent | T `TestAgentManagementAPI` / `TestAuthenticateAgentPaths` / `TestLocalImpersonationRejected`；S（真 Agent 二进制连接） |
| 7 | GET /api/v1/dashboard | T `TestDashboardFieldsAndUpcomingTasks`；S |
| 8 | GET /api/v1/devices | T `TestListDevicesPagination`；S |
| 9 | POST /api/v1/devices | T `TestCreateDeviceValidation` / `TestCreateDeviceDuplicateMACAndOfflineWarning`；S |
| 10 | GET /api/v1/devices/{id} | T `TestGetDeviceByID`（终验补）；S（列表） |
| 11 | PUT /api/v1/devices/{id} | T `TestUpdateDevicePartialAndNotFound`；S |
| 12 | DELETE /api/v1/devices/{id} | T `TestDeleteDeviceNotFound`；S（不存在 404） |
| 13 | POST /api/v1/devices/{id}/wake | T `TestWakeDeviceSuccessAndVerify` / `TestWakeDeviceOfflineAndDisabled`；S |
| 14 | POST /api/v1/devices/batch-wake | T `TestBatchWakeStructure`；S |
| 15 | POST /api/v1/devices/{id}/check | T `TestCheckDeviceOnline` / `TestCheckDeviceBadResultDoesNotWriteDirtyState` / `TestDeviceCheckWritesAudit`；S |
| 16 | POST /api/v1/devices/{id}/sleep | T `TestSleepMalformedJSON` / `TestSleepSuccessViaFakeAgent`；S |
| 17 | GET /api/v1/groups | T `TestListGroupsPaginationEnvelope`；S |
| 18 | POST /api/v1/groups | T `TestCreateGroupDuplicateReturns409`（M8）；S |
| 19 | GET /api/v1/agents | T `TestAgentManagementAPI`；S |
| 20 | POST /api/v1/agents | T `TestAgentManagementAPI` / `TestCreateAgentApprovedWithToken`；S |
| 21 | GET /api/v1/agents/{id} | T `TestAgentManagementAPI`；S |
| 22 | POST /api/v1/agents/{id}/approve | T `TestAgentManagementAPI` / `TestCreateAgentPendingAndApprove` |
| 23 | POST /api/v1/agents/{id}/reject | T `TestAgentManagementAPI` |
| 24 | POST /api/v1/agents/{id}/revoke-token | T `TestAgentManagementAPI` / `TestTokenRevokeIsolation` / `TestRevokeTokenIsolation`；S |
| 25 | POST /api/v1/agents/{id}/reissue-token | T `TestAgentManagementAPI`；S |
| 26 | POST /api/v1/agents/{id}/disable | T `TestAgentManagementAPI`；S |
| 27 | POST /api/v1/agents/{id}/enable | T `TestAgentManagementAPI`；S |
| 28 | DELETE /api/v1/agents/{id} | T `TestAgentManagementAPI`；S |
| 29 | POST /api/v1/agents/{id}/subnets | T `TestAgentManagementAPI` / `TestAllowedSubnets`；S |
| 30 | DELETE /api/v1/agents/{id}/subnets | T `TestAgentManagementAPI`；S |
| 31 | POST /api/v1/scan | T `TestScanValidationErrors` / `TestScanEndToEndWithFakeAgent`；S |
| 32 | GET /api/v1/scan | T `TestScanStoreProtocol`；S（冒烟未单独调列表，复现时调过） |
| 33 | GET /api/v1/scan/{scan_id} | T `TestScanEndToEndWithFakeAgent`；S |
| 34 | DELETE /api/v1/scan/{scan_id} | T `TestScanCancelEndToEnd` / `TestScanCancelHandlers`；S（已完成 400 + 不存在 404） |
| 35 | POST /api/v1/scan/import | T `TestScanImport`；S |
| 36 | GET /api/v1/tasks | T `TestTasksCRUD`；S（冒烟未单独调列表，CRUD 测试覆盖） |
| 37 | POST /api/v1/tasks | T `TestTasksCRUD` / `TestValidateSchedule`；S |
| 38 | PUT /api/v1/tasks/{id} | T `TestTasksCRUD` |
| 39 | DELETE /api/v1/tasks/{id} | T `TestTasksCRUD`；S |
| 40 | POST /api/v1/tasks/{id}/pause | T `TestPauseResumeAndRunOnce`；S |
| 41 | POST /api/v1/tasks/{id}/resume | T `TestPauseResumeAndRunOnce`；S |
| 42 | GET /api/v1/tasks/{id}/runs | T `TestExecuteScheduleSuccessWithLiveAgent`（m4_test.go:477 直接调 handler）；S（空列表 `[]`） |
| 43 | GET /api/v1/logs | T `TestLogsFilterAndPagination`；S |

**缺口**：无——终验补 `TestGetDeviceByID` 后 43 个端点全部有测试或冒烟覆盖。

## 5. 审计 28 项新发现销账表

口径：**已修复**=代码已改 + 有测试（或冒烟）佐证；**部分**=核心已修但有明确残留；**仅代码**=改了代码但无测试覆盖。25 已修复、1 部分、2 仅代码、0 未实现。

### 后端 B-N1～B-N15（15 项：14 已修复、1 仅代码）

| # | 问题 | 状态 | 证据 |
|---|---|---|---|
| B-N1 | 畸形 JSON 默认触发睡眠 | 已修复 | `TestSleepMalformedJSON`；冒烟“畸形 JSON 睡眠 400” |
| B-N2 | 检测解析失败写脏状态 | 已修复 | `TestCheckDeviceBadResultDoesNotWriteDirtyState` |
| B-N3 | 扫描解析错误返回 ok+null | 已修复 | `TestNetworkScanBadResult`（M3 改为异步后更新为 202 + 失败计数） |
| B-N4 | 更新设备返回请求体 | 已修复 | `TestUpdateDevicePartialAndNotFound`；冒烟“更新返回重查行” |
| B-N5 | 更新/删除不存在返回成功 | 已修复 | `TestDeleteDeviceNotFound` / `TestRepositoryUpdateDeleteNotFound` / `TestUpdateDevicePartialAndNotFound`；冒烟 404 组 |
| B-N6 | 用户名枚举时序侧信道 | 已修复 | `TestAuthenticateUnknownUserDummyPath` |
| B-N7 | Agent Token 非恒定时间比对 | 已修复 | `internal/agent/store.go:102` `subtle.ConstantTimeCompare`；`TestAuthenticateAgentPaths` / `TestTokenRevokeIsolation` |
| B-N8 | 远程 Agent 可冒认 `local` | 已修复 | `TestLocalImpersonationRejected`；M8 冒烟 agent `local` 保留值拒绝启动 |
| B-N9 | 调度器启动失败仅记日志 | 仅代码 | `cmd/server/main.go` 改为 `log.Fatalf`（M1）；**无专用测试**（main 包无测试）；冒烟中调度器正常启动，未触发该分支 |
| B-N10 | Ping 参数注入与 locale 依赖 | 已修复 | `TestCheckPingInjection` / `TestCheckInvalidIP` |
| B-N11 | `wol.Send` 端口钳制/广播未校验/睡眠不响应 ctx | 已修复 | `TestSendPortOutOfRange` / `TestSendBroadcastInvalid` / `TestSendContextCancel` |
| B-N12 | Dispatch TOCTOU | 已修复 | `TestConcurrentReconnectKeepsOnlyLiveSession` / `TestOldConnDeferDoesNotDeleteNew`；`-race` 全量通过 |
| B-N13 | Dashboard 全表加载内存计数 | 已修复 | `TestRepositoryDashboardStatsSQLAggregation` |
| B-N14 | 用户名含 `\|` 登录恒失败 | 已修复 | `TestEnsureAdminUsernameValidation`（用户名正则） |
| B-N15 | 认证错误外泄内部原文 | 已修复 | `TestWithAuthDoesNotLeakErrors` |

### 前端 F-N1～F-N8（8 项：7 已修复、1 仅代码未实测）

| # | 问题 | 状态 | 证据 |
|---|---|---|---|
| F-N1 | Token 存 localStorage 可被 XSS 窃取 | 已修复 | `grep localStorage web/src/`：只剩 `wakehub_theme`；`TestLoginSuccessSetsCookie`（Cookie 会话）；前端 client 测试“401 触发未登录回调（不拼 Authorization 头）” |
| F-N2 | 登录页明文默认密码 | 已修复 | 登录页无 `wakehub123` 字样；该字符串只出现在 `validate.ts` 的禁用密码校验（“不得使用默认密码”）及其测试 |
| F-N3 | PWA 离线能力反了、manifest 不可安装 | 已修复 | `sw.js` 含 Cache API（7 处 `caches` 引用）+ manifest 192/512 图标；**可安装性未在真浏览器验证**（见 §7） |
| F-N4 | API 错误处理丢信息 | 已修复 | 前端测试 `it('非 JSON 错误体不抛 SyntaxError（F-N4）')`（`client.test.ts`） |
| F-N5 | 无防重复提交 | 已修复 | 提交按钮 `disabled` 遍布 `DeviceForm` / `TaskForm` / `LoginView` / `DashboardView` 等（代码检查确认；无组件测试） |
| F-N6 | 删除任务无确认 | 已修复 | `TasksView.vue:173` 走 `ui.confirm`（代码检查确认；无组件测试） |
| F-N7 | 搜索是死代码 | 仅代码未实测 | `filteredDevices` 已接 `searchQuery`（代码检查确认）；**无组件测试，搜索行为未在浏览器实测** |
| F-N8 | API 地址 dev 写死、生产无配置 | 已修复 | `vite.config.ts` 支持 `VITE_DEV_API_TARGET`，生产用相对路径 `/api`；`npm run build` 通过 |

### 部署与文档 D-N1～D-N5（5 项：4 已修复、1 部分）

| # | 问题 | 状态 | 证据 |
|---|---|---|---|
| D-N1 | Server 容器内 `local` 节点 ping 默认残废 | 已修复 | `docker-compose.yml` agent 服务 `cap_add: [NET_RAW]`；**容器内未实测**（见 §7） |
| D-N2 | 定时唤醒 WOL 重发次数硬编码 | 已修复 | 调度器改用 `WAKEHUB_WOL_RETRIES`；`TestExecuteScheduleSuccessWithLiveAgent` |
| D-N3 | 默认网段与用户真实局域网不符 | 部分 | install.sh 显式提问网段 + CIDR 校验；`.env.example` 注明 192.168.1.0/24 只是示例不要照抄；**残留：install.sh 的默认值仍为 192.168.1.0/24，用户须按实际局域网修改** |
| D-N4 | README 与 install.sh 提权口径冲突 | 已修复 | README 统一为 `bash install.sh`；M8 复核 `Makefile docker-up` 现有 `.env` 守卫并同步了 README 措辞 |
| D-N5 | Makefile `docker-up` 绕过 install.sh | 已修复 | `docker-up` 加 `.env` 守卫（无 `.env` 直接拒绝）；`build-web` 改 `npm ci`（M7 遗留，父代理补） |

## 6. 任务书 34 项核对（简述）

- **P0 第 1–7 项**：全部已实现并有测试（重连竞态、全局 token、CheckOrigin、默认凭证、Agent 安全通信、Sleep SSRF、设备 Agent 默认分配）。其中第 6 项的“签名认证”按契约 §6.2 明确不做（无对端验签实现，wire 格式已预留），已写入 `docs/security.md` 与 CHANGELOG。
- **P1 第 8–19 项**：全部已实现（批量唤醒、调度器、状态检测、扫描、统一验证、登录限流、goroutine 泄漏、统一校验、API 规范、数据库迁移、审计、Docker 部署）。其中第 11 项的真 ARP 按 M4 评估结论降级（沙箱内 root 也打不开 raw socket，保留 TCP-touch + 读 ARP 缓存并写明边界）；第 19 项的 ARM64 配置已写（buildx `TARGETARCH`），但未在本机实测构建。
- **UI 第 20–30 项**：全部已实现（主题/Design Tokens、左侧导航、移动端、Dashboard、Devices、扫描向导、Agents、Schedules、Logs、Settings）。PWA 可安装性与移动端渲染未在真机目视验证（见 §7）。
- **前端工程 §六**：已实现（Router + Pinia + 统一 API Client + tsconfig + vitest 22 测试）；ESLint 未配（原项目就没有，M6 未新增，免得为配而配）。
- **测试验收第 31–34 项**：第 31、32 项已实现（后端 142 测试 + 前端 22 测试）；第 33 项 Docker 验收部分未完成（本机无 docker CLI、跑不起容器，见 §7）；第 34 项真实网络测试 12 项中仅 1 项曾在 VM 验证（2026-11-05 同局域网唤醒），其余 11 项未验证，其中“无权限用户”一项在单管理员模型下无法验收（已顺延，契约 §13）。

## 7. 本机做不了、须 VM / 真实局域网 / 真机验证的清单

1. **Docker 容器构建与运行**：`docker build`（多架构）、`docker compose up`、healthcheck 实测、数据卷持久化（重启后 DB 与 Agent token 文件还在）、`depends_on: service_healthy`。
2. **真实网段扫描**：多台真设备、去重与已存在识别、OUI 识别效果、扫描取消（进行中取消后已发现结果可导入）。
3. **真实 WOL 冷启动回检**：目标机关机后唤醒，`verify=true` 时 `boot_status` 从 `pending` 变 `online`（`sent` ≠ `booted` 的核心语义，沙箱内无真机可验）。
4. **多 Agent 跨网段**：两个 Agent 管理不同局域网、Agent 断网恢复、Server/Agent 重启恢复。
5. **ARM64 构建**：`--platform linux/arm64` 构建产物能在 ARM 迷你主机/电视盒子（ARM）上跑。
6. **PWA 真机安装**：手机浏览器“添加到主屏幕”、离线打开、manifest 图标显示。
7. **移动端目视**：底部导航、44px 触控尺寸、Safe Area、长 MAC/IP 不溢出、扫描向导与表单在小屏可用。
8. **备份往返（容器内）**：`scripts/backup.sh` 在部署环境实测备份/恢复（M5 只在沙箱用 sqlite 文件实测过往返逻辑）。
9. **手机网络远程唤醒**：手机经公网/Tunnel 访问后唤醒家里设备（第 34 项之一）。

## 8. 已知问题与限制（非阻塞）

1. **B-N9 仅代码未实测**：调度器启动失败改 fatal 后无专用测试（main 包无测试）；冒烟中调度器正常启动，未触发该分支。风险低（该分支只在 cron 初始化失败时走）。
2. **F-N7 仅代码未实测**：搜索接 `searchQuery` 的改动无组件测试，未在浏览器实测搜索行为。
3. **测试期偶现日志行**（M5 已记录）：`TestDashboardFieldsAndUpcomingTasks` 通过时偶现 `[Audit] 写入审计日志失败 agent_disconnect: sql: database is closed`——前一个测试的 WS 断开审计 goroutine 在该测试 DB 关闭后才写，属测试收尾竞态；生产中 DB 在服务运行期间不关闭，仅关机时可能出现，无害。
4. **D-N3 残留**：install.sh 的网段默认值仍为 192.168.1.0/24，用户安装时须按实际局域网（192.168.1.0/24）修改；`.env.example` 已注明不要照抄。
5. **Agent token 文件默认路径**：`./data/agent-token`，容器重启后须挂卷持久化（compose 已配共享卷；裸二进制部署时须注意工作目录）。
6. **扫描 `failed` 语义**：按 M4 裁决并修订契约 §7.3——只计探测任务本身失败，单台主机无响应是正常阴性结果，不计入 failed。
7. **睡眠 allowlist 含回环**：按 M3 裁决并修订契约 §6.1——接受 127.0.0.0/8（Sleep-on-LAN 服务可能跑在 Agent 本机上，回环不可路由出本机）。
8. **迁移目录配置**：按 M5 实现并修订迁移文档——沿用 `WAKEHUB_MIGRATIONS_PATH` 所在目录，不新增 `WAKEHUB_MIGRATIONS_DIR`。

## 9. 交付物清单

- 代码：`~/workspace/wakehub`（M1–M8 全部改动，未提交 git，未打包；打包与推送由父代理在终验后统一做）
- 契约：`docs/contract-v0.2.0.md`（§6.1 回环、§7.3 failed 语义两处修订已同步）
- 审计：`docs/audit-v0.2.0.md`
- 迁移说明：`docs/migration-002-003.md`（迁移目录一处修订已同步）
- API 文档：`docs/api.md`（43 端点）、安全说明：`docs/security.md`、升级指南：`docs/upgrade.md`、部署指南：`docs/deployment.md`（含 §7 验证清单）
- 变更日志：`CHANGELOG.md`（M8 复核并修正 3 处：OUI 27 条、README 默认值相关表述）
- 本报告：`docs/acceptance-v0.2.0.md`
- 冒烟脚本（不在交付包内，仅验收用）：`/tmp/m8-smoke/run.sh`（74 项断言，退出码 0）
