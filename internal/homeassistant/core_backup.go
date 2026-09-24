package homeassistant

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/gorilla/websocket"
)

// CoreBackup uses the native admin-only Backup Manager available to Container
// installations. AgentID is explicitly selected from backup/agents/info.
// It does not claim Supervisor authority or install an agent/add-on.
type CoreBackup struct {
	Client        *Client
	AgentID       string
	BackupGranted bool
}

func (c *CoreBackup) BackupAllowed() bool { return c != nil && c.BackupGranted }
func (c *CoreBackup) ws(ctx context.Context, message map[string]any, out any) error {
	if c.Client == nil || c.AgentID == "" || !c.BackupGranted {
		return errors.New("explicit Core backup agent and admin grant required")
	}
	return c.Client.websocket(ctx, message, out)
}
func (c *Client) websocket(ctx context.Context, message map[string]any, out any) error {
	transport := c.http.Transport.(*http.Transport)
	dialer := websocket.Dialer{NetDialContext: transport.DialContext, TLSClientConfig: transport.TLSClientConfig, HandshakeTimeout: 10 * time.Second}
	u := *c.endpoint
	u.Path = "/api/websocket"
	if u.Scheme == "https" {
		u.Scheme = "wss"
	} else {
		u.Scheme = "ws"
	}
	conn, _, err := dialer.DialContext(ctx, u.String(), nil)
	if err != nil {
		return ErrUnavailable
	}
	defer conn.Close()
	deadline := time.Now().Add(30 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	_ = conn.SetReadDeadline(deadline)
	_ = conn.SetWriteDeadline(deadline)
	conn.SetReadLimit(2 * 1024 * 1024)
	var auth struct {
		Type string `json:"type"`
	}
	if conn.ReadJSON(&auth) != nil {
		return ErrUnavailable
	}
	if auth.Type != "auth_required" {
		return errors.New("Home Assistant WebSocket auth challenge missing")
	}
	if conn.WriteJSON(map[string]any{"type": "auth", "access_token": c.token}) != nil || conn.ReadJSON(&auth) != nil || auth.Type != "auth_ok" {
		return ErrUnauthorized
	}
	message["id"] = 1
	if conn.WriteJSON(message) != nil {
		return errors.New("Home Assistant WebSocket dispatch failed")
	}
	var result struct {
		ID      int             `json:"id"`
		Type    string          `json:"type"`
		Success bool            `json:"success"`
		Result  json.RawMessage `json:"result"`
	}
	if conn.ReadJSON(&result) != nil {
		return ErrUnavailable
	}
	if result.ID != 1 || result.Type != "result" || !result.Success {
		return errors.New("native Core backup command failed or outcome uncertain")
	}
	if out != nil && json.Unmarshal(result.Result, out) != nil {
		return errors.New("invalid native Core backup response")
	}
	return nil
}
func (c *CoreBackup) Observe(ctx context.Context) (PlatformObservation, error) {
	observation, err := c.Client.Observe(ctx)
	if err != nil {
		return PlatformObservation{}, err
	}
	var agents struct {
		Agents []struct {
			ID string `json:"agent_id"`
		} `json:"agents"`
	}
	if err = c.ws(ctx, map[string]any{"type": "backup/agents/info"}, &agents); err != nil {
		return PlatformObservation{}, err
	}
	for _, a := range agents.Agents {
		if a.ID == c.AgentID {
			return PlatformObservation{CoreVersion: observation.CoreVersion, InstallationMethod: "container"}, nil
		}
	}
	return PlatformObservation{}, errors.New("selected native Core backup agent unavailable")
}
func (c *CoreBackup) SubmitFullBackup(ctx context.Context, name, password string) (Submission, error) {
	if name == "" || password == "" {
		return Submission{}, errors.New("backup name and password required")
	}
	var result struct {
		JobID string `json:"backup_job_id"`
	}
	err := c.ws(ctx, map[string]any{"type": "backup/generate", "agent_ids": []string{c.AgentID}, "name": name, "password": password, "include_database": true, "include_homeassistant": true, "include_all_addons": true}, &result)
	if err == nil && result.JobID == "" {
		err = errors.New("native Core backup lacks job correlation")
	}
	return Submission{JobID: result.JobID}, err
}

type coreBackupInfo struct {
	ID            string `json:"backup_id"`
	Name          string `json:"name"`
	Database      bool   `json:"database_included"`
	HomeAssistant bool   `json:"homeassistant_included"`
	Agents        map[string]struct {
		Protected bool `json:"protected"`
	} `json:"agents"`
	FailedAddons  []json.RawMessage `json:"failed_addons"`
	FailedFolders []string          `json:"failed_folders"`
	FailedAgents  []string          `json:"failed_agent_ids"`
}

func (c *CoreBackup) convert(info coreBackupInfo) (Backup, error) {
	if !info.HomeAssistant || !info.Database || !info.Agents[c.AgentID].Protected || len(info.FailedAddons) > 0 || len(info.FailedFolders) > 0 || len(info.FailedAgents) > 0 {
		return Backup{}, errors.New("Core backup is incomplete or not encrypted on selected agent")
	}
	b := Backup{Slug: info.ID, Name: info.Name, Type: "full", Protected: true}
	b.Content.HomeAssistant = true
	return b, nil
}
func (c *CoreBackup) BackupInfo(ctx context.Context, id string) (Backup, error) {
	var out struct {
		Backup coreBackupInfo `json:"backup"`
	}
	if err := c.ws(ctx, map[string]any{"type": "backup/details", "backup_id": id}, &out); err != nil {
		return Backup{}, err
	}
	if out.Backup.ID != id {
		return Backup{}, errors.New("Core backup identity mismatch")
	}
	return c.convert(out.Backup)
}
func (c *CoreBackup) FindBackup(ctx context.Context, name string) (*Backup, error) {
	var out struct {
		Backups []coreBackupInfo `json:"backups"`
	}
	if err := c.ws(ctx, map[string]any{"type": "backup/info"}, &out); err != nil {
		return nil, err
	}
	var found *Backup
	for _, info := range out.Backups {
		if info.Name != name {
			continue
		}
		if found != nil {
			return nil, errors.New("ambiguous native Core backup")
		}
		b, err := c.convert(info)
		if err != nil {
			return nil, err
		}
		found = &b
	}
	return found, nil
}
func (c *CoreBackup) ObserveJob(ctx context.Context, _ string) (bool, error) {
	var out struct {
		State      string `json:"state"`
		LastAction struct {
			State string `json:"state"`
		} `json:"last_action_event"`
	}
	if err := c.ws(ctx, map[string]any{"type": "backup/info"}, &out); err != nil {
		return false, err
	}
	if out.LastAction.State == "failed" {
		return false, errors.New("native Core backup manager reported failure")
	}
	return out.State == "idle", nil
}
func (c *CoreBackup) DownloadFullBackup(ctx context.Context, id string) (io.ReadCloser, error) {
	if _, err := c.BackupInfo(ctx, id); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Client.endpoint.String()+"/api/backup/download/"+url.PathEscape(id)+"?agent_id="+url.QueryEscape(c.AgentID), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Client.token)
	client := *c.Client.http
	client.Timeout = 10 * time.Minute
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("native Core encrypted download failed")
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, errors.New("native Core download denied")
	}
	return resp.Body, nil
}
