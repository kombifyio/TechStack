package homeassistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"regexp"
	"time"
)

// Supervisor requires its own endpoint and credential. A Core observation or a
// loaded hassio component never constructs this privileged client implicitly.
type Supervisor struct {
	viaCore bool
	client  *Client
	grants  Grants
}
type Grants struct{ Backup, Restore, Update bool }
type PlatformObservation struct {
	CoreVersion        string `json:"core_version"`
	OSVersion          string `json:"os_version"`
	Architecture       string `json:"architecture"`
	InstallationMethod string `json:"installation_method"`
}
type Backup struct {
	HomeAssistantVersion string `json:"homeassistant,omitempty"`
	ExcludeDatabase      bool   `json:"homeassistant_exclude_database,omitempty"`
	Slug                 string `json:"slug"`
	Name                 string `json:"name"`
	Type                 string `json:"type"`
	Protected            bool   `json:"protected"`
	Content              struct {
		HomeAssistant bool     `json:"homeassistant"`
		Addons        []string `json:"addons"`
		Folders       []string `json:"folders"`
	} `json:"content"`
}
type Submission struct {
	JobID string `json:"job_id"`
	Slug  string `json:"slug"`
}

var supervisorSlug = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func NewLocalSupervisor(ctx context.Context, endpoint, token string, grants Grants) (*Supervisor, error) {
	c, err := NewLocalClient(ctx, endpoint, token)
	if err != nil {
		return nil, err
	}
	return &Supervisor{client: c, grants: grants}, nil
}
func (s *Supervisor) Close() { s.client.Close() }

// NewSupervisorViaCore explicitly selects HA's admin-only supervisor/api
// WebSocket bridge. Core read authority never enables it automatically.
func NewSupervisorViaCore(ctx context.Context, origin, adminToken string, grants Grants) (*Supervisor, error) {
	s, err := NewLocalSupervisor(ctx, origin, adminToken, grants)
	if err != nil {
		return nil, err
	}
	s.viaCore = true
	return s, nil
}
func (s *Supervisor) BackupAllowed() bool { return s != nil && s.grants.Backup }

// DownloadFullBackup returns only an inspected native encrypted full backup.
// Callers must close the stream and verify the durable off-instance copy.
func (s *Supervisor) DownloadFullBackup(ctx context.Context, slug string) (io.ReadCloser, error) {
	if !s.grants.Backup {
		return nil, errors.New("explicit backup export grant required")
	}
	if _, err := s.BackupInfo(ctx, slug); err != nil {
		return nil, err
	}
	prefix := ""
	if s.viaCore {
		prefix = "/api/hassio"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.client.endpoint.String()+prefix+"/backups/"+slug+"/download", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.client.token)
	client := *s.client.http
	client.Timeout = 10 * time.Minute
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("backup download failed")
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, errors.New("backup download refused")
	}
	return resp.Body, nil
}

