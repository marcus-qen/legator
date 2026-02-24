package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
)

type policySimulationCLIRequest struct {
	Subject            *policySimulationCLISubject `json:"subject,omitempty"`
	Actions            []string                    `json:"actions,omitempty"`
	Resources          []string                    `json:"resources,omitempty"`
	ProposedPolicy     *policySimulationCLIPolicy  `json:"proposedPolicy,omitempty"`
	RequestRatePerHour int                         `json:"requestRatePerHour,omitempty"`
	RunRatePerHour     int                         `json:"runRatePerHour,omitempty"`
}

type policySimulationCLISubject struct {
	Subject string   `json:"subject,omitempty"`
	Email   string   `json:"email,omitempty"`
	Name    string   `json:"name,omitempty"`
	Groups  []string `json:"groups,omitempty"`
}

type policySimulationCLIPolicy struct {
	Name     string                           `json:"name,omitempty"`
	Role     string                           `json:"role"`
	Subjects []policySimulationCLIPolicyMatch `json:"subjects,omitempty"`
	Scope    policySimulationCLIPolicyScope   `json:"scope,omitempty"`
}

type policySimulationCLIPolicyMatch struct {
	Claim string `json:"claim"`
	Value string `json:"value"`
}

type policySimulationCLIPolicyScope struct {
	Tags       []string `json:"tags,omitempty"`
	Namespaces []string `json:"namespaces,omitempty"`
	Agents     []string `json:"agents,omitempty"`
}

const policyFlagJSON = "--json"

type policySimulationCLIResponse struct {
	Subject struct {
		Subject string   `json:"subject"`
		Email   string   `json:"email"`
		Name    string   `json:"name"`
		Groups  []string `json:"groups"`
	} `json:"subject"`
	BasePolicy *struct {
		Name string `json:"name"`
		Role string `json:"role"`
	} `json:"basePolicy,omitempty"`
	Current *struct {
		Name string `json:"name"`
		Role string `json:"role"`
	} `json:"currentUserPolicy,omitempty"`
	Proposed *struct {
		Name string `json:"name"`
		Role string `json:"role"`
	} `json:"proposedUserPolicy,omitempty"`
	Evaluations []struct {
		Action   string `json:"action"`
		Resource string `json:"resource,omitempty"`
		Current  struct {
			Allowed   bool   `json:"allowed"`
			Reason    string `json:"reason"`
			RateLimit *struct {
				Allowed      bool   `json:"allowed"`
				Reason       string `json:"reason"`
				LimitPerHour int    `json:"limitPerHour"`
				Requested    int    `json:"requestedPerHour"`
			} `json:"rateLimit,omitempty"`
		} `json:"current"`
		Projected struct {
			Allowed   bool   `json:"allowed"`
			Reason    string `json:"reason"`
			RateLimit *struct {
				Allowed      bool   `json:"allowed"`
				Reason       string `json:"reason"`
				LimitPerHour int    `json:"limitPerHour"`
				Requested    int    `json:"requestedPerHour"`
			} `json:"rateLimit,omitempty"`
		} `json:"projected"`
	} `json:"evaluations"`
}

func handlePolicy(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: legator policy <simulate>")
		os.Exit(1)
	}

	switch args[0] {
	case "simulate", "sim":
		handlePolicySimulate(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "Unknown policy subcommand: %s\n", args[0])
		os.Exit(1)
	}
}

