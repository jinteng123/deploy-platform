import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { CircleAlert } from 'lucide-react'
import { toast } from 'sonner'
import { platformApi, type DeliveryEnvironment } from '@/api/client'
import { Button } from '@/components/ui/button'
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Input } from '@/components/ui/input'
import { Label } from '@/components/ui/label'
import { Textarea } from '@/components/ui/textarea'

function errorMessage(error: unknown) {
  return (
    (error as { response?: { data?: { detail?: string } } })?.response?.data
      ?.detail ?? '保存失败，请稍后重试。'
  )
}

const evidenceKindOptions = [
  { value: 'ci', label: 'CI 测试', description: '构建和自动化测试的结果' },
  { value: 'smoke', label: '部署后 Smoke', description: '目标版本的健康检查' },
  { value: 'manual', label: '人工验收', description: '由人工账号记录业务验收' },
] as const

type EvidenceKind = (typeof evidenceKindOptions)[number]['value']
type SubmitterDraft = Record<EvidenceKind, string>
type PolicyDraft = {
  requiredKinds: EvidenceKind[]
  maxAgeMinutes: string
  submitters: SubmitterDraft
}

function parseSubmitters(value: string) {
  return value
    .split(/\r?\n/)
    .map((identity) => identity.trim())
    .filter(Boolean)
}

function validSubmitter(kind: EvidenceKind, identity: string) {
  if (
    !identity ||
    identity === '*' ||
    Array.from(identity).length > 128 ||
    Array.from(identity).some((character) => {
      const code = character.codePointAt(0) ?? 0
      return code < 32 || code === 127
    })
  ) {
    return false
  }
  const runnerID = identity.startsWith('runner:')
    ? identity.slice('runner:'.length)
    : ''
  const runnerIdentity =
    /^[1-9][0-9]*$/.test(runnerID) &&
    (runnerID.length < 19 ||
      (runnerID.length === 19 && runnerID <= '9223372036854775807'))
  return kind === 'manual'
    ? !identity.startsWith('apikey:') && !identity.startsWith('runner:')
    : (identity.startsWith('apikey:') && identity.length > 'apikey:'.length) ||
        runnerIdentity
}

