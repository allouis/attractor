package cli

import (
	"net"
	"reflect"
	"testing"
)

// ipNet builds a *net.IPNet the way net.InterfaceAddrs reports one, so
// tests can fabricate interface addresses without a real interface.
func ipNet(cidr string) *net.IPNet {
	ip, n, err := net.ParseCIDR(cidr)
	if err != nil {
		panic(err)
	}
	n.IP = ip
	return n
}

func ips(addrs ...string) []net.IP {
	out := make([]net.IP, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, net.ParseIP(a))
	}
	return out
}

func TestTailnetIfaceIPs(t *testing.T) {
	tests := []struct {
		name  string
		addrs []net.Addr
		want  []string
	}{
		{"none", []net.Addr{ipNet("192.168.1.5/24"), ipNet("127.0.0.1/8")}, nil},
		{"one CGNAT", []net.Addr{ipNet("192.168.1.5/24"), ipNet("100.101.102.103/10")}, []string{"100.101.102.103"}},
		{"public and LAN rejected", []net.Addr{ipNet("10.0.0.4/8"), ipNet("8.8.8.8/32"), ipNet("172.16.0.1/12")}, nil},
		{"multiple CGNAT in order", []net.Addr{ipNet("100.64.0.1/10"), ipNet("100.127.255.254/10")}, []string{"100.64.0.1", "100.127.255.254"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotS []string
			for _, ip := range tailnetIfaceIPs(tt.addrs) {
				gotS = append(gotS, ip.String())
			}
			if !reflect.DeepEqual(gotS, tt.want) {
				t.Fatalf("tailnetIfaceIPs = %v, want %v", gotS, tt.want)
			}
		})
	}
}

func TestUIBinds(t *testing.T) {
	loopback := uiBind{Addr: "127.0.0.1:0", Kind: bindLoopback}
	tests := []struct {
		name        string
		explicit    string
		explicitSet bool
		tailnet     []net.IP
		addTailnet  bool
		want        []uiBind
	}{
		{name: "no tailnet: loopback only", addTailnet: true, want: []uiBind{loopback}},
		{name: "tailnet present: dual bind", tailnet: ips("100.101.102.103"), addTailnet: true, want: []uiBind{loopback, {Addr: "100.101.102.103:0", Kind: bindTailnet}}},
		{name: "tailnet present but addTailnet off (announce-only): loopback only", tailnet: ips("100.101.102.103"), addTailnet: false, want: []uiBind{loopback}},
		{name: "multiple tailnet: only first bound", tailnet: ips("100.64.0.1", "100.127.255.254"), addTailnet: true, want: []uiBind{loopback, {Addr: "100.64.0.1:0", Kind: bindTailnet}}},
		{name: "explicit suppresses tailnet", explicit: "0.0.0.0:8080", explicitSet: true, tailnet: ips("100.101.102.103"), addTailnet: true, want: []uiBind{{Addr: "0.0.0.0:8080", Kind: bindExplicit}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := uiBinds(tt.explicit, tt.explicitSet, tt.tailnet, tt.addTailnet)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("uiBinds = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestBindKindRoles(t *testing.T) {
	cases := []struct {
		kind    bindKind
		primary bool
		bare    bool
	}{
		{bindLoopback, true, false},
		{bindTailnet, false, true},
		{bindExplicit, true, false},
	}
	for _, c := range cases {
		b := uiBind{Kind: c.kind}
		if b.primary() != c.primary || b.bare() != c.bare {
			t.Errorf("kind %d: primary=%v bare=%v, want %v/%v", c.kind, b.primary(), b.bare(), c.primary, c.bare)
		}
	}
}

func TestIPIsPublic(t *testing.T) {
	tailnet := ips("100.64.0.9")
	tests := []struct {
		ip      string
		tailnet []net.IP
		want    bool
	}{
		{"127.0.0.1", nil, false},
		{"::1", nil, false},
		// A CGNAT address is public UNLESS it is one of the host's detected
		// tailnet interface IPs — range membership alone is not trust.
		{"100.64.0.9", nil, true},
		{"100.64.0.9", tailnet, false},
		{"100.64.0.1", tailnet, true},
		{"0.0.0.0", nil, true},
		{"::", nil, true},
		{"192.168.1.5", nil, true},
		{"8.8.8.8", nil, true},
	}
	for _, tt := range tests {
		if got := ipIsPublic(net.ParseIP(tt.ip), tt.tailnet); got != tt.want {
			t.Errorf("ipIsPublic(%s, %v) = %v, want %v", tt.ip, tt.tailnet, got, tt.want)
		}
	}
	if !ipIsPublic(nil, nil) {
		t.Errorf("ipIsPublic(nil) = false, want true (fail closed)")
	}
}
