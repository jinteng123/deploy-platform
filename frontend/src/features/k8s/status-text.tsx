import { cn } from '@/lib/utils'

type Tone = 'ok' | 'warn' | 'bad' | 'muted'

// statusTone maps common Kubernetes condition/phase values onto a traffic-light
// tone. Unknown values fall back to muted so new kinds never show a wrong color.
const STATUS_TONE: Record<string, Tone> = {
  Ready: 'ok',
  Running: 'ok',
  Active: 'ok',
  Succeeded: 'ok',
  Complete: 'ok',
  Completed: 'ok',
  Bound: 'ok',
  Available: 'ok',
  Normal: 'ok',
  Pending: 'warn',
  ContainerCreating: 'warn',
  PodInitializing: 'warn',
  Terminating: 'warn',
  Warning: 'warn',
  Suspended: 'warn',
  Released: 'warn',
  NotReady: 'bad',
  Failed: 'bad',
  Error: 'bad',
  CrashLoopBackOff: 'bad',
  ImagePullBackOff: 'bad',
  ErrImagePull: 'bad',
  CreateContainerConfigError: 'bad',
  OOMKilled: 'bad',
  Evicted: 'bad',
  BackOff: 'bad',
  Lost: 'bad',
  Unhealthy: 'bad',
  FailedScheduling: 'bad',
  FailedMount: 'bad',
}

const DOT: Record<Tone, string> = {
  ok: 'bg-emerald-500',
  warn: 'bg-amber-500',
  bad: 'bg-red-500',
  muted: 'bg-muted-foreground/60',
}

// StatusText renders a kubectl-style status word with a colored dot — the dot
// aids scanning while the text still carries the meaning (not color-only).
export function StatusText({ value }: { value: string }) {
  const tone = STATUS_TONE[value] ?? 'muted'
  return (
    <span className='inline-flex items-center gap-1.5'>
      <span
        aria-hidden='true'
        className={cn('inline-block size-1.5 rounded-full', DOT[tone])}
      />
      {value}
    </span>
  )
}
