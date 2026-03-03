package sandbox

// Event type constants for sandbox lifecycle events.
// These are published to the events bus.
const (
	EventCreated      = "sandbox.created"
	EventProvisioning = "sandbox.provisioning"
	EventReady        = "sandbox.ready"
	EventRunning      = "sandbox.running"
	EventFailed       = "sandbox.failed"
	EventDestroyed    = "sandbox.destroyed"
)
