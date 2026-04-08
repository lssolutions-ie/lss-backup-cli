package retention

import (
	"fmt"
	"strings"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/config"
)

// ForgetFlags returns the restic forget flags for the given retention config.
// Returns nil if no retention should be applied (mode "none", empty, or
// unrecognised legacy values).
func ForgetFlags(r config.Retention) []string {
	switch r.Mode {
	case "keep-last":
		if r.KeepLast > 0 {
			return []string{"--keep-last", fmt.Sprintf("%d", r.KeepLast)}
		}
	case "keep-within":
		if strings.TrimSpace(r.KeepWithin) != "" {
			return []string{"--keep-within", r.KeepWithin}
		}
	case "tiered":
		var flags []string
		// Optional high-frequency window: preserve full granularity before thinning.
		if strings.TrimSpace(r.KeepWithin) != "" {
			flags = append(flags, "--keep-within", r.KeepWithin)
		}
		if r.KeepDaily > 0 {
			flags = append(flags, "--keep-daily", fmt.Sprintf("%d", r.KeepDaily))
		}
		if r.KeepWeekly > 0 {
			flags = append(flags, "--keep-weekly", fmt.Sprintf("%d", r.KeepWeekly))
		}
		if r.KeepMonthly > 0 {
			flags = append(flags, "--keep-monthly", fmt.Sprintf("%d", r.KeepMonthly))
		}
		if r.KeepYearly > 0 {
			flags = append(flags, "--keep-yearly", fmt.Sprintf("%d", r.KeepYearly))
		}
		if len(flags) > 0 {
			return flags
		}
	}
	// "none", empty, and unrecognised legacy values ("keep-last-only", "full")
	// all map to no pruning.
	return nil
}

// Describe returns a plain English summary of what the retention policy keeps
// and how far back an operator can restore. Suitable for display in the CLI.
func Describe(r config.Retention) string {
	switch r.Mode {
	case "", "none", "keep-last-only", "full":
		return "Keep everything — all backups are kept forever. The repository grows over time."

	case "keep-last":
		if r.KeepLast <= 0 {
			return "Keep last N — not fully configured (keep_last is 0)."
		}
		return fmt.Sprintf(
			"Keep the %d most recent backups.\n"+
				"  After each run, any snapshot beyond the %d most recent is deleted.",
			r.KeepLast, r.KeepLast,
		)

	case "keep-within":
		if strings.TrimSpace(r.KeepWithin) == "" {
			return "Keep within window — not fully configured (keep_within is empty)."
		}
		return fmt.Sprintf(
			"Keep all backups from the last %s.\n"+
				"  Anything older is deleted after each run.",
			humanDuration(r.KeepWithin),
		)

	case "tiered":
		return describeTiered(r)

	default:
		return fmt.Sprintf("Unknown retention mode %q.", r.Mode)
	}
}

func describeTiered(r config.Retention) string {
	if r.KeepDaily == 0 && r.KeepWeekly == 0 && r.KeepMonthly == 0 && r.KeepYearly == 0 {
		return "Smart tiered retention — not fully configured (all tiers are 0)."
	}

	var lines []string

	// High-frequency window (optional) — shown first as it covers the finest detail.
	if strings.TrimSpace(r.KeepWithin) != "" {
		lines = append(lines, fmt.Sprintf("  Last %-8s      — every single snapshot kept (full granularity)", humanDuration(r.KeepWithin)))
	}
	if r.KeepDaily > 0 {
		lines = append(lines, fmt.Sprintf("  Last %-3d days   — one restore point per day", r.KeepDaily))
	}
	if r.KeepWeekly > 0 {
		lines = append(lines, fmt.Sprintf("  Last %-3d weeks  — one restore point per week", r.KeepWeekly))
	}
	if r.KeepMonthly > 0 {
		lines = append(lines, fmt.Sprintf("  Last %-3d months — one restore point per month", r.KeepMonthly))
	}
	if r.KeepYearly > 0 {
		lines = append(lines, fmt.Sprintf("  Last %-3d years  — one restore point per year", r.KeepYearly))
	}

	oldest := oldestPoint(r)

	result := "Smart tiered retention:\n"
	result += strings.Join(lines, "\n")
	if oldest != "" {
		result += fmt.Sprintf("\n  Oldest restore point: %s ago", oldest)
	}
	return result
}

// oldestPoint returns a human-readable string for how far back the policy reaches.
func oldestPoint(r config.Retention) string {
	if r.KeepYearly > 0 {
		return plural(r.KeepYearly, "year")
	}
	if r.KeepMonthly > 0 {
		return plural(r.KeepMonthly, "month")
	}
	if r.KeepWeekly > 0 {
		return plural(r.KeepWeekly, "week")
	}
	if r.KeepDaily > 0 {
		return plural(r.KeepDaily, "day")
	}
	return ""
}

func plural(n int, unit string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", unit)
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// humanDuration converts a restic duration string (e.g. "30d", "8w", "12m", "2y")
// into a readable form (e.g. "30 days", "8 weeks", "12 months", "2 years").
func humanDuration(d string) string {
	d = strings.TrimSpace(d)
	if len(d) < 2 {
		return d
	}
	num := d[:len(d)-1]
	unit := d[len(d)-1:]
	switch unit {
	case "d":
		return plural(atoi(num), "day")
	case "w":
		return plural(atoi(num), "week")
	case "m":
		return plural(atoi(num), "month")
	case "y":
		return plural(atoi(num), "year")
	}
	return d
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}
