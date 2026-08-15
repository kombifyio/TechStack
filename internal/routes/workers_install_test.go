package routes

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/kombifyio/techstack/pkg/httpx"
)

func TestServeLinuxInstallScriptFailsClosedWithoutArtifact(t *testing.T) {
	recorder := httptest.NewRecorder()
	event := &httpx.Event{
		Response: recorder,
		Request:  httptest.NewRequest(http.MethodGet, "/install.sh", nil),
	}

	err := serveLinuxInstallScript(event, []string{filepath.Join(t.TempDir(), "missing-install.sh")})
	if err != nil {
		t.Fatalf("serveLinuxInstallScript returned error: %v", err)
	}
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/plain; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want text/plain", got)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "installer artifact unavailable") {
		t.Fatalf("response does not explain missing installer artifact: %q", body)
	}
	if strings.Contains(body, "/api/v1/workers/register") {
		t.Fatalf("missing-artifact response must not contain a registration-only fallback: %q", body)
	}
}

func TestServeLinuxInstallScriptServesCompleteArtifact(t *testing.T) {
	path := filepath.Join("..", "..", "install.sh")
	script, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read authoritative installer: %v", err)
	}

	recorder := httptest.NewRecorder()
	event := &httpx.Event{
		Response: recorder,
		Request:  httptest.NewRequest(http.MethodGet, "/install.sh", nil),
	}

	err = serveLinuxInstallScript(event, []string{path})
	if err != nil {
		t.Fatalf("serveLinuxInstallScript returned error: %v", err)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := recorder.Header().Get("Content-Type"); got != "text/x-shellscript; charset=utf-8" {
		t.Fatalf("Content-Type = %q, want shell script", got)
	}
	if got := recorder.Header().Get("Content-Disposition"); got != "inline; filename=\"install.sh\"" {
		t.Fatalf("Content-Disposition = %q", got)
	}
	if got := recorder.Body.Bytes(); !bytes.Equal(got, script) {
		t.Fatalf("body changed: got %d bytes, want %d", len(got), len(script))
	}
}

func TestServeLinuxInstallScriptSkipsIncompleteArtifact(t *testing.T) {
	directory := t.TempDir()
	incompletePath := filepath.Join(directory, "incomplete-install.sh")
	if err := os.WriteFile(incompletePath, []byte("#!/bin/sh\n# registration-only legacy artifact\n"), 0o600); err != nil {
		t.Fatalf("write incomplete installer fixture: %v", err)
	}
	completePath := filepath.Join("..", "..", "install.sh")
	complete, err := os.ReadFile(completePath)
	if err != nil {
		t.Fatalf("read authoritative installer: %v", err)
	}

	recorder := httptest.NewRecorder()
	event := &httpx.Event{Response: recorder, Request: httptest.NewRequest(http.MethodGet, "/install.sh", nil)}
	if err := serveLinuxInstallScript(event, []string{incompletePath, completePath}); err != nil {
		t.Fatalf("serveLinuxInstallScript returned error: %v", err)
	}
	if recorder.Code != http.StatusOK || !bytes.Equal(recorder.Body.Bytes(), complete) {
		t.Fatalf("handler served incomplete artifact: status=%d body_bytes=%d", recorder.Code, recorder.Body.Len())
	}
}

func TestValidLinuxInstallerArtifactRejectsMarkerOnlyFake(t *testing.T) {
	markerOnly := []byte(`#!/bin/sh
curl -H "Content-Type: application/octet-stream" https://techstack.test/enroll
install -m 0600 "$ENROLLMENT_TMP" /etc/techstack/agent-enrollment.json
ExecStart=/usr/local/bin/techstack agent --transport=https --enrollment-file=/etc/techstack/agent-enrollment.json
systemctl enable techstack-agent.service
systemctl restart techstack-agent.service
`)
	if validLinuxInstallerArtifact(markerOnly) {
		t.Fatal("marker-only shell fragment must not satisfy the authoritative installer contract")
	}
}

