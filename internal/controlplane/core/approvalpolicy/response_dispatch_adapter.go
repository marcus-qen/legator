package approvalpolicy

import (
	"errors"
	"fmt"
	"net/http"
)

// DecideApprovalResponseDispatchWriter provides transport writers used by
// surface shells while emission policy is selected centrally in core.
type DecideApprovalResponseDispatchWriter struct {
	WriteSuccess   func(*DecideApprovalSuccess)
	WriteHTTPError func(*HTTPErrorContract)
	WriteMCPError  func(error)
}

type decideApprovalResponseDispatchPolicy interface {
	Dispatch(projection *DecideApprovalProjection, writer DecideApprovalResponseDispatchWriter)
}

type decideApprovalResponseDispatchPolicyFunc func(projection *DecideApprovalProjection, writer DecideApprovalResponseDispatchWriter)

func (f decideApprovalResponseDispatchPolicyFunc) Dispatch(projection *DecideApprovalProjection, writer DecideApprovalResponseDispatchWriter) {
	f(projection, writer)
}

type decideApprovalResponseDispatchPolicyMap map[DecideApprovalRenderSurface]decideApprovalResponseDispatchPolicy

func (m decideApprovalResponseDispatchPolicyMap) Resolve(surface DecideApprovalRenderSurface) (decideApprovalResponseDispatchPolicy, bool) {
	policy, ok := m[surface]
	return policy, ok
}

var defaultDecideApprovalResponseDispatchPolicyRegistry = decideApprovalResponseDispatchPolicyMap{
	DecideApprovalRenderSurfaceHTTP: decideApprovalResponseDispatchPolicyFunc(dispatchDecideApprovalHTTP),
	DecideApprovalRenderSurfaceMCP:  decideApprovalResponseDispatchPolicyFunc(dispatchDecideApprovalMCP),
}

// DispatchDecideApprovalResponseForSurface dispatches the shared decide
// projection to transport writers using centrally-selected surface policy.
func DispatchDecideApprovalResponseForSurface(projection *DecideApprovalProjection, surface DecideApprovalRenderSurface, writer DecideApprovalResponseDispatchWriter) {
	policy, ok := defaultDecideApprovalResponseDispatchPolicyRegistry.Resolve(surface)
	if !ok {
		dispatchDecideApprovalUnsupportedSurface(surface, writer)
		return
	}
	policy.Dispatch(normalizeDecideApprovalProjection(projection), writer)
}

func dispatchDecideApprovalHTTP(projection *DecideApprovalProjection, writer DecideApprovalResponseDispatchWriter) {
	if httpErr, ok := projection.HTTPError(); ok {
		if writer.WriteHTTPError != nil {
			writer.WriteHTTPError(httpErr)
		}
		return
	}
	if writer.WriteSuccess != nil {
		writer.WriteSuccess(normalizeDecideApprovalSuccess(projection.Success))
	}
}

func dispatchDecideApprovalMCP(projection *DecideApprovalProjection, writer DecideApprovalResponseDispatchWriter) {
	if err := projection.MCPError(); err != nil {
		if writer.WriteMCPError != nil {
			writer.WriteMCPError(err)
		}
		return
	}
	if writer.WriteSuccess != nil {
		writer.WriteSuccess(normalizeDecideApprovalSuccess(projection.Success))
	}
}

func dispatchDecideApprovalUnsupportedSurface(surface DecideApprovalRenderSurface, writer DecideApprovalResponseDispatchWriter) {
	httpErr := &HTTPErrorContract{
		Status:  http.StatusInternalServerError,
		Code:    "internal_error",
		Message: fmt.Sprintf("unsupported approval decide dispatch surface %q", string(surface)),
	}
	if writer.WriteHTTPError != nil {
		writer.WriteHTTPError(httpErr)
		return
	}
	if writer.WriteMCPError != nil {
		writer.WriteMCPError(errors.New(httpErr.Message))
	}
}

func normalizeDecideApprovalProjection(projection *DecideApprovalProjection) *DecideApprovalProjection {
	if projection == nil {
		return ProjectDecideApprovalTransport(nil)
	}
	return projection
}

func normalizeDecideApprovalSuccess(success *DecideApprovalSuccess) *DecideApprovalSuccess {
	if success == nil {
		return &DecideApprovalSuccess{}
	}
	return success
}
