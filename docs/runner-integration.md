# 云效内网 Runner 接入指南

本功能把一条云效流水线的镜像产物对应到一个已保存的组件和非生产部署位置。云效 Runner 主动拉取云端任务，内网通知任务调用平台，平台更新绑定的 Deployment 容器。平台不需要公网入站访问；Runner 宿主机安装和云效注册由运维完成。

## 操作步骤

1. 在「多环境管理」确认目标是已启用的**非生产、自动发布**集群。在「应用与组件」接入组件，填写实际镜像仓库并核对 namespace / Deployment / container。旧绑定先补全镜像仓库。
2. 在组件行点击「接入云效 Runner」。填写云效流水线 ID、**私有构建集群 ID**（不是 Runner 机器名），以及流水线实际任务环境可访问的平台根地址。其他机器上的 Runner 不能填 localhost。operator/admin 可以设置绑定；只有 admin 可管理云效连接与自动写入流水线。
3. 生成配置，将仅显示一次的 `dpci_...` 密钥放进云效私密变量 `DEPLOY_RUNNER_TOKEN_<绑定ID>`。平台只保存哈希；关闭页面后不能取回。重新生成立即撤销旧密钥，需要同步更新云效变量。默认有效期 90 天；管理 API 可指定未来一年以内的 `expires_at`。
4. 镜像推送成功后，向通知任务显式传入：

   | 变量 | 含义 |
   |---|---|
   | `DEPLOY_IMAGE_FULL` | 同次构建完整镜像地址，例如 `registry.example.com/orders/api:build-42` |
   | `DEPLOY_IMAGE_DIGEST` | 推送产物的真实 `sha256:<64位十六进制>` |
   | `PIPELINE_ID` / `BUILD_NUMBER` | 云效内置流水线 ID / 正整数运行编号 |
   | `CI_COMMIT_SHA` | 可选内置提交号；有值时脚本可在 Git checkout 中读取提交者时间 |
   | `CI_COMMIT_TIMESTAMP` | 可选，优先使用的 RFC 3339 Git commit 时间；不得填构建完成时间 |
   | `DEPLOY_WAIT` | 默认 1，等待最终发布；0 仅等待平台受理 |

   不填写 `APP_NAME`、目标集群、Deployment 或容器；这些由专属授权派生。平台只检查镜像仓库精确对应，不猜测代码仓库与镜像的关系，也不自动选择 registry 中的 latest。镜像构建、推送和 digest 提取仍由 CI 完成。

5. 复制生成的云效通知任务 YAML，合并到镜像成功推送之后执行的阶段；或下载 Bash 放进已有通知步骤。生成任务的 `runsOn.group` 为 `private/<私有构建集群ID>`。任务需要 **Bash、Python 3、curl 7.76+（含 fail-with-body/retry-all-errors）、seq**。生成配置不含密钥明文。
6. 先在同一个实际任务环境运行第三步的「接入测试脚本」。它检查网络、专属授权、流水线匹配及当前组件映射，记录检查时间，**不创建发布任务**。它不证明镜像可拉取，也不替代正式 rollout 或业务验收。
7. 正式构建推送后运行通知任务。平台固定 digest，更新绑定容器，等待 rollout。202 是受理；默认 Bash 等到 success 才返回 0，failed/interrupted 返回失败，约 90 轮查询后超时。网络重试可能延长等待。查看「发布中心」及第三步的最近通知/发布状态。

接入向导按「接入配置 → 流水线任务 → 检查与验证」组织。第一步会并列显示必须产出的镜像仓库和唯一更新的 `namespace/Deployment:container`，保存配置前即可确认这条流水线会更新哪个目标；不需要自行把代码仓库名猜成 Kubernetes 资源名。步骤导航和底部操作固定，表单、流水线 YAML 与证据脚本在中间区域滚动；缺少必填值或脚本仍在加载时，底部会说明继续条件。创建配置后，复制专属私密变量与通知 YAML，再在第三步运行只读接入测试。

