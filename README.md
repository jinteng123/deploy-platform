# Deploy Platform | Kubernetes 发布平台

A self-hosted Kubernetes deployment platform with a Go API, React console, and Helm chart. It supports digest-pinned image releases, approval-gated production deployments, rolling and blue-green workflows, and Kubernetes resource browsing.

这是一个可自行部署的 Kubernetes 发布与管理平台。当前主要后端位于 `backend-go/`；`backend/` 保留早期 Python 实现，作为旧版代码。

## Repository layout

- `backend-go/` — Go API and release worker
- `frontend/` — React web console
- `charts/deploy-platform/` — Helm chart
- `backend/` — legacy Python backend
- `docs/openapi.yaml` — external API contract
- `docs/cicd-operations-runbook.md` — deployment and operations guide

## Local development

Requirements: Go, Node.js with pnpm, Docker, `kind`, and `kubectl`.

```bash
# Start the local MySQL and Kind development infrastructure
make dev-infra

# Start the API (configure backend-go/.env first)
make backend

# In another terminal, start the web console
make frontend
```

See [backend setup](backend-go/README.md), the [user guide](USER-GUIDE.md), and the [operations runbook](docs/cicd-operations-runbook.md) for configuration and deployment details.

## License

This project is distributed under the GNU General Public License v3.0. See [LICENSE](LICENSE).
