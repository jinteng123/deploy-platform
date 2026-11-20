-- ============================================================
-- 预录入应用映射 (基于 kubectl get pods -n dev-demo-002 推导)
-- ============================================================
-- Deployment 命名规律: {namespace}-{app}
-- 文件命名规律: demo-{app}-{env}-{timestamp}.tar.gz
-- match_keyword 取 app 名中最能唯一匹配的子串

USE deploy_platform;

INSERT INTO `apps` (`app_name`, `match_keyword`, `deployment_name`, `container_name`, `namespace`, `harbor_project`, `description`) VALUES
('demo-agent-core-api',      'agent-core-api',      'dev-demo-002-demo-agent-core-api',      'demo-agent-core-api',      'dev-demo-002', 'Demo', 'Agent Core API'),
('demo-ai-assistant-web',    'ai-assistant-web',    'dev-demo-002-demo-ai-assistant-web',    'demo-ai-assistant-web',    'dev-demo-002', 'Demo', 'AI 助手前端'),
('demo-angel-agent-web',     'angel-agent-web',     'dev-demo-002-demo-angel-agent-web',     'demo-angel-agent-web',     'dev-demo-002', 'Demo', '天使护理 Agent 前端'),
('demo-data-asset-api',      'data-asset-api',      'dev-demo-002-demo-data-asset-api',      'demo-data-asset-api',      'dev-demo-002', 'Demo', '数据资产 API'),
('demo-data-asset-web',      'data-asset-web',      'dev-demo-002-demo-data-asset-web',      'demo-data-asset-web',      'dev-demo-002', 'Demo', '数据资产前端'),
('demo-gateway',             'demo-gateway',         'dev-demo-002-demo-gateway',             'demo-gateway',             'dev-demo-002', 'Demo', '网关'),
('demo-hospital-api',        'hospital-api',        'dev-demo-002-demo-hospital-api',        'demo-hospital-api',        'dev-demo-002', 'Demo', '医院 API'),
('demo-hospital-web',        'hospital-web',        'dev-demo-002-demo-hospital-web',        'demo-hospital-web',        'dev-demo-002', 'Demo', '医院前端'),
('demo-out-gateway',         'out-gateway',         'dev-demo-002-demo-out-gateway',         'demo-out-gateway',         'dev-demo-002', 'Demo', '外部网关'),
('demo-platform-map-web',    'platform-map-web',    'dev-demo-002-demo-platform-map-web',    'demo-platform-map-web',    'dev-demo-002', 'Demo', '平台地图前端'),
('demo-proxy',               'demo-proxy',           'dev-demo-002-demo-proxy',               'demo-proxy',               'dev-demo-002', 'Demo', '代理服务'),
('demo-user-api',            'user-api',            'dev-demo-002-demo-user-api',            'demo-user-api',            'dev-demo-002', 'Demo', '用户 API'),
('demo-user-web',            'user-web',            'dev-demo-002-demo-user-web',            'demo-user-web',            'dev-demo-002', 'Demo', '用户前端')
ON DUPLICATE KEY UPDATE
  match_keyword = VALUES(match_keyword),
  deployment_name = VALUES(deployment_name),
  container_name = VALUES(container_name),
  namespace = VALUES(namespace),
  description = VALUES(description);