export function EnvironmentEvidencePolicyDialog({
  environment,
  onClose,
  onSaved,
}: {
  environment: DeliveryEnvironment
  onClose: () => void
  onSaved: () => void
}) {
  const queryClient = useQueryClient()
  const policyQuery = useQuery({
    queryKey: ['delivery-environment-evidence-policy', environment.id],
    queryFn: async () =>
      (await platformApi.deliveryEnvironmentEvidencePolicy(environment.id))
        .data,
  })
  const currentPolicy = policyQuery.data?.versions.find(
    (version) => version.version === policyQuery.data.current_version
  )
  const initialDraft: PolicyDraft = {
    requiredKinds: currentPolicy?.required_verification_kinds ?? [
      'ci',
      'smoke',
    ],
    maxAgeMinutes: String(currentPolicy?.max_evidence_age_minutes ?? 1440),
    submitters: {
      ci: currentPolicy?.trusted_submitters.ci?.join('\n') ?? '',
      smoke: currentPolicy?.trusted_submitters.smoke?.join('\n') ?? '',
      manual: currentPolicy?.trusted_submitters.manual?.join('\n') ?? '',
    },
  }
  const [draft, setDraft] = useState<PolicyDraft | null>(null)
  const form = draft ?? initialDraft
  const { requiredKinds, maxAgeMinutes, submitters: submitterDraft } = form
  const updateDraft = (update: (current: PolicyDraft) => PolicyDraft) => {
    setDraft((current) => update(current ?? initialDraft))
  }

  const parsedSubmitters = Object.fromEntries(
    requiredKinds.map((kind) => [kind, parseSubmitters(submitterDraft[kind])])
  ) as Partial<Record<EvidenceKind, string[]>>
  const sourceErrors = Object.fromEntries(
    requiredKinds.map((kind) => {
      const identities = parsedSubmitters[kind] ?? []
      const valid =
        identities.length > 0 &&
        identities.every((id) => validSubmitter(kind, id))
      return [kind, valid]
    })
  ) as Partial<Record<EvidenceKind, boolean>>
  const age = Number(maxAgeMinutes)
  const ageIsValid = Number.isInteger(age) && age >= 1 && age <= 7 * 24 * 60
  const valid =
    requiredKinds.length > 0 &&
    ageIsValid &&
    requiredKinds.every((kind) => sourceErrors[kind])

  const mutation = useMutation({
    mutationFn: () => {
      if (!policyQuery.data || !valid) {
        throw new Error('请补齐必需验证类型、有效时长和可信提交身份。')
      }
      return platformApi.saveDeliveryEnvironmentEvidencePolicy(environment.id, {
        expected_version: policyQuery.data.current_version,
        required_verification_kinds: requiredKinds,
        max_evidence_age_minutes: age,
        trusted_submitters: parsedSubmitters,
      })
    },
    onSuccess: async () => {
      toast.success('生产验证规则已保存为新版本')
      await queryClient.invalidateQueries({
        queryKey: ['delivery-environment-evidence-policy', environment.id],
      })
      onSaved()
      onClose()
    },
  })

  return (
    <Dialog
      open
      onOpenChange={(open) => !open && !mutation.isPending && onClose()}
    >
      <DialogContent className='max-h-[min(90dvh,52rem)] grid-rows-[auto_minmax(0,1fr)_auto] gap-0 overflow-hidden p-0 sm:max-w-2xl'>
        <DialogHeader className='px-6 pt-6 pb-4'>
          <DialogTitle>配置生产验证规则</DialogTitle>
          <DialogDescription>
            {environment.name} 的每种必需证据都必须对应同一镜像
            digest、结论通过、仍在有效期内，并由指定身份提交。修改会生成新版本；未配置前该环境不能申请生产发布。
          </DialogDescription>
        </DialogHeader>

        <div className='min-h-0 overflow-y-auto overscroll-contain px-6 pb-5'>
          {policyQuery.isPending ? (
            <p
              role='status'
              aria-live='polite'
              className='py-8 text-center text-sm text-muted-foreground'
            >
              正在读取当前规则…
            </p>
          ) : policyQuery.isError ? (
            <div
              role='alert'
              className='flex gap-2 rounded-lg border border-destructive/30 bg-destructive/5 p-3 text-sm text-destructive'
            >
              <CircleAlert
                className='mt-0.5 size-4 shrink-0'
                aria-hidden='true'
              />
              <span>
                无法读取生产验证规则。刷新页面后重试，避免覆盖其他管理员刚保存的版本。
              </span>
            </div>
          ) : (
            <div className='space-y-5'>
              <section className='space-y-3'>
                <div>
                  <h3 className='text-sm font-semibold'>必须提供哪些证据</h3>
                  <p className='mt-1 text-xs leading-5 text-muted-foreground'>
                    勾选项全部满足才可申请晋级。低风险环境可只要求
                    CI；需要运行时检查时再增加 Smoke。
                  </p>
                </div>
                <div className='grid gap-2 sm:grid-cols-3'>
                  {evidenceKindOptions.map((option) => {
                    const checked = requiredKinds.includes(option.value)
                    return (
                      <label
                        key={option.value}
                        className={`flex cursor-pointer gap-3 rounded-xl border p-3 transition-colors ${checked ? 'border-primary/50 bg-primary/5' : 'border-border bg-card hover:bg-muted/40'}`}
                      >
                        <input
                          type='checkbox'
                          className='mt-0.5 size-4 shrink-0 accent-primary'
                          checked={checked}
                          onChange={(event) => {
                            updateDraft((current) => ({
                              ...current,
                              requiredKinds: event.target.checked
                                ? [...current.requiredKinds, option.value]
                                : current.requiredKinds.filter(
                                    (kind) => kind !== option.value
                                  ),
                            }))
                          }}
                        />
                        <span className='min-w-0'>
                          <span className='block text-sm font-medium'>
                            {option.label}
                          </span>
                          <span className='mt-1 block text-xs leading-5 text-muted-foreground'>
                            {option.description}
                          </span>
                        </span>
                      </label>
                    )
                  })}
                </div>
              </section>

              <div className='grid gap-4 sm:grid-cols-[minmax(0,1fr)_minmax(0,1.3fr)]'>
                <div className='space-y-2'>
                  <Label htmlFor='evidence-max-age'>
                    证据最长允许多旧（分钟）
                  </Label>
                  <Input
                    id='evidence-max-age'
                    type='number'
                    className='min-h-11 sm:min-h-10'
                    min={1}
                    max={7 * 24 * 60}
                    step={1}
                    value={maxAgeMinutes}
                    aria-invalid={!ageIsValid}
                    aria-describedby='evidence-max-age-help'
                    onChange={(event) => {
                      updateDraft((current) => ({
                        ...current,
                        maxAgeMinutes: event.target.value,
                      }))
                    }}
                  />
                  <p
                    id='evidence-max-age-help'
                    className={`text-xs leading-5 ${ageIsValid ? 'text-muted-foreground' : 'text-destructive'}`}
                  >
                    1–10080 分钟；单份证据仍需在自身有效期内。
                  </p>
                </div>
                <div className='rounded-lg bg-muted/50 p-3 text-xs leading-5 text-muted-foreground'>
                  安全扫描目前只展示报告，尚未纳入生产阻断策略。此规则控制
                  CI、Smoke 和人工验收证据。
                </div>
              </div>

              <section className='space-y-3'>
                <div>
                  <h3 className='text-sm font-semibold'>可信提交身份</h3>
                  <p className='mt-1 text-xs leading-5 text-muted-foreground'>
                    每行填写一个精确身份。CI/Smoke 可使用 API 密钥身份
                    apikey:yunxiao-prod，或绑定专属 Runner 身份 runner:12；接入
                    ID 可在 Runner
                    配置中查看。人工验收使用平台用户名。通配符不允许。
                  </p>
                </div>
                <div className='grid gap-3 sm:grid-cols-2'>
                  {requiredKinds.map((kind) => {
                    const option = evidenceKindOptions.find(
                      (item) => item.value === kind
                    )!
                    const sourceIsValid = sourceErrors[kind]
                    return (
                      <div key={kind} className='space-y-2'>
                        <Label htmlFor={`evidence-source-${kind}`}>
                          {option.label} 来源
                        </Label>
                        <Textarea
                          id={`evidence-source-${kind}`}
                          rows={3}
                          value={submitterDraft[kind]}
                          placeholder={
                            kind === 'manual'
                              ? 'alice\nrelease-manager'
                              : 'apikey:yunxiao-prod\nrunner:12'
                          }
                          aria-invalid={!sourceIsValid}
                          onChange={(event) => {
                            updateDraft((current) => ({
                              ...current,
                              submitters: {
                                ...current.submitters,
                                [kind]: event.target.value,
                              },
                            }))
                          }}
                          className='font-mono text-xs'
                        />
                        {!sourceIsValid && (
                          <p className='text-xs text-destructive'>
                            {kind === 'manual'
                              ? '填写至少一个人工用户名，不可使用机器身份。'
                              : '填写 apikey:<密钥名称> 或 runner:<接入 ID> 身份。'}
                          </p>
                        )}
                      </div>
                    )
                  })}
                </div>
              </section>

              {mutation.isError && (
                <p role='alert' className='text-sm text-destructive'>
                  {errorMessage(mutation.error)}
                </p>
              )}
              {policyQuery.data.current_version > 0 && (
                <p className='text-xs text-muted-foreground'>
                  当前版本 v{policyQuery.data.current_version} ·
                  保存后新计划使用新规则，已有申请会因快照变化而失效。
                </p>
              )}
            </div>
          )}
        </div>

        <DialogFooter className='border-t bg-background px-6 py-4'>
          <Button
            variant='outline'
            className='min-h-11 sm:min-h-9'
            onClick={onClose}
            disabled={mutation.isPending}
          >
            取消
          </Button>
          <Button
            className='min-h-11 sm:min-h-9'
            onClick={() => mutation.mutate()}
            disabled={
              policyQuery.isPending ||
              policyQuery.isError ||
              !valid ||
              mutation.isPending
            }
          >
            {mutation.isPending ? '保存中…' : '保存规则'}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
