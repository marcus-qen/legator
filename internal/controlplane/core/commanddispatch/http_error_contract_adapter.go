package commanddispatch

import "github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"

func newCommandHTTPErrorContract(status int, code, message string) *HTTPErrorContract {
	return &HTTPErrorContract{Status: status, Code: code, Message: message}
}

func adaptCommandHTTPErrorWriter(write func(*HTTPErrorContract)) func(*transportwriter.HTTPError) {
	return transportwriter.AdaptHTTPErrorWriter(write, newCommandHTTPErrorContract)
}
