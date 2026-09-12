// Package care is the scheduling engine. It is pure: the standard library and
// internal/domain only, no clock, no I/O. Everything it needs arrives as an
// argument. C03 fills in ScheduleWater and ScheduleFixed.
package care
