package stackkitrelease

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

// CandidateArchive selects the exact retained archive named by the independently
// pinned digest, not the deployment kit label. The signed Guard tree and root
// install admission remain the authority; this function only projects metadata.
func CandidateArchive(indexRaw []byte, pin Pin) (kit, name, sbomSHA256 string, err error) {
	refuse := func() (string, string, string, error) {
		return "", "", "", fmt.Errorf("candidate archive identity differs from its exact admitted index")
	}
	if pin.Candidate == nil || pin.Candidate.Validate() != nil || len(indexRaw) == 0 || len(indexRaw) > 1<<20 || pin.IndexSHA256 != pin.Candidate.IndexSHA256 {
		return refuse()
	}
	sum := sha256.Sum256(indexRaw)
	if hex.EncodeToString(sum[:]) != pin.IndexSHA256 {
		return refuse()
	}
	var index struct {
		DeclaredVersion string `json:"declaredVersion"`
		Assets          []struct {
			Name   string `json:"name"`
			SHA256 string `json:"sha256"`
		} `json:"assets"`
	}
	if json.Unmarshal(indexRaw, &index) != nil || index.DeclaredVersion != strings.TrimPrefix(pin.Version, "v") || len(index.Assets) < 2 || len(index.Assets) > 128 {
		return refuse()
	}
	extension := ".tar.gz"
	if pin.Platform.OS == "windows" {
		extension = ".zip"
	}
	suffix := "_" + index.DeclaredVersion + "_" + pin.Platform.OS + "_" + pin.Platform.Arch + extension
	seen := map[string]bool{}
	count := 0
	for _, asset := range index.Assets {
		key := strings.ToLower(asset.Name)
		if seen[key] || !sha256Pattern.MatchString(asset.SHA256) {
			return refuse()
		}
		seen[key] = true
		if asset.SHA256 != pin.ArchiveSHA256 || !strings.HasSuffix(asset.Name, suffix) {
			continue
		}
		count++
		archive, ok := strings.CutPrefix(asset.Name, "stackkits-")
		if !ok {
			return refuse()
		}
		kit = strings.TrimSuffix(archive, suffix)
		if !safeKitPattern.MatchString(kit) {
			return refuse()
		}
		name = asset.Name
	}
	if count != 1 {
		return refuse()
	}
	for _, asset := range index.Assets {
		if asset.Name == name+".spdx.json" {
			sbomSHA256 = asset.SHA256
		}
	}
	if !sha256Pattern.MatchString(sbomSHA256) {
		return refuse()
	}
	return kit, name, sbomSHA256, nil
}
