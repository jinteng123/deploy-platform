#!/bin/bash
set -e

ENV_BIN="/opt/deploy-platform/env/bin"
SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)

echo '=== 安装 Supervisor ==='

if [ ! -f ${ENV_BIN}/python ]; then
    echo '[错误] 未找到 Python 环境，请先执行 install.sh 安装发布平台'
    exit 1
fi

# 离线安装 supervisor whl
${ENV_BIN}/python -m pip install --no-index --find-links=${SCRIPT_DIR} \
  --root-user-action=ignore supervisor 2>&1
echo ''

mkdir -p /etc/supervisord.d
mkdir -p /data/logs/supervisor

# 生成主配置文件
if [ ! -f /etc/supervisord.conf ]; then
cat > /etc/supervisord.conf << 'CONFEOF'
[unix_http_server]
file=/var/run/supervisor.sock

[supervisord]
logfile=/data/logs/supervisor/supervisord.log
pidfile=/var/run/supervisord.pid
childlogdir=/data/logs/supervisor

[rpcinterface:supervisor]
supervisor.rpcinterface_factory = supervisor.rpcinterface:make_main_rpcinterface

[supervisorctl]
serverurl=unix:///var/run/supervisor.sock

[include]
files = /etc/supervisord.d/*.conf
CONFEOF
echo '  已生成 /etc/supervisord.conf'
else
echo '  /etc/supervisord.conf 已存在，跳过'
fi

# 软链接到全局
ln -sf ${ENV_BIN}/supervisord /usr/local/bin/supervisord
ln -sf ${ENV_BIN}/supervisorctl /usr/local/bin/supervisorctl

# systemd 服务（如果系统支持）
if command -v systemctl &>/dev/null; then
  cat > /etc/systemd/system/supervisord.service << SVCEOF
[Unit]
Description=Supervisor process control system
After=network.target

[Service]
Type=forking
ExecStart=${ENV_BIN}/supervisord -c /etc/supervisord.conf
ExecStop=${ENV_BIN}/supervisorctl shutdown
ExecReload=${ENV_BIN}/supervisorctl reread && ${ENV_BIN}/supervisorctl update
KillMode=process
Restart=on-failure
RestartSec=30s

[Install]
WantedBy=multi-user.target
SVCEOF
  systemctl daemon-reload
  systemctl enable supervisord 2>/dev/null || true
  # 如果已在运行则 restart，否则 start
  if systemctl is-active supervisord &>/dev/null; then
    systemctl restart supervisord
    echo '  supervisord 已重启'
  else
    systemctl start supervisord
    echo '  supervisord 已启动'
  fi
else
  echo '  [提示] 未检测到 systemctl，请手动启动:'
  echo "    ${ENV_BIN}/supervisord -c /etc/supervisord.conf"
fi

echo ''
echo '=== 安装完成 ==='
echo "  supervisord:  $(which supervisord)"
echo "  supervisorctl: $(which supervisorctl)"
echo "  配置目录: /etc/supervisord.d/"
echo ''
supervisorctl status 2>/dev/null || echo '(暂无管理的进程)'
