# API 契约基线 — K8s 控制台（步骤 1 冻结）

冻结日期：2026-09-18。本文档记录现有已验证接口的请求/响应形状与安全约束，作为后续步骤的回归基线。契约测试位于 `backend-go/internal/httpapi/httpapi_test.go`（HTTP 层）与 `internal/service`、`internal/kube` 测试。

## 通用约定

- 所有接口在 `/api/v1` 前缀下；`GET /api/v1` 返回 `{"service":"deploy-platform","status":"ready"}`，`GET /health` 返回 `{"status":"ok","env","version"}`。
- 错误统一为 `{"detail": "<消息>"}`，HTTP 语义：参数非法 400、资源不存在 404、唯一性/状态冲突 409、外部不可达 502、内部错误 500。
- 数值型 path/query 参数非法一律 400；带范围的 query 参数超出范围 400。
- 请求体大小受限：集群相关 3 MiB、Webhook 1 MiB；超限 400。
- 凭据永不序列化：集群响应只含 `credential_configured` 布尔位；不返回明文、密文、SSH 私钥路径内容。
- CORS 当前为 `*`（开发基线）；管理 API 尚无用户鉴权——**上线权限功能前禁止暴露公网**（步骤 9 引入鉴权）。
- `MOCK_RUNTIME=true` 时 `K8sService.For()` 返回 `MockKubernetes`，真实集群验收必须 `MOCK_RUNTIME=false`。

## clusters.go — 集群

clusterOut 字段：`id, name, env_code, environment, environment_tier(non_production|production), deploy_policy(automatic|manual), connection_type(kubeconfig|token|legacy_ssh), api_server, credential_configured, ca_cert, insecure_skip_tls_verify, enabled, k8s_master_host/port/user(仅 legacy), default_namespace, description, created_at, updated_at`（时间为 `YYYY-MM-DDTHH:MM:SS`）。

- `GET /clusters` → 200 `[clusterOut]`。
- `POST /clusters` → 200 `clusterOut`；400 校验失败；409 `env_code` 重复。
  - 必填：`name≤64字符`、`env_code`（`^[A-Za-z0-9][A-Za-z0-9._-]{0,15}$`）、`environment≤64字符`。可选长度：`description≤256`、`default_namespace≤64`、`api_server≤512`、credential/ca_cert≤1MiB。
  - 默认值：`connection_type=kubeconfig`、`environment_tier=non_production`、`deploy_policy=manual`、`enabled=true`、`default_namespace=default`。
  - `production` 集群禁止 `automatic` 策略。
  - `kubeconfig`：必须提供 `credential`；kubeconfig 内禁止 `exec`、`auth-provider`、`client-certificate`/`client-key`/`tokenFile`/`certificate-authority` 文件路径（必须内嵌 data）；须能被 `clientcmd` 解析为合法 RESTConfig。
  - `token`：必须提供 `api_server`（http/https，无 userinfo/query/fragment，允许 path）+ `credential`。
  - `legacy_ssh`：必须提供 `k8s_master_host≤128`；port 1-65535 默认 22；user≤64 默认 root。
  - 保存启用的直连集群要求服务端已配置 `CREDENTIAL_ENCRYPTION_KEY`（AES-256-GCM 入库），否则 400。
- `POST /clusters/test-connection` → 200 `{"ok":true,"version":"<gitVersion>"}`；400 校验失败；502 连接失败。**不落库**。
- `PUT /clusters/:id` → 200 `clusterOut`；400 非法 id/校验失败；404 不存在；409 `env_code` 冲突。
  - 省略字段保留原值：`enabled`、`insecure_skip_tls_verify`、`environment_tier`、`deploy_policy`、`connection_type`、`api_server`、`ca_cert`、`credential`、`k8s_master_key_path`。
  - 切换 `connection_type` 必须提供新 `credential`。
- `DELETE /clusters/:id` → 200 `{"message":"集群 <name> 已删除"}`；404 不存在；409 被应用或发布单引用。

## k8s.go — 现有 K8s 只读/操作接口

公共参数：`cluster_id`（必填数值；缺失/非法 400；集群不存在 404；**禁用集群 = 403 `集群 <name> 已禁用`**，步骤 3 起由 500 细化为 403，所有 /k8s 端点一致）；`namespace` 缺省用集群 `default_namespace`。K8s 调用错误当前为 500。