弹窗高度同时受视口和安全边距约束，在 200% 等效窄视口下标题、步骤和主操作仍留在屏内，长正文继续独立滚动。

接入页还会生成 **CI / Smoke / Scan 报告脚本**。通知任务返回的 JSON 含 `release_id`，并把它写入 `deploy-release-id.txt`（可用 `DEPLOY_RELEASE_ID_FILE` 改路径）；将该文件通过云效支持的产物机制传给后续检查任务，报告脚本会自动读取。也可直接设置 `RELEASE_ID`。CI 在对应制品的流水线检查完成后提交；Smoke 必须先等测试环境 rollout 成功，再执行实际健康或业务探测并提交。扫描上报还需要 `DEPLOY_SCAN_TOOL`、`DEPLOY_SCAN_TOOL_VERSION`、`DEPLOY_SCAN_RULES_VERSION`；报告固定工具和规则版本，但当前只作报告，不阻断晋级或发布。跨任务请使用云效明确支持的任务输出或产物传递，不能依赖共享环境变量或工作目录。报告脚本必需变量为 `DEPLOY_RUNNER_TOKEN_<绑定ID>` 和 `DEPLOY_EVIDENCE_KIND`（`ci`、`smoke` 或 `scan`）；可用 `DEPLOY_EVIDENCE_STATUS=failed` 记录失败结论，CI / Smoke 失败时脚本返回失败，扫描失败状态仍只记录报告。报告链接、摘要和有效时长可分别通过 `DEPLOY_EVIDENCE_REPORT_URL`、`DEPLOY_EVIDENCE_SUMMARY`、`DEPLOY_EVIDENCE_VALID_HOURS` 提供；默认 24 小时，允许 1–168 小时。服务端从该 release 固定 digest 和 `runner:<接入ID>` 身份，Smoke 未等到测试 rollout 成功时会拒绝。该身份可配置为生产环境 CI / Smoke 可信来源，人工验收仍必须由人工账号提交。

跨构建任务不共享文件或临时环境变量。若镜像在另一构建集群产生，使用步骤输出或包含完整镜像、digest、提交号的构建产物清单，把**同一次构建**的结果传给通知任务。通知步骤无需安装 docker；需要能访问平台，平台/集群需要能拉取镜像。

## 在平台预览并保存云效 YAML

管理员可在接入引导中创建云效连接：中心版填写 `openapi-rdc.aliyuncs.com` 和组织 ID；Region 版填写该实例的官方 devops 接入域名。PAT 需要读取/更新目标流水线的权限，通过后端凭据加密密钥加密保存，不返回前端、不放入通知脚本。

选定连接并保存接入配置后，点击预览。平台只对 `PIPELINEASCODE` 且能识别有序 `stages` 的 YAML 追加 `deploy_platform_<绑定ID>` 通知阶段，显示完整修改前后内容。核对通知阶段应在推送成功后执行、任务能取得镜像输出、私密变量已配置，再勾选并保存。

保存时重读远端流水线，校验预览基线和接入配置，先加密保存原始 YAML，再调用更新接口。已有阶段/任务顺序保留，YAML 排版和注释可能变化；预览用于核对最终内容。远端内容变化返回 409，需重新预览。已有同名相同阶段不重复添加；内容不同则拒绝覆盖，需手工合并。UI 引擎格式等无法安全编辑的内容也使用手工片段。

云效更新接口没有公开原子版本条件，重读和保存之间仍有并发编辑窗口，应避免其他人同时修改。保存成功后管理员可下载最近一次更新前的 YAML 备份；恢复需在云效核对并手工保存。自动配置不安装 Runner、不写入云效私密变量、不修改原有构建/推送任务或猜测产物输出。

## 授权和失败处理

