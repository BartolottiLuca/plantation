package web

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/BartolottiLuca/plantation/internal/catalog"
	"github.com/BartolottiLuca/plantation/internal/domain"
	"github.com/BartolottiLuca/plantation/internal/species"
)

const (
	// The form is plain HTML with no script, so task rows are a fixed grid:
	// existing tasks first, then blank rows to add into.
	maxTaskRows    = 8
	minBlankRows   = 2
	maxNameChars   = 200
	maxProseChars  = 4000
	maxLabelChars  = 200
	formMonthsHint = "Months as numbers 1–12, separated by commas"
)

type speciesTaskRow struct {
	Slug         string
	Kind         string
	Label        string
	IntervalDays string
	ActiveMonths string
	OnlyIn       string // "", "indoor" or "outdoor"
	// Existing marks a task that is already saved on the species: its slug is a
	// permanent identity, so the input is read-only and a rename is not offered.
	Existing bool
	Remove   bool
}

func (r speciesTaskRow) blank() bool {
	return r.Slug == "" && r.Label == "" && r.IntervalDays == "" && r.ActiveMonths == "" && !r.Existing
}

// speciesForm is one species record as a person edits it: every value a string,
// so a half-typed number can be shown back with its error instead of vanishing.
type speciesForm struct {
	Slug           string
	CommonName     string
	ScientificName string
	Description    string
	CareAdvice     string
	Substrate      string
	Kc             string
	MAD            string
	DormancyFactor string
	MinTempC       string
	BaseInterval   string
	MinInterval    string
	MaxInterval    string
	DormantMonths  string
	FrostTender    bool
	Retired        bool
	Tasks          []speciesTaskRow

	// What the form showed when it was rendered. Comparing the submission with
	// these is how the server tells an interval a person left alone (safe to
	// recompute when the constants change) from one they set on purpose.
	RenderedFrom string
	RenderedBase string
	RenderedMin  string
	RenderedMax  string

	// Provenance carried through review; only "ai" with a model and a
	// timestamp is honoured on save, anything else is stored as manual.
	Origin      string
	AIModel     string
	AIDraftedAt string

	Errors map[string]string
	Notice string
}

func fmtFloat(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func joinMonths(ms []time.Month) string {
	parts := make([]string, len(ms))
	for i, m := range ms {
		parts[i] = strconv.Itoa(int(m))
	}
	return strings.Join(parts, ", ")
}

var monthSep = regexp.MustCompile(`[,;\s]+`)

func parseMonths(s string) ([]time.Month, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, nil
	}
	var out []time.Month
	for _, part := range monthSep.Split(s, -1) {
		if part == "" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil {
			return nil, fmt.Errorf("%q is not a month number", part)
		}
		out = append(out, time.Month(n))
	}
	return out, nil
}

func blankSpeciesForm() speciesForm {
	f := speciesForm{
		Substrate:      string(domain.Peat),
		DormancyFactor: "1",
		Errors:         map[string]string{},
	}
	f.padTasks()
	return f
}

// speciesFormFrom fills a form from a species, as the edit screen and the
// draft review both do.
func speciesFormFrom(sp domain.Species, existingTasks bool) speciesForm {
	f := speciesForm{
		Slug:           sp.Slug,
		CommonName:     sp.CommonName,
		ScientificName: sp.ScientificName,
		Description:    sp.Description,
		CareAdvice:     sp.CareAdvice,
		Substrate:      string(sp.Substrate),
		Kc:             fmtFloat(sp.Kc),
		MAD:            fmtFloat(sp.MAD),
		DormancyFactor: fmtFloat(sp.DormancyFactor),
		MinTempC:       fmtFloat(sp.MinTempC),
		BaseInterval:   strconv.Itoa(sp.BaseIntervalDays),
		MinInterval:    strconv.Itoa(sp.MinIntervalDays),
		MaxInterval:    strconv.Itoa(sp.MaxIntervalDays),
		DormantMonths:  joinMonths(sp.DormantMonths),
		FrostTender:    sp.FrostTender,
		Retired:        sp.Retired,
		Errors:         map[string]string{},
	}
	for _, t := range sp.Tasks {
		f.Tasks = append(f.Tasks, speciesTaskRow{
			Slug:         t.Slug,
			Kind:         string(t.Kind),
			Label:        t.Label,
			IntervalDays: strconv.Itoa(t.IntervalDays),
			ActiveMonths: joinMonths(t.ActiveMonths),
			OnlyIn:       string(t.OnlyIn),
			Existing:     existingTasks,
		})
	}
	f.padTasks()
	f.markRendered()
	return f
}

