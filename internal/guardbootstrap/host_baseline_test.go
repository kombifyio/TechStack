package guardbootstrap

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// An immutable Ubuntu image choice must fail before any package or host mutation
// when the running OS differs; both the default and compatibility choices apply.
func TestNeutralHostBaselineRefusesWrongOSBeforeMutation(t *testing.T) {
	directory := t.TempDir()
	osRelease := filepath.Join(directory, "os-release")
	mutation := filepath.Join(directory, "mutation")
	if err := os.WriteFile(mutation, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	// First package mutation would change the sentinel rather than touch the host.
	if err := os.WriteFile(filepath.Join(directory, "timeout"), []byte("#!/bin/sh\nprintf changed >"+shellQuote(mutation)+"\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	for _, declared := range []string{"26.04", "24.04"} {
		for _, running := range []string{"ID=debian\nVERSION_ID=" + declared, "ID=ubuntu\nVERSION_ID=22.04"} {
			if err := os.WriteFile(osRelease, []byte(running), 0600); err != nil {
				t.Fatal(err)
			}
			script, err := RenderNeutralUbuntuHostBaseline(declared, "prepare")
			if err != nil {
				t.Fatal(err)
			}
			script = strings.Replace(script, ". /etc/os-release", ". "+shellQuote(osRelease), 1)
			command := exec.Command("bash", "-s")
			command.Stdin = strings.NewReader("export PATH=" + shellQuote(directory) + ":\"$PATH\"\n" + script)
			if err := command.Run(); err == nil {
				t.Fatalf("declared %s admitted incompatible OS", declared)
			}
			got, err := os.ReadFile(mutation)
			if err != nil || string(got) != "unchanged" {
				t.Fatalf("wrong OS reached package mutation: %q, %v", got, err)
			}
		}
	}
}

// Neutral images may only admit SSH ingress. A provider host can retain its
// workload policy, but the image verifier must reject any additional admission.
func TestNeutralHostBaselineRejectsAdditionalFirewallPermissions(t *testing.T) {
	directory := t.TempDir()
	osRelease := filepath.Join(directory, "os-release")
	policy := filepath.Join(directory, "ufw-policy")
	write := func(name, content string) {
		t.Helper()
		if err := os.WriteFile(name, []byte(content), 0700); err != nil {
			t.Fatal(err)
		}
	}
	write(osRelease, "ID=ubuntu\nVERSION_ID=26.04\n")
	for name, content := range map[string]string{
		"docker":    "exit 0",
		"nft":       "exit 0",
		"systemctl": "case \"$*\" in *nftables*) exit 1;; *) exit 0;; esac",
		"sshd":      "printf 'passwordauthentication no\\nkbdinteractiveauthentication no\\n'; awk 'BEGIN { for (i=0; i<10000; i++) print \"other-setting value\" }'",
		"ufw": "if [ \"$*\" = 'status verbose' ]; then\n" +
			"printf 'Status: active\\nDefault: deny (incoming), allow (outgoing), deny (routed)\\n'\n" +
			"else cat " + shellQuote(policy) + "; fi",
	} {
		write(filepath.Join(directory, name), "#!/bin/sh\n"+content+"\n")
	}
	script, err := RenderNeutralUbuntuHostBaseline("26.04", "verify")
	if err != nil {
		t.Fatal(err)
	}
	script = strings.Replace(script, ". /etc/os-release", ". "+shellQuote(osRelease), 1)
	ssh := "Status: active\n\nTo                         Action      From\n--                         ------      ----\n" +
		"22/tcp                     ALLOW       Anywhere\n" +
		"22/tcp (v6)                ALLOW       Anywhere (v6)\n"
	for _, rule := range []string{
		"", "443/tcp ALLOW Anywhere", "443/tcp (v6) ALLOW IN Anywhere (v6)",
		"Anywhere (v6) ALLOW IN Anywhere (v6)", "443/tcp LIMIT IN Anywhere",
		"22/tcp ALLOW FWD Anywhere", "443/tcp ALLOW OUT Anywhere", "22/udp ALLOW Anywhere",
	} {
		t.Run(rule, func(t *testing.T) {
			write(policy, ssh+rule+"\n")
			command := exec.Command("bash", "-s")
			command.Stdin = strings.NewReader("export PATH=" + shellQuote(directory) + ":\"$PATH\"\n" + script)
			output, err := command.CombinedOutput()
			if (err == nil) != (rule == "") {
				t.Fatalf("firewall admission for added rule %q: %v, %s", rule, err, output)
			}
		})
	}
}
