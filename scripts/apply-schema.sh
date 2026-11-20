#!/usr/bin/env bash
# 把 backend-go/schema.sql + migrations/*.sql 按序回放进目标数据库。
#
# 迁移文件里的 `USE deploy_platform;` 是项目约定的 mysql 客户端执行
# 方式（不带 -D，由文件选库）——目标库名不同时必须改写，否则语句会
# 静默落到 dev 库。本脚本是这条知识的唯一实现；e2e 测试与部署文档
# 都经由它回放，不要各写一份。
#
# 已知边界：schema.sql 顶部的 `CREATE DATABASE IF NOT EXISTS deploy_platform`
# 不改写——它只确保 dev 库存在（幂等空操作），表结构仍全部落在 <database>。
#
# 用法: scripts/apply-schema.sh <database>
# 环境: MYSQL_CMD 可覆盖 mysql 客户端调用（默认走本地 docker 容器）。
set -euo pipefail

DB="${1:?usage: apply-schema.sh <database>}"
# 前置契约：库名进入 sed 替换串与 mysql 命令行，限制为安全字符集。
[[ "$DB" =~ ^[A-Za-z0-9_]+$ ]] || { echo "invalid database name: $DB" >&2; exit 1; }
ROOT="$(cd "$(dirname "$0")/../backend-go" && pwd)"
MYSQL_CMD="${MYSQL_CMD:-docker exec -i deploy-platform-mysql mysql -udeploy -pdeploy-platform}"

shopt -s nullglob
files=("$ROOT/schema.sql" "$ROOT"/migrations/*.sql)
if [ "${#files[@]}" -lt 2 ]; then
  echo "no migration files found under $ROOT/migrations" >&2
  exit 1
fi

for f in "${files[@]}"; do
  echo "apply $(basename "$f") -> $DB" >&2
  sed "s/USE deploy_platform;/USE $DB;/g" "$f" | $MYSQL_CMD
done
