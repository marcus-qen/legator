package transportwriter

// ConvertHTTPErrorContract maps a transportwriter HTTP error into a domain
// HTTP error contract using the provided constructor.
func ConvertHTTPErrorContract[T any](err *HTTPError, construct func(status int, code, message string) *T) *T {
	if err == nil || construct == nil {
		return nil
	}
	return construct(err.Status, err.Code, err.Message)
}

// AdaptHTTPErrorWriter converts a domain HTTP error writer into a
// transportwriter HTTP error writer callback.
func AdaptHTTPErrorWriter[T any](write func(*T), construct func(status int, code, message string) *T) func(*HTTPError) {
	if write == nil {
		return nil
	}
	return func(err *HTTPError) {
		contract := ConvertHTTPErrorContract(err, construct)
		if contract == nil {
			return
		}
		write(contract)
	}
}