//nolint:gocyclo // CLI flag parsing is intentionally linear for readability.
func handlePolicySimulate(args []string) {
	apiClient, ok, err := tryAPIClient()
	if err != nil {
		fatal(err)
	}
	if !ok {
		fatal(fmt.Errorf("no API login session found; run 'legator login'"))
	}

	req := policySimulationCLIRequest{}
	var (
		actions          []string
		resources        []string
		groups           []string
		proposedSubjects []policySimulationCLIPolicyMatch
		scopeAgents      []string
		scopeNamespaces  []string
		scopeTags        []string
		jsonOut          bool
	)

	for i := 0; i < len(args); i++ {
		arg := args[i]
		next := func() string {
			i++
			if i >= len(args) {
				fatal(fmt.Errorf("missing value for %s", arg))
			}
			return args[i]
		}

		switch arg {
		case "--for-email":
			if req.Subject == nil {
				req.Subject = &policySimulationCLISubject{}
			}
			req.Subject.Email = next()
		case "--for-subject":
			if req.Subject == nil {
				req.Subject = &policySimulationCLISubject{}
			}
			req.Subject.Subject = next()
		case "--for-name":
			if req.Subject == nil {
				req.Subject = &policySimulationCLISubject{}
			}
			req.Subject.Name = next()
		case "--for-group":
			groups = append(groups, next())
		case "--action":
			actions = append(actions, next())
		case "--resource":
			resources = append(resources, next())
		case "--request-rate":
			v, convErr := strconv.Atoi(next())
			if convErr != nil {
				fatal(fmt.Errorf("invalid --request-rate: %w", convErr))
			}
			req.RequestRatePerHour = v
		case "--run-rate":
			v, convErr := strconv.Atoi(next())
			if convErr != nil {
				fatal(fmt.Errorf("invalid --run-rate: %w", convErr))
			}
			req.RunRatePerHour = v
		case "--proposed-role":
			if req.ProposedPolicy == nil {
				req.ProposedPolicy = &policySimulationCLIPolicy{}
			}
			req.ProposedPolicy.Role = next()
		case "--proposed-name":
			if req.ProposedPolicy == nil {
				req.ProposedPolicy = &policySimulationCLIPolicy{}
			}
			req.ProposedPolicy.Name = next()
		case "--proposed-subject":
			parts := strings.SplitN(next(), "=", 2)
			if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
				fatal(fmt.Errorf("--proposed-subject must be in claim=value format"))
			}
			proposedSubjects = append(proposedSubjects, policySimulationCLIPolicyMatch{
				Claim: strings.TrimSpace(parts[0]),
				Value: strings.TrimSpace(parts[1]),
			})
		case "--scope-agent":
			scopeAgents = append(scopeAgents, next())
		case "--scope-namespace":
			scopeNamespaces = append(scopeNamespaces, next())
		case "--scope-tag":
			scopeTags = append(scopeTags, next())
		case policyFlagJSON, "-j":
			jsonOut = true
		default:
			fatal(fmt.Errorf("unknown flag: %s", arg))
		}
	}

	if req.Subject != nil && len(groups) > 0 {
		req.Subject.Groups = append(req.Subject.Groups, groups...)
	}

	req.Actions = actions
	req.Resources = resources

	if req.ProposedPolicy != nil {
		if req.ProposedPolicy.Role == "" {
			fatal(fmt.Errorf("--proposed-role is required when providing proposed policy fields"))
		}

		req.ProposedPolicy.Scope = policySimulationCLIPolicyScope{
			Tags:       scopeTags,
			Namespaces: scopeNamespaces,
			Agents:     scopeAgents,
		}

		req.ProposedPolicy.Subjects = proposedSubjects
		if len(req.ProposedPolicy.Subjects) == 0 {
			if req.Subject != nil && req.Subject.Email != "" {
				req.ProposedPolicy.Subjects = []policySimulationCLIPolicyMatch{{Claim: "email", Value: req.Subject.Email}}
			} else {
				me, meErr := fetchWhoAmI(apiClient)
				if meErr != nil {
					fatal(fmt.Errorf(
						"proposed policy requires --proposed-subject claim=value (or resolvable subject email): %w",
						meErr,
					))
				}
				if me.Email == "" {
					fatal(fmt.Errorf("proposed policy requires --proposed-subject claim=value when email is unavailable"))
				}
				req.ProposedPolicy.Subjects = []policySimulationCLIPolicyMatch{{Claim: "email", Value: me.Email}}
			}
		}
	}

	var resp policySimulationCLIResponse
	if err := apiClient.postJSON("/api/v1/policy/simulate", req, &resp); err != nil {
		fatal(err)
	}

	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(resp); err != nil {
			fatal(err)
		}
		return
	}

	fmt.Println("🧪 Policy simulation")
	fmt.Println("──────────────────")
	fmt.Printf("Subject: %s (%s)\n", fallback(resp.Subject.Email, "(none)"), fallback(resp.Subject.Subject, "(none)"))
	if resp.BasePolicy != nil {
		fmt.Printf("Base policy: %s (%s)\n", resp.BasePolicy.Name, resp.BasePolicy.Role)
	}
	if resp.Current != nil {
		fmt.Printf("Current UserPolicy: %s (%s)\n", resp.Current.Name, resp.Current.Role)
	} else {
		fmt.Printf("Current UserPolicy: (none)\n")
	}
	if req.ProposedPolicy != nil {
		if resp.Proposed != nil {
			fmt.Printf("Proposed UserPolicy: %s (%s)\n", resp.Proposed.Name, resp.Proposed.Role)
		} else {
			fmt.Printf("Proposed UserPolicy: provided but not matched for subject\n")
		}
	}

	fmt.Println()
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "ACTION\tRESOURCE\tCURRENT\tPROJECTED\tRATE")
	for _, eval := range resp.Evaluations {
		current := "deny"
		if eval.Current.Allowed {
			current = "allow"
		}
		projected := "deny"
		if eval.Projected.Allowed {
			projected = "allow"
		}
		rate := "n/a"
		if eval.Projected.RateLimit != nil {
			rateAllow := "ok"
			if !eval.Projected.RateLimit.Allowed {
				rateAllow = "block"
			}
			rate = fmt.Sprintf(
				"%s (%d/%dh)",
				rateAllow,
				eval.Projected.RateLimit.Requested,
				eval.Projected.RateLimit.LimitPerHour,
			)
		}
		resource := eval.Resource
		if resource == "" {
			resource = "(global)"
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", eval.Action, resource, current, projected, rate)
	}
	_ = w.Flush()

	for _, eval := range resp.Evaluations {
		if eval.Projected.Allowed && (eval.Projected.RateLimit == nil || eval.Projected.RateLimit.Allowed) {
			continue
		}
		resource := eval.Resource
		if resource == "" {
			resource = "(global)"
		}
		fmt.Printf("\n⚠ %s %s\n", eval.Action, resource)
		if !eval.Projected.Allowed {
			fmt.Printf("  projected deny: %s\n", eval.Projected.Reason)
		}
		if eval.Projected.RateLimit != nil && !eval.Projected.RateLimit.Allowed {
			fmt.Printf("  projected rate-limit deny: %s\n", eval.Projected.RateLimit.Reason)
		}
	}
}
