package companion

import (
	"encoding/binary"
	"errors"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/chialecode/trace/internal/companionapi"
)

const (
	mdnsPort = 5353

	dnsTypeA    uint16 = 1
	dnsTypePTR  uint16 = 12
	dnsTypeTXT  uint16 = 16
	dnsTypeAAAA uint16 = 28
	dnsTypeSRV  uint16 = 33
	dnsTypeANY  uint16 = 255

	dnsClassIN         uint16 = 1
	dnsClassCacheFlush uint16 = 0x8000 | dnsClassIN
	dnsFlagResponse    uint16 = 0x8400 // QR=1, AA=1

	mdnsDefaultTTL uint32 = 120
	mdnsMetaQuery         = "_services._dns-sd._udp.local."
)

var mdnsMulticastUDPAddr = &net.UDPAddr{
	IP:   net.IPv4(224, 0, 0, 251),
	Port: mdnsPort,
}

// mdnsSocket abstracts a multicast UDP socket so LAN mDNS logic can be tested
// deterministically without binding real OS multicast ports.
type mdnsSocket interface {
	ReadFrom(b []byte) (int, net.Addr, error)
	WriteTo(b []byte, addr net.Addr) (int, error)
	Close() error
}

// mdnsBoundSocket pairs a multicast socket with the LAN interface name and IPv4
// addresses that should be advertised over that interface.
type mdnsBoundSocket struct {
	ifaceName string
	sock      mdnsSocket
	addrs     []netip.Addr
}

// MDNSFactory opens multicast UDP sockets strictly for the allowed LAN candidates.
// Used to inject fake sockets in unit tests.
type MDNSFactory func(candidates []ListenCandidate) ([]mdnsBoundSocket, error)

// mdnsRecordConfig holds the strictly non-sensitive metadata advertised via mDNS.
type mdnsRecordConfig struct {
	NodeID       string
	NodeName     string
	Channel      string
	Version      string
	Port         int
	InstanceName string // "<label>._trace-companion._tcp.local."
	HostName     string // "trace-<short>.local."
	TXT          []string
}

func newMDNSRecordConfig(nodeID, nodeName, channel, version string, port int) mdnsRecordConfig {
	short := shortASCIIHex(nodeID, 8)
	if short == "" {
		short = "node"
	}
	hostShort := shortASCIIHex(nodeID, 12)
	if hostShort == "" {
		hostShort = short
	}
	label := sanitizeMDNSInstanceLabel(nodeName, short)
	return mdnsRecordConfig{
		NodeID:       sanitizeMDNSTXTValue(nodeID, 64),
		NodeName:     nodeName,
		Channel:      sanitizeMDNSTXTValue(channel, 16),
		Version:      sanitizeMDNSTXTValue(version, 32),
		Port:         port,
		InstanceName: label + "." + companionapi.MDNSServiceFull,
		HostName:     "trace-" + hostShort + ".local.",
		TXT:          buildMDNSTXT(nodeID, channel, version),
	}
}

// buildMDNSTXT builds the strictly allowlisted ASCII TXT records (zero tokens,
// zero pairing codes, zero cookies, zero local paths).
func buildMDNSTXT(nodeID, channel, version string) []string {
	out := []string{
		companionapi.MDNSTXTNode + "=" + sanitizeMDNSTXTValue(nodeID, 64),
		companionapi.MDNSTXTChannel + "=" + sanitizeMDNSTXTValue(channel, 16),
		companionapi.MDNSTXTProto + "=" + companionapi.ProtocolVersion,
	}
	if v := sanitizeMDNSTXTValue(version, 32); v != "" {
		out = append(out, companionapi.MDNSTXTVersion+"="+v)
	}
	return out
}

func sanitizeMDNSTXTValue(raw string, maxLen int) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(raw) {
		if r >= 0x20 && r <= 0x7e && r != '=' {
			b.WriteRune(r)
			if b.Len() >= maxLen {
				break
			}
		}
	}
	return b.String()
}

func shortASCIIHex(raw string, n int) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(raw)) {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') {
			b.WriteRune(r)
			if b.Len() >= n {
				break
			}
		}
	}
	return b.String()
}