- `dpci_` 只允许本绑定的接入检查、事件上报和发布结果查询；不能访问通用 OpenAPI、审批生产或选择其他组件。通用 `dp_` 密钥也不能调用 Runner 接口。
- 密钥禁用、过期、组件映射改变、集群策略改变后请求被拒绝。修改部署位置后重新生成接入配置，替换私密变量，再执行接入检查。
- 事件键由流水线 ID、运行编号、绑定 ID 组成。同一事件重试返回同一 release_id，不重复部署；用同一编号改写镜像/digest 返回 409。较新运行已经接收后，较旧运行不会覆盖；同一工作负载已有活动任务也返回 409，稍后以原编号和相同产物重试。
- 通知超时/断网时查平台是否已经受理。不要更换运行编号绕过去重，保存本次产物并用原编号重试。Runner 离线时遵循云效任务重试机制；平台不能从未收到的事件推断构建成功。
- 最近检查/通知时间只是历史活动，不表示 Runner 当前在线。生产镜像继续经过测试验收与另一位人类审批。
- 持有密钥的调用者可提交该仓库下的镜像；上报 pipeline_id/commit_id 不构成独立供应链来源证明。

## 接口契约

机器接口使用 `Authorization: Bearer dpci_...`，错误为 `{"error":"消息"}`，每个请求 JSON 上限 1 MiB，不接受未知字段：

| 方法和路径 | 请求/返回 |
|---|---|
| `POST /openapi/v1/runner/bindings/:id/check` | `pipeline_id`，可选 `image_full`；返回目标摘要及 `deploy_created:false` |
| `POST /openapi/v1/runner/bindings/:id/events` | `pipeline_id`、`build_number`、`image_full`、`image_digest`，可选 `commit_id`、`commit_timestamp`（RFC 3339）；新事件 202，重复 200，返回 release_id、test_log_id、duplicate、status、result_url |
| `GET /openapi/v1/runner/bindings/:id/releases/:release_id` | 只读本绑定结果；status、current_step、error、image、finished_at |
| `POST /openapi/v1/runner/bindings/:id/releases/:release_id/configuration` | 固定本次镜像使用的配置 Git 仓库、目录和完整 commit SHA；相同请求幂等，不同来源不能覆盖。若组件已登记默认配置仓库，仓库/目录必须与绑定完全一致 |
| `POST /openapi/v1/runner/bindings/:id/releases/:release_id/verification` | 只为本绑定制品上报 `ci` / `smoke`；digest 和 `runner:<接入ID>` 来源由平台推导，Smoke 要求测试环境部署成功；不接受 `manual` |
| `POST /openapi/v1/runner/bindings/:id/releases/:release_id/scan` | 只为本绑定制品上报扫描结论；digest、身份和记录时间由平台推导，必须提交工具/工具版本/规则版本；当前仅报告、不阻断发布 |

在「应用与组件 → 接入组件」中登记部署配置仓库和目录后，接入页生成的 Runner 脚本从组件绑定读取这两项。脚本默认检查通知任务当前目录；如果配置仓库单独 checkout 到另一个目录，可选设置 `DEPLOY_CONFIG_WORKDIR` 指向该目录。上报前脚本会读取该目录的 Git `origin` 和 `HEAD`，要求远程仓库与组件登记一致、登记目录存在于仓库内且属于当前 commit，并确认该目录没有已修改、未跟踪或忽略文件，然后自动把实际 HEAD SHA 绑定到镜像 release。镜像代码的提交时间优先读取 `CI_COMMIT_TIMESTAMP`；未提供时，若 `CI_COMMIT_SHA` 存在且通知工作区可用 Git，则用 `git show -s --format=%cI` 读取该提交的 committer timestamp。两者都不可用时仍成功上报镜像，但提交时间留空，DORA 指标显示未知。仓库和目录由组件固定，脚本拒绝不一致的 `DEPLOY_CONFIG_REPOSITORY` / `DEPLOY_CONFIG_PATH` 覆盖。原有 `DEPLOY_CONFIG_REVISION` 可保留作一致性断言；若存在，必须与实际 HEAD 相同。配置来源提交可按同一值安全重试，不能用新 revision 覆盖已登记的制品来源。

