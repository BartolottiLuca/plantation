package domain

import (
	"testing"
	"time"
)

func TestDateSubAcrossDST(t *testing.T) {
	// 2026-03-29 Europe/London springs forward (01:00 GMT → 02:00 BST).
	before := Date{Year: 2026, Month: time.March, Day: 28}
	on := Date{Year: 2026, Month: time.March, Day: 29}
	after := Date{Year: 2026, Month: time.March, Day: 30}

	if got := on.Sub(before); got != 1 {
		t.Fatalf("spring-forward Sub = %d, want 1", got)
	}
	if got := after.Sub(before); got != 2 {
		t.Fatalf("span across spring-forward Sub = %d, want 2", got)
	}
	if got := before.AddDays(1); got != on {
		t.Fatalf("AddDays across spring-forward = %s, want %s", got, on)
	}

	// 2026-10-25 Europe/London falls back (02:00 BST → 01:00 GMT).
	fallBefore := Date{Year: 2026, Month: time.October, Day: 24}
	fallOn := Date{Year: 2026, Month: time.October, Day: 25}
	fallAfter := Date{Year: 2026, Month: time.October, Day: 26}

	if got := fallOn.Sub(fallBefore); got != 1 {
		t.Fatalf("fall-back Sub = %d, want 1", got)
	}
	if got := fallAfter.Sub(fallBefore); got != 2 {
		t.Fatalf("span across fall-back Sub = %d, want 2", got)
	}
}

func TestTodayInAcrossDST(t *testing.T) {
	loc, err := time.LoadLocation("Europe/London")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}

	on := Date{Year: 2026, Month: time.March, Day: 29}
	early := time.Date(2026, time.March, 29, 0, 30, 0, 0, loc)
	late := time.Date(2026, time.March, 29, 2, 30, 0, 0, loc)
	if got := TodayIn(early, loc); got != on {
		t.Fatalf("TodayIn before jump = %s, want %s", got, on)
	}
	if got := TodayIn(late, loc); got != on {
		t.Fatalf("TodayIn after jump = %s, want %s", got, on)
	}

	fall := Date{Year: 2026, Month: time.October, Day: 25}
	// The ambiguous 01:30 exists twice; both must still be 25 October.
	first := time.Date(2026, time.October, 25, 0, 30, 0, 0, loc)
	if got := TodayIn(first, loc); got != fall {
		t.Fatalf("TodayIn on fall-back morning = %s, want %s", got, fall)
	}
}

func TestDateAcrossYearEnd(t *testing.T) {
	nye := Date{Year: 2026, Month: time.December, Day: 31}
	nyd := Date{Year: 2027, Month: time.January, Day: 1}

	if got := nye.AddDays(1); got != nyd {
		t.Fatalf("AddDays over year end = %s, want %s", got, nyd)
	}
	if got := nyd.Sub(nye); got != 1 {
		t.Fatalf("Sub over year end = %d, want 1", got)
	}
	if !nye.Before(nyd) {
		t.Fatal("31 Dec must be Before 1 Jan")
	}
	if nyd.Before(nye) {
		t.Fatal("1 Jan must not be Before 31 Dec")
	}
	if got := nye.AddDays(365); got != (Date{Year: 2027, Month: time.December, Day: 31}) {
		t.Fatalf("AddDays(365) from non-leap 31 Dec = %s", got)
	}
}

func TestDateLeapDay(t *testing.T) {
	feb28 := Date{Year: 2024, Month: time.February, Day: 28}
	feb29 := Date{Year: 2024, Month: time.February, Day: 29}
	mar1 := Date{Year: 2024, Month: time.March, Day: 1}

	if got := feb28.AddDays(1); got != feb29 {
		t.Fatalf("AddDays onto leap day = %s, want %s", got, feb29)
	}
	if got := feb28.AddDays(2); got != mar1 {
		t.Fatalf("AddDays over leap day = %s, want %s", got, mar1)
	}
	if got := mar1.Sub(feb28); got != 2 {
		t.Fatalf("Sub over leap day = %d, want 2", got)
	}
}

func TestDateString(t *testing.T) {
	d := Date{Year: 2026, Month: time.September, Day: 11}
	if got := d.String(); got != "2026-09-11" {
		t.Fatalf("String = %q, want 2026-09-11", got)
	}
}
