package transportwriter

// ConvertSuccessPayload maps a transport success payload into a typed domain
// value using optional normalization.
func ConvertSuccessPayload[T any](payload any, normalize func(T) T) T {
	value, _ := payload.(T)
	if normalize != nil {
		return normalize(value)
	}
	return value
}

// AdaptSuccessPayloadWriter converts a typed domain success writer callback into
// a transportwriter success callback with optional payload normalization.
func AdaptSuccessPayloadWriter[T any](write func(T), normalize func(T) T) func(any) {
	if write == nil {
		return nil
	}
	return func(payload any) {
		write(ConvertSuccessPayload(payload, normalize))
	}
}