func sanitizeMDNSInstanceLabel(nodeName, suffix string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(nodeName) {
		if r == '.' || r < 0x20 || r == 0x7f || unicode.IsControl(r) {
			continue
		}
		// Keep label comfortably under 63 bytes after appending "-" + suffix.
		if b.Len()+len(string(r))+1+len(suffix) > 63 {
			break
		}
		b.WriteRune(r)
	}
	base := strings.TrimSpace(b.String())
	if base == "" {
		return "TRACE-" + suffix
	}
	return base + "-" + suffix
}

// mdnsAdvertiser broadcasts _trace-companion._tcp.local. strictly on allowed
// private LAN interfaces and responds to DNS-SD PTR/SRV/TXT/A/AAAA queries.
type mdnsAdvertiser struct {
	mu      sync.Mutex
	cfg     mdnsRecordConfig
	addrs   []netip.Addr
	sockets []mdnsBoundSocket
	active  bool
	stopped bool
	stopCh  chan struct{}
	wg      sync.WaitGroup
	logf    func(format string, args ...any)
}

func startMDNSAdvertiser(
	cfg mdnsRecordConfig,
	candidates []ListenCandidate,
	factory MDNSFactory,
	logf func(format string, args ...any),
) *mdnsAdvertiser {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	lanAddrs := filterMDNSAddrs(candidates)
	if factory == nil {
		if len(lanAddrs) == 0 {
			return nil
		}
		factory = defaultMDNSFactory
	}
	socks, err := factory(candidates)
	if err != nil || len(socks) == 0 {
		if err != nil {
			logf("companion mdns skipped: %v", err)
		}
		return nil
	}
	if len(lanAddrs) == 0 {
		for _, bs := range socks {
			for _, a := range bs.addrs {
				u := a.Unmap()
				if IsPrivateLANAddr(u) && !prefixLL6.Contains(u) {
					lanAddrs = append(lanAddrs, u)
				}
			}
		}
	}
	if len(lanAddrs) == 0 {
		for _, bs := range socks {
			_ = bs.sock.Close()
		}
		return nil
	}

	adv := &mdnsAdvertiser{
		cfg:     cfg,
		addrs:   lanAddrs,
		sockets: socks,
		active:  true,
		stopCh:  make(chan struct{}),
		logf:    logf,
	}

	// Send initial announcement on all bound LAN sockets.
	adv.broadcastAll(mdnsDefaultTTL)

	// RFC 6762 §8.3: repeat announcement once after 1 second (cancelable on Stop).
	adv.wg.Add(1)
	go func() {
		defer adv.wg.Done()
		t := time.NewTimer(time.Second)
		defer t.Stop()
		select {
		case <-adv.stopCh:
			return
		case <-t.C:
			adv.broadcastAll(mdnsDefaultTTL)
		}
	}()

	for _, bs := range socks {
		bs := bs
		adv.wg.Add(1)
		go func() {
			defer adv.wg.Done()
			adv.serveSocket(bs)
		}()
	}

	logf("companion mdns advertising %s on %d LAN socket(s)", cfg.InstanceName, len(socks))
	return adv
}

func (a *mdnsAdvertiser) Active() bool {
	if a == nil {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.active && !a.stopped
}

func (a *mdnsAdvertiser) Update(cfg mdnsRecordConfig, candidates []ListenCandidate) {
	if a == nil {
		return
	}
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return
	}
	a.cfg = cfg
	a.addrs = filterMDNSAddrs(candidates)
	a.mu.Unlock()
	a.broadcastAll(mdnsDefaultTTL)
}

// Stop sends TTL=0 goodbye packets per RFC 6762 §10.1 and closes all sockets cleanly.
func (a *mdnsAdvertiser) Stop() {
	if a == nil {
		return
	}
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return
	}
	a.stopped = true
	a.active = false
	close(a.stopCh)
	socks := append([]mdnsBoundSocket(nil), a.sockets...)
	cfg := a.cfg
	addrs := append([]netip.Addr(nil), a.addrs...)
	a.mu.Unlock()

	for _, bs := range socks {
		useAddrs := bs.addrs
		if len(useAddrs) == 0 {
			useAddrs = addrs
		}
		if pkt := buildMDNSAnnouncement(cfg, useAddrs, 0); len(pkt) > 0 {
			_, _ = bs.sock.WriteTo(pkt, mdnsMulticastUDPAddr)
		}
		_ = bs.sock.Close()
	}
	a.wg.Wait()
}

