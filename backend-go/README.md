# Deploy Platform Go Backend

Gin API、Kubernetes Deployment 镜像发布与集群控制台。CI 构建并推镜像，平台负责固定制品、测试环境部署、验证、独立审批和受限恢复。

## 本地运行

```bash
cd backend-go
go mod download
go run ./cmd/server
```

默认 `:8000`，读取当前目录 `.env`，不覆盖已有进程变量；配置见 `.env.example`。
`MOCK_RUNTIME=true` 使用模拟命令/集群，数据库不可用时可显式降级内存；真实运行必须连接数据库。
新数据库首次启动必须提供 `ADMIN_INITIAL_PASSWORD`，真实运行缺少时会拒绝创建管理员并停止启动；mock 开发模式保留 `admin123` 回退。已有用户不会重置，已有数据库可省略该值。

## 初始化和升级

全新库执行 `schema.sql` 与 `migrations/001…038`，可用根目录 `scripts/apply-schema.sh <database>`。
`make dev-mysql` / `infra/docker-compose.yml` 只在空卷初始化所有迁移；已有容器启动不会自动升级。
已有库只执行未执行的迁移；006 是一次性 ALTER，不能重放。升级前停止旧实例并备份。
旧 pending 审批在 006 置 expired，需要新建绑定快照的申请。

部署、升级和故障处理步骤见 [CI/CD 运营手册](../docs/cicd-operations-runbook.md)。

## 认证与集群

会话接口 `/api/v1` 要求登录，角色 viewer/operator/admin；生产申请需另一名人类 operator/admin 批准。
外部 `/openapi/v1` 使用 `dp_` API Key，机器可申请、提交 CI 报告、受限回退，禁止批准。
Webhook 使用 `YUNXIAO_WEBHOOK_TOKEN`；真实模式未配置 token 返回 503。

集群支持 kubeconfig、token、legacy_ssh；production 只允许 manual。启用直连凭据需
`CREDENTIAL_ENCRYPTION_KEY`（base64 编码的 32 字节）；凭据加密保存，不允许 kubeconfig exec/auth-provider/本地凭据路径。

真实运行模式启动时会验证密钥能否解密已启用直连集群的已保存凭据；不匹配会停止启动，避免健康检查正常却无法读取集群。重启前须保留原密钥，不能重新生成密钥后直接使用旧数据库。若原密钥丢失，需要备份旧记录，再以该集群现有的连接配置重新保存凭据；本地 `.env` 与进程配置应保持一致。
重启前可在 `backend-go` 目录运行 `go run ./cmd/server check-credentials`，只读加载当前 `.env`/进程环境与数据库，验证新数据库的初始管理员口令，以及当前密钥能否解密运行时需要的集群、云效和 Runner 凭据。该检查强制按真实运行模式校验，即使 `.env` 配置 `MOCK_RUNTIME=true` 也不会跳过；不会启动 HTTP 服务、Worker 或清理任务，也不会写数据库。配置无误会打印通过，数据库 schema、初始口令、密钥格式或密文校验失败则返回非零退出码和可操作的错误。
Docker 构建只接受运行期注入的密钥；根目录 `.dockerignore` 会排除所有本地 `.env`，不要把密钥写进 Dockerfile、构建参数或镜像层。
使用 TLS、平台角色和集群 RBAC 管理权限，平台外 kubectl 仍需单独治理。

**平台保持单副本**：登录会话、用户权限读取、API key 校验/管理读取和审计查询已以 MySQL 为权威来源，数据库故障时认证请求失败关闭；但 Worker 租约与接管恢复、进程内任务唤醒及其它运行状态尚未形成完整多实例一致性模型。
迁移 025 为 Worker 认领添加持久 claim ID，并在任务检查点和外部动作日志写入时校验该 ID；它解决同一 pending 行竞争时误中断活跃任务的问题，但没有实现租约/心跳、多进程启动恢复协调或任务运行状态共享。
每个实际目标集群只登记一个集群 ID。

## 发布流程

1. CI 构建/push 后调用 `/api/v1/webhooks/yunxiao`，提供 event_id、target_cluster（env_code）、app_name、image_full，推荐 image_digest。
2. 入站固定完整 SHA256 digest；无 digest 只解析配置 registry，无法固定则拒绝。重复 event key 不重新解析 tag。
3. 自动部署仅进入启用的 non_production/automatic 集群；执行 `k8s_set_image → rollout_wait → verify_image`（mock 跳过回读）。
4. 提交同 digest 的验证证据（CI、smoke 或真人验收），rollout 成功不能代替业务验证。
5. `/releases/:id/promote` 创建 pending 请求，绑定候选、证据、目标连接、工作负载配置摘要与生产基线。
6. 另一名人类批准，决定与生产 pending 任务同事务保存，worker 领取后再校验策略。

