package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/don-works/mcplexer/internal/oauth"
	"github.com/don-works/mcplexer/internal/store"
	"github.com/don-works/mcplexer/internal/store/sqlite"
)

type configDescribeResult struct {
	Servers         []serverEntry  `json:"servers"`
	Missing         []missingEntry `json:"missing"`
	Recommendations []string       `json:"recommendations"`
	Summary         summaryEntry   `json:"summary"`
}

type serverEntry struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Status    string `json:"status"`
	Namespace string `json:"namespace"`
	Transport string `json:"transport"`
}

type missingEntry struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Reason   string `json:"reason"`
	SetupURL string `json:"setup_url,omitempty"`
	HelpText string `json:"help_text,omitempty"`
}

type summaryEntry struct {
	TotalServers    int `json:"total_servers"`
	Connected       int `json:"connected"`
	Disabled        int `json:"disabled"`
	NeedsCreds      int `json:"needs_credentials"`
	Recommendations int `json:"recommendations"`
}

func cmdConfigDescribe(args []string) error {
	human := false
	for _, a := range args {
		if a == "--human" || a == "-H" {
			human = true
		}
	}

	ctx := context.Background()

	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	db, err := sqlite.New(ctx, cfg.DBDSN)
	if err != nil {
		return fmt.Errorf("open database: %w", err)
	}
	defer func() { _ = db.Close() }()

	result, err := buildConfigDescribe(ctx, db)
	if err != nil {
		return err
	}

	if human {
		printConfigDescribeHuman(result)
		return nil
	}

	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal result: %w", err)
	}
	fmt.Println(string(data))
	return nil
}

func buildConfigDescribe(ctx context.Context, s store.Store) (*configDescribeResult, error) {
	servers, err := s.ListDownstreamServers(ctx)
	if err != nil {
		return nil, fmt.Errorf("list servers: %w", err)
	}

	providers, err := s.ListOAuthProviders(ctx)
	if err != nil {
		return nil, fmt.Errorf("list oauth providers: %w", err)
	}

	result := &configDescribeResult{}

	providerByID := make(map[string]*store.OAuthProvider, len(providers))
	for i := range providers {
		providerByID[providers[i].ID] = &providers[i]
	}

	for _, srv := range servers {
		if srv.Transport == "internal" {
			continue
		}

		entry := serverEntry{
			ID:        srv.ID,
			Name:      srv.Name,
			Namespace: srv.ToolNamespace,
			Transport: srv.Transport,
		}

		if srv.Disabled {
			entry.Status = "disabled"
		} else {
			entry.Status = "connected"
		}

		result.Servers = append(result.Servers, entry)
	}

	sort.Slice(result.Servers, func(i, j int) bool {
		return result.Servers[i].Name < result.Servers[j].Name
	})

	oauthTemplates := oauth.ListTemplates()
	for _, t := range oauthTemplates {
		prov, hasProv := providerByID[t.ID]
		if !hasProv {
			continue
		}

		needsCreds := t.NeedsSecret
		hasClientID := prov.ClientID != ""
		hasClientSecret := len(prov.EncryptedClientSecret) > 0

		if needsCreds && (!hasClientID || !hasClientSecret) {
			result.Missing = append(result.Missing, missingEntry{
				ID:       t.ID,
				Name:     t.Name,
				Reason:   "needs_oauth_credentials",
				SetupURL: t.SetupURL,
				HelpText: t.HelpText,
			})
		}
	}

	sort.Slice(result.Missing, func(i, j int) bool {
		return result.Missing[i].Name < result.Missing[j].Name
	})

	result.Recommendations = buildRecommendations(result.Servers, result.Missing)

	for _, s := range result.Servers {
		result.Summary.TotalServers++
		switch s.Status {
		case "connected":
			result.Summary.Connected++
		case "disabled":
			result.Summary.Disabled++
		}
	}
	result.Summary.NeedsCreds = len(result.Missing)
	result.Summary.Recommendations = len(result.Recommendations)

	return result, nil
}

func buildRecommendations(servers []serverEntry, missing []missingEntry) []string {
	var recs []string

	connected := 0
	disabled := 0
	for _, s := range servers {
		if s.Status == "connected" {
			connected++
		} else {
			disabled++
		}
	}

	if disabled > 0 {
		recs = append(recs, fmt.Sprintf("Enable %d disabled server(s) with `mcplexer connect` to expand your tool surface", disabled))
	}

	if len(missing) > 0 {
		names := make([]string, len(missing))
		for i, m := range missing {
			names[i] = m.Name
		}
		recs = append(recs, fmt.Sprintf("Configure OAuth credentials for: %s", strings.Join(names, ", ")))
	}

	if connected == 0 {
		recs = append(recs, "No external servers are connected — run `mcplexer setup` to get started")
	}

	return recs
}

func printConfigDescribeHuman(r *configDescribeResult) {
	fmt.Println("MCPlexer Configuration Description")
	fmt.Println("━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━━")
	fmt.Printf("\nServers (%d total: %d connected, %d disabled)\n",
		r.Summary.TotalServers, r.Summary.Connected, r.Summary.Disabled)

	if len(r.Servers) == 0 {
		fmt.Println("  (none)")
	} else {
		for _, s := range r.Servers {
			status := "✓"
			if s.Status == "disabled" {
				status = "✗"
			}
			fmt.Printf("  %-2s %-30s  %-12s  ns:%s\n", status, s.Name, s.Status, s.Namespace)
		}
	}

	if len(r.Missing) > 0 {
		fmt.Printf("\nMissing OAuth Credentials (%d)\n", len(r.Missing))
		for _, m := range r.Missing {
			fmt.Printf("  • %s — %s\n", m.Name, m.Reason)
			if m.SetupURL != "" {
				fmt.Printf("    Setup: %s\n", m.SetupURL)
			}
			if m.HelpText != "" {
				fmt.Printf("    %s\n", m.HelpText)
			}
		}
	}

	if len(r.Recommendations) > 0 {
		fmt.Printf("\nRecommendations\n")
		for _, rec := range r.Recommendations {
			fmt.Printf("  → %s\n", rec)
		}
	}
}