func (a *mdnsAdvertiser) broadcastAll(ttl uint32) {
	a.mu.Lock()
	if a.stopped {
		a.mu.Unlock()
		return
	}
	cfg := a.cfg
	globalAddrs := append([]netip.Addr(nil), a.addrs...)
	socks := append([]mdnsBoundSocket(nil), a.sockets...)
	a.mu.Unlock()

	for _, bs := range socks {
		useAddrs := bs.addrs
		if len(useAddrs) == 0 {
			useAddrs = globalAddrs
		}
		if len(useAddrs) == 0 {
			continue
		}
		if pkt := buildMDNSAnnouncement(cfg, useAddrs, ttl); len(pkt) > 0 {
			_, _ = bs.sock.WriteTo(pkt, mdnsMulticastUDPAddr)
		}
	}
}

func (a *mdnsAdvertiser) serveSocket(bs mdnsBoundSocket) {
	buf := make([]byte, 4096)
	for {
		n, src, err := bs.sock.ReadFrom(buf)
		if err != nil {
			return
		}
		if n < 12 {
			continue
		}
		if src != nil {
			if ip, ok := ParseRemoteAddr(src.String()); !ok || !IsAllowedSource(ip, false) {
				continue
			}
		}

		a.mu.Lock()
		if a.stopped {
			a.mu.Unlock()
			return
		}
		cfg := a.cfg
		useAddrs := bs.addrs
		if len(useAddrs) == 0 {
			useAddrs = append([]netip.Addr(nil), a.addrs...)
		}
		a.mu.Unlock()

		resp, unicast := handleMDNSQuery(buf[:n], cfg, useAddrs)
		if len(resp) == 0 {
			continue
		}
		if unicast && src != nil {
			_, _ = bs.sock.WriteTo(resp, src)
		} else {
			_, _ = bs.sock.WriteTo(resp, mdnsMulticastUDPAddr)
		}
	}
}

// filterMDNSAddrs keeps only non-link-local allowed private/VPN addresses eligible
// for mDNS A/AAAA records (never public IPs, never physical CGNAT, never loopback).
func filterMDNSAddrs(candidates []ListenCandidate) []netip.Addr {
	var out []netip.Addr
	seen := make(map[string]bool, len(candidates))
	for _, c := range candidates {
		u := c.Addr.Unmap()
		if !u.IsValid() || u.IsLoopback() || u.IsMulticast() || u.IsUnspecified() {
			continue
		}
		if !IsPrivateLANAddr(u) && !(IsCGNATAddr(u) && c.Virtual) {
			continue
		}
		if prefixLL6.Contains(u) {
			continue
		}
		key := u.String()
		if !seen[key] {
			seen[key] = true
			out = append(out, u)
		}
	}
	return out
}

// defaultMDNSFactory opens multicast 224.0.0.251:5353 UDP sockets strictly on
// physical/LAN interfaces that carry an eligible RFC 1918 private IPv4 candidate.
func defaultMDNSFactory(candidates []ListenCandidate) ([]mdnsBoundSocket, error) {
	if os.Getenv("TRACE_QUIET") != "" {
		return nil, nil
	}
	byIface := make(map[string][]netip.Addr)
	var ifaceOrder []string
	for _, c := range candidates {
		u := c.Addr.Unmap()
		if c.Virtual || !u.Is4() || !IsPrivateLANAddr(u) || strings.TrimSpace(c.Iface) == "" {
			continue
		}
		if _, exists := byIface[c.Iface]; !exists {
			ifaceOrder = append(ifaceOrder, c.Iface)
		}
		byIface[c.Iface] = append(byIface[c.Iface], u)
	}
	// Also attach IPv6 ULA addresses belonging to the same LAN interfaces.
	for _, c := range candidates {
		u := c.Addr.Unmap()
		if c.Virtual || !u.Is6() || !IsPrivateLANAddr(u) {
			continue
		}
		if _, exists := byIface[c.Iface]; exists {
			byIface[c.Iface] = append(byIface[c.Iface], u)
		}
	}
	if len(ifaceOrder) == 0 {
		return nil, nil
	}

	var out []mdnsBoundSocket
	var lastErr error
	for _, name := range ifaceOrder {
		ifi, err := net.InterfaceByName(name)
		if err != nil {
			lastErr = err
			continue
		}
		if ifi.Flags&net.FlagUp == 0 || ifi.Flags&net.FlagMulticast == 0 || ifi.Flags&net.FlagLoopback != 0 {
			continue
		}
		conn, err := net.ListenMulticastUDP("udp4", ifi, mdnsMulticastUDPAddr)
		if err != nil {
			lastErr = err
			continue
		}
		out = append(out, mdnsBoundSocket{
			ifaceName: name,
			sock:      conn,
			addrs:     byIface[name],
		})
	}
	if len(out) == 0 {
		return nil, lastErr
	}
	return out, nil
}