- `GET /k8s/pods` → 200 `[{name,status,ready("x/y"),restarts,image,app_label}]`。
  - `restarts` = `kubectl get pods` RESTARTS 列口径：全部 `containerStatuses[].restartCount` 之和，另计入 `restartPolicy: Always` 的 init sidecar；普通 init 容器不计。
- `GET /k8s/pods/:pod/logs?tail=` → 200 `{"pod","logs"}`；`tail` 默认 200，范围 10–5000，越界 400。
- `POST /k8s/pods/:pod/restart` → 200 `{"pod","message":"pod \"<name>\" deleted"}`（实现为删除 Pod）。
- `GET /k8s/deployments/:name` → 200 `{name,namespace,replicas("ready/spec"),image,strategy,env,config_maps,ports,resources,conditions}`；取第一个容器。
- `GET /k8s/deployments/:name/events` → 200 `{"deployment","events"}`，events 为 `TIME\tTYPE\tREASON\tMESSAGE` 表头的文本。

## k8s.go — 统一资源 API（步骤 3）

GVR 只允许来自服务端 allowlist（`internal/kube/resources.go`，当前 16 种，**Secrets 自步骤 7 起脱敏**，见下文"YAML 编辑与 Secrets"）；`group` 可空（core 组），`version`/`resource` 必填，缺失或不在 allowlist → 400。运行时层报 `ErrResourceUnsupported`（集群不支持该 allowlisted 类型）→ 404；K8s `NotFound` → 404、`Forbidden` → 403、`BadRequest`（如非法 selector）→ 400、其他 → 500。

- `GET /k8s/resource-types` → 200 `[{group,version,resource,kind,namespaced}]`，服务端 allowlist 自省，无需 `cluster_id`。
- `GET /k8s/resources?cluster_id=&group=&version=&resource=&namespace=&all_namespaces=&label_selector=&field_selector=` → 200 `UnstructuredList`（`apiVersion/kind=<Kind>List/metadata/items`，items 与 `kubectl get -o json` 一致含 managedFields）。
  - namespaced 资源：`namespace` 缺省取集群 `default_namespace`，须为合法 DNS-1123 label，否则 400；`all_namespaces=true` 跨命名空间列表（仅 list 可用），非法取值 400。
  - cluster-scoped 资源：`namespace`/`all_namespaces` 一律忽略，不强制。
- `GET /k8s/resources/:name?cluster_id=&group=&version=&resource=&namespace=` → 200 单个 `Unstructured` 对象（GVK 已盖章，与 `kubectl get <r> <name> -o json` 一致）。namespaced 资源必须解析出具体 namespace（`all_namespaces=true` 在此 → 400）。
- `GET /k8s/overview?cluster_id=` → 200 集群概览（步骤 5）：
  - `server_version`（API server gitVersion）、`namespaces`（数量）、`workloads`（deployments/stateful_sets/daemon_sets/jobs/cron_jobs/pods/pods_running 全命名空间计数）、`warning_events`（type=Warning，按 lastTimestamp/eventTime 降序，上限 20，含 namespace/object/reason/message/count/last_timestamp）。
  - `nodes`：`{total, ready, items[]}`，items 含 name/ready/roles/kubelet_version/os_image/internal_ip/cpu_capacity_millis/mem_capacity_bytes；装 metrics-server 时另有 `usage{cpu_millis,mem_bytes}`。
  - `metrics`：`{available, message?}`；`top_pods`（按 CPU 降序前 10，namespace/name/usage）。metrics.k8s.io 缺失、无权限或不可用 **只翻转 `available=false` 并带 message，绝不影响其他区块**；CPU 单位为 milliCPU、内存为字节。
  - 错误语义同统一资源 API（缺 cluster_id 400、未知集群 404、禁用 403、版本/列表 K8s 错误按上表映射）；metrics GVR 固定服务端（`metrics.k8s.io/v1beta1` nodes|pods），不接受客户端输入。

## k8s.go — 受控写 API（步骤 6）

写操作只作用于服务端 allowlist 中显式开启能力位（`scalable/imageable/restartable/deletable`）的资源；`GET /k8s/resource-types` 随每个类型返回这四个布尔值供前端渲染操作入口。能力不满足 → 400 `资源 <r> 不支持<操作>`（不是 404/405——类型存在但能力关闭）。错误映射在只读语义上增加：K8s `Conflict` → **409**（乐观并发冲突，detail 透传 apiserver 消息）。