// padTasks tops the task grid up with blank rows so a person can add tasks.
func (f *speciesForm) padTasks() {
	blanks := 0
	for _, t := range f.Tasks {
		if t.blank() {
			blanks++
		}
	}
	for blanks < minBlankRows && len(f.Tasks) < maxTaskRows {
		f.Tasks = append(f.Tasks, speciesTaskRow{})
		blanks++
	}
	if len(f.Tasks) > maxTaskRows {
		f.Tasks = f.Tasks[:maxTaskRows]
	}
}

// constantsKey names the three inputs the intervals derive from.
func (f *speciesForm) constantsKey() string {
	return strings.Join([]string{strings.TrimSpace(f.Kc), strings.TrimSpace(f.MAD), strings.TrimSpace(f.Substrate)}, "|")
}

func (f *speciesForm) markRendered() {
	f.RenderedFrom = f.constantsKey()
	f.RenderedBase = strings.TrimSpace(f.BaseInterval)
	f.RenderedMin = strings.TrimSpace(f.MinInterval)
	f.RenderedMax = strings.TrimSpace(f.MaxInterval)
}

func parseSpeciesForm(r *http.Request) speciesForm {
	v := func(name string) string { return strings.TrimSpace(r.FormValue(name)) }
	f := speciesForm{
		Slug:           v("slug"),
		CommonName:     v("common_name"),
		ScientificName: v("scientific_name"),
		Description:    strings.TrimSpace(r.FormValue("description")),
		CareAdvice:     strings.TrimSpace(r.FormValue("care_advice")),
		Substrate:      v("substrate"),
		Kc:             v("kc"),
		MAD:            v("mad"),
		DormancyFactor: v("dormancy_factor"),
		MinTempC:       v("min_temp_c"),
		BaseInterval:   v("base_interval_days"),
		MinInterval:    v("min_interval_days"),
		MaxInterval:    v("max_interval_days"),
		DormantMonths:  v("dormant_months"),
		FrostTender:    r.FormValue("frost_tender") != "",
		Retired:        r.FormValue("retired") != "",
		RenderedFrom:   v("rendered_from"),
		RenderedBase:   v("rendered_base"),
		RenderedMin:    v("rendered_min"),
		RenderedMax:    v("rendered_max"),
		Origin:         v("origin"),
		AIModel:        v("ai_model"),
		AIDraftedAt:    v("ai_drafted_at"),
		Errors:         map[string]string{},
	}
	for i := 0; i < maxTaskRows; i++ {
		key := func(name string) string { return v(fmt.Sprintf("tasks[%d].%s", i, name)) }
		f.Tasks = append(f.Tasks, speciesTaskRow{
			Slug:         key("slug"),
			Kind:         key("kind"),
			Label:        key("label"),
			IntervalDays: key("interval_days"),
			ActiveMonths: key("active_months"),
			OnlyIn:       key("only_in"),
			Existing:     key("existing") != "",
			Remove:       key("remove") != "",
		})
	}
	return f
}

func (f *speciesForm) setErr(field, msg string) {
	if _, taken := f.Errors[field]; !taken {
		f.Errors[field] = msg
	}
}

func (f *speciesForm) ok() bool { return len(f.Errors) == 0 }

// DormantMonthsHint is the helper text for a months input.
func (f speciesForm) DormantMonthsHint() string { return formMonthsHint }