组件没有登记配置仓库时，为兼容现有流水线仍可按构建任务传入三个可选变量 `DEPLOY_CONFIG_REPOSITORY`、`DEPLOY_CONFIG_REVISION` 和 `DEPLOY_CONFIG_PATH`；仓库和 revision 同时必填，目录默认 `.`。如果实际发布依赖 Helm/Git 配置却没有上报来源，计划要求人工核对；若组件已登记仓库但本次 checkout 校验或来源报告未成功，计划创建会被阻断，需修复 Runner checkout 并重新生成计划。

生成的 Runner 脚本在报告前会验证 Runner 工作区可见的 Git remote、目录和 HEAD；Runner 专属配置 API 本身仍只接受认证请求中的来源值，不会从平台端重新克隆仓库核验。云效现有构建/部署任务仍负责 checkout 配置仓库、渲染 Helm 并运行原有命令。checkout 元数据一致只能证明脚本执行时该工作区的 Git 身份和 commit，不能证明 Helm 渲染或 Kubernetes 实际应用的内容。配置内容恢复、配置 drift 的自动核验和数据库变更兼容性仍需接入后续链路。

该字段会跟随镜像与审批计划快照保存。生成脚本成功核对 checkout 后才会提交 revision；组件固定仓库但来源报告缺失时，计划显示阻断条件；未登记固定仓库且未提交来源时，计划显示未知并要求人工审查。平台不读取仓库内容、不渲染 Helm values；即使 Git checkout 校验通过，revision 也不能证明配置内容已部署，不能替代 ConfigMap/Secret 漂移检查或恢复副本，也不包含数据库迁移兼容性结论。

事件受理后，响应中的 `release_id` 用于提交 CI 证据。Smoke 检查应先等待同一发布结果为 `success`，再访问测试环境执行健康/业务探测并提交证据；不能用 rollout 成功代替 Smoke 检查。请求仅含 `kind`、`status`、`valid_until`，可选 `report_url` 和 `summary`，不传 digest 或 `recorded_by`：

```json
{
  "kind": "ci",
  "status": "passed",
  "report_url": "https://ci.example.com/build/42",
  "summary": "单元测试与镜像扫描前置检查通过",
  "valid_until": "2026-10-13T00:00:00Z"
}
```

证据有效期必须晚于当前时间且不超过 7 天；失败证据同样保留，不会自动晋级。管理员可在生产环境规则中把该绑定的可信身份配置为 `runner:<接入ID>`；ID 显示在 Runner 接入页面。Runner 凭据仅能提交自身绑定创建的制品证据，仍不能调用通用 OpenAPI、提交人工验收、审批或发布生产。

接入页已生成可复制、下载的 `verification_script`，通常无需自行拼 JSON 或实现重试。若已有脚本需要直接调用 API，`RELEASE_ID` 来自镜像事件响应，`RUNNER_BINDING_ID` 和平台根地址来自组件接入配置。私密变量不要开启 shell trace，也不要把密钥或响应文件作为日志上传：

```bash
set +x
curl --silent --show-error --fail-with-body \
  -H "Authorization: Bearer ${DEPLOY_RUNNER_TOKEN}" \
  -H 'Content-Type: application/json' \
  --data-binary @evidence.json \
  "${PLATFORM_URL}/openapi/v1/runner/bindings/${RUNNER_BINDING_ID}/releases/${RELEASE_ID}/verification"
```

接入页的同一个证据脚本也支持 `DEPLOY_EVIDENCE_KIND=scan`。扫描任务需额外配置 `DEPLOY_SCAN_TOOL`、`DEPLOY_SCAN_TOOL_VERSION`、`DEPLOY_SCAN_RULES_VERSION`；可设置报告链接、摘要和 1–168 小时有效期。脚本按扫描类型调用 `/scan`，请求不带 `kind`、digest 或来源身份，避免未知字段并由平台从关联制品与授权生成这些信息。状态为 failed 时仍成功保存报告并返回成功，让发布门禁保持当前“仅报告”行为；若需在 CI 自行阻断，应由云效扫描任务按组织的漏洞策略处理，平台不会把扫描失败伪装成通过。

页面管理接口使用登录会话（operator/admin）：

