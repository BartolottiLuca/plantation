package catalog

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// decode parses one species YAML file strictly: an unrecognised key is an
// error, not a silently-dropped field. file and slug are only used to shape
// the error message; the file is not re-read from disk here.
func decode(file, slug string, data []byte) (speciesYAML, error) {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	var y speciesYAML
	if err := dec.Decode(&y); err != nil {
		return speciesYAML{}, fmt.Errorf("%s: %s: decoding: %w", file, slug, err)
	}
	return y, nil
}
