package tools

import (
	"context"
	"fmt"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/components/tool/utils"
)

// GetCurrentTimeInput is the optional argument for get_current_time.
type GetCurrentTimeInput struct {
	// Timezone is an optional IANA timezone name such as Asia/Shanghai.
	// When empty, the host local timezone is used.
	Timezone string `json:"timezone,omitempty" jsonschema:"description=Optional IANA timezone name such as Asia/Shanghai. Leave empty to use the host local timezone."`
}

// GetCurrentTimeOutput is the structured wall-clock result returned to the model.
type GetCurrentTimeOutput struct {
	Datetime  string `json:"datetime"`
	Timezone  string `json:"timezone"`
	UTCOffset string `json:"utc_offset"`
	Unix      int64  `json:"unix"`
	Weekday   string `json:"weekday"`
}

// NewGetCurrentTime builds the get_current_time tool.
// clock defaults to time.Now when nil; inject a fixed clock in tests.
func NewGetCurrentTime(clock func() time.Time) (tool.InvokableTool, error) {
	if clock == nil {
		clock = time.Now
	}

	return utils.InferTool(
		"get_current_time",
		"Return the real current local date and time from the host clock. Call this whenever the user asks about today's date, the current time, weekday, timezone, relative deadlines, or whether something is overdue. Never guess or invent the current date or time.",
		func(_ context.Context, input GetCurrentTimeInput) (GetCurrentTimeOutput, error) {
			now := clock()
			loc := time.Local
			if name := input.Timezone; name != "" {
				loaded, err := time.LoadLocation(name)
				if err != nil {
					return GetCurrentTimeOutput{}, fmt.Errorf("invalid timezone %q: %w", name, err)
				}
				loc = loaded
			}

			local := now.In(loc)
			zoneName, offset := local.Zone()
			return GetCurrentTimeOutput{
				Datetime:  local.Format("2006-01-02 15:04:05"),
				Timezone:  zoneName,
				UTCOffset: formatUTCOffset(offset),
				Unix:      local.Unix(),
				Weekday:   local.Weekday().String(),
			}, nil
		},
	)
}

func formatUTCOffset(offsetSeconds int) string {
	sign := "+"
	if offsetSeconds < 0 {
		sign = "-"
		offsetSeconds = -offsetSeconds
	}
	hours := offsetSeconds / 3600
	minutes := (offsetSeconds % 3600) / 60
	return fmt.Sprintf("%s%02d:%02d", sign, hours, minutes)
}
