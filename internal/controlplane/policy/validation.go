package policy

import (
	"fmt"
	"regexp"
	"sort"
	"strings"

	"github.com/marcus-qen/legator/internal/protocol"
)

const MaxPolicyRuntimeSec = 86400

var (
	allowedBreakglassReasons = map[string]struct{}{
		"incident_response":  {},
		"security_emergency": {},
		"service_outage":     {},
		"data_recovery":      {},
	}
	allowedScopePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9:_./\-*]{0,127}$`)
)

func AllowedBreakglassReasons() []string {
	reasons := make([]string, 0, len(allowedBreakglassReasons))
	for reason := range allowedBreakglassReasons {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	return reasons
}

func DefaultTemplateOptionsForLevel(level protocol.CapabilityLevel) TemplateOptions {
	switch level {
	case protocol.CapDiagnose:
		return TemplateOptions{
			ExecutionClassRequired: protocol.ExecDiagnoseSandbox,
			SandboxRequired:        true,
			ApprovalMode:           protocol.ApprovalMutationGate,
		}
	case protocol.CapRemediate:
		return TemplateOptions{
			ExecutionClassRequired: protocol.ExecRemediateSandbox,
			SandboxRequired:        true,
			ApprovalMode:           protocol.ApprovalMutationGate,
		}
	case protocol.CapObserve:
		fallthrough
	default:
		return TemplateOptions{
			ExecutionClassRequired: protocol.ExecObserveDirect,
			SandboxRequired:        false,
			ApprovalMode:           protocol.ApprovalNone,
		}
	}
}

func MergeTemplateOptions(base, override TemplateOptions) TemplateOptions {
	out := base
	if override.ExecutionClassRequired != "" {
		out.ExecutionClassRequired = override.ExecutionClassRequired
	}
	if override.ApprovalMode != "" {
		out.ApprovalMode = override.ApprovalMode
	}
	if override.SandboxRequired {
		out.SandboxRequired = true
	}
	if override.RequireSecondApproverSet || override.RequireSecondApprover {
		out.RequireSecondApprover = override.RequireSecondApprover
	}
	if override.Breakglass.Enabled || override.Breakglass.RequireTypedConfirmation || len(override.Breakglass.AllowedReasons) > 0 {
		out.Breakglass = override.Breakglass
	}
	if override.MaxRuntimeSec != 0 {
		out.MaxRuntimeSec = override.MaxRuntimeSec
	}
	if override.AllowedScopes != nil {
		out.AllowedScopes = append([]string(nil), override.AllowedScopes...)
	}
	return out
}

// NormalizeTemplateOptions trims and deduplicates additive policy v2 fields.
func NormalizeTemplateOptions(opts TemplateOptions) TemplateOptions {
	opts.ExecutionClassRequired = protocol.ExecutionClass(strings.TrimSpace(string(opts.ExecutionClassRequired)))
	opts.ApprovalMode = protocol.ApprovalMode(strings.TrimSpace(string(opts.ApprovalMode)))
	opts.Breakglass.AllowedReasons = normalizeStringSlice(opts.Breakglass.AllowedReasons)
	opts.AllowedScopes = normalizeStringSlice(opts.AllowedScopes)
	if opts.MaxRuntimeSec < 0 {
		opts.MaxRuntimeSec = 0
	}
	return opts
}

func ValidateExecutionClass(class protocol.ExecutionClass) error {
	switch class {
	case "", protocol.ExecObserveDirect, protocol.ExecDiagnoseSandbox, protocol.ExecRemediateSandbox, protocol.ExecBreakglassDirect:
		return nil
	default:
		return fmt.Errorf("invalid execution_class_required %q", class)
	}
}

func ValidateApprovalMode(mode protocol.ApprovalMode) error {
	switch mode {
	case "", protocol.ApprovalNone, protocol.ApprovalMutationGate, protocol.ApprovalPlanFirst, protocol.ApprovalEveryAction, protocol.ApprovalTwoPerson:
		return nil
	default:
		return fmt.Errorf("invalid approval_mode %q", mode)
	}
}

func ValidateBreakglass(bg protocol.BreakglassPolicy) error {
	if !bg.Enabled {
		return nil
	}
	if len(bg.AllowedReasons) == 0 {
		return fmt.Errorf("breakglass.allowed_reasons required when breakglass is enabled")
	}
	for _, reason := range bg.AllowedReasons {
		reason = strings.TrimSpace(reason)
		if reason == "" {
			return fmt.Errorf("breakglass.allowed_reasons contains empty value")
		}
		if _, ok := allowedBreakglassReasons[reason]; !ok {
			return fmt.Errorf("invalid breakglass reason %q", reason)
		}
	}
	return nil
}

func ValidateAllowedScopes(scopes []string) error {
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			return fmt.Errorf("allowed_scopes contains empty value")
		}
		if !allowedScopePattern.MatchString(scope) {
			return fmt.Errorf("invalid allowed_scope %q", scope)
		}
	}
	return nil
}

func normalizeStringSlice(values []string) []string {
	if len(values) == 0 {
		return nil
	}

	out := make([]string, 0, len(values))
	seen := map[string]struct{}{}
	for _, value := range values {
		normalized := strings.TrimSpace(strings.ToLower(value))
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}
