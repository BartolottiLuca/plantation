package store

import (
	"errors"
	"time"

	"github.com/BartolottiLuca/plantation/internal/domain"
	"github.com/jackc/pgx/v5"
)

func civilDate(d domain.Date) time.Time {
	return time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC)
}

func dateFromTime(t time.Time) domain.Date {
	y, m, day := t.UTC().Date()
	return domain.Date{Year: y, Month: m, Day: day}
}

func monthsToInts(months []time.Month) []int32 {
	if len(months) == 0 {
		return []int32{}
	}
	out := make([]int32, len(months))
	for i, m := range months {
		out[i] = int32(m)
	}
	return out
}

func intsToMonths(ints []int32) []time.Month {
	if len(ints) == 0 {
		return nil
	}
	out := make([]time.Month, len(ints))
	for i, n := range ints {
		out[i] = time.Month(n)
	}
	return out
}

func optionalDate(t *time.Time) *domain.Date {
	if t == nil {
		return nil
	}
	d := dateFromTime(*t)
	return &d
}

func dateArg(d *domain.Date) any {
	if d == nil {
		return nil
	}
	return civilDate(*d)
}

func mapNoRows(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	return err
}
