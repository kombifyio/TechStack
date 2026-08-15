package unifier

import (
	"encoding/json"
	"fmt"
	"strings"
	"text/template"

	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

func newIaCTemplateFuncMap() template.FuncMap {
	return template.FuncMap{
		"json":                 templateJSON,
		"jsonPretty":           templateJSONPretty,
		"lower":                strings.ToLower,
		"upper":                strings.ToUpper,
		"title":                templateTitle,
		"replace":              strings.ReplaceAll,
		"contains":             strings.Contains,
		"hasPrefix":            strings.HasPrefix,
		"hasSuffix":            strings.HasSuffix,
		"split":                strings.Split,
		"trim":                 strings.TrimSpace,
		"quote":                templateQuote,
		iacTemplateFuncDefault: templateDefault,
		"join":                 strings.Join,
		"list":                 templateList,
		"dict":                 templateDict,
		"add":                  templateAdd,
		"sub":                  templateSub,
		"isUbuntu":             isUbuntuOS,
		"isDebian":             isDebianOS,
		"isRHEL":               isRHELOS,
		"isFedora":             isFedoraOS,
		"isSUSE":               isSUSEOS,
		"packageManager":       packageManagerForOS,
	}
}

func templateJSON(v interface{}) string { b, _ := json.Marshal(v); return string(b) }

func templateJSONPretty(v interface{}) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

func templateTitle(s string) string { return cases.Title(language.English).String(s) }

func templateQuote(s string) string { return fmt.Sprintf("%q", s) }

func templateDefault(defVal, val interface{}) interface{} {
	if val == nil || val == "" {
		return defVal
	}
	return val
}

func templateList(args ...interface{}) []interface{} { return args }

func templateDict(values ...interface{}) (map[string]interface{}, error) {
	if len(values)%2 != 0 {
		return nil, fmt.Errorf("dict requires even number of arguments")
	}

	dict := make(map[string]interface{}, len(values)/2)
	for i := 0; i < len(values); i += 2 {
		key, ok := values[i].(string)
		if !ok {
			return nil, fmt.Errorf("dict keys must be strings")
		}
		dict[key] = values[i+1]
	}
	return dict, nil
}

func templateAdd(a, b int) int { return a + b }

func templateSub(a, b int) int { return a - b }

func isUbuntuOS(osType string) bool { return hasOSPrefix(osType, "ubuntu") }

func isDebianOS(osType string) bool { return hasOSPrefix(osType, "debian") }

func isRHELOS(osType string) bool { return hasOSPrefix(osType, "rhel", "centos", "rocky", "alma") }

func isFedoraOS(osType string) bool { return hasOSPrefix(osType, "fedora") }

func isSUSEOS(osType string) bool { return hasOSPrefix(osType, "suse", "opensuse", "sles") }

func packageManagerForOS(osType string) string {
	if isUbuntuOS(osType) || isDebianOS(osType) {
		return iacPackageManagerAPT
	}
	if isSUSEOS(osType) {
		return "zypper"
	}
	return iacPackageManagerDNF
}

func hasOSPrefix(osType string, prefixes ...string) bool {
	osType = strings.ToLower(osType)
	for _, prefix := range prefixes {
		if strings.HasPrefix(osType, prefix) {
			return true
		}
	}
	return false
}
