# deploy-platform Helm Chart

部署 Deploy Platform（CD 发布平台 + Kuboard 风格 K8s 控制台）。

## 前置条件

1. **镜像**：仓库根目录 `docker build -t <repo>/deploy-platform:<tag> .`（多阶段：React 静态资源 + Go 二进制）。构建容器需能访问 npm、Go module proxy 和 Alpine APK 源；内网或代理环境可用 `--build-arg GOPROXY=<可达的 Go module proxy>` 覆盖 Go 默认代理，Node/Go/Alpine 基础镜像也可通过对应 build args 指向镜像源。
2. **外部 MySQL 8.x**（不进 Chart）：全新库用 `scripts/apply-schema.sh <db>` 回放 `schema.sql` + 全部迁移（脚本统一处理迁移文件内 `USE` 行的目标库改写；非 docker 环境用 `MYSQL_CMD` 覆盖客户端调用）。该脚本会重放全部迁移，只用于全新库；已有库不要运行它，应先核对数据库已应用版本，再按顺序单独执行缺失的迁移文件，不能重放全量 ALTER。当前真实模式要求迁移升级到 036；后端读取事件因果判定字段，缺列会导致存储初始化失败。升级细节见 [CI/CD 运营手册](../../docs/cicd-operations-runbook.md)。
3. **Secrets**（全部外部创建，values 只引用名字，永不写明文）：
   - `mysql.existingSecret.name`：键 `password` = MySQL 账号口令。
   - `secrets.credentialEncryptionKey.secretName`：键 `credential-encryption-key` = base64 编码的 AES 密钥（`openssl rand -base64 32`）。
   - `secrets.yunxiaoWebhookToken.secretName`：键 `webhook-token` = 云效 webhook 鉴权 token（可省略，省略则 webhook 路由拒绝所有请求）。
   - `secrets.adminInitialPassword.secretName`：键 `admin-initial-password` = admin 首次登录口令。空数据库首次启动必须设置；已有用户时该值不参与登录验证，也不会重置账户。
   - 启用 `env.NOTIFICATION_WEBHOOK_URL` 时设置 `secrets.notificationWebhookSecret.secretName`，键 `notification-webhook-secret` 为至少 32 字节签名 secret；不在 values 保存明文。
   - 配置 `env.PLATFORM_PUBLIC_URL` 可让发布事件的 `task_url` 生成绝对链接，例如 `https://deploy.example.com`；地址应由通知接收人访问，并指向平台前端入口。留空时 payload 使用 `/tasks/logs/{log_id}` 相对路径。
   - `env.DRIFT_POLL_INTERVAL_SECONDS` 可启用只读镜像漂移后台观测，默认 `"0"` 关闭；启用范围为 60–86400 秒。任务使用平台集群执行身份，每集群每轮最多读取 48 个有成功部署基线的已登记 Deployment，每批并发上限为 8 个；更多目标跨轮扫描。它只写入平台漂移历史，不修改集群资源；启用前需确认执行身份具备目标 Deployment 读取权限。
   - 内网接收端使用私有 TLS CA 时，可将 PEM bundle 放入 ConfigMap，并设置 `notificationWebhookCA.configMapName` 和 `key`；Chart 只读挂载 CA，追加到系统信任根且继续校验证书。公网/系统已信任的证书无需配置。

## 安装

```bash
helm install dp charts/deploy-platform -n <ns> \
  --set image.repository=<repo>/deploy-platform \
  --set image.tag=<tag> \
  --set mysql.host=<mysql-host> \
  --set mysql.existingSecret.name=<db-secret> \
  --set secrets.credentialEncryptionKey.secretName=<app-secret> \
  --set secrets.yunxiaoWebhookToken.secretName=<app-secret> \
  --set secrets.adminInitialPassword.secretName=<app-secret>
```

Chart 会在首次安装和每次升级前运行一次只读凭据预检 Job。它使用本次 release 的镜像、MySQL 配置和 Secret，先验证数据库 schema、新数据库的初始管理员口令，以及当前密钥能否解密已有直连集群、云效和 Runner 凭据；检查通过后 Helm 才继续创建或替换后端 Deployment。数据库没有用户时必须提供 `ADMIN_INITIAL_PASSWORD`；数据库已有用户时可省略该 Secret，预检不会改动账户。检查失败会中止 Helm 操作，旧版本 Deployment 保持运行；失败 Job 会保留，可通过 `kubectl get jobs -n <ns> -l app.kubernetes.io/component=credential-preflight` 找到，再用 `kubectl logs -n <ns> job/<job-name>` 查看安全提示。重试前 Helm 会清除同名旧 Job；成功 Job 自动删除。外部 MySQL 应先按开发说明应用缺失迁移，再执行 Helm 安装/升级。

## 单副本约束（不可放开）

`replicaCount` 必须保持 **1**：Store 与任务队列均为进程内存实现，多副本会重复执行发布任务、分裂会话与审计状态。Deployment 已固定 `Recreate` 策略避免滚动期间双写。分布式改造完成后方可放开。

