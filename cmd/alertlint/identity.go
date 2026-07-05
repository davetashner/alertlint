package main

import (
	"flag"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/davetashner/alertlint/internal/identity"
)

// runIdentity handles `alertlint identity <subcommand>`. v1 ships
// `confirm`: the structured write that turns a fuzzy candidate into a
// durable strategy-2 mapping (the ratchet, ADR 0002).
func runIdentity(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "confirm" {
		fmt.Fprintln(stderr, "usage: alertlint identity confirm <source>/<kind>/<key> <ci_id> [flags]")
		return 2
	}
	fs := flag.NewFlagSet("identity confirm", flag.ContinueOnError)
	fs.SetOutput(stderr)
	mappingsPath := fs.String("mappings", "identity-mappings.yaml", "confirmed-mappings file to append to")
	by := fs.String("by", "", "who confirmed (required — confirmations must be auditable)")
	note := fs.String("note", "", "optional context note")
	originMethod := fs.String("origin-method", "fuzzy", "what suggested the mapping")
	originScore := fs.Float64("origin-score", 0, "suggestion score, when known")
	originHint := fs.String("origin-hint", "", "the hint that produced the suggestion")
	date := fs.String("date", "", "confirmation date YYYY-MM-DD (default today)")
	if len(args) < 3 {
		fmt.Fprintln(stderr, "usage: alertlint identity confirm <source>/<kind>/<key> <ci_id> [flags]")
		return 2
	}
	rest := args[1:3]
	if err := fs.Parse(args[3:]); err != nil {
		return 2
	}
	parts := strings.SplitN(rest[0], "/", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		fmt.Fprintf(stderr, "alertlint identity confirm: artifact ref %q must be <source>/<kind>/<key>\n", rest[0])
		return 2
	}
	if *by == "" {
		fmt.Fprintln(stderr, "alertlint identity confirm: --by is required")
		return 2
	}
	confirmedAt := *date
	if confirmedAt == "" {
		confirmedAt = time.Now().UTC().Format(time.DateOnly)
	}

	entry := identity.MappingEntry{
		Artifact:    identity.ArtifactRef{Source: parts[0], Kind: parts[1], Key: parts[2]},
		CIID:        rest[1],
		ConfirmedBy: *by,
		ConfirmedAt: confirmedAt,
		Note:        *note,
	}
	if *originMethod != "" {
		entry.Origin = &identity.MappingOrigin{Method: *originMethod, Score: *originScore, Hint: *originHint}
	}
	if err := identity.Confirm(*mappingsPath, entry); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "confirmed %s -> %s in %s\n", rest[0], rest[1], *mappingsPath)
	return 0
}
