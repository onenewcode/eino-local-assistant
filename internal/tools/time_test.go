package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestGetCurrentTimeUsesHostLocalZone(t *testing.T) {
	fixed := time.Date(2026, 7, 14, 15, 30, 0, 0, time.FixedZone("CST", 8*3600))
	tool, err := NewGetCurrentTime(func() time.Time { return fixed })
	if err != nil {
		t.Fatalf("NewGetCurrentTime() error = %v", err)
	}

	info, err := tool.Info(context.Background())
	if err != nil {
		t.Fatalf("Info() error = %v", err)
	}
	if got, want := info.Name, "get_current_time"; got != want {
		t.Fatalf("tool name = %q, want %q", got, want)
	}
	if !strings.Contains(info.Desc, "Never guess") {
		t.Errorf("tool description = %q, want guidance to avoid guessing", info.Desc)
	}

	raw, err := tool.InvokableRun(context.Background(), `{}`)
	if err != nil {
		t.Fatalf("InvokableRun() error = %v", err)
	}

	var out GetCurrentTimeOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode output %q: %v", raw, err)
	}
	if got, want := out.Datetime, "2026-07-14 15:30:00"; got != want {
		t.Errorf("datetime = %q, want %q", got, want)
	}
	if got, want := out.Timezone, "CST"; got != want {
		t.Errorf("timezone = %q, want %q", got, want)
	}
	if got, want := out.UTCOffset, "+08:00"; got != want {
		t.Errorf("utc_offset = %q, want %q", got, want)
	}
	if got, want := out.Unix, fixed.Unix(); got != want {
		t.Errorf("unix = %d, want %d", got, want)
	}
	if got, want := out.Weekday, "Tuesday"; got != want {
		t.Errorf("weekday = %q, want %q", got, want)
	}
}

func TestGetCurrentTimeAcceptsIANATimezone(t *testing.T) {
	fixed := time.Date(2026, 7, 14, 7, 30, 0, 0, time.UTC)
	tool, err := NewGetCurrentTime(func() time.Time { return fixed })
	if err != nil {
		t.Fatalf("NewGetCurrentTime() error = %v", err)
	}

	raw, err := tool.InvokableRun(context.Background(), `{"timezone":"Asia/Shanghai"}`)
	if err != nil {
		t.Fatalf("InvokableRun() error = %v", err)
	}

	var out GetCurrentTimeOutput
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("decode output %q: %v", raw, err)
	}
	if got, want := out.Datetime, "2026-07-14 15:30:00"; got != want {
		t.Errorf("datetime = %q, want %q", got, want)
	}
	if got, want := out.UTCOffset, "+08:00"; got != want {
		t.Errorf("utc_offset = %q, want %q", got, want)
	}
}

func TestGetCurrentTimeRejectsInvalidTimezone(t *testing.T) {
	tool, err := NewGetCurrentTime(time.Now)
	if err != nil {
		t.Fatalf("NewGetCurrentTime() error = %v", err)
	}

	_, err = tool.InvokableRun(context.Background(), `{"timezone":"Not/A_Zone"}`)
	if err == nil {
		t.Fatal("InvokableRun() error = nil, want invalid timezone error")
	}
	if !strings.Contains(err.Error(), "invalid timezone") {
		t.Errorf("error = %v, want invalid timezone", err)
	}
}
