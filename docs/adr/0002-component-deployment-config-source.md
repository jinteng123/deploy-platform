# ADR-0002: Bind one deployment configuration source to each component mapping

Status: Accepted (2026-10-14)

## Context

The Runner could report a configuration Git repository, path, and commit for each image release, but the repository and path were entered independently on every build. Operators had no durable place to state which configuration repository belongs to a component, and an accidental per-build override could associate a valid image with an unrelated configuration tree.

The current component target is represented by one `apps` row. Code-source metadata already lives on this row, while logical delivery bindings tie equivalent environments to one logical component. The platform records provenance and plans; it does not clone repositories, execute cloud CI checkout, render Helm, or apply configuration.

## Decision

Add optional `deployment_config_repo_url` and `deployment_config_path` fields to the component mapping in migration 029. The repository must be HTTPS without embedded credentials, query parameters, or fragments. The path is relative to the repository and defaults to `.`. Code source remains separate.

When environments share a logical component, their deployment configuration repository and path must match. The generated Runner script uses the registered repository and path, verifies the CI checkout's Git origin and HEAD, confirms the registered directory exists in that checkout and commit and has no modified, untracked, or ignored files, then reports the commit it read. By default it checks the notification task's current directory; `DEPLOY_CONFIG_WORKDIR` points to a separate checkout. A preexisting `DEPLOY_CONFIG_REVISION` is an optional assertion and must equal HEAD. A caller-supplied repository or path that differs from the component mapping is rejected. If no component configuration repository is registered, existing per-build repository/path/revision variables remain supported for compatibility.

Each release keeps an immutable Runner-reported configuration revision, and the delivery-plan artifact snapshot copies the component mapping and reported revision. A configured component with no reported revision blocks plan creation. A component without a configured repository and without a report remains unknown and requires explicit human review.

## Consequences

- Operators can review the code repository, deployment configuration repository, and deployment target as distinct relationships in the component onboarding flow.
- A build no longer has to copy a commit SHA by hand when the component mapping is registered; the Runner reads it from the checkout. The existing CI pipeline still performs checkout, Helm rendering, and configuration apply.
- Mapping changes invalidate the Runner binding snapshot, so the integration must be regenerated against the current component target.
- The preflight proves only the Git identity and HEAD visible to that Runner script. It does not prove Helm rendered from that checkout or that Kubernetes applied the resulting configuration. Full configuration recovery and migration compatibility remain separate A5 work.
- If a component later needs multiple independently versioned configuration repositories, replace the two fields with an explicit many-to-one source model and extend the plan schema.
