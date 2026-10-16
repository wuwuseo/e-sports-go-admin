package esports

import (
	"context"
	"strconv"
	"strings"

	"go-admin/internal/ent"
	"go-admin/internal/ent/systemconfig"
)

const (
	serviceDurationMismatchMinutesKey = "app.esports.service_duration_mismatch_minutes"
	defaultServiceMismatchMinutes     = 15
	// 严重差异阈值（§13.2）：超过该阈值时系统自动建立售后台账，供客服核实与处置。
	serviceDurationSevereMinutesKey = "app.esports.service_duration_severe_minutes"
	defaultServiceSevereMinutes     = 30
)

// serviceSevereSeconds 严重时长差异阈值（秒）：配置缺失或非法时回落 30 分钟。
func serviceSevereSeconds(ctx context.Context, db *ent.Client) int64 {
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(serviceDurationSevereMinutesKey)).Only(ctx)
	if err != nil {
		return defaultServiceSevereMinutes * 60
	}
	minutes, err := strconv.Atoi(strings.TrimSpace(item.Value))
	if err != nil || minutes < 1 || minutes > 24*60 {
		return defaultServiceSevereMinutes * 60
	}
	return int64(minutes) * 60
}

func serviceMismatchMinimumSeconds(ctx context.Context, db *ent.Client) int64 {
	item, err := db.SystemConfig.Query().Where(systemconfig.KeyEQ(serviceDurationMismatchMinutesKey)).Only(ctx)
	if err != nil {
		return defaultServiceMismatchMinutes * 60
	}
	minutes, err := strconv.Atoi(strings.TrimSpace(item.Value))
	if err != nil || minutes < 1 || minutes > 24*60 {
		return defaultServiceMismatchMinutes * 60
	}
	return int64(minutes) * 60
}

// The larger of a fixed tolerance and 20% of the reported duration triggers review.
func serviceDurationMismatch(systemSeconds, reportedSeconds, minimumSeconds int64) bool {
	if reportedSeconds <= 0 || minimumSeconds < 0 {
		return false
	}
	difference := systemSeconds - reportedSeconds
	if difference < 0 {
		difference = -difference
	}
	threshold := minimumSeconds
	if relative := reportedSeconds / 5; relative > threshold {
		threshold = relative
	}
	return difference >= threshold
}
