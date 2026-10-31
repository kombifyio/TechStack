package main

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/kombifyio/techstack/pkg/artifactdelivery"
)

// r2-mirror copies the delivery files of a verified publication to the private
// EU R2 bucket the Gateway Worker streams from (CI-CD "Render download
// prohibition"). Every object carries R2's native SHA-256, which R2 checks
// against the uploaded body. The Worker resolves an admitted index by its
// digest and streams only objects whose stored SHA-256 equals the index entry,
// so a Techstack release needs no Gateway repin and the uploader holds no
// integrity authority. The index is written last: an index in R2 always names
// objects that are already there.

const r2MirrorIndexMax = 16 << 10

// r2MirrorInstallerMax matches the Gateway Worker's installer bound.
const r2MirrorInstallerMax = 256 << 10

var (
	r2AccountPattern = regexp.MustCompile(`^[a-f0-9]{32}$`)
	r2BucketPattern  = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`)
	r2VersionPattern = regexp.MustCompile(`^[0-9]+\.[0-9]+\.[0-9]+$`)
	r2DigestPattern  = regexp.MustCompile(`^[a-f0-9]{64}$`)
)

type r2Mirror struct {
	endpoint, bucket, accessKey, secretKey string
	client                                 *http.Client
	now                                    func() time.Time
}

type r2MirrorFile struct {
	path, key, sha256, contentType string
	size                           int64
}

func newR2Mirror() (r2Mirror, error) {
	account := os.Getenv("GUARD_RELEASE_R2_ACCOUNT_ID")
	bucket := os.Getenv("GUARD_RELEASE_R2_BUCKET")
	mirror := r2Mirror{bucket: bucket, accessKey: os.Getenv("GUARD_RELEASE_R2_ACCESS_KEY_ID"),
		secretKey: os.Getenv("GUARD_RELEASE_R2_SECRET_ACCESS_KEY"), client: &http.Client{Timeout: 15 * time.Minute}, now: time.Now}
	if !r2AccountPattern.MatchString(account) || !r2BucketPattern.MatchString(bucket) || mirror.accessKey == "" || mirror.secretKey == "" {
		return r2Mirror{}, errors.New("GUARD_RELEASE_R2_ACCOUNT_ID, _BUCKET, _ACCESS_KEY_ID and _SECRET_ACCESS_KEY are required")
	}
	mirror.endpoint = "https://" + account + ".eu.r2.cloudflarestorage.com"
	return mirror, nil
}

func mirrorReleaseToR2(dir string) (string, error) {
	mirror, err := newR2Mirror()
	if err != nil {
		return "", err
	}
	files, err := releaseMirrorFiles(dir)
	if err != nil {
		return "", err
	}
	var report []string
	for _, file := range files {
		state, err := mirror.put(file)
		if err != nil {
			return "", fmt.Errorf("%s: %w", file.key, err)
		}
		report = append(report, state+" "+file.key)
	}
	return strings.Join(report, "\n"), nil
}

// mirrorInstallerToR2 copies a release source's Linux installer to its
// content-addressed key. The control plane built from that source admits only
// the digest it pins, and the Gateway Worker hashes the stored bytes before
// serving them, so installer bytes never come from Render and an upload under
// any other digest is inert. requirePinned refuses an installer whose digest
// differs from the pin compiled into this tool, which is the release source's
// own pin when the tool is built from that source.
func mirrorInstallerToR2(path string, requirePinned bool) (string, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- the workflow's own export of the release source.
	if err != nil {
		return "", err
	}
	if len(raw) == 0 || len(raw) > r2MirrorInstallerMax {
		return "", errors.New("install.sh is empty or exceeds the delivery bound")
	}
	normalized, digest := artifactdelivery.NormalizeLinuxInstaller(raw)
	if requirePinned && digest != artifactdelivery.LinuxInstallerSHA256 {
		return "", errors.New("install.sh differs from artifactdelivery.LinuxInstallerSHA256; bump the pin with the installer")
	}
	mirror, err := newR2Mirror()
	if err != nil {
		return "", err
	}
	staged, err := os.CreateTemp("", "install-*.sh")
	if err != nil {
		return "", err
	}
	defer func() { _ = os.Remove(staged.Name()) }()
	_, err = staged.Write(normalized)
	if closeErr := staged.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return "", err
	}
	file := r2MirrorFile{path: staged.Name(), key: artifactdelivery.LinuxInstallerObjectKey(digest), sha256: digest,
		size: int64(len(normalized)), contentType: "text/x-shellscript"}
	state, err := mirror.put(file)
	if err != nil {
		return "", fmt.Errorf("%s: %w", file.key, err)
	}
	return state + " " + file.key, nil
}

// releaseMirrorFiles binds every file to the index the publication generated:
// a file whose bytes differ from its index entry is never uploaded.
func releaseMirrorFiles(dir string) ([]r2MirrorFile, error) {
	indexPath := filepath.Join(dir, "artifact-index.json")
	raw, err := os.ReadFile(indexPath) // #nosec G304 -- the workflow's own dist directory.
	if err != nil {
		return nil, err
	}
	if len(raw) > r2MirrorIndexMax {
		return nil, errors.New("artifact index exceeds the delivery bound")
	}
	var index struct {
		Schema         string `json:"schema"`
		ReleaseVersion string `json:"release_version"`
		Artifacts      []struct {
			SHA256   string `json:"sha256"`
			Size     int64  `json:"size"`
			Filename string `json:"filename"`
		} `json:"artifacts"`
	}
	if err := json.Unmarshal(raw, &index); err != nil {
		return nil, err
	}
	if index.Schema != "techstack.artifact-index/v1" || !r2VersionPattern.MatchString(index.ReleaseVersion) || len(index.Artifacts) == 0 || len(index.Artifacts) > 4 {
		return nil, errors.New("invalid artifact index")
	}
	prefix := "techstack/guard-v" + index.ReleaseVersion + "/sha256/"
	var files []r2MirrorFile
	for _, entry := range index.Artifacts {
		if !r2DigestPattern.MatchString(entry.SHA256) || filepath.Base(entry.Filename) != entry.Filename || entry.Size <= 0 {
			return nil, errors.New("invalid artifact index entry")
		}
		path := filepath.Join(dir, entry.Filename)
		digest, size, err := fileDigest(path)
		if err != nil {
			return nil, err
		}
		if digest != entry.SHA256 || size != entry.Size {
			return nil, fmt.Errorf("%s differs from its artifact index entry", entry.Filename)
		}
		contentType := "application/octet-stream"
		if strings.HasSuffix(entry.Filename, ".tar.gz") {
			contentType = "application/gzip"
		}
		files = append(files, r2MirrorFile{path: path, key: prefix + entry.SHA256 + "/" + entry.Filename, sha256: digest, size: size, contentType: contentType})
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(raw))
	return append(files, r2MirrorFile{path: indexPath, key: prefix + digest + "/artifact-index.json",
		sha256: digest, size: int64(len(raw)), contentType: "application/json"}), nil
}

func fileDigest(path string) (string, int64, error) {
	file, err := os.Open(path) // #nosec G304 -- a file named by the verified index.
	if err != nil {
		return "", 0, err
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	return fmt.Sprintf("%x", hash.Sum(nil)), size, err
}

// put is idempotent. An existing object must already hold these bytes; one
// uploaded without a native checksum (earlier manual mirrors) is rewritten with
// the same bytes, which R2 refuses unless they match the checksum.
func (m r2Mirror) put(file r2MirrorFile) (string, error) {
	checksum, err := hex.DecodeString(file.sha256)
	if err != nil {
		return "", err
	}
	want := base64.StdEncoding.EncodeToString(checksum)
	size, stored, found, err := m.head(file.key)
	if err != nil {
		return "", err
	}
	state := "uploaded"
	if found {
		if size != file.size || (stored != "" && stored != want) {
			return "", errors.New("an object with different bytes exists; delivery objects are immutable")
		}
		if stored == want {
			return "present", nil
		}
		state = "checksummed"
	}
	body, err := os.Open(file.path)
	if err != nil {
		return "", err
	}
	defer func() { _ = body.Close() }()
	headers := map[string]string{"content-type": file.contentType, "x-amz-checksum-sha256": want}
	if !found {
		headers["if-none-match"] = "*"
	}
	response, err := m.do(http.MethodPut, file.key, body, file.size, file.sha256, headers)
	if err != nil {
		return "", err
	}
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return "", fmt.Errorf("upload answered %d", response.StatusCode)
	}
	size, stored, found, err = m.head(file.key)
	if err != nil || !found || size != file.size || stored != want {
		return "", errors.New("readback does not show the uploaded size and SHA-256")
	}
	return state, nil
}

func (m r2Mirror) head(key string) (int64, string, bool, error) {
	response, err := m.do(http.MethodHead, key, nil, 0, emptyPayloadSHA256, map[string]string{"x-amz-checksum-mode": "ENABLED"})
	if err != nil {
		return 0, "", false, err
	}
	_ = response.Body.Close()
	switch response.StatusCode {
	case http.StatusNotFound:
		return 0, "", false, nil
	case http.StatusOK:
		size, err := strconv.ParseInt(response.Header.Get("Content-Length"), 10, 64)
		return size, response.Header.Get("x-amz-checksum-sha256"), true, err
	}
	return 0, "", false, fmt.Errorf("readback answered %d", response.StatusCode)
}

const emptyPayloadSHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// do signs an S3 request with AWS Signature Version 4 (region "auto"). The
// payload hash is the file's SHA-256, so R2 checks the body twice.
func (m r2Mirror) do(method, key string, body io.Reader, size int64, payloadSHA256 string, extra map[string]string) (*http.Response, error) {
	segments := strings.Split(m.bucket+"/"+key, "/")
	for i, segment := range segments {
		segments[i] = url.PathEscape(segment)
	}
	path := "/" + strings.Join(segments, "/")
	request, err := http.NewRequest(method, m.endpoint+path, body)
	if err != nil {
		return nil, err
	}
	if body != nil {
		request.ContentLength = size
	}
	now := m.now().UTC()
	stamp, day := now.Format("20060102T150405Z"), now.Format("20060102")
	headers := map[string]string{"host": request.URL.Host, "x-amz-date": stamp, "x-amz-content-sha256": payloadSHA256}
	for name, value := range extra {
		headers[name] = value
	}
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	var canonical bytes.Buffer
	for _, name := range names {
		canonical.WriteString(name + ":" + strings.TrimSpace(headers[name]) + "\n")
		if name != "host" {
			request.Header.Set(name, headers[name])
		}
	}
	signed := strings.Join(names, ";")
	scope := day + "/auto/s3/aws4_request"
	canonicalRequest := strings.Join([]string{method, path, "", canonical.String(), signed, payloadSHA256}, "\n")
	digest := sha256.Sum256([]byte(canonicalRequest))
	toSign := "AWS4-HMAC-SHA256\n" + stamp + "\n" + scope + "\n" + hex.EncodeToString(digest[:])
	signingKey := []byte("AWS4" + m.secretKey)
	for _, part := range []string{day, "auto", "s3", "aws4_request"} {
		signingKey = hmacSHA256(signingKey, part)
	}
	request.Header.Set("Authorization", "AWS4-HMAC-SHA256 Credential="+m.accessKey+"/"+scope+
		", SignedHeaders="+signed+", Signature="+hex.EncodeToString(hmacSHA256(signingKey, toSign)))
	return m.client.Do(request)
}

func hmacSHA256(key []byte, value string) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(value))
	return mac.Sum(nil)
}