// build converts the form to a species and records a message for every value
// that could not be read. rows maps a position in the returned task list back to
// the form row it came from, so validator errors land on the right inputs.
func (f *speciesForm) build() (sp domain.Species, rows []int) {
	sp = domain.Species{
		Slug:           f.Slug,
		CommonName:     f.CommonName,
		ScientificName: f.ScientificName,
		Description:    f.Description,
		CareAdvice:     f.CareAdvice,
		Substrate:      domain.SubstrateKind(f.Substrate),
		FrostTender:    f.FrostTender,
		Retired:        f.Retired,
	}
	if sp.Slug == "" && f.ScientificName != "" {
		sp.Slug = species.Slugify(f.ScientificName)
	}

	for _, c := range []struct {
		field, label string
		raw          string
		dst          *float64
	}{
		{"kc", "Kc", f.Kc, &sp.Kc},
		{"mad", "MAD", f.MAD, &sp.MAD},
		{"dormancy_factor", "Dormancy factor", f.DormancyFactor, &sp.DormancyFactor},
		{"min_temp_c", "Minimum temperature", f.MinTempC, &sp.MinTempC},
	} {
		n, err := strconv.ParseFloat(c.raw, 64)
		if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
			f.setErr(c.field, c.label+" must be a number")
			continue
		}
		*c.dst = n
	}
	for _, c := range []struct {
		field, label string
		raw          string
		dst          *int
	}{
		{"base_interval_days", "Base interval", f.BaseInterval, &sp.BaseIntervalDays},
		{"min_interval_days", "Minimum interval", f.MinInterval, &sp.MinIntervalDays},
		{"max_interval_days", "Maximum interval", f.MaxInterval, &sp.MaxIntervalDays},
	} {
		n, err := strconv.Atoi(c.raw)
		if err != nil {
			f.setErr(c.field, c.label+" must be a whole number of days")
			continue
		}
		*c.dst = n
	}

	months, err := parseMonths(f.DormantMonths)
	if err != nil {
		f.setErr("dormant_months", err.Error())
	}
	sp.DormantMonths = months

	if len([]rune(f.CommonName)) > maxNameChars {
		f.setErr("common_name", "Name is too long")
	}
	if len([]rune(f.ScientificName)) > maxNameChars {
		f.setErr("scientific_name", "Scientific name is too long")
	}
	if len([]rune(f.Description)) > maxProseChars {
		f.setErr("description", "Description is too long")
	}
	if len([]rune(f.CareAdvice)) > maxProseChars {
		f.setErr("care_advice", "Care advice is too long")
	}

	for i, row := range f.Tasks {
		if row.Remove || row.blank() {
			continue
		}
		t := domain.SpeciesTask{
			Slug:   row.Slug,
			Kind:   domain.TaskKind(row.Kind),
			Label:  row.Label,
			OnlyIn: domain.Location(row.OnlyIn),
		}
		field := func(name string) string { return fmt.Sprintf("tasks[%d].%s", i, name) }
		if n, err := strconv.Atoi(row.IntervalDays); err != nil {
			f.setErr(field("interval_days"), "Interval must be a whole number of days")
		} else {
			t.IntervalDays = n
		}
		ms, err := parseMonths(row.ActiveMonths)
		if err != nil {
			f.setErr(field("active_months"), err.Error())
		}
		t.ActiveMonths = ms
		if len([]rune(row.Label)) > maxLabelChars {
			f.setErr(field("label"), "Label is too long")
		}
		sp.Tasks = append(sp.Tasks, t)
		rows = append(rows, i)
	}
	return sp, rows
}

var taskFieldIndex = regexp.MustCompile(`^tasks\[(\d+)\]\.`)

// validate runs the same gate a drafted species passes and folds its errors
// into the form, translating a task's position in the species back to its row.
func (f *speciesForm) validate(sp domain.Species, rows []int) {
	for _, e := range catalog.Validate(sp) {
		var fe *catalog.FieldError
		if !errors.As(e, &fe) {
			f.setErr("", e.Error())
			continue
		}
		field := taskFieldIndex.ReplaceAllStringFunc(fe.Field, func(m string) string {
			idx, _ := strconv.Atoi(taskFieldIndex.FindStringSubmatch(m)[1])
			if idx >= 0 && idx < len(rows) {
				return fmt.Sprintf("tasks[%d].", rows[idx])
			}
			return m
		})
		if existing, taken := f.Errors[field]; taken {
			// Several rules can hit one field; show them all rather than the first.
			if !strings.Contains(existing, fe.Message) {
				f.Errors[field] = existing + "; " + fe.Message
			}
			continue
		}
		f.Errors[field] = fe.Message
	}
}

