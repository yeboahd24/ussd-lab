package netdetect

import (
	"net"
	"net/netip"
	"testing"
)

func iface(name string, flags net.Flags, cidrs ...string) Interface {
	i := Interface{Name: name, Flags: flags}
	for _, c := range cidrs {
		i.Prefixes = append(i.Prefixes, netip.MustParsePrefix(c))
	}
	return i
}

const up = net.FlagUp | net.FlagBroadcast | net.FlagMulticast

// devMachine is a verbatim capture of the interfaces on the machine this tool
// was developed on: one real Wi-Fi NIC, Tailscale, docker0, and THIRTEEN Docker
// bridge networks. It is the reason this package exists, so it is a fixture
// rather than an anecdote.
func devMachine() []Interface {
	ifaces := []Interface{
		iface("lo", net.FlagUp|net.FlagLoopback, "127.0.0.1/8"),
		iface("wlp0s20f3", up, "172.23.4.142/24"),
		iface("tailscale0", net.FlagUp|net.FlagPointToPoint, "100.112.238.67/32"),
		iface("docker0", up, "172.17.0.1/16"),
	}
	// The thirteen bridges, exactly as `ip -4 -o addr` reported them.
	bridges := []string{
		"br-d72432caa9cd:172.19.0.1/16", "br-20043d841b6d:172.26.0.1/16",
		"br-67be0b04b0cd:172.20.0.1/16", "br-d50e9c3bc32c:172.27.0.1/16",
		"br-d60b3704d528:172.28.0.1/16", "br-e79929af2037:172.21.0.1/16",
		"br-ebf3f90da7c9:172.25.0.1/16", "br-03382ec0b376:172.23.0.1/16",
		"br-07560aa3d94b:172.18.0.1/16", "br-1be6bcc62957:172.30.0.1/16",
		"br-801573ba6e9f:172.29.0.1/16", "br-83871478dbeb:172.22.0.1/16",
		"br-baa5dde731c5:172.24.0.1/16",
	}
	for _, b := range bridges {
		for i := 0; i < len(b); i++ {
			if b[i] == ':' {
				ifaces = append(ifaces, iface(b[:i], up, b[i+1:]))
				break
			}
		}
	}
	return ifaces
}

// The headline case: the real Wi-Fi address must win against 13 Docker bridges,
// docker0 and Tailscale.
func TestDetect_DeveloperMachine(t *testing.T) {
	t.Parallel()

	res := Detect(devMachine())

	if res.Best == nil {
		t.Fatal("Best = nil, want the Wi-Fi address")
	}
	if got := res.Best.Addr.String(); got != "172.23.4.142" {
		t.Errorf("Best = %s (%s), want 172.23.4.142 (wlp0s20f3)", got, res.Best.Interface)
	}
	if res.Ambiguous {
		t.Error("Ambiguous = true; this machine has one obvious answer")
	}

	// Nothing virtual may even be offered as an alternative.
	for _, c := range res.Candidates {
		if isVirtual(c.Interface) {
			t.Errorf("virtual interface %s offered as a candidate", c.Interface)
		}
	}
	if len(res.Candidates) != 1 {
		t.Errorf("candidates = %v, want only the Wi-Fi address", res.Candidates)
	}
}

func TestDetect_ExcludesVirtual(t *testing.T) {
	t.Parallel()

	names := []string{
		"docker0", "br-abc123", "virbr0", "veth1234", "tun0", "tap0",
		"tailscale0", "zt7q4mn", "wg0", "vboxnet0", "vmnet1", "bridge100",
		"utun3", "awdl0",
	}

	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			res := Detect([]Interface{iface(name, up, "192.168.1.5/24")})
			if res.Best != nil {
				t.Errorf("Best = %v, want nil: %s must be excluded", res.Best, name)
			}
		})
	}
}

func TestDetect_ExcludesUnusable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		iface Interface
	}{
		{"loopback", iface("lo", net.FlagUp|net.FlagLoopback, "127.0.0.1/8")},
		{"down", iface("eth0", 0, "192.168.1.5/24")},
		{"point to point", iface("ppp0", net.FlagUp|net.FlagPointToPoint, "10.0.0.2/32")},
		{"ipv6 only", iface("eth0", up, "fe80::1/64")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if res := Detect([]Interface{tt.iface}); res.Best != nil {
				t.Errorf("Best = %v, want nil", res.Best)
			}
		})
	}
}

// Wi-Fi should be preferred over Ethernet: the phone is on Wi-Fi.
func TestDetect_PrefersWireless(t *testing.T) {
	t.Parallel()

	res := Detect([]Interface{
		iface("eth0", up, "192.168.1.10/24"),
		iface("wlp3s0", up, "192.168.1.20/24"),
	})

	if res.Best == nil || res.Best.Interface != "wlp3s0" {
		t.Errorf("Best = %v, want wlp3s0", res.Best)
	}
}

// A self-assigned 169.254 address means DHCP failed; a real LAN address on
// another interface must win.
func TestDetect_DeprioritisesLinkLocal(t *testing.T) {
	t.Parallel()

	res := Detect([]Interface{
		iface("eth0", up, "169.254.31.7/16"),
		iface("eth1", up, "192.168.1.20/24"),
	})

	if res.Best == nil || res.Best.Addr.String() != "192.168.1.20" {
		t.Errorf("Best = %v, want 192.168.1.20", res.Best)
	}
}

// Two equally plausible interfaces must be reported as ambiguous so the caller
// can show alternatives instead of guessing.
func TestDetect_Ambiguous(t *testing.T) {
	t.Parallel()

	res := Detect([]Interface{
		iface("wlan0", up, "192.168.1.10/24"),
		iface("wlan1", up, "192.168.2.10/24"),
	})

	if !res.Ambiguous {
		t.Error("Ambiguous = false, want true for two equal candidates")
	}
	if len(res.Candidates) != 2 {
		t.Errorf("candidates = %d, want 2", len(res.Candidates))
	}
}

func TestDetect_NoInterfaces(t *testing.T) {
	t.Parallel()

	res := Detect(nil)
	if res.Best != nil {
		t.Errorf("Best = %v, want nil", res.Best)
	}
	if res.Ambiguous {
		t.Error("Ambiguous = true with no candidates")
	}
}

// Ordering must not depend on enumeration order.
func TestDetect_Deterministic(t *testing.T) {
	t.Parallel()

	forward := Detect([]Interface{
		iface("eth0", up, "192.168.1.10/24"),
		iface("eth1", up, "192.168.1.11/24"),
	})
	reversed := Detect([]Interface{
		iface("eth1", up, "192.168.1.11/24"),
		iface("eth0", up, "192.168.1.10/24"),
	})

	if forward.Best.Addr != reversed.Best.Addr {
		t.Errorf("order-dependent result: %v vs %v", forward.Best, reversed.Best)
	}
}

func TestDetect_ReasonsAreRecorded(t *testing.T) {
	t.Parallel()

	res := Detect([]Interface{iface("wlp0s20f3", up, "192.168.1.20/24")})
	if res.Best == nil {
		t.Fatal("Best = nil")
	}
	if len(res.Best.Reasons) == 0 {
		t.Error("no reasons recorded; diagnostics depend on them")
	}
}

// System() must not error on the machine running the tests.
func TestSystem(t *testing.T) {
	t.Parallel()

	ifaces, err := System()
	if err != nil {
		t.Fatalf("System() error = %v", err)
	}
	if len(ifaces) == 0 {
		t.Error("System() returned no interfaces")
	}
}
