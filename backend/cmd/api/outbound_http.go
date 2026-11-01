package main

import (
	"errors"
	"net/http"
)

// Explicit isolated-test switch. Existing transports remain unchanged by default.
// The current Ozon boundary is a local unavailable stub, not an external client.
type disabledOutboundHTTP struct{}

func (disabledOutboundHTTP) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("outbound HTTP disabled for this isolated API process")
}
