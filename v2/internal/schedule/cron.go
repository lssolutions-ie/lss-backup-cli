package schedule

import (
	"fmt"
	"strconv"
	"strings"
)

type fieldRange struct {
	name string
	min  int
	max  int
}

var (
	minuteSpec = fieldRange{"minute", 0, 59}
	hourSpec   = fieldRange{"hour", 0, 23}
	domSpec    = fieldRange{"day-of-month", 1, 31}
	monthSpec  = fieldRange{"month", 1, 12}
	dowSpec    = fieldRange{"day-of-week", 0, 7}
)

// ValidateCron validates a standard 5-field cron expression or a @shorthand.
// Returns a plain English description on success, or an error on failure.
func ValidateCron(expr string) (string, error) {
	expr = strings.TrimSpace(expr)
	if expr == "" {
		return "", fmt.Errorf("expression cannot be empty")
	}

	if strings.HasPrefix(expr, "@") {
		return validateShorthand(expr)
	}

	parts := strings.Fields(expr)
	if len(parts) != 5 {
		return "", fmt.Errorf("expected 5 fields (minute hour day-of-month month day-of-week), got %d", len(parts))
	}

	minute, hour, dom, month, dow := parts[0], parts[1], parts[2], parts[3], parts[4]

	if err := validateField(minute, minuteSpec); err != nil {
		return "", fmt.Errorf("minute field: %w", err)
	}
	if err := validateField(hour, hourSpec); err != nil {
		return "", fmt.Errorf("hour field: %w", err)
	}
	if err := validateField(dom, domSpec); err != nil {
		return "", fmt.Errorf("day-of-month field: %w", err)
	}
	if err := validateField(month, monthSpec); err != nil {
		return "", fmt.Errorf("month field: %w", err)
	}
	if err := validateField(dow, dowSpec); err != nil {
		return "", fmt.Errorf("day-of-week field: %w", err)
	}

	return describe(minute, hour, dom, month, dow), nil
}

func validateShorthand(expr string) (string, error) {
	switch strings.ToLower(expr) {
	case "@yearly", "@annually":
		return "Once a year on 1 January at midnight", nil
	case "@monthly":
		return "On the 1st of every month at midnight", nil
	case "@weekly":
		return "Every Sunday at midnight", nil
	case "@daily", "@midnight":
		return "Every day at midnight (00:00)", nil
	case "@hourly":
		return "Every hour", nil
	default:
		return "", fmt.Errorf("unknown shorthand %q — valid options: @yearly @annually @monthly @weekly @daily @midnight @hourly", expr)
	}
}

func validateField(field string, spec fieldRange) error {
	for _, token := range strings.Split(field, ",") {
		token = strings.TrimSpace(token)
		if err := validateToken(token, spec); err != nil {
			return fmt.Errorf("invalid token %q: %w", token, err)
		}
	}
	return nil
}

func validateToken(token string, spec fieldRange) error {
	if token == "*" {
		return nil
	}

	// Step: */n  or  base/n  where base is * | value | range
	if idx := strings.Index(token, "/"); idx >= 0 {
		base, stepStr := token[:idx], token[idx+1:]
		step, err := strconv.Atoi(stepStr)
		if err != nil || step < 1 {
			return fmt.Errorf("step must be a positive integer, got %q", stepStr)
		}
		if base == "*" {
			return nil
		}
		return validateRangeOrValue(base, spec)
	}

	return validateRangeOrValue(token, spec)
}

func validateRangeOrValue(token string, spec fieldRange) error {
	// Range: n-m
	if idx := strings.Index(token, "-"); idx >= 0 {
		loStr, hiStr := token[:idx], token[idx+1:]
		lo, err1 := strconv.Atoi(loStr)
		hi, err2 := strconv.Atoi(hiStr)
		if err1 != nil || err2 != nil {
			return fmt.Errorf("range bounds must be integers")
		}
		if lo < spec.min || lo > spec.max {
			return fmt.Errorf("%d is out of allowed range %d–%d for %s", lo, spec.min, spec.max, spec.name)
		}
		if hi < spec.min || hi > spec.max {
			return fmt.Errorf("%d is out of allowed range %d–%d for %s", hi, spec.min, spec.max, spec.name)
		}
		if lo > hi {
			return fmt.Errorf("range start %d must not exceed end %d", lo, hi)
		}
		return nil
	}

	// Single value
	n, err := strconv.Atoi(token)
	if err != nil {
		return fmt.Errorf("expected a number, got %q", token)
	}
	if n < spec.min || n > spec.max {
		return fmt.Errorf("%d is out of allowed range %d–%d for %s", n, spec.min, spec.max, spec.name)
	}
	return nil
}