- `POST /k8s/resources/:name/scale?cluster_id=&group=&version=&resource=&namespace=` body `{"replicas":<int>, "resource_version"?}` → 200 更新后的 Unstructured。仅 `scalable` 类型（deployments/statefulsets）；`replicas` 必填整数 0–50，缺省/非数/越界 → 400。
- `POST /k8s/resources/:name/image?cluster_id=&...` body `{"container":<str>, "image":<str>, "resource_version"?}` → 200 更新后对象。仅 `imageable` 类型（deployments/statefulsets/daemonsets）；container/image 去空白后必填（400）；**先 GET 校验容器确实存在于 spec.template.spec.containers，不存在 → 400 `容器 <c> 不存在`**（阻止幽灵容器 patch 出无效副本）。
- `POST /k8s/resources/:name/restart?cluster_id=&...` body `{"resource_version"?}` 或空 → 200 更新后对象。仅 `restartable` 类型；实现为在 pod template annotations 写入 `kubectl.kubernetes.io/restartedAt=<RFC3339>`。
- `DELETE /k8s/resources/:name?cluster_id=&...&resource_version=` → 200 `{"detail":"<resource> \"<name>\" 已删除"}`。仅 `deletable` 类型（controllers、jobs/cronjobs、pods、services、ingresses、configmaps、secrets、pvc）；cluster-scoped 高风险类型（nodes/namespaces/pv/storageclasses）与 events 一律 400 拒绝。

并发控制：三个 patch 接口把 `resource_version`（若提供）写入 patch body `metadata.resourceVersion`，apiserver 原子校验，不匹配 → 409（直连与 SSH 路径一致）；DELETE 走 `Preconditions.ResourceVersion`（直连 apiserver 原子校验；SSH 路径先 `kubectl get -o json` 比对 rv，不符返回同样语义的 Conflict，再 delete）。`resource_version` 省略则不校验（后端允许，前端始终携带详情页对象的最新值）。

## k8s.go — YAML 编辑与 Secrets（步骤 7）

- `GET /k8s/resource-types` 新增 `editable` 字段。allowlist 新增 `secrets`（core v1，namespaced）——**默认读取与创建响应脱敏**：list/普通 detail/PUT/POST 响应中的 `data`/`stringData` 值一律替换为 `"***"`（保留键名）；`editable` 保持关闭（PATCH 系、PUT 一律 400），脱敏内容永远不可能写回集群。创建功能落地后 `creatable`/`deletable` 开启：创建经 `stringData` 提交明文（apiserver 落盘为 `data` 的 base64，查看弹窗可按需读取原值），删除不涉及数据回显。
- `GET /k8s/resources/:name?...&reveal_secret=true`：查看/编辑 Secret 弹窗按需读取原始 base64 数据并解码回显。沿用登录与 Kubernetes 读取权限，记录 `secret_view` 审计（不含值），响应 `Cache-Control: no-store`；列表忽略此参数，始终脱敏。前端关闭弹窗后释放该查询缓存。
- `PUT /k8s/resources/:name?cluster_id=&group=&version=&resource=&namespace=` body `{"manifest":"<yaml text>"}` → 200 更新后的 Unstructured（Secret 响应同样脱敏）。仅 `editable` 类型（= deletable 集合：deployments/statefulsets/daemonsets/jobs/cronjobs/pods/services/ingresses/configmaps/pvc）。
  - 校验（均 400）：manifest 为空/YAML 解析失败；`apiVersion`/`kind` 与 allowlisted def 不符（**客户端 GVK 永不作为路由依据**）；`metadata.name` 缺失或与路径不一致；`metadata.namespace` 与解析 ns 冲突（缺失则回填）；`metadata.resourceVersion` 缺失（update 前置条件，提示基于最新对象编辑）。
  - 净化：剥离 `metadata.managedFields`/`uid`/`selfLink`/`generation`/`creationTimestamp`；YAML 标量经 JSON round-trip 归一化（unstructured 兼容）。
  - 语义：等价 `kubectl replace`——直连走 dynamic `Update`（apiserver 原子校验 rv → 409）；SSH 走 `kubectl replace -f -`（JSON 经 stdin 管道，`RunSSHStdin`）；`UpdateOptions` 无附加字段。K8s 校验错误（字段非法等）按原表映射（400/403/404/409）。
