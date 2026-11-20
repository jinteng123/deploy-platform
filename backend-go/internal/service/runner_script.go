package service

import (
	"fmt"
	"strings"

	"github.com/goccy/go-yaml"
)

type RunnerScripts struct {
	Script                        string   `json:"script"`
	CheckScript                   string   `json:"check_script"`
	VerificationScript            string   `json:"verification_script"`
	ConfigurationVariables        []string `json:"configuration_variables"`
	TaskYAML                      string   `json:"task_yaml"`
	ReleaseIDFile                 string   `json:"release_id_file"`
	SecretVariable                string   `json:"secret_variable"`
	RequiredVariables             []string `json:"required_variables"`
	VerificationRequiredVariables []string `json:"verification_required_variables"`
}

// Shell literals are always quoted, including the user-chosen platform URL.
func runnerShellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
func GenerateRunnerScripts(b RunnerBinding) (RunnerScripts, error) {
	if _, err := validateRunnerURL(b.PlatformURL); err != nil {
		return RunnerScripts{}, err
	}
	if !runnerPipelineID.MatchString(b.PipelineID) || !runnerIdentifier.MatchString(b.PrivateClusterID) {
		return RunnerScripts{}, NewValidationError("无效的流水线配置")
	}
	configurationVariables := []string{"DEPLOY_CONFIG_REPOSITORY", "DEPLOY_CONFIG_REVISION", "DEPLOY_CONFIG_PATH"}
	requiredConfigurationVariables := []string{}
	configurationSetup := `config_repository="${DEPLOY_CONFIG_REPOSITORY:-}"
config_revision="${DEPLOY_CONFIG_REVISION:-}"
config_path="${DEPLOY_CONFIG_PATH:-.}"
if [ -n "${DEPLOY_CONFIG_PATH:-}" ] && [ -z "$config_repository" ]; then echo '配置目录需要同时指定 DEPLOY_CONFIG_REPOSITORY' >&2; exit 1; fi
if [ -n "$config_repository" ] && [ -z "$config_revision" ]; then echo '配置仓库需要同时指定 DEPLOY_CONFIG_REVISION' >&2; exit 1; fi
if [ -z "$config_repository" ] && [ -n "$config_revision" ]; then echo '配置 revision 需要同时指定 DEPLOY_CONFIG_REPOSITORY' >&2; exit 1; fi
if [ -n "$config_repository" ]; then
  case "$config_revision" in ''|*[!0-9a-fA-F]*) echo '配置 revision 必须是完整 Git commit SHA' >&2; exit 1 ;; esac
  if [ "${#config_revision}" -ne 40 ] && [ "${#config_revision}" -ne 64 ]; then echo '配置 revision 必须是完整 40 或 64 位 Git commit SHA' >&2; exit 1; fi
fi
`
	if b.Binding.DeploymentConfigRepoURL != "" {
		repository, repositoryPath, err := normalizeDeploymentConfigSource(b.Binding.DeploymentConfigRepoURL, b.Binding.DeploymentConfigPath)
		if err != nil {
			return RunnerScripts{}, err
		}
		configurationVariables = []string{"DEPLOY_CONFIG_WORKDIR"}
		configurationSetup = fmt.Sprintf(`bound_config_repository=%s
bound_config_path=%s
if [ -n "${DEPLOY_CONFIG_REPOSITORY:-}" ] && [ "$DEPLOY_CONFIG_REPOSITORY" != "$bound_config_repository" ]; then echo '配置仓库与组件登记不一致' >&2; exit 1; fi
if [ -n "${DEPLOY_CONFIG_PATH:-}" ] && [ "$DEPLOY_CONFIG_PATH" != "$bound_config_path" ]; then echo '配置目录与组件登记不一致' >&2; exit 1; fi
config_repository="$bound_config_repository"
config_path="$bound_config_path"
config_revision=$(python3 - "$bound_config_repository" "$bound_config_path" "${DEPLOY_CONFIG_WORKDIR:-.}" "${DEPLOY_CONFIG_REVISION:-}" <<'PY'
import os,re,subprocess,sys
from urllib.parse import urlsplit,unquote

bound,relative_path,workdir,expected_revision=sys.argv[1:]
def fail(message):
    print(message,file=sys.stderr)
    raise SystemExit(1)
def git(*args):
    try:
        return subprocess.run(['git','-C',workdir,*args],check=True,stdout=subprocess.PIPE,stderr=subprocess.DEVNULL,text=True).stdout.strip()
    except (OSError,subprocess.CalledProcessError):
        fail('无法从配置 checkout 读取 Git 信息，请检查 DEPLOY_CONFIG_WORKDIR')
def normalize_repository(raw):
    raw=raw.strip()
    scp_match=re.match(r'^(?:[^/@:]+@)?([^/:]+):(.+)$',raw)
    if raw.startswith('ssh://'):
        parsed=urlsplit(raw)
        scheme='https'
        host=parsed.hostname or ''
        path=parsed.path
        port=None
    elif '://' not in raw and scp_match:
        host,path=scp_match.groups()
        scheme='https'
        port=None
    else:
        parsed=urlsplit(raw)
        scheme=parsed.scheme.lower()
        host=parsed.hostname or ''
        path=parsed.path
        try:
            port=parsed.port
        except ValueError:
            return None
        if parsed.query or parsed.fragment:
            return None
    if scheme != 'https' or not host:
        return None
    host=host.lower()
    if port and port != 443:
        host=f'{host}:{port}'
    path=unquote(path).strip('/')
    if not path or any(part in ('.','..') for part in path.split('/')):
        return None
    if path.endswith('.git'):
        path=path[:-4]
    return f'https://{host}/{path}'

try:
    expected=normalize_repository(bound)
    actual=normalize_repository(git('remote','get-url','origin'))
    if expected is None or actual != expected:
        fail('配置 checkout 的 origin 与组件登记仓库不一致')
    root=os.path.realpath(git('rev-parse','--show-toplevel'))
    config_dir=os.path.realpath(os.path.join(root,relative_path))
    if os.path.commonpath([root,config_dir]) != root or not os.path.isdir(config_dir):
        fail('组件登记的配置目录不存在或越出 checkout 仓库')
    tree_object='HEAD^{tree}' if relative_path == '.' else f'HEAD:{relative_path}'
    if git('cat-file','-t',tree_object) != 'tree':
        fail('组件登记的配置目录不属于当前 checkout commit')
    if git('status','--porcelain','--untracked-files=all','--ignored','--',relative_path):
        fail('配置目录存在未提交或忽略文件，无法与 commit 对应')
    revision=git('rev-parse','HEAD').lower()
    if not re.fullmatch(r'(?:[0-9a-f]{40}|[0-9a-f]{64})',revision):
        fail('配置 checkout 未返回完整 Git commit SHA')
    if expected_revision and (not re.fullmatch(r'(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})',expected_revision) or expected_revision.lower() != revision):
        fail('DEPLOY_CONFIG_REVISION 与实际 checkout HEAD 不一致')
    print(revision)
except (OSError,ValueError):
    fail('配置 checkout 验证失败；请检查仓库地址、目录和 Git 状态')
PY
)
`, runnerShellQuote(repository), runnerShellQuote(repositoryPath))
	} else if b.Binding.DeploymentConfigPath != "" {
		return RunnerScripts{}, NewValidationError("部署配置目录不能脱离配置仓库单独设置")
	}
	secret := fmt.Sprintf("DEPLOY_RUNNER_TOKEN_%d", b.ID)
	requiredVariables := append([]string{secret, "DEPLOY_IMAGE_FULL", "DEPLOY_IMAGE_DIGEST"}, requiredConfigurationVariables...)
	header := fmt.Sprintf(`#!/usr/bin/env bash
set +x
set -euo pipefail
umask 077
: "${%s:?请配置组件专属私密变量 %s}"
: "${PIPELINE_ID:?缺少云效流水线 ID}"
if [ "$PIPELINE_ID" != %s ]; then echo '流水线与接入配置不匹配' >&2; exit 1; fi
`, secret, secret, runnerShellQuote(b.PipelineID))
	script := header + fmt.Sprintf(`: "${BUILD_NUMBER:?缺少构建编号}"
: "${DEPLOY_IMAGE_FULL:?请传入本次推送的完整镜像地址}"
: "${DEPLOY_IMAGE_DIGEST:?请传入本次推送的 sha256 digest}"
%s
body_file=$(mktemp)
response_file=$(mktemp)
trap 'rm -f "$body_file" "$response_file"' EXIT
commit_timestamp="${CI_COMMIT_TIMESTAMP:-}"
if [ -z "$commit_timestamp" ] && [ -n "${CI_COMMIT_SHA:-}" ] && command -v git >/dev/null 2>&1; then
  commit_timestamp=$(git show -s --format=%%cI "$CI_COMMIT_SHA" 2>/dev/null || true)
fi
python3 - "$PIPELINE_ID" "$BUILD_NUMBER" "$DEPLOY_IMAGE_FULL" "$DEPLOY_IMAGE_DIGEST" "${CI_COMMIT_SHA:-}" "$commit_timestamp" > "$body_file" <<'PY'
import json,sys
json.dump(dict(zip(['pipeline_id','build_number','image_full','image_digest','commit_id','commit_timestamp'],sys.argv[1:])),sys.stdout)
PY
curl --silent --show-error --fail-with-body --connect-timeout 10 --max-time 30 \
  --retry 5 --retry-all-errors --retry-delay 3 \
  -H "Authorization: Bearer ${%s}" -H 'Content-Type: application/json' \
  --data-binary "@$body_file" --output "$response_file" %s
cat "$response_file"
echo
release_id=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["release_id"])' "$response_file")
release_id_file="${DEPLOY_RELEASE_ID_FILE:-deploy-release-id.txt}"
printf '%%s\n' "$release_id" > "$release_id_file"
echo "release_id=$release_id"
if [ -n "$config_repository" ]; then
  python3 - "$config_repository" "$config_path" "$config_revision" > "$body_file" <<'PY'
import json,sys
json.dump({'repository_url':sys.argv[1],'path':sys.argv[2],'revision':sys.argv[3]},sys.stdout)
PY
  curl --silent --show-error --fail-with-body --connect-timeout 10 --max-time 30 \
    --retry 5 --retry-all-errors --retry-delay 3 \
    -H "Authorization: Bearer ${%s}" -H 'Content-Type: application/json' \
    --data-binary "@$body_file" --output "$response_file" %s"/$release_id/configuration"
  cat "$response_file"
  echo
fi
# Set DEPLOY_WAIT=0 to return once accepted; default waits for rollout.
if [ "${DEPLOY_WAIT:-1}" = 1 ]; then
  for attempt in $(seq 1 90); do
    curl --silent --show-error --fail-with-body --connect-timeout 10 --max-time 20 \
      --retry 3 --retry-all-errors --retry-delay 2 \
      -H "Authorization: Bearer ${%s}" --output "$response_file" %s"/$release_id"
    status=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["status"])' "$response_file")
    case "$status" in
      success) echo '镜像发布成功'; exit 0 ;;
      failed|interrupted) cat "$response_file"; exit 1 ;;
    esac
    sleep 5
  done
  echo '等待发布结果超时，请在平台发布中心查看；不要更换构建编号重试' >&2
  exit 1
fi
`, configurationSetup, secret, runnerShellQuote(fmt.Sprintf("%s/openapi/v1/runner/bindings/%d/events", b.PlatformURL, b.ID)), secret, runnerShellQuote(fmt.Sprintf("%s/openapi/v1/runner/bindings/%d/releases", b.PlatformURL, b.ID)), secret, runnerShellQuote(fmt.Sprintf("%s/openapi/v1/runner/bindings/%d/releases", b.PlatformURL, b.ID)))
	check := header + fmt.Sprintf(`curl --silent --show-error --fail-with-body --connect-timeout 10 --max-time 30 \
  -H "Authorization: Bearer ${%s}" -H 'Content-Type: application/json' \
  --data %s %s
`, secret, runnerShellQuote(fmt.Sprintf(`{"pipeline_id":%q}`, b.PipelineID)), runnerShellQuote(fmt.Sprintf("%s/openapi/v1/runner/bindings/%d/check", b.PlatformURL, b.ID)))
	verification := header + fmt.Sprintf(`release_id="${RELEASE_ID:-}"
if [ -z "$release_id" ]; then
  release_id_file="${DEPLOY_RELEASE_ID_FILE:-deploy-release-id.txt}"
  if [ -r "$release_id_file" ]; then IFS= read -r release_id < "$release_id_file"; fi
fi
: "${release_id:?请配置 RELEASE_ID 或传入 deploy-release-id.txt 产物文件}"
RELEASE_ID="$release_id"
: "${DEPLOY_EVIDENCE_KIND:?请指定 ci、smoke 或 scan}"
evidence_status="${DEPLOY_EVIDENCE_STATUS:-passed}"
valid_hours="${DEPLOY_EVIDENCE_VALID_HOURS:-24}"
case "$RELEASE_ID" in ''|*[!0-9]*|0*) echo 'release_id 必须为正整数' >&2; exit 1 ;; esac
case "$DEPLOY_EVIDENCE_KIND" in
  ci|smoke) evidence_path=verification ;;
  scan)
    evidence_path=scan
    : "${DEPLOY_SCAN_TOOL:?扫描报告需要 DEPLOY_SCAN_TOOL}"
    : "${DEPLOY_SCAN_TOOL_VERSION:?扫描报告需要 DEPLOY_SCAN_TOOL_VERSION}"
    : "${DEPLOY_SCAN_RULES_VERSION:?扫描报告需要 DEPLOY_SCAN_RULES_VERSION}"
    ;;
  *) echo '证据类型只能为 ci、smoke 或 scan' >&2; exit 1 ;;
esac
case "$evidence_status" in passed|failed) ;; *) echo '证据状态只能为 passed 或 failed' >&2; exit 1 ;; esac
case "$valid_hours" in ''|*[!0-9]*) echo '有效时长必须为 1–168 小时' >&2; exit 1 ;; esac
if [ "$valid_hours" -lt 1 ] || [ "$valid_hours" -gt 168 ]; then echo '有效时长必须为 1–168 小时' >&2; exit 1; fi
body_file=$(mktemp)
response_file=$(mktemp)
trap 'rm -f "$body_file" "$response_file"' EXIT
python3 - "$DEPLOY_EVIDENCE_KIND" "$evidence_status" "${DEPLOY_EVIDENCE_REPORT_URL:-}" "${DEPLOY_EVIDENCE_SUMMARY:-}" "$valid_hours" "${DEPLOY_SCAN_TOOL:-}" "${DEPLOY_SCAN_TOOL_VERSION:-}" "${DEPLOY_SCAN_RULES_VERSION:-}" > "$body_file" <<'PY'
import json,sys
from datetime import datetime,timedelta,timezone
kind,status,url,summary,hours,tool,tool_version,rules_version=sys.argv[1:]
body={'status':status,'valid_until':(datetime.now(timezone.utc)+timedelta(hours=int(hours))).isoformat(timespec='seconds').replace('+00:00','Z')}
if kind != 'scan': body['kind']=kind
if url: body['report_url']=url
if summary: body['summary']=summary
if kind == 'scan': body.update(tool=tool,tool_version=tool_version,rules_version=rules_version)
json.dump(body,sys.stdout,ensure_ascii=False)
PY
curl --silent --show-error --fail-with-body --connect-timeout 10 --max-time 30 \
  --retry 5 --retry-all-errors --retry-delay 3 \
  -H "Authorization: Bearer ${%s}" -H 'Content-Type: application/json' \
  --data-binary "@$body_file" --output "$response_file" %s"/$RELEASE_ID/$evidence_path"
cat "$response_file"
echo
if [ "$evidence_status" = failed ] && [ "$DEPLOY_EVIDENCE_KIND" != scan ]; then echo "$DEPLOY_EVIDENCE_KIND 验证失败，失败证据已记录" >&2; exit 1; fi
if [ "$DEPLOY_EVIDENCE_KIND" = scan ] && [ "$evidence_status" = failed ]; then echo '扫描报告已记录；扫描状态当前仅作报告，不阻断发布'; fi
`, secret, runnerShellQuote(fmt.Sprintf("%s/openapi/v1/runner/bindings/%d/releases", b.PlatformURL, b.ID)))
	stage := RunnerNotificationStage(b, script)
	raw, err := yaml.MarshalWithOptions(map[string]any{"stages": map[string]any{fmt.Sprintf("deploy_platform_%d", b.ID): stage}}, yaml.UseLiteralStyleIfMultiline(true))
	if err != nil {
		return RunnerScripts{}, err
	}
	return RunnerScripts{
		Script: script, CheckScript: check, VerificationScript: verification,
		ConfigurationVariables: configurationVariables,
		TaskYAML:               string(raw), ReleaseIDFile: "deploy-release-id.txt", SecretVariable: secret,
		RequiredVariables:             requiredVariables,
		VerificationRequiredVariables: []string{secret, "DEPLOY_EVIDENCE_KIND"},
	}, nil
}
func RunnerNotificationStage(b RunnerBinding, script string) map[string]any {
	return map[string]any{"name": "通知部署平台：" + b.Binding.AppName, "jobs": map[string]any{"notify": map[string]any{"name": "上报镜像并等待发布结果", "runsOn": map[string]any{"group": "private/" + b.PrivateClusterID}, "steps": map[string]any{"notify": map[string]any{"name": "发布绑定组件", "step": "Command", "with": map[string]any{"run": script}}}}}}
}
