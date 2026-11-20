package repository

import (
	"deploy-platform/backend-go/internal/service"
	"encoding/json"
)

func (m *MySQL) RunnerBindings() ([]service.RunnerBinding, error) {
	rows, err := m.db.Query(`SELECT id,app_id,token_hash,data_json,pipeline_backup_ciphertext FROM runner_bindings ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []service.RunnerBinding{}
	for rows.Next() {
		var id int64
		var appID int
		var hash, raw, backup string
		if err := rows.Scan(&id, &appID, &hash, &raw, &backup); err != nil {
			return nil, err
		}
		var b service.RunnerBinding
		if err := json.Unmarshal([]byte(raw), &b); err != nil {
			return nil, err
		}
		b.ID = id
		b.AppID = appID
		b.TokenHash = hash
		b.PipelineBackupCiphertext = backup
		out = append(out, b)
	}
	return out, rows.Err()
}
func (m *MySQL) SaveRunnerBinding(b service.RunnerBinding) (service.RunnerBinding, error) {
	raw, err := json.Marshal(b)
	if err != nil {
		return b, err
	}
	if b.ID == 0 {
		result, err := m.db.Exec(`INSERT INTO runner_bindings(app_id,token_hash,data_json,pipeline_backup_ciphertext) VALUES(?,?,?,?)`, b.AppID, b.TokenHash, string(raw), b.PipelineBackupCiphertext)
		if err != nil {
			return b, wrapConflict(err)
		}
		b.ID, err = result.LastInsertId()
		return b, err
	}
	_, err = m.db.Exec(`UPDATE runner_bindings SET token_hash=?,data_json=?,pipeline_backup_ciphertext=? WHERE id=?`, b.TokenHash, string(raw), b.PipelineBackupCiphertext, b.ID)
	return b, err
}
func (m *MySQL) YunxiaoConnections() ([]service.YunxiaoConnection, error) {
	rows, err := m.db.Query(`SELECT id,token_ciphertext,data_json FROM yunxiao_connections ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []service.YunxiaoConnection{}
	for rows.Next() {
		var id int64
		var cipher, raw string
		if err := rows.Scan(&id, &cipher, &raw); err != nil {
			return nil, err
		}
		var c service.YunxiaoConnection
		if err := json.Unmarshal([]byte(raw), &c); err != nil {
			return nil, err
		}
		c.ID = id
		c.TokenCiphertext = cipher
		out = append(out, c)
	}
	return out, rows.Err()
}
func (m *MySQL) SaveYunxiaoConnection(c service.YunxiaoConnection) (service.YunxiaoConnection, error) {
	raw, err := json.Marshal(c)
	if err != nil {
		return c, err
	}
	result, err := m.db.Exec(`INSERT INTO yunxiao_connections(token_ciphertext,data_json) VALUES(?,?)`, c.TokenCiphertext, string(raw))
	if err != nil {
		return c, err
	}
	c.ID, err = result.LastInsertId()
	return c, err
}