- `POST /k8s/resources?cluster_id=&group=&version=&resource=&namespace=[&dry_run=true]` body `{"manifest":"<yaml or json>"}` → **201** 创建后的 Unstructured（dry run → 200）。仅 `creatable` 类型（`persistentvolumeclaims`、`configmaps`、`secrets`——对应的创建对话框）；非 creatable → 400 `资源 <r> 不允许创建`（deny 审计）。Secret 创建经 manifest `stringData` 携带明文值，**响应体 `data`/`stringData` 值始终遮蔽为 `"***"`**，与读取路径同一不变量。
  - 校验（均 400）：除 PUT 共享项（GVK 匹配、namespace 一致/回填、空 manifest）外，`metadata.name` 必须是 DNS-1123 subdomain；**`metadata.resourceVersion` 必须为空**（新建不存在乐观并发前置条件）；另剥离 `creationTimestamp` 与顶层 `status`。
  - 语义：等价 `kubectl create`——直连 dynamic `Create`，SSH `kubectl create -f -`（stdin JSON）。K8s `AlreadyExists` → **409** `同名资源已存在`。
  - `GET /k8s/resource-types` 随每个类型返回 `creatable`；`GET /k8s/capabilities` 的每个 ResourceCaps 新增 `create`（rules 的 `create` 动词映射）。审计 action `create` / dry run `create_dryrun`。
- `POST /k8s/resources/:name/data?cluster_id=&...&dry_run=` body `{"resource_version":<str>, "type"?:<str>, "set"?:{"k":"<plaintext>"}, "delete_keys"?:["k"]}` → 200 更新后对象（dry run → 200）。仅 `data_editable` 类型（`configmaps`、`secrets`——结构化键值编辑对话框）；其他类型 → 400 `资源 <r> 不支持数据编辑`（deny 审计）。
  - **服务端合并语义**（与整对象 PUT 的本质区别）：先 GET 活动对象，`resource_version` 必填且必须与 live 对象一致，不一致 → 409（双运行时统一手工比对，不依赖 apiserver）；然后合入 diff——`delete_keys` 删键、`set` 写键（Secret 值在此 base64 编码进 `data`，等价 `stringData` 语义；ConfigMap 直接写字符串）、`type` 仅 secrets 可改；合并结果经 `Update`/`kubectl replace -f -` 写回，dry run 同 PUT 的 `UpdateOptions.DryRun`/`--dry-run=server`。查看弹窗可通过显式 reveal 读取原值；编辑仅提交差异，未触碰的键保留服务端原值，支持将键值设为空字符串。
  - `immutable: true` 的 ConfigMap 携带数据变更时 → 400 `该 ConfigMap 已标记 immutable，数据不可修改`（先于 apiserver 拒绝）。
  - 校验（均 400）：`resource_version` 缺失；`set`/`delete_keys`/`type` 全空 `无变更`；键不满足 `[-._a-zA-Z0-9]+`（≤253，IsConfigMapKey）；`type` 出现在非 secrets。
  - 可选 `metadata: {labels?: {...}, annotations?: {...}}` 替换对应映射（空映射清空），可选 `immutable: boolean` 设置不可变标记。已不可变资源只允许修改元数据，不能取消 immutable；同一次保存可修改数据并设置 immutable。
  - 审计 action `update`/`update_dryrun`，detail 只含键名（`keys +a,b -c`、`type=<v>`）——**值永不落审计**。响应出口对 secrets 照常 `MaskSecretData`。
  - `GET /k8s/resource-types` 随每个类型返回 `data_editable`；能力门控复用 `update` 动词。

## k8s_stream.go — 流式日志与 Web 终端（步骤 8）

两个 WebSocket 端点共享只读接口的参数校验与错误映射（cluster_id/namespace/pod 校验在升级前完成，失败按 400/403/404/429 返回普通 HTTP 错误；升级后错误经 close frame 传达）。所有会话绑定请求 ctx；每类会话有独立并发预算（日志 32、终端 8，超限升级前 → 429）、空闲超时（日志 15min 无新数据、终端 10min 无入站消息 → close `GoingAway`）与最大时长（日志 60min、终端 30min）。服务端每 25s 发送 WS ping 保活。浏览器断开/超时后的终端清理按序升级：先向 stdin 写 `exit\n`（TTY shell 收到命令自行退出——裸 stdin close 会被运行时吞掉，这是 kubectl 硬断开遗留 exec 孤儿进程的原因）、再关闭 stdin（非 TTY 读端收到 EOF）、最后取消 exec ctx 断开上游 SPDY/WebSocket，每步有界等待。