// ---- Pure Go stdlib RFC 1035 / RFC 6762 / RFC 6763 DNS wire codec ----

type dnsQuestion struct {
	Name    string
	Type    uint16
	Class   uint16
	Unicast bool
}

type dnsRR struct {
	Name  string
	Type  uint16
	Class uint16
	TTL   uint32
	RData []byte
}

func handleMDNSQuery(pkt []byte, cfg mdnsRecordConfig, addrs []netip.Addr) ([]byte, bool) {
	if len(pkt) < 12 || len(addrs) == 0 {
		return nil, false
	}
	flags := binary.BigEndian.Uint16(pkt[2:4])
	if flags&0x8000 != 0 {
		// Response packet, ignore.
		return nil, false
	}
	qdCount := int(binary.BigEndian.Uint16(pkt[4:6]))
	if qdCount == 0 || qdCount > 64 {
		return nil, false
	}

	questions, err := parseDNSQuestions(pkt, qdCount)
	if err != nil || len(questions) == 0 {
		return nil, false
	}

	var answers []dnsRR
	var additionals []dnsRR
	added := make(map[string]bool)
	addRR := func(dst *[]dnsRR, rr dnsRR) {
		key := strings.ToLower(rr.Name) + "|" + strconv.Itoa(int(rr.Type)) + "|" + string(rr.RData)
		if added[key] {
			return
		}
		added[key] = true
		*dst = append(*dst, rr)
	}

	svcLower := strings.ToLower(companionapi.MDNSServiceFull)
	instLower := strings.ToLower(cfg.InstanceName)
	hostLower := strings.ToLower(cfg.HostName)

	wantUnicast := false
	matchedAny := false

	for _, q := range questions {
		qName := strings.ToLower(q.Name)
		qClass := q.Class & 0x7fff
		if qClass != dnsClassIN && qClass != dnsTypeANY {
			continue
		}
		switch {
		case qName == mdnsMetaQuery && (q.Type == dnsTypePTR || q.Type == dnsTypeANY):
			matchedAny = true
			if q.Unicast {
				wantUnicast = true
			}
			addRR(&answers, makePTRRecord(mdnsMetaQuery, companionapi.MDNSServiceFull, mdnsDefaultTTL))

		case qName == svcLower && (q.Type == dnsTypePTR || q.Type == dnsTypeANY):
			matchedAny = true
			if q.Unicast {
				wantUnicast = true
			}
			addRR(&answers, makePTRRecord(companionapi.MDNSServiceFull, cfg.InstanceName, mdnsDefaultTTL))
			addRR(&additionals, makeSRVRecord(cfg.InstanceName, cfg.HostName, uint16(cfg.Port), mdnsDefaultTTL))
			addRR(&additionals, makeTXTRecord(cfg.InstanceName, cfg.TXT, mdnsDefaultTTL))
			for _, rr := range makeAddrRecords(cfg.HostName, addrs, mdnsDefaultTTL) {
				addRR(&additionals, rr)
			}

		case qName == instLower:
			switch q.Type {
			case dnsTypeSRV:
				matchedAny = true
				if q.Unicast {
					wantUnicast = true
				}
				addRR(&answers, makeSRVRecord(cfg.InstanceName, cfg.HostName, uint16(cfg.Port), mdnsDefaultTTL))
				for _, rr := range makeAddrRecords(cfg.HostName, addrs, mdnsDefaultTTL) {
					addRR(&additionals, rr)
				}
			case dnsTypeTXT:
				matchedAny = true
				if q.Unicast {
					wantUnicast = true
				}
				addRR(&answers, makeTXTRecord(cfg.InstanceName, cfg.TXT, mdnsDefaultTTL))
			case dnsTypeANY:
				matchedAny = true
				if q.Unicast {
					wantUnicast = true
				}
				addRR(&answers, makeSRVRecord(cfg.InstanceName, cfg.HostName, uint16(cfg.Port), mdnsDefaultTTL))
				addRR(&answers, makeTXTRecord(cfg.InstanceName, cfg.TXT, mdnsDefaultTTL))
				for _, rr := range makeAddrRecords(cfg.HostName, addrs, mdnsDefaultTTL) {
					addRR(&additionals, rr)
				}
			}

		case qName == hostLower && (q.Type == dnsTypeA || q.Type == dnsTypeAAAA || q.Type == dnsTypeANY):
			for _, rr := range makeAddrRecords(cfg.HostName, addrs, mdnsDefaultTTL) {
				if q.Type == dnsTypeANY || rr.Type == q.Type {
					matchedAny = true
					if q.Unicast {
						wantUnicast = true
					}
					addRR(&answers, rr)
				}
			}
		}
	}

	if !matchedAny || len(answers) == 0 {
		return nil, false
	}
	return encodeDNSResponse(answers, additionals), wantUnicast
}