// UploadBackup streams the retained encrypted bytes; it does not restore them.
func (s *Supervisor) UploadBackup(ctx context.Context, archive io.Reader) (Submission, error) {
	if !s.grants.Restore || archive == nil {
		return Submission{}, errors.New("explicit restore upload grant required")
	}
	reader, writer := io.Pipe()
	multipartWriter := multipart.NewWriter(writer)
	go func() {
		part, err := multipartWriter.CreateFormFile("file", "backup.tar")
		if err == nil {
			_, err = io.Copy(part, archive)
		}
		if err == nil {
			err = multipartWriter.Close()
		}
		_ = writer.CloseWithError(err)
	}()
	defer reader.Close()
	prefix := ""
	if s.viaCore {
		prefix = "/api/hassio"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.client.endpoint.String()+prefix+"/backups/new/upload", reader)
	if err != nil {
		return Submission{}, err
	}
	req.Header.Set("Authorization", "Bearer "+s.client.token)
	req.Header.Set("Content-Type", multipartWriter.FormDataContentType())
	client := *s.client.http
	client.Timeout = 10 * time.Minute
	resp, err := client.Do(req)
	if err != nil {
		return Submission{}, errors.New("backup upload outcome uncertain")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return Submission{}, errors.New("backup upload refused")
	}
	var envelope struct {
		Result string     `json:"result"`
		Data   Submission `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 65536)).Decode(&envelope); err != nil || envelope.Result != "ok" || envelope.Data.Slug == "" {
		return Submission{}, errors.New("backup upload acceptance missing")
	}
	return envelope.Data, nil
}
func (s *Supervisor) request(ctx context.Context, method, path string, input, output any) error {
	if s.viaCore {
		message := map[string]any{"type": "supervisor/api", "endpoint": path, "method": method, "timeout": 10}
		if input != nil {
			message["data"] = input
		}
		return s.client.websocket(ctx, message, output)
	}
	var body bytes.Buffer
	if input != nil {
		if err := json.NewEncoder(&body).Encode(input); err != nil {
			return errors.New("invalid Supervisor payload")
		}
	}
	var envelope struct {
		Result string          `json:"result"`
		Data   json.RawMessage `json:"data"`
	}
	if err := s.client.json(ctx, method, path, &body, &envelope); err != nil {
		return err
	}
	if envelope.Result != "ok" {
		return errors.New("Supervisor operation was not successful")
	}
	if output != nil {
		if err := json.Unmarshal(envelope.Data, output); err != nil {
			return errors.New("invalid Supervisor response data")
		}
	}
	return nil
}

func (s *Supervisor) Observe(ctx context.Context) (PlatformObservation, error) {
	var core struct {
		Version string `json:"version"`
		Arch    string `json:"arch"`
	}
	var os struct {
		Version string `json:"version"`
	}
	if err := s.request(ctx, http.MethodGet, "/core/info", nil, &core); err != nil {
		return PlatformObservation{}, err
	}
	if err := s.request(ctx, http.MethodGet, "/os/info", nil, &os); err != nil {
		return PlatformObservation{}, err
	}
	if core.Version == "" || core.Arch == "" || os.Version == "" {
		return PlatformObservation{}, errors.New("Supervisor did not prove HAOS, Core and architecture")
	}
	return PlatformObservation{CoreVersion: core.Version, OSVersion: os.Version, Architecture: core.Arch, InstallationMethod: "haos"}, nil
}

// SubmitFullBackup returns acceptance only. The workflow must observe the job,
// inspect BackupInfo, export the archive and verify separate recovery-key
// custody before it may label the source recoverable. Submission is never
// automatically retried: native Supervisor offers no idempotency key.
func (s *Supervisor) SubmitFullBackup(ctx context.Context, operationName, password string) (Submission, error) {
	if !s.grants.Backup || operationName == "" || password == "" {
		return Submission{}, errors.New("explicit backup grant, correlation name and encryption password required")
	}
	var out Submission
	err := s.request(ctx, http.MethodPost, "/backups/new/full", map[string]any{"name": operationName, "password": password, "background": true, "homeassistant_exclude_database": false}, &out)
	if err == nil && out.JobID == "" && out.Slug == "" {
		err = errors.New("Supervisor backup acceptance lacks correlation")
	}
	return out, err
}

func (s *Supervisor) BackupInfo(ctx context.Context, slug string) (Backup, error) {
	if !supervisorSlug.MatchString(slug) {
		return Backup{}, errors.New("invalid backup reference")
	}
	var out Backup
	err := s.request(ctx, http.MethodGet, "/backups/"+slug+"/info", nil, &out)
	if err == nil && (out.Slug != slug || out.Type != "full" || !out.Protected || out.HomeAssistantVersion == "" || out.ExcludeDatabase) {
		err = errors.New("backup is not a verified encrypted full Home Assistant archive")
	}
	if err == nil {
		out.Content.HomeAssistant = true
	}
	return out, err
}

// FindBackup reconciles an uncertain submission by its unique operation name.
// Multiple matches are ambiguous and never justify selecting an arbitrary one.
func (s *Supervisor) FindBackup(ctx context.Context, operationName string) (*Backup, error) {
	if operationName == "" {
		return nil, errors.New("backup correlation name required")
	}
	var out struct {
		Backups []Backup `json:"backups"`
	}
	if err := s.request(ctx, http.MethodGet, "/backups", nil, &out); err != nil {
		return nil, err
	}
	var found *Backup
	for _, b := range out.Backups {
		if b.Name != operationName {
			continue
		}
		if found != nil {
			return nil, errors.New("ambiguous backup correlation")
		}
		copy := b
		found = &copy
	}
	return found, nil
}

func (s *Supervisor) SubmitRestore(ctx context.Context, slug, password string) (Submission, error) {
	if !s.grants.Restore || password == "" || !supervisorSlug.MatchString(slug) {
		return Submission{}, errors.New("explicit restore grant, archive and key required")
	}
	if _, err := s.BackupInfo(ctx, slug); err != nil {
		return Submission{}, err
	}
	var out Submission
	err := s.request(ctx, http.MethodPost, "/backups/"+slug+"/restore/full", map[string]any{"password": password, "background": true}, &out)
	if err == nil && out.JobID == "" {
		err = errors.New("Supervisor restore acceptance lacks job correlation")
	}
	return out, err
}

// SubmitCoreUpdate pins a requested Core version. Full off-instance backup and
// recovery evidence are workflow preconditions; the native partial backup is an
// additional safeguard and cannot substitute for that source archive.
func (s *Supervisor) SubmitCoreUpdate(ctx context.Context, version string) error {
	if !s.grants.Update || version == "" || version == "latest" {
		return errors.New("explicit update grant and exact Core version required")
	}
	return s.request(ctx, http.MethodPost, "/core/update", map[string]any{"version": version, "backup": true}, nil)
}

func (s *Supervisor) SubmitOSUpdate(ctx context.Context, version string) error {
	if !s.grants.Update || !pinnedOSVersion.MatchString(version) {
		return ErrUnauthorized
	}
	return s.request(ctx, http.MethodPost, "/os/update", map[string]any{"version": version}, nil)
}

func (s *Supervisor) ObserveJob(ctx context.Context, id string) (bool, error) {
	if !supervisorSlug.MatchString(id) {
		return false, errors.New("invalid Supervisor job reference")
	}
	var out struct {
		Done   bool              `json:"done"`
		Errors []json.RawMessage `json:"errors"`
	}
	if err := s.request(ctx, http.MethodGet, "/jobs/"+id, nil, &out); err != nil {
		return false, err
	}
	if len(out.Errors) > 0 {
		return false, fmt.Errorf("Supervisor job failed (%d reported errors)", len(out.Errors))
	}
	return out.Done, nil
}