- `GET /k8s/pods/:pod/logs/stream?cluster_id=&namespace=&container=&tail=&follow=` → WebSocket。`container` 必须是 DNS-1123 label（默认容器留空）；`tail` 1–5000 默认 200；`follow` 默认 true（`false` 时发送 backlog 后正常关闭）。数据帧为 **binary message**（原始日志字节，无通道前缀）；上游错误 → close `InternalServerErr`+reason，正常结束 → close 1000。
- `GET /k8s/pods/:pod/terminal?cluster_id=&namespace=&container=&command=&tty=` → WebSocket。`command` 按空白分词为 argv（默认 `/bin/sh`）；`tty` 默认 true。帧为 binary message + **1 字节通道前缀**：客户端→服务端 `0`=stdin、`4`=resize（payload JSON `{"cols":n,"rows":n}`）；服务端→客户端 `1`=stdout、`2`=stderr（tty 合并流时无 stderr）、`3`=会话结束（payload 为退出原因/错误文本，随后 close）。
  - 直连集群经 client-go `remotecommand`：优先 WebSocket 通道，upgrade 失败自动回退 SPDY。SSH 接入集群不支持交互终端（管道无法携带 PTY/resize），会话建立后立即收到 `3`+`该集群接入方式不支持容器终端`。
  - 已有非流式接口 `GET /k8s/pods/:pod/logs` 保持不变（tail 快照，JSON 返回）。

## auth.go — 认证、用户与审计（步骤 9）

会话为不透明 HttpOnly Cookie（`Path=/api`，SameSite=Lax，HTTPS 下 Secure，TTL 24h）；JS 拿不到 token 本体。公开组仅 `/api/v1` 根、登录、webhook；其余全部 `/api/v1/*` 在 session 中间件之后，未认证 → 401 `{"error":"未登录或会话已过期"}`。

- `POST /auth/login` body `{username,password}` → 200 `{"token","user":PublicUser}` + Set-Cookie；错误凭据 401；禁用账户同样 401。
- `GET /auth/me` → 200 `{"user":PublicUser}`；会话失效/账户被禁用 → 401。
- `POST /auth/logout` → 200 `{"status":"ok"}`，服务端吊销会话并清 Cookie。
- `PublicUser`：`{id,username,display_name,role,k8s_username,k8s_groups,enabled}`；`password_hash` 永不返回。
- 角色语义：`viewer` 只读；`operator` 可 K8s 写操作/终端/生产发布；`admin` 另有用户与审计管理。角色门禁在路由层执行，deny 一律 403 `{"error":"当前角色无权执行该操作"}` 并写审计。
- `GET /users`、`POST /users`、`PUT /users/:id`（admin-only）：创建 `username/display_name/role/password(≥8)/k8s_username/k8s_groups`；PUT 支持局部更新（`password` 留空不变更），用户名不可变、不能禁用当前登录账户。
- `GET /audit?limit=`（admin-only）→ 200 `[AuditEntry]` 倒序（`{id,username,action,cluster_id,namespace,gvr,name,detail,result,created_at}`，result ∈ success/failed/denied/conflict；detail 已脱敏）。
- 初始管理员：空数据库真实运行必须配置 `ADMIN_INITIAL_PASSWORD`，否则拒绝启动并且不创建默认管理员；仅 mock 开发模式缺省回退 `admin123` 并告警。已有用户不会被重置。

### Kubernetes 身份映射（impersonation）

平台用户经 `k8s_username/k8s_groups` 映射为 K8s 身份：直连集群（kubeconfig/token）设置 `rest.Config.Impersonate`（含流式 exec/log 配置）；SSH 集群在每条 `kubectl` argv 前注入 `--as/--as-group`。两层权限独立生效——平台角色决定路由可达性，K8s RBAC 决定 apiserver 是否放行；任一拒绝都是 403。`k8s_username` 为空的用户沿用集群凭据身份；CD worker/webhook 永远以 nil 用户解析，不继承浏览器身份。

