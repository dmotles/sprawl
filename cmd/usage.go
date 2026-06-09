// Package cmd: usage.go implements `sprawl usage`, which exposes
// recorded per-turn token + cost data (QUM-368) to the user. Subcommands:
//
//	usage tail     — print recent rows (filtered, optionally --follow)
//	usage summary  — grouped totals (agent/model/session/day, tokens/cost/all)
//	usage export   — dump full records as CSV or NDJSON
//
// All output is deterministic (sorted), pipe-safe (footers / hints on stderr,
// data on stdout). See QUM-370 acceptance criteria.
package cmd

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/dmotles/sprawl/internal/usage"
)

type usageDeps struct {
	sprawlRoot func() (string, error)
	now        func() time.Time
	stdout     io.Writer
	stderr     io.Writer
	sleep      func(time.Duration)
	followStop func() bool
}

var (
	// tail flags
	usageTailAgent  string
	usageTailFollow bool
	usageTailLast   int
	usageTailQuiet  bool

	// summary flags
	usageSummaryBy    string
	usageSummaryGroup string
	usageSummarySince string
	usageSummaryUntil string
	usageSummaryQuiet bool

	// export flags
	usageExportFormat string
	usageExportSince  string
	usageExportAgent  string
	usageExportQuiet  bool
)

var usageCmd = &cobra.Command{
	Use:   "usage",
	Short: "Inspect recorded per-turn token + cost usage",
	Long:  "Inspect NDJSON usage logs recorded under .sprawl/logs/usage/<agent>/<session>.ndjson (see QUM-368).",
}

var usageTailCmd = &cobra.Command{
	Use:   "tail",
	Short: "Print the most recent usage records",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		return runUsageTail(resolveUsageDeps(), usageTailAgent, usageTailFollow, usageTailLast, usageTailQuiet)
	},
}

var usageSummaryCmd = &cobra.Command{
	Use:   "summary",
	Short: "Print grouped totals over recorded usage",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		return runUsageSummary(resolveUsageDeps(), usageSummaryBy, usageSummaryGroup, usageSummarySince, usageSummaryUntil, usageSummaryQuiet)
	},
}

var usageExportCmd = &cobra.Command{
	Use:   "export",
	Short: "Export recorded usage as CSV or NDJSON",
	Args:  cobra.NoArgs,
	RunE: func(_ *cobra.Command, _ []string) error {
		return runUsageExport(resolveUsageDeps(), usageExportFormat, usageExportSince, usageExportAgent, usageExportQuiet)
	},
}

func init() {
	usageTailCmd.Flags().StringVar(&usageTailAgent, "agent", "", "Only show records for this agent")
	usageTailCmd.Flags().BoolVar(&usageTailFollow, "follow", false, "Continue polling for new records")
	usageTailCmd.Flags().IntVar(&usageTailLast, "last", 50, "Number of recent records to print")
	usageTailCmd.Flags().BoolVar(&usageTailQuiet, "quiet", false, "Suppress next-action hint on stderr")

	usageSummaryCmd.Flags().StringVar(&usageSummaryBy, "by", "tokens", "What to display: tokens, cost, or all")
	usageSummaryCmd.Flags().StringVar(&usageSummaryGroup, "group", "agent", "Group by: agent, model, session, day")
	usageSummaryCmd.Flags().StringVar(&usageSummarySince, "since", "", "Only include records newer than DURATION ago (e.g. 1h, 7d)")
	usageSummaryCmd.Flags().StringVar(&usageSummaryUntil, "until", "", "Exclude records newer than DURATION ago (i.e. keep records with ts <= now - DURATION)")
	usageSummaryCmd.Flags().BoolVar(&usageSummaryQuiet, "quiet", false, "Suppress footers and hints on stderr")

	usageExportCmd.Flags().StringVar(&usageExportFormat, "format", "csv", "Output format: csv or json")
	usageExportCmd.Flags().StringVar(&usageExportSince, "since", "", "Only include records newer than DURATION ago")
	usageExportCmd.Flags().StringVar(&usageExportAgent, "agent", "", "Only export records for this agent")
	usageExportCmd.Flags().BoolVar(&usageExportQuiet, "quiet", false, "Suppress next-action hint on stderr")

	usageCmd.AddCommand(usageTailCmd)
	usageCmd.AddCommand(usageSummaryCmd)
	usageCmd.AddCommand(usageExportCmd)
	rootCmd.AddCommand(usageCmd)
}

func resolveUsageDeps() *usageDeps {
	// Wire SIGINT (Ctrl-C) into followStop so `usage tail --follow` exits
	// cleanly in production. Tests inject their own followStop closure.
	ctx, _ := signal.NotifyContext(context.Background(), os.Interrupt)
	return &usageDeps{
		sprawlRoot: func() (string, error) {
			r := os.Getenv("SPRAWL_ROOT")
			if r == "" {
				return "", fmt.Errorf("SPRAWL_ROOT environment variable is not set")
			}
			return r, nil
		},
		now:    time.Now,
		stdout: os.Stdout,
		stderr: os.Stderr,
		sleep:  time.Sleep,
		followStop: func() bool {
			select {
			case <-ctx.Done():
				return true
			default:
				return false
			}
		},
	}
}

