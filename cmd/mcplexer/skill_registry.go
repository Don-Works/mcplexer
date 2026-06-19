package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/don-works/mcplexer/internal/idtrunc"
	"github.com/don-works/mcplexer/internal/skills"
	"github.com/don-works/mcplexer/internal/store"
)

// cmdSkillList implements `mcplexer skill list`.
func cmdSkillList() error {
	ctx := context.Background()
	db, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()

	rows, err := skills.List(ctx, db)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		fmt.Println("No skills installed.")
		fmt.Println("Install one with: mcplexer skill install <bundle.mcskill>")
		return nil
	}
	fmt.Printf("%-24s %-10s %-30s %s\n", "NAME", "VERSION", "CAPABILITIES", "SIGNER")
	for _, r := range rows {
		caps := summarizeCapabilities(r.ManifestJSON)
		signer := summarizeSigner(r.SignerPubkey)
		fmt.Printf("%-24s %-10s %-30s %s\n",
			truncate(r.Name, 24), truncate(r.Version, 10),
			truncate(caps, 30), signer)
	}
	return nil
}

// cmdSkillRemove implements `mcplexer skill remove <name>`.
func cmdSkillRemove(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: mcplexer skill remove <name>")
	}
	name := args[0]
	ctx := context.Background()
	db, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	if err := skills.Remove(ctx, db, defaultDataPath("skills"), name); err != nil {
		if errors.Is(err, skills.ErrSkillNotInstalled) {
			return fmt.Errorf("skill not installed: %s", name)
		}
		return err
	}
	fmt.Printf("Removed: %s\n", name)
	return nil
}

// cmdSkillShow implements `mcplexer skill show <name>`.
func cmdSkillShow(args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: mcplexer skill show <name>")
	}
	name := args[0]
	ctx := context.Background()
	db, err := openStore(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	row, err := db.GetInstalledSkill(ctx, name)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return fmt.Errorf("skill not installed: %s", name)
		}
		return err
	}
	pretty, err := json.MarshalIndent(row, "", "  ")
	if err != nil {
		return fmt.Errorf("format manifest: %w", err)
	}
	fmt.Println(string(pretty))
	return nil
}

// printReview renders a human-readable summary of what's about to be granted.
// This is a v1 stand-in for the M2.5 capability review screen.
func printReview(r *skills.InstallReview) {
	if r == nil || r.Manifest == nil {
		return
	}
	m := r.Manifest
	fmt.Println("─── Capability Review ───")
	fmt.Printf("Name:        %s\n", m.Name)
	fmt.Printf("Version:     %s\n", m.Version)
	fmt.Printf("Description: %s\n", m.Description)
	if m.Author != "" {
		fmt.Printf("Author:      %s\n", m.Author)
	}
	fmt.Printf("Signer:      %s\n", formatSignerLine(r))
	fmt.Println("Grants:")
	fmt.Printf("  MCP servers: %s\n", summarizeMCPServers(m.Capabilities.MCPServers))
	fmt.Printf("  Network:     %s\n", summarizeNetwork(m.Capabilities.Network))
	fmt.Printf("  Filesystem:  %s\n", summarizeFS(m.Capabilities.Filesystem))
	if r.Source != "" {
		fmt.Printf("Source:      %s\n", r.Source)
	}
	fmt.Println("─────────────────────────")
}

// printReviewIfPresent prints a partial review when an error happened mid-way.
// Useful so the user sees why the install failed (e.g. missing capability).
func printReviewIfPresent(r *skills.InstallReview) {
	if r != nil && r.Manifest != nil {
		printReview(r)
	}
}

func formatSignerLine(r *skills.InstallReview) string {
	if r.SignerPubkey == "" && r.SignerKeyID == "" {
		return "UNSIGNED (allowed via --allow-unsigned)"
	}
	if r.UnknownSigner {
		return fmt.Sprintf("UNKNOWN (key id %s — not in trust store)", r.SignerKeyID)
	}
	name := r.SignerName
	if name == "" {
		name = "unlabelled"
	}
	return fmt.Sprintf("%s (key id %s)", name, r.SignerKeyID)
}

func summarizeMCPServers(srv []skills.MCPServer) string {
	if len(srv) == 0 {
		return "none"
	}
	names := make([]string, 0, len(srv))
	for _, s := range srv {
		tag := s.Name
		if s.Optional {
			tag += "?"
		}
		names = append(names, tag)
	}
	return strings.Join(names, ", ")
}

func summarizeNetwork(n skills.NetworkCapability) string {
	if !n.Enabled {
		return "disabled"
	}
	if len(n.AllowedHosts) == 0 {
		return "enabled (any host)"
	}
	return "enabled (" + strings.Join(n.AllowedHosts, ", ") + ")"
}

func summarizeFS(fs skills.FilesystemCapability) string {
	mode := string(fs.Mode)
	if mode == "" {
		mode = string(skills.FilesystemModeNone)
	}
	if mode == string(skills.FilesystemModeNone) {
		return mode
	}
	return mode + " (" + strings.Join(fs.Paths, ", ") + ")"
}

// summarizeCapabilities is the compact one-line form used in `skill list`.
func summarizeCapabilities(manifestJSON []byte) string {
	var m skills.Manifest
	if err := json.Unmarshal(manifestJSON, &m); err != nil {
		return "(invalid)"
	}
	parts := []string{}
	if len(m.Capabilities.MCPServers) > 0 {
		names := make([]string, 0, len(m.Capabilities.MCPServers))
		for _, s := range m.Capabilities.MCPServers {
			names = append(names, s.Name)
		}
		parts = append(parts, "mcp:"+strings.Join(names, "+"))
	}
	if m.Capabilities.Network.Enabled {
		parts = append(parts, "net")
	}
	if m.Capabilities.Filesystem.Mode != "" &&
		m.Capabilities.Filesystem.Mode != skills.FilesystemModeNone {
		parts = append(parts, "fs:"+string(m.Capabilities.Filesystem.Mode))
	}
	if len(parts) == 0 {
		return "(none)"
	}
	return strings.Join(parts, " ")
}

func summarizeSigner(pubkey string) string {
	if pubkey == "" {
		return "(unsigned)"
	}
	return idtrunc.Ellipsis(pubkey, 8, 4)
}
