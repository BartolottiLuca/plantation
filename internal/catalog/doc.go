// Package catalog loads, validates and upserts the species catalog.
//
// The YAML lives in catalog/species and is embedded there (not here): the Go
// embed directive only accepts paths at or below the directory of the file
// that carries it, and never "..", so internal/catalog cannot reach
// catalog/species directly. catalog/species/embed.go declares the embed.FS
// next to the YAML; this package imports that FS and owns everything about
// turning it into validated domain.Species and reconciling it with the
// database.
package catalog
