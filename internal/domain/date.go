package domain

import (
	"fmt"
	"time"
)

// Date is a civil date with no location. All scheduling comparisons use it.
type Date struct {
	Year  int
	Month time.Month
	Day   int
}

func TodayIn(now time.Time, loc *time.Location) Date {
	t := now.In(loc)
	return Date{Year: t.Year(), Month: t.Month(), Day: t.Day()}
}

func (d Date) AddDays(n int) Date {
	t := d.utc()
	t = t.AddDate(0, 0, n)
	return Date{Year: t.Year(), Month: t.Month(), Day: t.Day()}
}

// Sub returns whole civil days, d − o. Arithmetic is on UTC midnights so a
// DST transition cannot change the count.
func (d Date) Sub(o Date) int {
	return int(d.utc().Sub(o.utc()) / (24 * time.Hour))
}

func (d Date) Before(o Date) bool {
	if d.Year != o.Year {
		return d.Year < o.Year
	}
	if d.Month != o.Month {
		return d.Month < o.Month
	}
	return d.Day < o.Day
}

func (d Date) String() string {
	return fmt.Sprintf("%04d-%02d-%02d", d.Year, int(d.Month), d.Day)
}

func (d Date) utc() time.Time {
	return time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC)
}
