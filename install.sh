#!/bin/bash
set -e

INSTALL_DIR="/opt/deploy-platform"
ENV_DIR="${INSTALL_DIR}/env"
LOG_DIR="/data/logs/supervisor/deploy_platform"
TRANSFER_DIR="/data/transfer_space"

echo '============================================'
echo '  Demo 发布平台 — 院内安装'
echo '============================================'
echo ''

echo '[1/7] 创建目录...'
mkdir -p ${INSTALL_DIR}/{backend,frontend}
mkdir -p ${ENV_DIR}
mkdir -p ${LOG_DIR}
mkdir -p ${TRANSFER_DIR}
echo "  ${INSTALL_DIR}"
echo "  ${LOG_DIR}"
echo "  ${TRANSFER_DIR}"

echo ''
echo '[2/7] 安装 Python 环境...'
if [ -f deploy-platform-env.tar.gz ]; then
  if [ -f "${ENV_DIR}/bin/python" ]; then
    echo '  Python 环境已存在，跳过解压'
  else
    tar -xzf deploy-platform-env.tar.gz -C ${ENV_DIR}
    source ${ENV_DIR}/bin/activate
    conda-unpack 2>/dev/null || true
    source deactivate 2>/dev/null || true
  fi
  echo "  Python: $(${ENV_DIR}/bin/python --version)"
else
  echo '  [跳过] deploy-platform-env.tar.gz 不存在'
fi

echo ''
echo '[3/7] 部署后端代码...'
cp -r backend/* ${INSTALL_DIR}/backend/
echo '  后端代码已复制'

echo ''
echo '[4/7] 部署前端资源...'
rm -rf ${INSTALL_DIR}/frontend/dist
cp -r frontend/dist ${INSTALL_DIR}/frontend/dist
echo '  前端 dist 已复制'

echo ''
echo '[5/7] 配置环境变量...'
if [ ! -f ${INSTALL_DIR}/backend/.env ]; then
  cp .env.production ${INSTALL_DIR}/backend/.env
  echo '  .env.production -> .env (请检查并修改密码等配置)'
else
  echo '  .env 已存在，跳过覆盖'
  cp .env.production ${INSTALL_DIR}/backend/.env.production.new
fi

echo ''
echo '[6/7] 配置 Supervisor...'
mkdir -p /etc/supervisord.d
cat > /etc/supervisord.d/deploy_platform.conf << 'SUPEOF'
[program:deploy_platform]
directory = /opt/deploy-platform/backend
command = /opt/deploy-platform/env/bin/uvicorn run:app --host 0.0.0.0 --port 8800 --workers 2
autostart = true
autorestart = true
startsecs = 5
stopwaitsecs = 10
stdout_logfile = /data/logs/supervisor/deploy_platform/server.log
redirect_stderr = true
environment = PATH="/opt/deploy-platform/env/bin:%(ENV_PATH)s"
SUPEOF
echo '  /etc/supervisord.d/deploy_platform.conf 已写入'

echo ''
echo '[7/7] 数据库初始化...'
echo ''

# 读取 .env 中的数据库配置
DB_H=$(grep '^DB_HOST=' ${INSTALL_DIR}/backend/.env | cut -d= -f2)
DB_P=$(grep '^DB_PORT=' ${INSTALL_DIR}/backend/.env | cut -d= -f2)
DB_U=$(grep '^DB_USER=' ${INSTALL_DIR}/backend/.env | cut -d= -f2)
DB_H=${DB_H:-127.0.0.1}
DB_P=${DB_P:-3306}
DB_U=${DB_U:-root}

echo "  数据库地址: ${DB_H}:${DB_P}"
echo "  请执行以下命令初始化数据库:"
echo ''
echo "  mysql -h ${DB_H} -P ${DB_P} -u ${DB_U} -p < ${INSTALL_DIR}/backend/schema.sql"
echo "  mysql -h ${DB_H} -P ${DB_P} -u ${DB_U} -p deploy_platform < ${INSTALL_DIR}/backend/seed_apps.sql"
echo ''
echo '============================================'
echo '  安装完成!'
echo '============================================'
echo ''
echo '后续步骤:'
echo '  1. 检查 /opt/deploy-platform/backend/.env'
echo '  2. 执行数据库初始化'
echo '  3. supervisorctl reread && supervisorctl update'
echo '  4. supervisorctl start deploy_platform'
echo '  5. curl http://localhost:8800/health'
echo ''