| 方法和路径 | 用途 |
|---|---|
| `GET /api/v1/runner/bindings` | 授权、最近发布及配置失效提示 |
| `POST /api/v1/runner/bindings` | 创建授权，返回 binding、仅一次 key、scripts |
| `PUT /api/v1/runner/bindings/:id/config` | 更新配置并轮换密钥，同创建字段 |
| `PUT /api/v1/runner/bindings/:id/enabled` | `{"enabled":false}` 禁用，true 启用（不延长有效期） |
| `GET /api/v1/runner/bindings/:id/scripts` | 重新读取无密钥的通知脚本、测试脚本、CI / Smoke / Scan 报告脚本、YAML |

创建/更新字段：`app_id`（当前绑定的 apps ID）、`pipeline_id`（字符串）、`private_cluster_id`、`platform_url`、`connection_id`（0 为手工），可选 RFC3339 `expires_at`。一个 apps 绑定最多一个 Runner 授权；一个组件的不同环境是不同 apps 绑定。管理接口不是机器密钥接口。

以下管理接口只允许 admin：

| 方法和路径 | 用途 |
|---|---|
| `GET/POST /api/v1/runner/connections` | 列出/创建连接；创建字段 name、edition（central/region）、domain、organization_id、token |
| `POST /api/v1/runner/bindings/:id/pipeline-preview` | 获取 before、after、fingerprint、change_hash、already_installed、note |
| `POST /api/v1/runner/bindings/:id/pipeline-apply` | 提交核对后的 fingerprint、change_hash |
| `GET /api/v1/runner/bindings/:id/pipeline-backup` | 获取最近一次更新前的原始 YAML |

`POST /api/v1/runner/deployment-pipelines/search` 由 operator/admin 使用，body 为 `{"connection_id":9,"name":"orders-config","page":1}`。它调用云效只读 `ListPipelines`，按名称分页返回最多 30 项的流水线 ID/名称，并保留云效总数和页数；`POST /api/v1/runner/deployment-pipeline-check` 再按所选 ID 读取类型和最近更新时间。两项检查都不会返回 YAML/PAT 或运行任务，也不核实流水线具体会写哪些资源。组件 Runner 接入页和环境绑定页均提供名称搜索和选择，仍允许手工填 ID；前者绑定镜像构建通知流水线，后者只登记独立配置部署流水线。

机器端完整定义见 [OpenAPI](openapi.yaml)。写操作审计不包含密钥/PAT；数据库只保存专属密钥哈希及加密 PAT/备份。需稳定保管 `CREDENTIAL_ENCRYPTION_KEY`，更换它不能直接解密原始密文。

## 升级和验证范围

