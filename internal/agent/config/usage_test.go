package config

import "testing"

func TestDailyRequestLimitRejectsUnboundedAndInvalidValues(t *testing.T) {
	for _, text := range []string{"", "0", "-1", "1.5", "unlimited", "1000001", "9999999999999999999999"} {
		if _, err := ParseDailyRequestLimit(text); err == nil {
			t.Errorf("accepted invalid limit %q", text)
		}
	}
	for _, text := range []string{"1", "100", " 250 ", "1000000"} {
		if _, err := ParseDailyRequestLimit(text); err != nil {
			t.Errorf("rejected valid limit %q: %v", text, err)
		}
	}
}
