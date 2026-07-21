package p2p

import "errors"

// ErrHubSyncNotImplemented is returned when the hub sync skeleton is
// called but the full p2p implementation is not active.
var ErrHubSyncNotImplemented = errors.New("p2p: hub sync not implemented")
