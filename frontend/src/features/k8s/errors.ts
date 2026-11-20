// errDetail extracts the backend's `detail` field from an axios error,
// falling back to the generic message. Shared by the read and write UI.
export function errDetail(err: unknown): string {
  return (
    (err as { response?: { data?: { detail?: string } } })?.response?.data?.detail ??
    (err instanceof Error ? err.message : '请求失败')
  )
}

// isConflict reports a 409 — the object's resourceVersion moved under us.
export function isConflict(err: unknown): boolean {
  return (err as { response?: { status?: number } })?.response?.status === 409
}
