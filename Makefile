.PHONY: dev-infra dev-mysql dev-kind dev-down backend frontend test-helm-chart test-helm-preflight-kind

dev-infra: dev-mysql dev-kind

dev-mysql:
	docker start deploy-platform-mysql 2>/dev/null || docker run -d --name deploy-platform-mysql --restart unless-stopped -e MYSQL_ROOT_PASSWORD=deploy-platform -e MYSQL_DATABASE=deploy_platform -e MYSQL_USER=deploy -e MYSQL_PASSWORD=deploy-platform -e TZ=Asia/Shanghai -p 3306:3306 -v deploy-platform-mysql:/var/lib/mysql -v $(CURDIR)/backend-go/schema.sql:/docker-entrypoint-initdb.d/01-schema.sql:ro -v $(CURDIR)/backend-go/seed_apps.sql:/docker-entrypoint-initdb.d/02-seed.sql:ro -v $(CURDIR)/infra/mysql/03-local-cluster.sql:/docker-entrypoint-initdb.d/03-local-cluster.sql:ro $(foreach migration,$(sort $(wildcard $(CURDIR)/backend-go/migrations/*.sql)),-v $(migration):/docker-entrypoint-initdb.d/10-$(notdir $(migration)):ro) mysql:8.4

dev-kind:
	kind get clusters | grep -qx deploy-platform || kind create cluster --config infra/kind-config.yaml

dev-down:
	docker stop deploy-platform-mysql || true
	kind delete cluster --name deploy-platform

backend:
	cd backend-go && go run ./cmd/server

frontend:
	cd frontend && pnpm dev

test-helm-chart:
	./scripts/test-helm-chart.sh

test-helm-preflight-kind:
	./scripts/test-helm-preflight-kind.sh