- `GET /k8s/capabilities?cluster_id=&namespace=` → 200 `{"resources":{"<group>/<version>/<resource>":{list,update,delete}},"exec":bool,"logs":bool}`：对当前用户执行一次 SelfSubjectRulesReview（SSH 为 `kubectl auth can-i --list`）并按 allowlist 收敛。保留 `resourceNames` 范围；只允许访问单个对象的规则不会被扩成该资源类型的通用能力。不完整的规则查询作为错误返回，不据此显示授权成功。
- `POST /delivery-preview`（operator，Cookie 会话）body `{cluster_id,namespace,deployment_name}` → 200 `DeliveryCapabilityPreview`：Deployment/Service/HPA/ResourceQuota 的关系读取按当前用户 K8s 身份执行；滚动和蓝绿发布权限规则另按集群连接保存的 Worker 执行身份核验。滚动要求目标 Deployment 的 `get+update`（SSH 另需 `patch+watch`）；蓝绿要求候选 Deployment/Service 的 `create+delete` 与稳定 Deployment/入口 Service 的 `patch`。`resourceNames` 限定不会扩大为资源级通用权限；蓝绿动态候选名称的创建/清理必须拥有相应 namespace 级权限。规则结果不完整为 `unknown`，不能显示通过。创建发布计划时仍用 Worker 身份新鲜读取目标；缺权或读取失败会阻断审批。
- 写路由（scale/image/restart/delete/PUT manifest）、终端、生产发布要求 `operator` 及以上；Secret 读取保持登录即可（apiserver RBAC 兜底）；用户/审计管理要求 `admin`。审计覆盖写操作与角色门禁拒绝。

### 多集群生命周期与缓存（步骤 10）

直连客户端按 `(cluster_id, credential_version, k8s_identity)` 缓存，`credential_version` 即 `cluster.updated_at`——任何集群写（凭据轮换、停用、改名）都抬高版本令旧键作废。缓存只存构建好的 client，永不缓存明文凭据；test-connection 的瞬时 `PlainCredential` 路径完全绕过缓存。`PUT /clusters/:id`（含停用）与 `DELETE /clusters/:id` 成功后立即调用 `InvalidateCluster`：丢弃该集群全部缓存 client，并取消其已注册的活动流式会话（日志/终端的 session ctx 被取消，连接按正常 teardown 关闭）。停用后所有读写返回 403；删除/停用后没有 watcher 能存活。

### 审计与安全边界（步骤 11）

- **覆盖**：K8s 写操作（scale/image/restart/delete/PUT manifest/终端）、平台层拒绝（不支持的动词、越界输入、无效 manifest、角色门禁）、生产发布 promote、文件发布 deploy、回滚 rollback、webhook_deploy（用户名记 `-`）、集群增删改、test-connection、Secret 显式查看（`secret_view`，含失败读取）。
- **结果口径**：`success`/`failed`/`denied`（角色门禁、allowlist 拒绝、apiserver RBAC Forbidden、平台校验拒绝）/`conflict`（乐观并发 409）。审计持久化失败不回滚操作。
- **脱敏红线**：detail 只含操作者提供的有限元数据（replicas、container/image、connection_type、app@image）——Secret 数据、Token、KubeConfig、完整 manifest 永不落审计；`secret_view`/`cluster_test`/`webhook_deploy` 的 detail 不含凭据。Secret 的 `data`/`stringData` 在 list、普通单查与创建响应出口统一 `***` 掩码，显式 reveal 单查可回显原值，前端列表永远不显示 Secret 值。
- **删除确认**：所有资源删除要求在前端对话框中输入完整资源名确认（比 Kuboard 的高风险资源确认更严——覆盖全部可删类型）；namespace/PV/storageclass 本就不在可删 allowlist，无法经 API 删除。

## cd.go — 镜像发布

- `POST /webhooks/yunxiao`：
  - 鉴权：`YUNXIAO_WEBHOOK_TOKEN` 已配置时接受 `Authorization: Bearer <token>` 或 `X-Webhook-Token: <token>`（常量时间比较），错误 401；`MOCK_RUNTIME=false` 且未配置 token → 503 拒绝接收。
  - 负载双形态：自定义 `{event_id,target_cluster,app_name,image_full,image_digest?,pipeline_id?,pipeline_name?,build_number?,branch?,commit_id?}`；云效标准 `{task:{pipelineId,pipelineName,buildNumber,statusCode},sources:[{branch,commitId}],globalParams:[{key,value}]}`（取 APP_NAME/IMAGE_FULL/IMAGE_DIGEST/TARGET_CLUSTER，大小写不敏感；statusCode≠SUCCESS → 202 `{"ignored":true,"status"}`）。
  - 成功 → 202 `{"release_id","duplicate":false,"test_log_id","status"}`；重复事件（`sha256(source:eventID:targetCluster)`）→ 200 `{"release_id","duplicate":true,"test_log_id"}`，不重复排队。
  - 校验：event_id/target_cluster/app_name/image_full 必填，image_full 不含空白且可解析版本（`:tag` 或 `@sha256:` ≥12 hex），列超长 400；目标集群必须是 `enabled + non_production + automatic`，违反 400；集群/应用映射不存在 404。
