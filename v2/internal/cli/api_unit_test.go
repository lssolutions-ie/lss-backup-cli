package cli

// Pure-function unit tests for helpers in api.go. Run in the default
// `go test ./...` path (no integration tag). These cover the validation +
// transformation logic without spawning a process or touching disk.

import (
	"reflect"
	"strings"
	"testing"

	"github.com/lssolutions-ie/lss-backup-cli/v2/internal/config"
)

func TestBuildScheduleDaily(t *testing.T) {
	got, err := buildSchedule("daily", "", 3, 30, "", 0)
	if err != nil {
		t.Fatalf("daily valid: %v", err)
	}
	want := config.Schedule{Mode: "daily", Hour: 3, Minute: 30}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestBuildScheduleDailyInvalidHour(t *testing.T) {
	if _, err := buildSchedule("daily", "", 25, 0, "", 0); err == nil {
		t.Error("daily with hour=25 should fail")
	}
	if _, err := buildSchedule("daily", "", 0, 60, "", 0); err == nil {
		t.Error("daily with minute=60 should fail")
	}
}

func TestBuildScheduleManual(t *testing.T) {
	got, err := buildSchedule("manual", "", 0, 0, "", 0)
	if err != nil || got.Mode != "manual" {
		t.Errorf("manual: %v / %+v", err, got)
	}
}

func TestBuildScheduleCronRequiresExpression(t *testing.T) {
	if _, err := buildSchedule("cron", "", 0, 0, "", 0); err == nil {
		t.Error("cron without --cron should fail")
	}
	// Malformed cron is rejected by the schedule validator.
	if _, err := buildSchedule("cron", "not a cron", 0, 0, "", 0); err == nil {
		t.Error("invalid cron expression should fail")
	}
	// Valid 5-field cron succeeds.
	if _, err := buildSchedule("cron", "*/5 * * * *", 0, 0, "", 0); err != nil {
		t.Errorf("valid cron rejected: %v", err)
	}
}

func TestBuildScheduleWeeklyRequiresDays(t *testing.T) {
	if _, err := buildSchedule("weekly", "", 2, 0, "", 0); err == nil {
		t.Error("weekly without --days should fail")
	}
}

func TestBuildScheduleMonthlyClampsAt28(t *testing.T) {
	if _, err := buildSchedule("monthly", "", 3, 0, "", 0); err == nil {
		t.Error("monthly with day=0 should fail")
	}
	if _, err := buildSchedule("monthly", "", 3, 0, "", 29); err == nil {
		t.Error("monthly with day=29 should fail (Feb safety)")
	}
	if _, err := buildSchedule("monthly", "", 3, 0, "", 28); err != nil {
		t.Errorf("monthly day=28 rejected: %v", err)
	}
}

func TestParseDays(t *testing.T) {
	tests := []struct {
		in      string
		want    []int
		wantErr bool
	}{
		{"1,2,3", []int{1, 2, 3}, false},
		{"1", []int{1}, false},
		{"1,2,3,4,5,6,7", []int{1, 2, 3, 4, 5, 6, 7}, false},
		{"0", nil, true},        // 0 invalid (1-7)
		{"8", nil, true},        // 8 invalid
		{"1,abc", nil, true},    // non-numeric
		{"1, 2, 3", []int{1, 2, 3}, false}, // spaces OK
	}
	for _, tt := range tests {
		got, err := parseDays(tt.in)
		if tt.wantErr {
			if err == nil {
				t.Errorf("%q: expected error, got %v", tt.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("%q: unexpected error %v", tt.in, err)
			continue
		}
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%q: got %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestBuildRetentionKeepLastRequiresPositive(t *testing.T) {
	if _, err := buildRetention("keep-last", 0, "", 0, 0, 0, 0); err == nil {
		t.Error("keep-last=0 should fail")
	}
	if _, err := buildRetention("keep-last", -1, "", 0, 0, 0, 0); err == nil {
		t.Error("keep-last=-1 should fail")
	}
	got, err := buildRetention("keep-last", 5, "", 0, 0, 0, 0)
	if err != nil {
		t.Errorf("keep-last=5 rejected: %v", err)
	}
	if got.KeepLast != 5 {
		t.Errorf("KeepLast: got %d, want 5", got.KeepLast)
	}
}

func TestBuildRetentionKeepWithinRequiresDuration(t *testing.T) {
	if _, err := buildRetention("keep-within", 0, "", 0, 0, 0, 0); err == nil {
		t.Error("keep-within without duration should fail")
	}
	got, _ := buildRetention("keep-within", 0, "30d", 0, 0, 0, 0)
	if got.KeepWithin != "30d" {
		t.Errorf("KeepWithin: %q", got.KeepWithin)
	}
}

func TestBuildRetentionTieredRequiresAtLeastOne(t *testing.T) {
	if _, err := buildRetention("tiered", 0, "", 0, 0, 0, 0); err == nil {
		t.Error("tiered with no knobs should fail")
	}
	got, _ := buildRetention("tiered", 0, "", 7, 4, 12, 1)
	if got.Mode != "tiered" || got.KeepDaily != 7 || got.KeepWeekly != 4 || got.KeepMonthly != 12 || got.KeepYearly != 1 {
		t.Errorf("tiered fields wrong: %+v", got)
	}
}

func TestBuildRetentionUnknownMode(t *testing.T) {
	if _, err := buildRetention("invalid", 0, "", 0, 0, 0, 0); err == nil {
		t.Error("unknown retention mode should fail")
	}
}

func TestViewOfRedactsHealthchecksID(t *testing.T) {
	job := config.Job{
		ID: "x",
		Notifications: config.Notifications{
			HealthchecksEnabled: true,
			HealthchecksDomain:  "https://healthchecks.io",
			HealthchecksID:      "SECRET-UUID-SHOULD-NOT-LEAK",
		},
	}
	view := viewOf(job)
	if strings.Contains(mustJSON(view), "SECRET-UUID-SHOULD-NOT-LEAK") {
		t.Error("viewOf leaked HealthchecksID in JSON projection")
	}
	// But domain is fine to include.
	if !strings.Contains(mustJSON(view), "https://healthchecks.io") {
		t.Error("viewOf dropped the domain too aggressively")
	}
}

func mustJSON(v any) string {
	// tiny helper: stable JSON for substring search, no error checks.
	// Avoids importing encoding/json at top again (it's in the file).
	var sb strings.Builder
	enc := jsonNewEncoder(&sb)
	_ = enc.Encode(v)
	return sb.String()
}