// reconcileIntervals decides what to do about the three interval fields, which
// derive from kc, mad and substrate. It returns true when it rewrote them and
// the form must be shown again for confirmation instead of saved.
//
//   - All three blank: fill them in and carry on. A person typing a species by
//     hand has no reason to know them.
//   - Constants changed since the form was rendered and the intervals were left
//     as rendered: recompute, and stop, so the new numbers are seen before saving.
//   - Anything else: the intervals are the person's, so keep them.
func (f *speciesForm) reconcileIntervals(sp *domain.Species) (recomputed bool) {
	if _, bad := f.Errors["kc"]; bad {
		return false
	}
	if _, bad := f.Errors["mad"]; bad {
		return false
	}
	if !catalogSubstrate(sp.Substrate) || !inRangeF(sp.Kc, catalog.KcMin, catalog.KcMax) || !inRangeF(sp.MAD, catalog.MADMin, catalog.MADMax) {
		return false
	}

	base := catalog.ModelledIntervalDays(sp.Kc, sp.MAD, sp.Substrate)
	lo, hi := species.IntervalBounds(base)
	fill := func() {
		f.BaseInterval, f.MinInterval, f.MaxInterval = strconv.Itoa(base), strconv.Itoa(lo), strconv.Itoa(hi)
		delete(f.Errors, "base_interval_days")
		delete(f.Errors, "min_interval_days")
		delete(f.Errors, "max_interval_days")
		sp.BaseIntervalDays, sp.MinIntervalDays, sp.MaxIntervalDays = base, lo, hi
	}

	if f.BaseInterval == "" && f.MinInterval == "" && f.MaxInterval == "" {
		fill()
		f.markRendered()
		return false
	}

	stale := f.RenderedFrom != "" && f.RenderedFrom != f.constantsKey()
	untouched := f.BaseInterval == f.RenderedBase && f.MinInterval == f.RenderedMin && f.MaxInterval == f.RenderedMax
	if stale && untouched {
		fill()
		f.markRendered()
		f.Notice = fmt.Sprintf("The intervals are derived from kc, mad and substrate, so they were recomputed for your changes (base %d days, between %d and %d). Check them and save again.", base, lo, hi)
		return true
	}
	return false
}

func catalogSubstrate(s domain.SubstrateKind) bool {
	return s == domain.Peat || s == domain.Cactus || s == domain.Coir
}

func inRangeF(v, lo, hi float64) bool { return v >= lo && v <= hi }

// provenance turns the carried-through hidden fields into stored provenance.
// Only a complete AI claim is honoured; anything else is a hand-entered record.
func (f *speciesForm) provenance() (domain.SpeciesOrigin, string, *time.Time) {
	if f.Origin == string(domain.OriginAI) && f.AIModel != "" {
		if t, err := time.Parse(time.RFC3339, f.AIDraftedAt); err == nil {
			return domain.OriginAI, f.AIModel, &t
		}
	}
	return domain.OriginManual, "", nil
}

// speciesFormFromDraft fills the review form from a drafted species and maps
// the problems that survived the model's correction onto their inputs.
func speciesFormFromDraft(res species.Result) speciesForm {
	f := speciesFormFrom(res.Species, false)
	f.Origin = string(domain.OriginAI)
	f.AIModel = res.Species.AIModel
	if res.Species.AIDraftedAt != nil {
		f.AIDraftedAt = res.Species.AIDraftedAt.Format(time.RFC3339)
	}
	rows := make([]int, len(res.Species.Tasks))
	for i := range rows {
		rows[i] = i
	}
	f.validate(res.Species, rows)
	return f
}