// --- helpers ---

var dayDurationRE = regexp.MustCompile(`^[0-9]+d$`)

// parseDurationExt accepts standard Go durations plus the convenience suffix
// "Nd" for N*24h. Mixed forms like "2d12h" are rejected.
func parseDurationExt(s string) (time.Duration, error) {
	if s == "" {
		return 0, nil
	}
	if dayDurationRE.MatchString(s) {
		nStr := strings.TrimSuffix(s, "d")
		n, err := strconv.Atoi(nStr)
		if err != nil {
			return 0, fmt.Errorf("parse duration %q: %w", s, err)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	d, err := time.ParseDuration(s)
	if err != nil {
		return 0, fmt.Errorf("parse duration %q: %w", s, err)
	}
	return d, nil
}

// formatTokens renders an integer with comma thousands separators.
func formatTokens(n int) string {
	neg := n < 0
	if neg {
		n = -n
	}
	s := strconv.Itoa(n)
	if len(s) <= 3 {
		if neg {
			return "-" + s
		}
		return s
	}
	var b strings.Builder
	first := len(s) % 3
	if first > 0 {
		b.WriteString(s[:first])
		if len(s) > first {
			b.WriteByte(',')
		}
	}
	for i := first; i < len(s); i += 3 {
		b.WriteString(s[i : i+3])
		if i+3 < len(s) {
			b.WriteByte(',')
		}
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

// formatCost renders a USD float as "$0.0000".
func formatCost(c float64) string {
	return fmt.Sprintf("$%.4f", c)
}

// --- tail ---

func formatTailLine(r usage.Record) string {
	return fmt.Sprintf("%s %s %s in=%d out=%d cache_read=%d cache_creation=%d cost=%s",
		r.Timestamp, r.AgentName, r.Model,
		r.InputTokens, r.OutputTokens, r.CacheReadInputTokens, r.CacheCreationInputTokens,
		formatCost(r.TotalCostUsd))
}

func runUsageTail(deps *usageDeps, agent string, follow bool, last int, quiet bool) error {
	root, err := deps.sprawlRoot()
	if err != nil {
		return err
	}
	if last <= 0 {
		return fmt.Errorf("--last must be positive (default 50 if unspecified)")
	}
	filter := usage.Filter{Agent: agent}
	recs, err := usage.TailRecords(root, filter, last)
	if err != nil {
		return err
	}
	if len(recs) == 0 && !follow {
		fmt.Fprintln(deps.stderr, "no usage data found under .sprawl/logs/usage/ (see QUM-368 for the recorder). next: run any agent through a turn, then re-run `sprawl usage tail`.")
		return nil
	}
	var watermark string
	for _, r := range recs {
		fmt.Fprintln(deps.stdout, formatTailLine(r))
		if r.Timestamp > watermark {
			watermark = r.Timestamp
		}
	}
	if !follow {
		if !quiet {
			fmt.Fprintln(deps.stderr, "tip: sprawl usage summary --group agent")
		}
		return nil
	}
	for {
		if deps.followStop() {
			return nil
		}
		deps.sleep(250 * time.Millisecond)
		if deps.followStop() {
			return nil
		}
		next, err := usage.LoadRecords(root, filter)
		if err != nil {
			return err
		}
		for _, r := range next {
			if r.Timestamp <= watermark {
				continue
			}
			fmt.Fprintln(deps.stdout, formatTailLine(r))
			watermark = r.Timestamp
		}
	}
}

// --- summary ---

func runUsageSummary(deps *usageDeps, by, group, sinceStr, untilStr string, quiet bool) error {
	root, err := deps.sprawlRoot()
	if err != nil {
		return err
	}
	by = strings.ToLower(by)
	switch by {
	case "tokens", "cost", "all":
	default:
		return fmt.Errorf("--by must be one of: tokens, cost, all (got %q)", by)
	}
	group = strings.ToLower(group)
	var gk usage.GroupKey
	var groupCol string
	switch group {
	case "agent":
		gk, groupCol = usage.GroupAgent, "AGENT"
	case "model":
		gk, groupCol = usage.GroupModel, "MODEL"
	case "session":
		gk, groupCol = usage.GroupSession, "SESSION"
	case "day":
		gk, groupCol = usage.GroupDay, "DAY"
	default:
		return fmt.Errorf("--group must be one of: agent, model, session, day (got %q)", group)
	}

	sinceDur, err := parseDurationExt(sinceStr)
	if err != nil {
		return err
	}
	untilDur, err := parseDurationExt(untilStr)
	if err != nil {
		return err
	}
	filter := usage.Filter{}
	if sinceDur > 0 {
		filter.Since = deps.now().Add(-sinceDur)
	}
	if untilDur > 0 {
		filter.Until = deps.now().Add(-untilDur)
	}

	totals, err := usage.SumGrouped(root, gk, filter)
	if err != nil {
		return err
	}
	if len(totals) == 0 {
		fmt.Fprintln(deps.stderr, "no usage data found under .sprawl/logs/usage/ (see QUM-368 for the recorder). next: run any agent through a turn, then re-run `sprawl usage summary`.")
		return nil
	}

	keys := make([]string, 0, len(totals))
	for k := range totals {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	tw := tabwriter.NewWriter(deps.stdout, 0, 0, 2, ' ', 0)
	switch by {
	case "tokens":
		fmt.Fprintf(tw, "%s\tINPUT\tOUTPUT\tCACHE_READ\tCACHE_CREATE\n", groupCol)
	case "cost":
		fmt.Fprintf(tw, "%s\tCOST\n", groupCol)
	case "all":
		fmt.Fprintf(tw, "%s\tINPUT\tOUTPUT\tCACHE_READ\tCACHE_CREATE\tCOST\n", groupCol)
	}

	var sum usage.TokenTotals
	for _, k := range keys {
		t := totals[k]
		sum.InputTokens += t.InputTokens
		sum.OutputTokens += t.OutputTokens
		sum.CacheReadInputTokens += t.CacheReadInputTokens
		sum.CacheCreationInputTokens += t.CacheCreationInputTokens
		sum.TotalCostUsd += t.TotalCostUsd
		switch by {
		case "tokens":
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", k,
				formatTokens(t.InputTokens), formatTokens(t.OutputTokens),
				formatTokens(t.CacheReadInputTokens), formatTokens(t.CacheCreationInputTokens))
		case "cost":
			fmt.Fprintf(tw, "%s\t%s\n", k, formatCost(t.TotalCostUsd))
		case "all":
			fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", k,
				formatTokens(t.InputTokens), formatTokens(t.OutputTokens),
				formatTokens(t.CacheReadInputTokens), formatTokens(t.CacheCreationInputTokens),
				formatCost(t.TotalCostUsd))
		}
	}
	switch by {
	case "tokens":
		fmt.Fprintf(tw, "TOTAL\t%s\t%s\t%s\t%s\n",
			formatTokens(sum.InputTokens), formatTokens(sum.OutputTokens),
			formatTokens(sum.CacheReadInputTokens), formatTokens(sum.CacheCreationInputTokens))
	case "cost":
		fmt.Fprintf(tw, "TOTAL\t%s\n", formatCost(sum.TotalCostUsd))
	case "all":
		fmt.Fprintf(tw, "TOTAL\t%s\t%s\t%s\t%s\t%s\n",
			formatTokens(sum.InputTokens), formatTokens(sum.OutputTokens),
			formatTokens(sum.CacheReadInputTokens), formatTokens(sum.CacheCreationInputTokens),
			formatCost(sum.TotalCostUsd))
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	if !quiet {
		if by == "cost" || by == "all" {
			fmt.Fprintln(deps.stderr, "note: cost is API-reported; doesn't reflect subscription credits (Claude Max etc.)")
		}
		fmt.Fprintln(deps.stderr, "tip: sprawl usage tail --agent <name>")
	}
	return nil
}

// --- export ---

func runUsageExport(deps *usageDeps, format, sinceStr, agent string, quiet bool) error {
	root, err := deps.sprawlRoot()
	if err != nil {
		return err
	}
	format = strings.ToLower(format)
	switch format {
	case "csv", "json":
	default:
		return fmt.Errorf("--format must be one of: csv, json (got %q)", format)
	}
	sinceDur, err := parseDurationExt(sinceStr)
	if err != nil {
		return err
	}
	filter := usage.Filter{Agent: agent}
	if sinceDur > 0 {
		filter.Since = deps.now().Add(-sinceDur)
	}
	recs, err := usage.LoadRecords(root, filter)
	if err != nil {
		return err
	}

	switch format {
	case "csv":
		w := csv.NewWriter(deps.stdout)
		header := []string{
			"timestamp", "agent_name", "agent_type", "agent_family", "parent_name",
			"session_id", "branch", "model",
			"input_tokens", "output_tokens", "cache_read_input_tokens", "cache_creation_input_tokens",
			"total_cost_usd",
		}
		if err := w.Write(header); err != nil {
			return err
		}
		for _, r := range recs {
			row := []string{
				r.Timestamp, r.AgentName, r.AgentType, r.AgentFamily, r.ParentName,
				r.SessionID, r.Branch, r.Model,
				strconv.Itoa(r.InputTokens), strconv.Itoa(r.OutputTokens),
				strconv.Itoa(r.CacheReadInputTokens), strconv.Itoa(r.CacheCreationInputTokens),
				strconv.FormatFloat(r.TotalCostUsd, 'f', -1, 64),
			}
			if err := w.Write(row); err != nil {
				return err
			}
		}
		w.Flush()
		if err := w.Error(); err != nil {
			return err
		}
	case "json":
		enc := json.NewEncoder(deps.stdout)
		for _, r := range recs {
			if err := enc.Encode(&r); err != nil {
				return err
			}
		}
	}
	if !quiet {
		fmt.Fprintf(deps.stderr, "exported %d records; tip: pipe through `jq` or your CSV tool of choice.\n", len(recs))
	}
	return nil
}
