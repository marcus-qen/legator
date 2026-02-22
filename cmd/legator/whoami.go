package main

import (
	"fmt"
	"os"
	"sort"
	"text/tabwriter"
)

func handleWhoAmI(args []string) {
	if len(args) > 0 {
		fmt.Fprintln(os.Stderr, "Usage: legator whoami")
		os.Exit(1)
	}

	apiClient, ok, err := tryAPIClient()
	if err != nil {
		fatal(err)
	}
	if !ok {
		fatal(fmt.Errorf("no API login session found; run 'legator login'"))
	}

	resp, err := fetchWhoAmI(apiClient)
	if err != nil {
		fatal(err)
	}

	fmt.Println("👤 Authenticated identity")
	fmt.Println("────────────────────────")
	fmt.Printf("Name:           %s\n", fallback(resp.Name, "(none)"))
	fmt.Printf("Email:          %s\n", fallback(resp.Email, "(none)"))
	fmt.Printf("Subject:        %s\n", fallback(resp.Subject, "(none)"))
	fmt.Printf("Effective role: %s\n", fallback(resp.EffectiveRole, "unknown"))

	if len(resp.Groups) > 0 {
		sort.Strings(resp.Groups)
		fmt.Printf("Groups:         %s\n", joinCSV(resp.Groups))
	} else {
		fmt.Printf("Groups:         (none)\n")
	}

	fmt.Println("\nPermissions")
	w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "ACTION\tALLOWED\tREASON")

	actions := make([]string, 0, len(resp.Permissions))
	for action := range resp.Permissions {
		actions = append(actions, action)
	}
	sort.Strings(actions)

	for _, action := range actions {
		p := resp.Permissions[action]
		allowed := "no"
		if p.Allowed {
			allowed = "yes"
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\n", action, allowed, p.Reason)
	}
	_ = w.Flush()
}

func fallback(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

func joinCSV(items []string) string {
	if len(items) == 0 {
		return ""
	}
	out := items[0]
	for i := 1; i < len(items); i++ {
		out += ", " + items[i]
	}
	return out
}