// describe produces a plain English description of a validated 5-field expression.
func describe(minute, hour, dom, month, dow string) string {
	// Every minute
	if minute == "*" && hour == "*" && dom == "*" && month == "*" && dow == "*" {
		return "Every minute"
	}

	// Every N minutes: */N * * * *
	if strings.HasPrefix(minute, "*/") && hour == "*" && dom == "*" && month == "*" && dow == "*" {
		n := minute[2:]
		if n == "1" {
			return "Every minute"
		}
		return fmt.Sprintf("Every %s minutes", n)
	}

	// Every hour: 0 * * * *
	if minute == "0" && hour == "*" && dom == "*" && month == "*" && dow == "*" {
		return "Every hour"
	}

	// Every N hours: M */N * * *
	if strings.HasPrefix(hour, "*/") && dom == "*" && month == "*" && dow == "*" {
		n := hour[2:]
		suffix := ""
		if minute != "0" && isSimpleInt(minute) {
			suffix = fmt.Sprintf(" at minute %s", minute)
		}
		if n == "1" {
			return fmt.Sprintf("Every hour%s", suffix)
		}
		return fmt.Sprintf("Every %s hours%s", n, suffix)
	}

	timeStr := timeDesc(minute, hour)

	// Every day: M H * * *
	if dom == "*" && month == "*" && dow == "*" {
		return fmt.Sprintf("Every day %s", timeStr)
	}

	// Specific weekday(s): M H * * DOW
	if dom == "*" && month == "*" && dow != "*" {
		return fmt.Sprintf("%s %s", dowDesc(dow), timeStr)
	}

	// Specific day of month: M H D * *
	if dom != "*" && month == "*" && dow == "*" {
		return fmt.Sprintf("Day %s of every month %s", dom, timeStr)
	}

	// Specific day and month (once a year): M H D Mo *
	if dom != "*" && month != "*" && dow == "*" {
		return fmt.Sprintf("Once a year on %s %s %s", dom, monthName(month), timeStr)
	}

	// Fallback: show all fields
	return fmt.Sprintf("At %s — day-of-month: %s, month: %s, day-of-week: %s", timeStr, dom, month, dow)
}

func timeDesc(minute, hour string) string {
	if isSimpleInt(hour) && isSimpleInt(minute) {
		h, _ := strconv.Atoi(hour)
		m, _ := strconv.Atoi(minute)
		return fmt.Sprintf("at %02d:%02d", h, m)
	}
	if isSimpleInt(hour) {
		h, _ := strconv.Atoi(hour)
		return fmt.Sprintf("at %02d:(%s)", h, minute)
	}
	return fmt.Sprintf("at hour %s, minute %s", hour, minute)
}

func dowDesc(dow string) string {
	switch dow {
	case "1-5":
		return "Every weekday (Mon–Fri)"
	case "0,6", "6,0":
		return "Every weekend (Sat, Sun)"
	case "0-6", "*":
		return "Every day"
	}

	// Comma-separated single values only — show day names
	if !strings.ContainsAny(dow, "/-") {
		parts := strings.Split(dow, ",")
		names := make([]string, 0, len(parts))
		for _, p := range parts {
			n, err := strconv.Atoi(strings.TrimSpace(p))
			if err == nil {
				names = append(names, longDayName(n))
			}
		}
		if len(names) == len(parts) {
			return fmt.Sprintf("Every %s", strings.Join(names, ", "))
		}
	}

	return fmt.Sprintf("On weekday(s) %s", dow)
}

func monthName(month string) string {
	names := []string{"", "January", "February", "March", "April", "May", "June",
		"July", "August", "September", "October", "November", "December"}
	n, err := strconv.Atoi(month)
	if err == nil && n >= 1 && n <= 12 {
		return names[n]
	}
	return month
}

func longDayName(n int) string {
	names := map[int]string{
		0: "Sunday", 1: "Monday", 2: "Tuesday", 3: "Wednesday",
		4: "Thursday", 5: "Friday", 6: "Saturday", 7: "Sunday",
	}
	if name, ok := names[n]; ok {
		return name
	}
	return strconv.Itoa(n)
}

func isSimpleInt(s string) bool {
	_, err := strconv.Atoi(s)
	return err == nil
}
