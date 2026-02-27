package approvalpolicy

import "github.com/marcus-qen/legator/internal/controlplane/core/transportwriter"

func adaptApprovalSuccessPayloadWriter(write func(*DecideApprovalSuccess)) func(any) {
	return transportwriter.AdaptSuccessPayloadWriter(write, normalizeDecideApprovalSuccess)
}