func TestLinuxInstallerReusesExistingRuntimeEnrollmentWithoutRegistration(t *testing.T) {
	shell := "sh"
	if runtime.GOOS == "windows" {
		shell = `C:\Program Files\Git\bin\sh.exe`
	}
	if _, err := os.Stat(shell); runtime.GOOS == "windows" && err != nil {
		t.Skip("Git Bash is required for the installer behavior test on Windows")
	}
	if runtime.GOOS != "windows" {
		if _, err := exec.LookPath(shell); err != nil {
			t.Skip("POSIX shell is required for the installer behavior test")
		}
	}

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatalf("resolve repository root: %v", err)
	}
	temp := t.TempDir()
	stubDir := filepath.Join(temp, "bin")
	logDir := filepath.Join(temp, "log")
	installDir := filepath.Join(temp, "installed")
	operationsDir := filepath.Join(temp, "libexec")
	etcDir := filepath.Join(temp, "etc")
	for _, directory := range []string{stubDir, logDir, installDir, operationsDir, etcDir} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatalf("create %s: %v", directory, err)
		}
	}

	binaryFixture := filepath.Join(temp, "techstack-fixture")
	binaryBody := []byte("#!/bin/sh\n[ \"${1:-}\" = version ] && echo 0.6.18\nexit 0\n")
	if err := os.WriteFile(binaryFixture, binaryBody, 0o700); err != nil {
		t.Fatalf("write binary fixture: %v", err)
	}
	binaryDigest := sha256.Sum256(binaryBody)

	writeExecutableFixture(t, filepath.Join(stubDir, "curl"), `#!/bin/sh
printf '%s\n' "$@" > "$STUB_LOG_DIR/curl.args"
headers_file=""
output_file=""
while [ "$#" -gt 0 ]; do
  case "$1" in
    -D) headers_file=$2; shift 2 ;;
    -o) output_file=$2; shift 2 ;;
    -H)
      case "$2" in @*) cp "${2#@}" "$STUB_LOG_DIR/request.headers" ;; esac
      shift 2
      ;;
    *) shift ;;
  esac
done
cp "$STUB_BINARY_FIXTURE" "$output_file"
printf 'HTTP/1.1 200 OK\r\nContent-Type: application/octet-stream\r\nContent-Disposition: attachment; filename="techstack-linux-amd64"\r\nX-Kombify-Artifact-SHA256: %s\r\n\r\n' "$STUB_BINARY_SHA256" > "$headers_file"
printf '200'
`)
	writeExecutableFixture(t, filepath.Join(stubDir, "sudo"), `#!/bin/sh
printf '%s\n' "$*" >> "$STUB_LOG_DIR/sudo.actions"
command_name=$1
shift
if [ "$command_name" = "systemctl" ]; then
  exit 0
fi
if [ "$command_name" != "install" ]; then
  exit 1
fi
if [ "${1:-}" = "-d" ]; then
  exit 0
fi
mode=""
if [ "${1:-}" = "-m" ]; then
  mode=$2
  shift 2
fi
source_file=$1
target_file=$2
case "$target_file" in
  /etc/techstack/agent-enrollment.json) target_file="$STUB_ETC_DIR/agent-enrollment.json" ;;
  /etc/systemd/system/techstack-agent.service) target_file="$STUB_ETC_DIR/techstack-agent.service" ;;
  *) exit 1 ;;
esac
cp "$source_file" "$target_file"
chmod "$mode" "$target_file"
`)
	writeExecutableFixture(t, filepath.Join(stubDir, "systemctl"), "#!/bin/sh\nexit 0\n")
	writeExecutableFixture(t, filepath.Join(stubDir, "uname"), `#!/bin/sh
case "${1:-}" in
  -s) printf 'Linux\n' ;;
  -m) printf 'x86_64\n' ;;
  *) printf 'Linux\n' ;;
esac
`)

	command := exec.Command(shell, "-c", `
stub_path=$STUB_BIN_DIR
if command -v cygpath >/dev/null 2>&1; then
  stub_path=$(cygpath -u "$STUB_BIN_DIR")
fi
PATH="$stub_path:$PATH"
export PATH
sh install.sh
`)
	command.Dir = root
	// The repository-wide mise environment pins TECHSTACK_VERSION for release
	// identity checks. This behavior test must exercise the control-plane binary
	// lane regardless of its parent process, otherwise the mock curl receives a
	// release-metadata request with no output path and the affected gate flakes.
	baseEnv := make([]string, 0, len(os.Environ()))
	for _, entry := range os.Environ() {
		if strings.HasPrefix(strings.ToUpper(entry), "TECHSTACK_VERSION=") {
			continue
		}
		baseEnv = append(baseEnv, entry)
	}
	command.Env = append(baseEnv,
		"STUB_BIN_DIR="+stubDir,
		"STUB_LOG_DIR="+logDir,
		"STUB_ETC_DIR="+etcDir,
		"STUB_BINARY_FIXTURE="+binaryFixture,
		"STUB_BINARY_SHA256="+hex.EncodeToString(binaryDigest[:]),
		"INSTALL_DIR="+installDir,
		"TECHSTACK_OPERATIONS_DIR="+operationsDir,
		"KOMBI_SERVER=https://techstack.example",
		"KOMBI_TOKEN=runtime-secret-not-for-output",
		"KOMBI_RUNTIME_AGENT_ID=runtime-1",
		"KOMBI_TENANT_ID=auth0|demo-subject",
		"KOMBI_OWNER_ID=auth0|demo-subject",
		"KOMBI_STACK_ID=stack-1",
		"KOMBI_SERVER_ID=server-1",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("run installer: %v\n%s", err, output)
	}

	curlArgs := readFixture(t, filepath.Join(logDir, "curl.args"))
	if strings.Count(curlArgs, "/api/v1/agent/binary/linux/x86_64") != 1 {
		t.Fatalf("binary download count = %d, want 1; args=%q", strings.Count(curlArgs, "/api/v1/agent/binary/linux/x86_64"), curlArgs)
	}
	if strings.Contains(curlArgs, "/api/v1/workers/register") {
		t.Fatalf("existing runtime enrollment attempted pairing registration: %q", curlArgs)
	}
	requestHeaders := readFixture(t, filepath.Join(logDir, "request.headers"))
	for _, want := range []string{
		"X-Kombify-Runtime-Agent-ID: runtime-1",
		"X-Kombify-Tenant-ID: auth0|demo-subject",
	} {
		if !strings.Contains(requestHeaders, want) {
			t.Fatalf("download headers missing %q", want)
		}
	}
	sudoActions := readFixture(t, filepath.Join(logDir, "sudo.actions"))
	if !strings.Contains(sudoActions, "install -m 0600") {
		t.Fatalf("runtime token was not installed with mode 0600: %q", sudoActions)
	}
	unit := readFixture(t, filepath.Join(etcDir, "techstack-agent.service"))
	for _, want := range []string{
		"--enrollment-file=/etc/techstack/agent-enrollment.json",
	} {
		if !strings.Contains(unit, want) {
			t.Fatalf("systemd unit missing %q", want)
		}
	}
	enrollment := readFixture(t, filepath.Join(etcDir, "agent-enrollment.json"))
	for _, want := range []string{`"runtime_agent_id":"runtime-1"`, `"tenant_id":"auth0|demo-subject"`, `"stack_id":"stack-1"`, `"agent_token":"runtime-secret-not-for-output"`} {
		if !strings.Contains(enrollment, want) {
			t.Fatalf("enrollment missing %q", want)
		}
	}
	operationsBinary, err := os.ReadFile(filepath.Join(operationsDir, "techstack-stackkit-operations"))
	if err != nil || !bytes.Equal(operationsBinary, binaryBody) {
		t.Fatalf("operations binary is not the exact downloaded artifact: err=%v", err)
	}
}

func writeExecutableFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatalf("write executable fixture %s: %v", path, err)
	}
}

func readFixture(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	return string(content)
}

func TestServeLinuxInstallScriptFailsClosedWithOnlyIncompleteArtifact(t *testing.T) {
	path := filepath.Join(t.TempDir(), "incomplete-install.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n# registration-only legacy artifact\n"), 0o600); err != nil {
		t.Fatalf("write incomplete installer fixture: %v", err)
	}

	recorder := httptest.NewRecorder()
	event := &httpx.Event{Response: recorder, Request: httptest.NewRequest(http.MethodGet, "/install.sh", nil)}
	if err := serveLinuxInstallScript(event, []string{path}); err != nil {
		t.Fatalf("serveLinuxInstallScript returned error: %v", err)
	}
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d body=%q, want 503", recorder.Code, recorder.Body.String())
	}
}