// buildMDNSAnnouncement builds an unsolicited announcement (ttl > 0) or
// RFC 6762 §10.1 goodbye packet (ttl == 0) containing PTR, SRV, TXT, and A/AAAA records.
func buildMDNSAnnouncement(cfg mdnsRecordConfig, addrs []netip.Addr, ttl uint32) []byte {
	if cfg.InstanceName == "" || cfg.HostName == "" || cfg.Port <= 0 || cfg.Port > 65535 || len(addrs) == 0 {
		return nil
	}
	answers := make([]dnsRR, 0, 3+len(addrs))
	answers = append(answers,
		makePTRRecord(companionapi.MDNSServiceFull, cfg.InstanceName, ttl),
		makeSRVRecord(cfg.InstanceName, cfg.HostName, uint16(cfg.Port), ttl),
		makeTXTRecord(cfg.InstanceName, cfg.TXT, ttl),
	)
	answers = append(answers, makeAddrRecords(cfg.HostName, addrs, ttl)...)
	return encodeDNSResponse(answers, nil)
}

func makePTRRecord(serviceName, targetName string, ttl uint32) dnsRR {
	return dnsRR{
		Name:  serviceName,
		Type:  dnsTypePTR,
		Class: dnsClassIN, // PTR is shared, never cache-flush
		TTL:   ttl,
		RData: encodeDNSName(targetName),
	}
}

func makeSRVRecord(instanceName, hostName string, port uint16, ttl uint32) dnsRR {
	target := encodeDNSName(hostName)
	rdata := make([]byte, 6+len(target))
	// Priority=0, Weight=0, Port=port
	binary.BigEndian.PutUint16(rdata[4:6], port)
	copy(rdata[6:], target)
	return dnsRR{
		Name:  instanceName,
		Type:  dnsTypeSRV,
		Class: dnsClassCacheFlush,
		TTL:   ttl,
		RData: rdata,
	}
}

func makeTXTRecord(instanceName string, kv []string, ttl uint32) dnsRR {
	var rdata []byte
	for _, s := range kv {
		b := []byte(s)
		if len(b) > 255 {
			b = b[:255]
		}
		rdata = append(rdata, byte(len(b)))
		rdata = append(rdata, b...)
	}
	if len(rdata) == 0 {
		rdata = []byte{0}
	}
	return dnsRR{
		Name:  instanceName,
		Type:  dnsTypeTXT,
		Class: dnsClassCacheFlush,
		TTL:   ttl,
		RData: rdata,
	}
}

func makeAddrRecords(hostName string, addrs []netip.Addr, ttl uint32) []dnsRR {
	out := make([]dnsRR, 0, len(addrs))
	for _, a := range addrs {
		u := a.Unmap()
		if u.Is4() {
			b4 := u.As4()
			out = append(out, dnsRR{
				Name:  hostName,
				Type:  dnsTypeA,
				Class: dnsClassCacheFlush,
				TTL:   ttl,
				RData: append([]byte(nil), b4[:]...),
			})
		} else if u.Is6() && !prefixLL6.Contains(u) {
			b16 := u.As16()
			out = append(out, dnsRR{
				Name:  hostName,
				Type:  dnsTypeAAAA,
				Class: dnsClassCacheFlush,
				TTL:   ttl,
				RData: append([]byte(nil), b16[:]...),
			})
		}
	}
	return out
}

