// Package mdns discovers paired TRACE desktop companion endpoints on the LAN
// via Android NsdManager (_trace-companion._tcp.) when stored addresses fail
// (FR-MOB-030). Discovered endpoints are candidate addresses only: callers MUST
// still verify the pinned TLS 1.3 certificate fingerprint before trusting any server.
package mdns

// Browser browses local network mDNS/DNS-SD services via Android NsdManager.
//
//mygo:native
type Browser interface {
	// Start browses serviceType (e.g. "_trace-companion._tcp.") and invokes
	// onFound for each resolved candidate host:port with its TXT node & channel.
	Start(serviceType string, onFound func(host string, port int, node string, channel string), onError func(msg string))
	// Stop stops active discovery and releases any multicast lock.
	Stop()
	// Close stops active discovery and releases all native resources.
	Close() error
}
