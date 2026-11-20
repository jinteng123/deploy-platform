// age renders a timestamp like kubectl's AGE column: "45s", "12m",
// "3h20m", "5d3h". Empty/invalid input renders "-".
export function age(timestamp: string | undefined, now: Date = new Date()): string {
  if (!timestamp) return '-'
  const then = new Date(timestamp)
  if (Number.isNaN(then.getTime())) return '-'
  let seconds = Math.max(0, Math.floor((now.getTime() - then.getTime()) / 1000))
  const days = Math.floor(seconds / 86400)
  seconds -= days * 86400
  const hours = Math.floor(seconds / 3600)
  seconds -= hours * 3600
  const minutes = Math.floor(seconds / 60)
  seconds -= minutes * 60
  if (days > 0) return `${days}d${hours}h`
  if (hours > 0) return `${hours}h${minutes}m`
  if (minutes > 0) return `${minutes}m`
  return `${seconds}s`
}
