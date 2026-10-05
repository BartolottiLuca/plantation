package species

import (
	"fmt"

	"github.com/BartolottiLuca/plantation/internal/catalog"
	"github.com/BartolottiLuca/plantation/internal/domain"
)

// outputSchema is the JSON Schema the response must satisfy. Structured outputs
// accept a restricted subset: no minimum/maximum and no length limits. The
// ranges therefore live in the descriptions, where they steer the model, and
// catalog.Validate enforces them afterwards — the schema guarantees shape,
// Validate guarantees sense.
// onlyInAnywhere is how the response says a task has no placement restriction.
// Every field is required, so the absence has to be a value of its own.
const onlyInAnywhere = "anywhere"

func outputSchema() map[string]any {
	str := func(desc string) map[string]any {
		return map[string]any{"type": "string", "description": desc}
	}
	num := func(desc string) map[string]any {
		return map[string]any{"type": "number", "description": desc}
	}
	enum := func(desc string, values ...string) map[string]any {
		return map[string]any{"type": "string", "enum": values, "description": desc}
	}
	months := func(desc string) map[string]any {
		return map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "integer"},
			"description": desc,
		}
	}
	object := func(props map[string]any, required ...string) map[string]any {
		return map[string]any{
			"type":                 "object",
			"properties":           props,
			"required":             required,
			"additionalProperties": false,
		}
	}

	kinds := make([]string, 0)
	for _, k := range catalog.SpeciesTaskKinds() {
		kinds = append(kinds, string(k))
	}

	task := object(map[string]any{
		"slug":          str("kebab-case identity for this task within the species, e.g. pinch-flowers. Never \"water\"."),
		"kind":          enum("the kind of care", kinds...),
		"label":         str("what a person reads in a reminder: the instruction, not the kind name"),
		"interval_days": map[string]any{"type": "integer", "description": fmt.Sprintf("days between repeats, %d to %d", catalog.TaskIntervalMin, catalog.TaskIntervalMax)},
		"active_months": months("months 1-12 in which the task can fall due; empty means all year"),
		"only_in":       enum("where the plant must be kept for this task to apply; anywhere for most tasks", onlyInAnywhere, string(domain.Indoor), string(domain.Outdoor)),
	}, "slug", "kind", "label", "interval_days", "active_months", "only_in")

	props := map[string]any{
		"common_name":     str("the name most people call it"),
		"scientific_name": str("binomial scientific name, e.g. Monstera deliciosa"),
		"description":     str("a paragraph of background: what it is, where it comes from, what it wants"),
		"care_advice":     str("one or two sentences of specific, practical horticultural advice"),
		"kc":              num(fmt.Sprintf("crop coefficient, %v to %v", catalog.KcMin, catalog.KcMax)),
		"substrate":       enum("the growing medium", string(domain.Peat), string(domain.Cactus), string(domain.Coir)),
		"mad":             num(fmt.Sprintf("management allowed depletion, %v to %v", catalog.MADMin, catalog.MADMax)),
		"dormant_months":  months("months 1-12 of the species' rest period as it shows indoors in a warm room; empty if it has none"),
		"dormancy_factor": num(fmt.Sprintf("water-use multiplier during dormant_months, %v to %v; 1.0 when there is no rest period", catalog.DormancyFactorMin, catalog.DormancyFactorMax)),
		"min_temp_c":      num("degrees C below which the plant needs protection"),
		"frost_tender":    map[string]any{"type": "boolean", "description": "true if frost at min_temp_c would damage it outdoors"},
		"tasks": map[string]any{
			"type":        "array",
			"items":       task,
			"description": "fixed-interval care tasks in display order; may be empty",
		},
		"confidence":    enum("how sure you are you identified the right plant", string(ConfidenceHigh), string(ConfidenceMedium), string(ConfidenceLow)),
		"identified_as": str("the binomial you actually described"),
		"alternatives": map[string]any{
			"type":        "array",
			"items":       map[string]any{"type": "string"},
			"description": "other plants the description could mean; empty when you are confident",
		},
		"reasoning": str("two or three sentences on why you chose these constants"),
	}

	required := []string{
		"common_name", "scientific_name", "description", "care_advice",
		"kc", "substrate", "mad", "dormant_months", "dormancy_factor", "min_temp_c",
		"frost_tender", "tasks", "confidence", "identified_as", "alternatives", "reasoning",
	}
	return object(props, required...)
}
