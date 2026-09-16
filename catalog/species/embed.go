// Package species embeds the curated catalog YAML so it ships inside the
// binary. The embed directive has to live here, not in internal/catalog: Go's
// //go:embed only accepts paths at or below the directory of the source file
// that carries the directive, and never accepts "..". internal/catalog cannot
// reach catalog/species without a "..", so the FS is declared next to the
// YAML and internal/catalog imports it by package path instead.
package species

import "embed"

// FS holds every *.yaml file in this directory, including _template.yaml.
// internal/catalog is responsible for skipping filenames starting with "_"
// when it loads species from FS.
//
//go:embed *.yaml
var FS embed.FS
