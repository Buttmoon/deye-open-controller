package main

import (
	"fmt"
	"time"
)

func getTimezoneOptions() []TimezoneOption {
	now := time.Now().UTC()
	timezones := []string{
		"UTC", "Europe/London", "Europe/Berlin", "Europe/Paris", "Europe/Madrid", "Europe/Rome",
		"Europe/Warsaw", "Europe/Athens", "Europe/Helsinki", "Europe/Kiev", "Europe/Moscow",
		"Asia/Dubai", "Asia/Baku", "Asia/Yerevan", "Asia/Karachi", "Asia/Almaty", "Asia/Bangkok",
		"Asia/Jakarta", "Asia/Singapore", "Asia/Hong_Kong", "Asia/Shanghai", "Asia/Tokyo",
		"Asia/Seoul", "Asia/Kolkata", "Australia/Perth", "Australia/Sydney", "Pacific/Auckland",
		"America/Sao_Paulo", "America/Argentina/Buenos_Aires", "America/Halifax", "America/New_York",
		"America/Chicago", "America/Denver", "America/Los_Angeles", "America/Anchorage", "Pacific/Honolulu",
	}

	options := make([]TimezoneOption, 0, len(timezones))
	for _, tz := range timezones {
		loc, err := time.LoadLocation(tz)
		if err != nil {
			continue
		}
		_, offset := now.In(loc).Zone()
		options = append(options, TimezoneOption{Value: tz, Label: fmt.Sprintf("UTC%s — %s", formatUTCOffset(offset), tz)})
	}
	return options
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