Runner 表由 009 创建；后续功能依赖 010–038 的环境模型、计划、审批关联、资源锁、动作回读、蓝绿入口/运行实例、限时清理租约、工作负载身份引用、策略版本、漂移观测、任务环境等级快照、金丝雀阈值、生产证据策略、阶段快照、Worker 认领 fencing、通知告警索引、审批范围和持久登录会话；032 为每个环境绑定保存与镜像构建流水线分离的 Yunxiao 配置部署流水线；035 保存按定义指纹绑定的人工写入范围核对历史，不开放触发。033 仅存储登录 token 的 SHA-256 哈希并使会话跨重启验证。028 保存关联生产部署的故障与业务恢复事件，029 为 `apps` 增加部署配置仓库和目录，030 为故障台账增加干预开始事件，036 为事件增加人工因果判定历史，037 为事件增加修复处置、计划状态及生产部署关联，031 为 operator 保存逻辑应用与生产环境审批授权。新数据库执行 schema 和 001–038；已有 009 的库按序执行迁移 `010_delivery_model.sql` 至 `038_repair_legacy_environment_name.sql`。迁移 031 不会自动授予现有 operator 范围；管理员需在「设置 → 发布审批范围」配置。迁移 024 为发布事件增加受 64 KiB 限制的 JSON payload，当前用于持久化和回放金丝雀阶段快照；网关权重和指标适配器仍待联调。本服务不会自动升级已有数据库。011 保存不可变计划快照；012 将人类审批关联到计划；013 为活动部署任务加数据库资源锁；014 为现有镜像写入持久化意图与集群回读结果，未知状态会中断并要求人工核对，不会自动重放；015/016 支持蓝绿入口、独立运行实例和阶段事件；017 为候选清理锁增加 15 分钟租约；018 分别保存模板与活动 Deployment 的 UID；019 回填策略 v1 并启用策略历史/乐观并发 API；020 保存按需读取的漂移状态与变化历史；021 固化新任务创建时的环境等级，旧任务等级未知时不按当前集群配置猜测；022 将显式金丝雀阈值加入策略版本和发布计划快照；023 为生产环境保存版本化验证证据要求，未配置策略时阻断新计划和晋级。策略版本随发布计划固定，蓝绿可配置候选保留 1–720 小时，默认 24 小时。旧绑定 UID 留空，首次计划读取实际资源后固定；UID 不符会阻断发布。环境绑定页提供锁感知的显式只读 UID 重新核对。升级前需排查同一 Deployment 是否存在多个 pending/running 任务，避免回填时触发唯一键冲突。蓝绿已接入独立审批、切流、稳定恢复、候选清理及操作员关口的暂停/继续/取消控制；滚动支持 `rollout_wait` 平台暂停/继续和镜像写入前取消（Deployment 控制器继续工作，镜像写入后须走显式回退）。本机 Kind 已通过连续发布、切流后恢复稳定版本及切流后取消 E2E，fake Kubernetes 单测覆盖恢复、清理故障和滚动控制边界；独立非测试环境的故障演练仍未完成。漂移观察按用户操作发起，使用当前用户的集群权限，不做后台轮询或自动恢复。网关金丝雀仍待既有网关接口联调。配置部署流水线目前仅完成按环境绑定登记，尚未实现审批后触发、固定配置 commit / 镜像 digest、运行状态跟踪或自动恢复。

自动测试覆盖授权、映射变更、幂等/旧构建、生成通知与 CI / Smoke Bash 对真实本地 API 的实际调用、SQL 秘密分离、云效 HTTP 合约和冲突、页面脚本引导流程。真实云效 PAT、组织/Region 版本、私有集群调度、release_id 任务间传递、产物变量和真实 Kubernetes 镜像发布仍需实际流水线联调。本次实现没有修改真实云效流水线或执行镜像发布。

官方依据：[Runner](https://help.aliyun.com/zh/yunxiao/user-guide/pipeline-runner)、[私网 CI/CD](https://help.aliyun.com/zh/yunxiao/user-guide/how-to-use-cloud-efficiency-pipeline-for-ci-cd-in-private-network-environment)、[服务接入点](https://help.aliyun.com/zh/yunxiao/developer-reference/service-access-point-domain)、[获取流水线](https://help.aliyun.com/zh/yunxiao/developer-reference/getpipeline-get-pipeline-details)、[列出流水线](https://help.aliyun.com/zh/yunxiao/developer-reference/listpipelines-get-a-list-of-pipelines)、[更新流水线](https://help.aliyun.com/zh/yunxiao/developer-reference/updatepipeline-update-pipeline)、[环境变量](https://help.aliyun.com/zh/yunxiao/user-guide/environment-variables/)。

## 发布策略接入边界（2026-10-09）

现有 Runner 通知契约保持不变，CI 上报镜像与 digest，组件/目标由专属授权确定。测试构建仍走
既有 Deployment 链路；生产蓝绿由平台按环境绑定和人类批准的计划选择，通知内容不增加任意
Deployment、路由、流量权重或审批指令。生产继续由人类独立审批，机器授权不能确认切流或扩大流量。

Runner 通知本身不会启动蓝绿或金丝雀。网关金丝雀尚未实现，未来实施需同步执行状态查询与等待脚本：
受理、配置生效和业务验证分开，未知不能返回成功；长观测窗口超出通知等待期限时提供任务链接，
通知超时不代表平台已取消或回退。详见 [原生编排设计](./progressive-delivery-design.md)与
[前端体验规范](./release-experience-design.md)。