func encodeDNSResponse(answers, additionals []dnsRR) []byte {
	var buf []byte
	var hdr [12]byte
	binary.BigEndian.PutUint16(hdr[2:4], dnsFlagResponse)
	binary.BigEndian.PutUint16(hdr[6:8], uint16(len(answers)))
	binary.BigEndian.PutUint16(hdr[10:12], uint16(len(additionals)))
	buf = append(buf, hdr[:]...)
	for _, rr := range answers {
		buf = appendDNSRR(buf, rr)
	}
	for _, rr := range additionals {
		buf = appendDNSRR(buf, rr)
	}
	return buf
}

func appendDNSRR(dst []byte, rr dnsRR) []byte {
	dst = append(dst, encodeDNSName(rr.Name)...)
	dst = binary.BigEndian.AppendUint16(dst, rr.Type)
	dst = binary.BigEndian.AppendUint16(dst, rr.Class)
	dst = binary.BigEndian.AppendUint32(dst, rr.TTL)
	dst = binary.BigEndian.AppendUint16(dst, uint16(len(rr.RData)))
	dst = append(dst, rr.RData...)
	return dst
}

func encodeDNSName(name string) []byte {
	trimmed := strings.TrimSuffix(strings.TrimSpace(name), ".")
	if trimmed == "" {
		return []byte{0}
	}
	labels := strings.Split(trimmed, ".")
	out := make([]byte, 0, len(trimmed)+2)
	for _, lbl := range labels {
		b := []byte(lbl)
		if len(b) == 0 {
			continue
		}
		if len(b) > 63 {
			b = b[:63]
		}
		out = append(out, byte(len(b)))
		out = append(out, b...)
	}
	out = append(out, 0)
	return out
}

func parseDNSQuestions(pkt []byte, qdCount int) ([]dnsQuestion, error) {
	off := 12
	out := make([]dnsQuestion, 0, qdCount)
	for i := 0; i < qdCount; i++ {
		name, nextOff, err := decodeDNSName(pkt, off)
		if err != nil {
			return nil, err
		}
		if nextOff+4 > len(pkt) {
			return nil, errors.New("dns: truncated question")
		}
		qtype := binary.BigEndian.Uint16(pkt[nextOff : nextOff+2])
		qclassRaw := binary.BigEndian.Uint16(pkt[nextOff+2 : nextOff+4])
		out = append(out, dnsQuestion{
			Name:    name,
			Type:    qtype,
			Class:   qclassRaw & 0x7fff,
			Unicast: qclassRaw&0x8000 != 0,
		})
		off = nextOff + 4
	}
	return out, nil
}

func decodeDNSName(pkt []byte, off int) (string, int, error) {
	var labels []string
	cur := off
	endOff := -1
	jumped := false
	for steps := 0; steps < 128; steps++ {
		if cur >= len(pkt) {
			return "", 0, errors.New("dns: out of bounds")
		}
		l := int(pkt[cur])
		if l == 0 {
			if !jumped {
				endOff = cur + 1
			}
			return strings.Join(labels, ".") + ".", endOff, nil
		}
		if l&0xc0 == 0xc0 {
			if cur+1 >= len(pkt) {
				return "", 0, errors.New("dns: truncated pointer")
			}
			ptr := int(binary.BigEndian.Uint16(pkt[cur:cur+2]) & 0x3fff)
			if ptr >= len(pkt) {
				return "", 0, errors.New("dns: invalid pointer")
			}
			if !jumped {
				endOff = cur + 2
				jumped = true
			}
			cur = ptr
			continue
		}
		if l&0xc0 != 0 {
			return "", 0, errors.New("dns: unsupported label type")
		}
		cur++
		if cur+l > len(pkt) {
			return "", 0, errors.New("dns: truncated label")
		}
		labels = append(labels, string(pkt[cur:cur+l]))
		cur += l
	}
	return "", 0, errors.New("dns: pointer loop")
}