同一制品可以分别发布到多个生产目标，各目标与尝试独立记录。实际执行使用 `repository@sha256:...`。
真实运行始终独立审批；关闭 `PROMOTION_APPROVAL` 仅在 mock 模式生效。
扫描报告当前仅报告，不启用漏洞阻断。完整外部契约见 [OpenAPI](../docs/openapi.yaml)。

旧文件包 `POST /api/v1/deploy` 仅非生产使用：`check_file → docker_load → docker_tag → docker_push → resolve_digest → k8s_set_image → rollout_wait`（mock 跳过解析）。
生产 Deployment 禁止控制台直接写，需发布/恢复流程；真实 dry-run 预览允许。

## 恢复与观测

`POST /rollback` 请求改为 `{"cluster_id":2,"target_log_id":123}`；目标必须为同环境成功且固定 digest 的历史记录，服务器派生全部工作负载字段。
这是**镜像回退**，不恢复配置、清单、数据库；执行前确认兼容性。
异步提交返回 202；日志状态含 pending/running/success/failed/interrupted，成功 rollout 不证明业务恢复。

启动先恢复再消费：尚未领取的 pending 任务可重新排队；任何 running 任务都标记 interrupted，提示先核对集群实际状态，避免进程重启后盲目重复写入。
channel 仅唤醒；pending 扫描、条件领取防止丢任务和重复消费。持久化异常停止后续步骤，需核对真实集群状态。

迁移 013 增加跨进程 Kubernetes 资源锁，当前镜像部署、生产晋级和镜像回退按集群/namespace/Deployment 原子占锁，任务进入终态时事务释放。迁移 014 增加外部动作意图/回读记录；镜像写入先持久化目标，再读取实际镜像确认。写入响应丢失但回读为目标时继续；回读失败或值异常时任务中断并提示核对，不会自动重放未知写入。动作详情通过 `GET /api/v1/deploy/logs/:id/actions` 查询；操作员可用 `POST /api/v1/deploy/logs/:id/reconcile` 重新取得资源锁并核对，只有实际镜像已是计划目标时才继续 rollout 检查。014 还记录蓝绿 Deployment/Service 写入动作意图与集群回读，网关动作仍未接入。013 回填旧活动任务时若发现同一 Deployment 有重叠任务会因唯一键冲突而停止；先在备份上核对并处理活动任务，再应用迁移。

迁移 015 在已核对的逻辑绑定上保存蓝绿入口 Service 名称；创建计划时只读快照 Service UID、resourceVersion、selector 和配置摘要，并检查 selector 是否匹配当前 Deployment 的 Pod 标签。迁移 016 为发布计划创建独立执行实例和版本化阶段事件，迁移 017 为候选清理添加可过期资源租约，迁移 018 保存模板与活动 Deployment 身份，迁移 019 保存不可变发布策略版本并回填旧绑定当前草稿，迁移 020 保存按需镜像漂移的当前状态和变化历史，迁移 021 保存新任务创建时的目标环境等级供历史指标使用；迁移 022 将金丝雀阈值纳入不可变策略版本；迁移 023 为生产逻辑环境保存必需验证证据策略，未配置时生产计划与晋级 fail closed；迁移 024 限制运行事件 payload，迁移 025 持久化 Worker claim 所有权与执行写入 fencing；026 为通知告警页增加复合索引；027 为数据库待投递队列增加按到期时间排序的复合索引；028 新建关联生产部署的线上故障与业务恢复事件台账；029 为组件绑定增加独立于源码仓库的部署配置仓库和目录；030 为故障台账增加干预开始时间、不可变干预事件和按发生时间筛选的索引；031 为 operator 按逻辑应用与生产环境配置审批范围，迁移不自动授予既有用户；032 为每个环境目标绑定独立的 Yunxiao 配置部署流水线，尚未实现运行触发；033 持久化登录会话的 SHA-256 token hash，进程重启后仍可验证，注销会删除共享会话记录；034 为 Worker 待执行任务扫描增加 `(status,id)` 复合索引，按有界 keyset 查询待处理 ID；035 保存按流水线定义指纹追加的人工写入范围审阅历史，不授予触发权限。恢复时长观察从完整事件台账按窗口聚合；036 追加带版本和依据的人工因果判定；037 追加版本化的修复分类、计划内/外状态和同一生产应用的修复部署引用；038 只修复未人工改名的旧环境乱码。DORA 指标在事件覆盖率不足时仍未知。旧任务等级未知时不按当前集群配置猜测。`GET/PUT /api/v1/apps/:id/delivery-policy` 提供历史读取和 expected_version 乐观并发更新；`PUT /api/v1/apps/:id/delivery-binding` 可同时传入 `expected_policy_version`、`strategy` 与策略参数，在一个事务中提交逻辑关系和策略版本；版本冲突或策略写入失败会回滚整笔变更。每个计划固定策略版本、Service 入口与候选保留时间，后续修改不影响已生成计划。蓝绿候选保留时间可配置为 1–720 小时，默认 24 小时。蓝绿已接通独立审批、候选 Deployment/预览 Service 准备、Service 条件切流、人工业务确认、稳定版本恢复及核对 API；申请人需确认容量和外部路由等人工预检项。运行控制通过 `/api/v1/delivery-runs/:id/actions`，携带预期版本与幂等键。稳定恢复后，operator/admin 可显式清理经 UID 和所有权标签验证的预览 Service 与候选 Deployment。迁移 010–038 已写入源码，服务启动不会自动升级已有数据库；网关金丝雀策略仅保存为草稿，仍关闭审批与执行，直到确认既有网关权重 API 并完成联调。漂移观察仍由用户按需发起，不会后台轮询或自动覆盖变更。