修改 Chart 后可运行 `make test-helm-chart`，验证 Chart lint、单副本成功渲染，以及多副本配置被模板硬拒绝。

在 Kind 测试集群中验证安装与升级钩子，可将本地构建镜像通过 `DEPLOY_PLATFORM_IMAGE` 传入：

```bash
DOCKER_CONTEXT=colima KUBE_CONTEXT=kind-kbdev DEPLOY_PLATFORM_IMAGE=deploy-platform:<tag> make test-helm-preflight-kind
```

该测试会把应用镜像和 `MYSQL_IMAGE`（默认 `mysql:8.4`）导入指定 Kind 集群，在随机 namespace 中创建临时 MySQL 和测试 Secret，使用 1 GiB 内存型 `emptyDir` 运行全量 schema/迁移；随后验证新库缺少管理员初始口令时安装会被阻止、配置后安装成功、坏凭据会阻止升级且旧 Service 继续服务、修复后升级成功。退出时删除整个临时 namespace。请只指定隔离的测试 Kind 集群；镜像须与节点架构相同。

## RBAC 分层（最小权限）

平台 Pod 的 ServiceAccount 只决定**宿集群**的权限面；其他目标集群的权限由各自 kubeconfig/token 凭据 + impersonation 决定（见步骤 9）。绝不默认 cluster-admin。

| 层 | 默认 | 覆盖能力 | 对应功能 |
|---|---|---|---|
| `rbac.read` | 开 | get/list/watch：pods、pods/log、services、configmaps、namespaces、nodes、PVC/PV、events、apps/batch/networking 工作负载、metrics.k8s.io | 资源浏览、概览、日志 |
| `rbac.write` | 关 | 上述资源的 update/patch/delete + `pods/exec` create + `secrets` get/list | 改副本/镜像、YAML 编辑、删除、Web 终端、Secret 显式查看 |

刻意从 read 层排除 `secrets`：Secret 显式查看属高敏感读，仅在开启 write 层时授予。对只读控制台场景，write 层保持关闭即可——apiserver RBAC 是最终执行点，平台角色门禁之外的越权调用仍会被拒绝。

## 安全默认值

- `runAsNonRoot`（uid 10001）、`readOnlyRootFilesystem`、`allowPrivilegeEscalation: false`、drop ALL capabilities
- 无 hostPath、无 hostNetwork、无 privileged；`/data/transfer_space` 与 `/tmp` 均为 emptyDir
- liveness 使用 `/health/live`，只判断进程可响应；readiness 使用 `/health/ready`，MySQL 不可达时返回 503 并停止接收 Service 流量；旧 `/health` 保留为 liveness 兼容入口。默认 requests 100m/256Mi、limits 500m/512Mi
- Ingress 默认关闭，显式开启并配 TLS 后对外暴露

## CI/CD 生产策略

真实运行始终独立人工审批，API Key 可以申请和提交报告但不能批准。生产 Deployment 的控制台直接写关闭，
旧文件发布仅非生产；回退只引用同环境成功的历史 digest，不能承诺恢复配置或数据库。
通知 URL 默认空；启用后只接受 HTTPS，支持从只读 ConfigMap 挂载内部 PEM CA，持久记录失败并退避重试，首次启用可能补发历史事件，接收端须按事件 ID 去重。
发布页漂移支持按需观测，也可通过 `DRIFT_POLL_INTERVAL_SECONDS` 开启后台镜像采集；启用出站 webhook 后，首次确认漂移和恢复一致会投递通知。配置完整对账、GitOps 与多副本未实现。滚动和原生蓝绿编排已接入代码；精确金丝雀仍需现有网关权重/回读/指标接口联调。平台不要求安装 Argo CRD。生产放量仍需真实 registry/MySQL/Kubernetes 验收与业务恢复演练。

云效内网 Runner 引导、组件专属通知授权与流水线预览/保存参见 [Runner 接入指南](../../docs/runner-integration.md)。迁移 015 为逻辑绑定保存蓝绿 Service 入口，016 为计划创建执行实例和阶段事件，017 为候选清理增加限时租约，018 保存模板/活动 Deployment 身份，019 回填并保存不可变策略版本，020 保存按需镜像漂移状态和历史，021 固定环境等级快照，022 固定金丝雀策略，023 固定环境验证证据策略，024 限制运行事件 payload，025 持久化 Worker claim 所有权，029 为组件绑定保存部署配置仓库/目录，030 为故障台账增加干预开始记录，031 新建按逻辑应用和生产环境限定 operator 审批资格的授权表，032 保存环境目标的 Yunxiao 配置部署流水线身份，033 持久化登录会话哈希。迁移不会自动授权现有 operator；管理员需在「设置 → 发布审批范围」配置。升级已有数据库时只执行缺失迁移；服务启动不会自动升级数据库。
