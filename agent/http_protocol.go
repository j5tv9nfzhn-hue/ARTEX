package agent

import (
	"net/http"
	"sync"
)

type compatibilityTransportKey struct {
	base    *http.Transport
	version string
}

var compatibilityTransports sync.Map

// Explicit protocol selection, not TLS/browser fingerprint impersonation. The
// empty version preserves legacy transport behavior. Clones are cached and the
// shared base transport/TLS configuration is never mutated after use.
func compatibilityTransport(base *http.Transport, version string) *http.Transport {
	if version == "" {
		return base
	}
	key := compatibilityTransportKey{base, version}
	if v, ok := compatibilityTransports.Load(key); ok {
		return v.(*http.Transport)
	}
	tr := base.Clone()
	protocols := new(http.Protocols)
	protocols.SetHTTP1(version != "2")
	protocols.SetHTTP2(version != "1.1")
	tr.Protocols = protocols
	tr.ForceAttemptHTTP2 = version != "1.1"
	tr.TLSNextProto = nil
	actual, loaded := compatibilityTransports.LoadOrStore(key, tr)
	if loaded {
		tr.CloseIdleConnections()
	}
	return actual.(*http.Transport)
}