迁移 022 的 `canary_analysis_json` 同时保存金丝雀阈值与显式流量阶段（每阶段候选比例/最短观察时长），2–10 阶段严格递增并以 100% 收尾。新增阶段字段沿用 JSON 兼容演进，不再增加列；旧草稿若缺少阶段仍可读取，但必须编辑并补齐后才能保存新策略版本。保存草稿不触碰网关，真实权重与指标适配完成前审批/执行保持关闭。

发布页提供独立验证、多个生产目标、执行指标、DORA 数据缺失说明、按需镜像漂移；漂移不自动修复。
通知默认关闭，可配置 HTTPS webhook + HMAC secret；有持久记录、稳定 ID、退避和失败重投。首次启用可能补发历史终态事件。

## 验证

```bash
go test ./...
go test -race ./...
go vet ./...
```

真实 Kind 集成按 `e2e/kind_e2e_test.go` 的前置条件运行（`KB_E2E=1`）；普通测试跳过真实环境。
E2E 会先确认当前 kubectl 集群对应本机 Kind，再创建随机命名的临时数据库和 namespace；只给测试用户临时数据库授权，结束时撤销授权并清理两者，不重置固定开发库或同名 namespace。蓝绿 E2E 覆盖 Runner 镜像事件、人工验收、独立生产审批、候选资源、Service selector 切流和 EndpointSlice 回读，并验证第二次连续发布、切流后恢复稳定版本及切流后取消。真实故障演练覆盖候选 Deployment/preview Service 创建、生产 Service 切流、`restore_stable` 恢复以及恢复后的候选 preview Service 清理在 Kubernetes 成功后丢失响应并强制重启；恢复流程会回读动作结果、保留资源锁、处理 Deployment 前台删除和租约到期重试。fake Kubernetes 单测覆盖取消恢复写入失败/响应丢失，以及候选删除失败、响应丢失、部分清理和限时租约重试；真实非测试环境的故障演练仍需完成。

通知端到端演练单独运行：`KB_E2E_NOTIFICATION=1 go test ./e2e -run '^TestNotificationWebhookE2E$' -count=1 -v`。它启动真实后端并连接随机 MySQL 数据库，使用 loopback HTTPS 接收端和显式信任的临时 CA 验证证书、HMAC、终态通知 outbox 持久状态、管理员 API 不暴露 payload，以及重启后不重复投递。该演练不验证真实接收端的 DNS 或网络可达性。

真实 MySQL 迁移与进程停启演练单独运行：`KB_E2E_MYSQL_RESTART=1 go test ./e2e -run '^TestMySQLInstanceRestartPersistsDeliveryData$' -count=1 -v`。测试创建随机命名的临时 MySQL 容器和本机端口，回放 `schema.sql` 与迁移 001–038，保存并追加生产证据策略版本、生产故障/干预/因果/处置/恢复事件、漂移状态/历史及配置流水线写入范围审阅后停止并重启实例，验证历史记录仍可读；测试退出时删除该容器。不会停止共享开发 MySQL；此检查不等同于 HA 主从故障切换。
生产部署与数据库运维见 [CI/CD 运营手册](../docs/cicd-operations-runbook.md)。

组件接入向导、来源与镜像精确绑定参见 [组件接入说明](../docs/component-onboarding.md)。已有库按缺失迁移升级；保存映射不会触发部署。

云效内网 Runner 引导、组件专属通知授权与流水线预览/保存参见 [Runner 接入指南](../docs/runner-integration.md)。当前真实后端要求数据库按顺序升级到迁移 036；已有库必须先执行所有缺失迁移再升级后端。operator 的生产审批权限还需管理员在「设置 → 发布审批范围」按业务应用和生产环境显式配置；迁移不会自动授予现有账号范围。
