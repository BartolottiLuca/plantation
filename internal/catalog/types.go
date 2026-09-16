package catalog

// speciesYAML mirrors domain.Species field-for-field but keeps every value in
// its raw YAML shape (strings for enums, plain ints for months) so decode and
// validate can each name the offending field precisely before anything is
// converted to a typed domain.Species.
type speciesYAML struct {
	Slug             string         `yaml:"slug"`
	CommonName       string         `yaml:"common_name"`
	ScientificName   string         `yaml:"scientific_name"`
	Placement        string         `yaml:"placement"`
	Kc               float64        `yaml:"kc"`
	Substrate        string         `yaml:"substrate"`
	MAD              float64        `yaml:"mad"`
	BaseIntervalDays int            `yaml:"base_interval_days"`
	MinIntervalDays  int            `yaml:"min_interval_days"`
	MaxIntervalDays  int            `yaml:"max_interval_days"`
	DormantMonths    []int          `yaml:"dormant_months"`
	DormancyFactor   *float64       `yaml:"dormancy_factor"`
	MinTempC         float64        `yaml:"min_temp_c"`
	FrostTender      bool           `yaml:"frost_tender"`
	Prune            *fixedTaskYAML `yaml:"prune"`
	Fertilize        *fixedTaskYAML `yaml:"fertilize"`
	Repot            *fixedTaskYAML `yaml:"repot"`
	CareAdvice       string         `yaml:"care_advice"`
	Retired          bool           `yaml:"retired"`
}

// fixedTaskYAML mirrors domain.FixedTask.
type fixedTaskYAML struct {
	IntervalDays int   `yaml:"interval_days"`
	ActiveMonths []int `yaml:"active_months"`
}
