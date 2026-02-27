package commanddispatch

import "github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"

func adaptCommandMCPSuccessPayloadWriter(write func(string)) func(any) {
	return transportwriter.AdaptSuccessPayloadWriter(write, nil)
}