- `GET /releases?app_name=&limit=` → 200 `[ImageReleaseView]`（release 字段 + `test_status/test_cluster_name/test_current_step/test_error/prod_status/prod_current_step/prod_error/can_promote`），按 id 倒序；limit 默认 50 范围 1–200。`can_promote = test_status==success && (prod_status==null || prod_status=="failed")`。
- `GET /releases/:id/promotion-targets` → 200 `[clusterOut]`；404 发布单不存在。仅列 `enabled + production + manual` 且存在同名应用映射的集群。
- `GET /releases/:id/current-image?cluster_id=` → 200 `{"image":"repo/app:tag"}`——发布单 app 在目标集群当前运行的镜像（晋级对话框"当前 vs 待发布"对比）。`cluster_id` 缺失/≤0 → 400；发布单不存在或该集群无此 app 映射 → 404；集群不可达/K8s 报错 → 502。
- `POST /releases/:id/promote` body `{"cluster_id"}` → 202 `{"release_id","prod_log_id","status"}`。
  - 门禁：测试发布必须已 success（否则 400）；目标必须 `enabled + production + manual` 且存在应用映射（400）；集群/发布单不存在 404；已有 pending/running/success 的生产发布 409（failed 可重试）。**生产发布复用发布单原 `image_full`，禁止手填镜像**。

## deploy.go — 文件发布

- `POST /deploy` body `{cluster_id,file_name,deployment_name,container_name,version,namespace?}` → 202 `{"log_id","status":"pending","message","steps":null}`。
  - 必填字段去空白后非空，缺失 400；`namespace` 默认 `default`；`file_name` 必须是 TRANSFER_DIR 内的纯文件名（拒绝 `..`/绝对路径），400；集群不存在 404；任务提交失败 500（日志标记 failed）。
- `GET /deploy/logs?cluster_id=&app_name=&status=&limit=` → 200 `[deployLogOut]`：`{id,cluster_id,app_name,deployment_name,container_name,namespace,version,image_full,deploy_type,status,current_step,step_detail,error_message,started_at,finished_at,created_at}`，按 id 倒序；limit 默认 20 范围 1–100。
- `GET /deploy/logs/:id` → 200 单条 `deployLogOut`（发布详情 drawer 的数据源）；404 记录不存在。
- `step_detail` 为 JSON 字符串：`[{step,success,stdout,stderr}]`。步骤名：`check_file/docker_load/docker_tag/docker_push`（仅文件发布）、`query_current`（仅回滚）、`k8s_set_image`、**`rollout_wait`（`set image` 后等待 Deployment 滚动完成；镜像拉不下来/Pod 未就绪会让发布失败而非假"成功"，等待上限 120s）**。

## rollback.go — 回滚

- `POST /rollback` body `{cluster_id,app_name,deployment_name,container_name,namespace,image_full,version}`（全部必填）→ 202 `{"log_id","status","message"}`；400 缺字段；404 集群不存在。
- `GET /rollback/history/:app_name?cluster_id=&limit=` → 200 `[{id,version,image_full,deploy_type,finished_at}]`，仅 status=success；limit 默认 10 范围 1–50。

## apps.go — 应用映射

- `GET /apps?cluster_id=` → 200 `[App]`（`{id,cluster_id,app_name,match_keyword,deployment_name,container_name,namespace,harbor_project,description,created_at}`）；cluster_id 缺省返回全部。
- `POST /apps` → 200 `App`；必填 `cluster_id≥1,app_name,match_keyword,deployment_name,container_name`；`namespace` 默认 `default`、`harbor_project` 默认 `library`；同 cluster 内 `app_name` 重复 409。
- `PUT /apps/:id` → 200 `App`；400 非法 id/缺字段；404 不存在；409 重名。
- `DELETE /apps/:id` → 200 `{"message":"应用 <name> 已删除"}`；404 不存在。

## openapi.go — 对外 OpenAPI（API 密钥）

给外部自动化系统的版本化接口，规范定义见 `docs/openapi.yaml`。鉴权独立于浏览器会话：`Authorization: Bearer dp_<token>` 或 `X-Api-Key: dp_<token>`；密钥无效/禁用/过期 → 401 `{"error":"无效或已过期的 API 密钥"}`。解析成功后置合成用户 `apikey:<密钥名>`——`requireRole` 角色门禁与 `audit()` 审计原样生效，外部写操作按密钥名落审计。

