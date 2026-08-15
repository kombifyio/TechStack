package discovery

import "testing"

func TestEstimateIPv4HostCount(t *testing.T) {
	tests := []struct {
		cidr    string
		want    int
		wantErr bool
	}{
		{"192.168.1.0/24", 254, false},
		{"10.0.0.0/8", 16777214, false},
		{"192.168.1.0/30", 2, false},
		{"192.168.1.1/32", 1, false},
		{"not-a-cidr", 0, true},
		{"2001:db8::/64", 0, true},
	}

	for _, tt := range tests {
		got, err := estimateIPv4HostCount(tt.cidr)
		if (err != nil) != tt.wantErr {
			t.Fatalf("estimateIPv4HostCount(%q) err=%v wantErr=%v", tt.cidr, err, tt.wantErr)
		}
		if err == nil && got != tt.want {
			t.Fatalf("estimateIPv4HostCount(%q)=%d want %d", tt.cidr, got, tt.want)
		}
	}
}

func TestSampleIPv4Hosts_Bounded(t *testing.T) {
	hosts, err := sampleIPv4Hosts("192.168.1.0/24", 16)
	if err != nil {
		t.Fatalf("sampleIPv4Hosts error: %v", err)
	}
	if len(hosts) != 16 {
		t.Fatalf("expected 16 hosts, got %d", len(hosts))
	}
	// Ensure sampled hosts are within subnet and never include network/broadcast for /24.
	for _, h := range hosts {
		if h == "192.168.1.0" || h == "192.168.1.255" {
			t.Fatalf("sample included reserved address %s", h)
		}
	}
}

func TestSampleIPv4Hosts_SmallerThanLimit(t *testing.T) {
	hosts, err := sampleIPv4Hosts("192.168.1.0/30", 100)
	if err != nil {
		t.Fatalf("sampleIPv4Hosts error: %v", err)
	}
	if len(hosts) != 2 {
		t.Fatalf("expected 2 hosts, got %d", len(hosts))
	}
}

func TestSampleIPv4Hosts_InvalidInputs(t *testing.T) {
	if _, err := sampleIPv4Hosts("not-a-cidr", 10); err == nil {
		t.Fatal("expected error for invalid CIDR")
	}
	if _, err := sampleIPv4Hosts("2001:db8::/64", 10); err == nil {
		t.Fatal("expected error for IPv6 CIDR")
	}
	if _, err := sampleIPv4Hosts("192.168.1.0/24", 0); err == nil {
		t.Fatal("expected error for limit=0")
	}
}
