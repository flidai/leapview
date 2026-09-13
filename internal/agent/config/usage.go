package config

import (
	"fmt"
	"strconv"
	"strings"
)

const DailyRequestLimitSettingKey = "agent.daily_request_limit"
const DefaultDailyRequestLimit int64 = 100
const MaxDailyRequestLimit int64 = 1000000

func NormalizeDailyRequestLimit(value int64) (int64, error) {
	if value < 1 || value > MaxDailyRequestLimit {
		return 0, fmt.Errorf("dailyRequestLimit must be a whole number between 1 and %d", MaxDailyRequestLimit)
	}
	return value, nil
}

func ParseDailyRequestLimit(value string) (int64, error) {
	limit, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("dailyRequestLimit must be a whole number")
	}
	return NormalizeDailyRequestLimit(limit)
}
