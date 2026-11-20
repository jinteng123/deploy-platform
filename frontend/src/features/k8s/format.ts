// Display formatting for cluster-overview quantities.

// formatCPU renders milliCPU the way kubectl top does: under one core
// stays in millicores, larger values switch to fractional cores.
export function formatCPU(millis: number): string {
  if (millis < 1000) return `${Math.round(millis)}m`
  return `${(millis / 1000).toFixed(2)}`.replace(/0+$/, '').replace(/\.$/, '')
}

// formatBytes renders bytes in the largest whole-ish unit (Ki/Mi/Gi),
// matching the capacity/usage figures Kubernetes reports.
export function formatBytes(bytes: number): string {
  if (bytes <= 0) return '0'
  const units = ['', 'Ki', 'Mi', 'Gi', 'Ti'] as const
  let v = bytes
  let u = 0
  while (v >= 1024 && u < units.length - 1) {
    v /= 1024
    u++
  }
  return `${v >= 100 || Number.isInteger(v) ? Math.round(v) : v.toFixed(1)}${units[u]}`
}

// formatPercent renders usage/capacity as a whole percent; 0 capacity
// avoids NaN.
export function formatPercent(usage: number, capacity: number): string {
  if (capacity <= 0) return '0%'
  return `${Math.round((usage / capacity) * 100)}%`
}