- `GET /openapi/v1/releases?app_name=&limit=` → 200 `[ImageReleaseView]`（viewer+）。
- `GET /openapi/v1/releases/:id` → 200 `ImageReleaseView`；404。
- `GET /openapi/v1/releases/:id/promotion-targets` → 200 `[clusterOut]`（viewer+）。
- `GET /openapi/v1/deploy-logs/:id` → 200 `deployLogOut`（viewer+）；404。
- `POST /openapi/v1/releases/:id/promote` body `{"cluster_id"}` → 202；operator+；与浏览器端同一处理函数（测试未成功 400 / 目标不合法 400 / 重复发布 409）。
- `POST /openapi/v1/rollback` body 同 `/api/v1/rollback` → 202；operator+。

**密钥管理**（`/api/v1` session + admin，不进 OpenAPI）：

- `GET /api-keys` → 200 `[APIKey]`（`{id,name,prefix,role,enabled,expires_at,last_used_at,created_at,updated_at}`；`key_hash` 永不返回）。
- `POST /api-keys` body `{name,role?,expires_at?}` → 201 密钥对象 + `key` 明文**仅此一次**；name 必填 ≤64、role 仅 viewer|operator（admin 不给自动化）、expires_at 为 RFC3339。
- `PUT /api-keys/:id` → 200 局部更新（name/role/enabled/expires_at，expires_at 传空串清除）。
- `DELETE /api-keys/:id` → 200；密钥立即失效。
- 存储：明文为 `dp_<32B base64url>`（43 字符），库中只存 sha256 hash + 前缀 `dp_xxxxxxxx`；内存模式 ID 从 1 懒初始化。`apikey_create/update/delete` 均落审计。

## files.go — 传输文件

- `GET /files?cluster_id=` → 200 `[{file_name,size_mb,modified_at("YYYY-MM-DD HH:MM:SS"),matched_app(App|null)}]`；目录不存在返回 `[]`；按 mtime 倒序；`matched_app` 为 `match_keyword` 被文件名包含的第一个应用。
- `GET /files/parse-version?file_name=` → 200 `{"version"}`；缺参数 400。规则：14 位时间戳优先返回 `v<ts>`，其次 `v?x.y(.z)` → `v<x.y>`，否则 `""`。

## 安全约束汇总

1. KubeConfig/Token 经 AES-256-GCM 加密入库（`v1:` 前缀密文），API 仅暴露 `credential_configured`。
2. kubeconfig 禁止 `exec`、`auth-provider`、`tokenFile`、宿主机证书/密钥路径。
3. Webhook token 常量时间比较；非 mock 模式未配置 token 拒绝服务。
4. 文件名限制在 TRANSFER_DIR 内，管道执行前还会拒绝符号链接。
5. 所有写接口先持久化后排队；持久化失败不污染内存状态。
6. CD 环境门禁不可绕：Webhook 只接受 `enabled+non_production+automatic`，生产晋级只接受 `enabled+production+manual` 且复用同一 `image_full`。
7. 密码仅 bcrypt 存储；会话 token 为随机 256-bit，仅存散列；审计不记录 Secret 数据、Token、KubeConfig 或完整 manifest。
8. 平台角色与 K8s RBAC 双层鉴权：角色门禁拒绝路由级越权，impersonation 让 apiserver 成为最终执行点——绕过前端隐藏按钮直接调 API 仍被两端拒绝。
9. API 密钥只存 sha256 hash + 前缀，明文仅在创建响应返回一次；密钥 role 限定 viewer/operator（admin 只保留给浏览器会话）；OpenAPI 写操作经同一 `requireRole` 与审计链路，无法绕过晋级/回滚门禁。

## E2E 验证（步骤 12）

`KB_E2E=1 go test ./e2e/ -v`：本地 Kind + MySQL 容器（专用 `deploy_platform_e2e` 库，每轮重建）上跑完整序列——启动真实二进制 → 接入集群 → 浏览 → 建 Deployment → scale/image → 日志 → exec → 改 YAML → 建 PVC（dry-run/409）→ 建 ConfigMap（dry-run/409）→ 建 Secret（stringData 写入、API 响应与详情均 `***` 遮蔽、kubectl 可见真实 base64）→ 数据编辑（CM 合并保留未动键、stale rv → 409；Secret set 转 base64、delete 生效、响应遮蔽）→ API 删除。所有 K8s 对象限定 `kbtest-e2e` namespace，cleanup 删整个 ns，可重复执行零残留。
