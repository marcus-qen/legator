package approvalpolicy

import "github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"

func newApprovalHTTPErrorContract(status int, code, message string) *HTTPErrorContract {
	return &HTTPErrorContract{Status: status, Code: code, Message: message}
}

func adaptApprovalHTTPErrorWriter(write func(*HTTPErrorContract)) func(*transportwriter.HTTPError) {
	return transportwriter.AdaptHTTPErrorWriter(write, newApprovalHTTPErrorContract)
}
