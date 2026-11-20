# ADR-0001: Persist Worker ownership while retaining the single-replica limit

Status: Accepted (2026-10-13)

## Context

The Worker queue is a wake-up hint over durable pending deploy logs. Each API process has its own in-memory Store and runtime caches. Login sessions, user/role authorization reads, API key validation/admin reads, and recent audit reads now use MySQL as the request-time authority, but task discovery and startup recovery still depend on process-local Store state. Startup recovery treats running tasks as potentially ambiguous because an external Kubernetes or gateway write may have happened before the process stopped. There is no lease expiry or heartbeat that can distinguish a dead Worker from a slow Worker.

A conditional `pending -> running` update prevents duplicate execution in MySQL, but the old claim API did not identify its owner. If two Stores raced, the losing Store could interpret the already-running row as a lost acknowledgement and mark the active task interrupted. A stale process also needed a durable way to prove it still owned the task before writing checkpoints or action readbacks.

## Decision

Add a random per-attempt `worker_claim_id` to `deploy_logs` in migration 025. Claiming runs in a transaction with a task-row lock. A matching claim ID makes the operation idempotent; a different owner is never overwritten. Worker checkpoint, versioned run-transition, and external-action journal writes carry the claim ID and fail if the durable row no longer matches. Terminal transitions, explicit ownership release, and recovery into `needs_attention` clear the claim.

Keep the Helm `replicaCount=1` hard limit. Migration 025 fences task writes and fixes the competing-claim race; it does not make the service safe to scale horizontally.

## Consequences

- A competing Store that sees an active claim leaves the task running and does not execute it.
- A stale Worker cannot overwrite task checkpoints or append external-action intent/readback after its claim has been replaced or cleared.
- A process crash leaves a non-expiring claim on a running row. Existing single-process startup recovery still marks ambiguous running tasks interrupted for operator reconciliation.
- Multi-replica operation remains unsupported while startup recovery, task discovery, and process-local runtime state are not coordinated across instances.

## Follow-up findings (2026-10-15)

Shared authentication and audit reads have been implemented and verified with two independent Stores against one MySQL database. They remove those request-time caches as sources of authorization disagreement, but do not coordinate Worker startup or recovery.

Do not add lease expiry alone and automatically requeue an expired `running` row. A database claim ID fences stale database writes, but Kubernetes and gateway APIs do not understand that token; an old process may still have an external request in flight after losing its database lease. An expired owner must therefore invalidate its database claim and move the run to an explicit reconciliation state while retaining its resource locks. It must not replay an ambiguous external action automatically. Recovery can release the locks only after live resource readback confirms a safe state.

Before lifting the Helm replica limit, make pending-task discovery and recovery database-authoritative, coordinate startup so one process cannot interrupt another live owner, define lease renewal and loss behavior, and verify that every external action is fenced or treated as unknown on owner loss. Exercise two live Workers, stale-owner writes, process termination during each external write, MySQL disconnect/failover, and operator reconciliation in an isolated cluster. Keep the single-replica guard until those checks pass.

## Conditions before lifting the replica limit

Design and test expiring leases with heartbeats and recovery that can distinguish live owners from abandoned claims; make task discovery and recovery database-authoritative; coordinate startup and preserve ambiguity around unfenced external writes; then run competing-worker, stale-owner, process-kill, reconciliation, and database-failover drills before changing the Helm guard.
