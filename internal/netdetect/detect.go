// Package netdetect chooses the LAN address to advertise to the phone.
//
// This is harder than it looks. The machine this tool was developed on has
// sixteen global IPv4 interfaces: one real Wi-Fi NIC, a Tailscale interface,
// docker0, and thirteen Docker bridge networks in 172.18-172.30. A naive
// "first non-loopback address" heuristic picks a Docker bridge and prints a QR
// code that cannot possibly work -- and the resulting failure (the phone scans,
// the page never loads) gives the developer almost no diagnostic signal.
//
// So candidates are SCORED, and when the result is ambiguous the caller is
// expected to say so rather than guess silently (ADR-004).
package netdetect

import (
	"fmt"
	"net"
	"net/netip"
	"sort"
	"strings"
)

// virtualPrefixes name interfaces created by container runtimes, VPNs and
// hypervisors. They are excluded outright rather than down-weighted: a phone on
// the Wi-Fi network can never reach a Docker bridge, so such an address is
// always wrong, never merely worse.
var virtualPrefixes = []string{
	"docker", "br-", "virbr", "veth", "cni", "flannel", "kube",
	"tun", "tap", "utun", "ppp",
	"tailscale", "zt", "wg", "nordlynx", "proton",
	"vboxnet", "vmnet", "vnet", "vmware", "hyperv",
	"bridge", "awdl", "llw", "ap1",
}

// wirelessPrefixes name interfaces that are usually Wi-Fi. A phone sharing a
// network with the laptop is nearly always on Wi-Fi, so this is the single most
// useful signal after excluding virtual interfaces.
var wirelessPrefixes = []string{"wl", "wlan", "wlp", "wifi", "ath", "ra"}

// wiredPrefixes name ordinary Ethernet interfaces.
var wiredPrefixes = []string{"en", "eth", "eno", "ens", "enp", "em"}

// Score weights. Absolute values do not matter; the ordering does.
const (
	scorePrivate    = 50 // RFC1918: a real LAN address
	scoreWireless   = 30
	scoreWired      = 15
	scoreHomeSubnet = 10  // /24-ish, as home and office networks usually are
	scoreLinkLocal  = -20 // 169.254.x: self-assigned, means DHCP failed
)

// Interface is the subset of a network interface that matters here.
//
// It is a plain struct rather than *net.Interface so that scoring can be tested
// against captured fixtures instead of whatever the test machine happens to
// have configured.
type Interface struct {
	Name     string
	Flags    net.Flags
	Prefixes []netip.Prefix
}

// Candidate is one address that could be advertised.
type Candidate struct {
	Interface string
	Addr      netip.Addr
	Prefix    netip.Prefix
	Score     int
	Reasons   []string
}

func (c Candidate) String() string {
	return fmt.Sprintf("%s (%s)", c.Addr, c.Interface)
}

// Result is the outcome of detection.
type Result struct {
	// Best is the highest-scoring candidate, or nil if there were none.
	Best *Candidate

	// Candidates are every viable address, best first.
	Candidates []Candidate

	// Ambiguous is true when the top two candidates scored equally. The caller
	// should show the alternatives rather than silently commit to one.
	Ambiguous bool
}

// Detect scores the supplied interfaces and picks the best LAN address.
func Detect(ifaces []Interface) Result {
	var candidates []Candidate

	for _, iface := range ifaces {
		if !usable(iface) {
			continue
		}
		for _, p := range iface.Prefixes {
			addr := p.Addr()

			// IPv6 is excluded deliberately. A URL must then be bracketed,
			// link-local forms need a zone index, and no phone browser handles
			// that gracefully. IPv4 is what works.
			if !addr.Is4() || addr.IsLoopback() || addr.IsUnspecified() || addr.IsMulticast() {
				continue
			}

			c := Candidate{Interface: iface.Name, Addr: addr, Prefix: p}
			score(&c, iface)
			candidates = append(candidates, c)
		}
	}

	// Sort by score, then by name, so the outcome is deterministic across runs
	// rather than dependent on interface enumeration order.
	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].Score != candidates[j].Score {
			return candidates[i].Score > candidates[j].Score
		}
		return candidates[i].Interface < candidates[j].Interface
	})

	res := Result{Candidates: candidates}
	if len(candidates) > 0 {
		res.Best = &candidates[0]
	}
	if len(candidates) > 1 && candidates[0].Score == candidates[1].Score {
		res.Ambiguous = true
	}
	return res
}

// usable rejects interfaces a phone could never reach.
func usable(iface Interface) bool {
	if iface.Flags&net.FlagUp == 0 {
		return false
	}
	if iface.Flags&net.FlagLoopback != 0 {
		return false
	}
	if iface.Flags&net.FlagPointToPoint != 0 {
		return false
	}
	return !isVirtual(iface.Name)
}

func isVirtual(name string) bool {
	lower := strings.ToLower(name)
	for _, p := range virtualPrefixes {
		if strings.HasPrefix(lower, p) {
			// "en" would otherwise be shadowed by nothing, but "bridge0" on
			// macOS and "br-xxxx" on Linux both need to go.
			return true
		}
	}
	return false
}

func score(c *Candidate, iface Interface) {
	addr := c.Addr
	lower := strings.ToLower(iface.Name)

	if addr.IsPrivate() {
		c.Score += scorePrivate
		c.Reasons = append(c.Reasons, "private address")
	}
	if addr.IsLinkLocalUnicast() {
		c.Score += scoreLinkLocal
		c.Reasons = append(c.Reasons, "link-local (DHCP may have failed)")
	}

	switch {
	case hasAnyPrefix(lower, wirelessPrefixes):
		c.Score += scoreWireless
		c.Reasons = append(c.Reasons, "wireless interface")
	case hasAnyPrefix(lower, wiredPrefixes):
		c.Score += scoreWired
		c.Reasons = append(c.Reasons, "wired interface")
	}

	// A /24 is the classic home and small-office network. A /16 is far more
	// often a container bridge that slipped past the name filter.
	if bits := c.Prefix.Bits(); bits >= 22 && bits <= 30 {
		c.Score += scoreHomeSubnet
		c.Reasons = append(c.Reasons, "typical LAN subnet size")
	}
}

func hasAnyPrefix(s string, prefixes []string) bool {
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// System enumerates the host's real interfaces.
func System() ([]Interface, error) {
	raw, err := net.Interfaces()
	if err != nil {
		return nil, fmt.Errorf("netdetect: enumerate interfaces: %w", err)
	}

	out := make([]Interface, 0, len(raw))
	for _, ri := range raw {
		iface := Interface{Name: ri.Name, Flags: ri.Flags}

		addrs, err := ri.Addrs()
		if err != nil {
			// One unreadable interface must not prevent detection.
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			pfx, ok := toPrefix(ipnet)
			if ok {
				iface.Prefixes = append(iface.Prefixes, pfx)
			}
		}
		out = append(out, iface)
	}
	return out, nil
}

func toPrefix(ipnet *net.IPNet) (netip.Prefix, bool) {
	addr, ok := netip.AddrFromSlice(ipnet.IP)
	if !ok {
		return netip.Prefix{}, false
	}
	addr = addr.Unmap()
	ones, _ := ipnet.Mask.Size()
	return netip.PrefixFrom(addr, ones), true
}

// DetectSystem scores the host's real interfaces.
func DetectSystem() (Result, error) {
	ifaces, err := System()
	if err != nil {
		return Result{}, err
	}
	return Detect(ifaces), nil
}
