// Package catalog holds the rules for what a valid species is: value ranges,
// the closed task vocabulary, and the water-balance consistency check that
// keeps base_interval_days honest.
//
// It does not load or store species — the species table is the only source of
// truth, and internal/store owns reading and writing it. Anything that accepts
// a species from outside the code (a form, a model) passes it through Validate
// before it is stored.
package catalog
