package species

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/BartolottiLuca/plantation/internal/catalog"
	"github.com/BartolottiLuca/plantation/internal/domain"
)

// Everything in this file is independent of which model provider drafts the
// record: what the model is told, how its answer is read, and how the one
// correction round is phrased.

// userMessage carries only the form's fields, delimited, and escapes angle
// brackets so nothing a user types can close a tag or open one of its own.
func userMessage(r Request) string {
	esc := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace
	var b strings.Builder
	b.WriteString("<plant>\n")
	fmt.Fprintf(&b, "<name>%s</name>\n", esc(strings.TrimSpace(r.Name)))
	if s := strings.TrimSpace(r.LabelText); s != "" {
		fmt.Fprintf(&b, "<label_text>%s</label_text>\n", esc(s))
	}
	if s := strings.TrimSpace(r.Notes); s != "" {
		fmt.Fprintf(&b, "<notes>%s</notes>\n", esc(s))
	}
	b.WriteString("</plant>\n\nDraft the species record for this plant.")
	return b.String()
}

// correctionMessage turns validation errors into the second-round user turn.
// Interval and slug problems are not fields the model returns, so they are
// explained in terms of the fields it does control.
func correctionMessage(problems []error) string {
	var b strings.Builder
	b.WriteString("The record you returned was checked and these problems were found:\n")
	for _, p := range problems {
		var fe *catalog.FieldError
		if errors.As(p, &fe) {
			switch fe.Field {
			case "base_interval_days", "min_interval_days", "max_interval_days":
				fmt.Fprintf(&b, "- %s (the app derives the watering interval from kc, mad and substrate; reconsider those three values)\n", fe.Message)
				continue
			case "slug":
				fmt.Fprintf(&b, "- %s (the slug is derived from scientific_name; give the plain binomial)\n", fe.Message)
				continue
			}
		}
		fmt.Fprintf(&b, "- %s\n", p.Error())
	}
	b.WriteString("\nReturn a corrected record. Change only what these problems require.")
	return b.String()
}

// wireProposal is the JSON shape of the response, kept separate from Proposal so
// the domain types carry no serialisation concerns.
type wireProposal struct {
	CommonName     string     `json:"common_name"`
	ScientificName string     `json:"scientific_name"`
	Description    string     `json:"description"`
	CareAdvice     string     `json:"care_advice"`
	Kc             float64    `json:"kc"`
	Substrate      string     `json:"substrate"`
	MAD            float64    `json:"mad"`
	DormantMonths  []int      `json:"dormant_months"`
	DormancyFactor float64    `json:"dormancy_factor"`
	MinTempC       float64    `json:"min_temp_c"`
	FrostTender    bool       `json:"frost_tender"`
	Tasks          []wireTask `json:"tasks"`
	Confidence     string     `json:"confidence"`
	IdentifiedAs   string     `json:"identified_as"`
	Alternatives   []string   `json:"alternatives"`
	Reasoning      string     `json:"reasoning"`
}

type wireTask struct {
	Slug         string `json:"slug"`
	Kind         string `json:"kind"`
	Label        string `json:"label"`
	IntervalDays int    `json:"interval_days"`
	ActiveMonths []int  `json:"active_months"`
	OnlyIn       string `json:"only_in"`
}

// decodeProposal parses the response text strictly: an unexpected field means
// the schema and the decoder have drifted, which should be loud.
func decodeProposal(text string) (Proposal, error) {
	dec := json.NewDecoder(bytes.NewReader([]byte(text)))
	dec.DisallowUnknownFields()
	var w wireProposal
	if err := dec.Decode(&w); err != nil {
		return Proposal{}, err
	}
	conf := Confidence(w.Confidence)
	if !conf.valid() {
		return Proposal{}, fmt.Errorf("confidence %q is not high, medium or low", w.Confidence)
	}

	p := Proposal{
		CommonName:     w.CommonName,
		ScientificName: w.ScientificName,
		Description:    w.Description,
		CareAdvice:     w.CareAdvice,
		Kc:             w.Kc,
		Substrate:      domain.SubstrateKind(w.Substrate),
		MAD:            w.MAD,
		DormantMonths:  toMonths(w.DormantMonths),
		DormancyFactor: w.DormancyFactor,
		MinTempC:       w.MinTempC,
		FrostTender:    w.FrostTender,
		Confidence:     conf,
		IdentifiedAs:   w.IdentifiedAs,
		Alternatives:   w.Alternatives,
		Reasoning:      w.Reasoning,
	}
	for _, t := range w.Tasks {
		task := domain.SpeciesTask{
			Slug:         t.Slug,
			Kind:         domain.TaskKind(t.Kind),
			Label:        t.Label,
			IntervalDays: t.IntervalDays,
			ActiveMonths: toMonths(t.ActiveMonths),
		}
		// The schema makes the field required, so "anywhere" is spelt out on the
		// wire and stored as the empty value.
		switch t.OnlyIn {
		case onlyInAnywhere:
		case string(domain.Indoor), string(domain.Outdoor):
			task.OnlyIn = domain.Location(t.OnlyIn)
		default:
			return Proposal{}, fmt.Errorf("task %q: only_in %q is not anywhere, indoor or outdoor", t.Slug, t.OnlyIn)
		}
		p.Tasks = append(p.Tasks, task)
	}
	return p, nil
}

func toMonths(in []int) []time.Month {
	if len(in) == 0 {
		return nil
	}
	out := make([]time.Month, len(in))
	for i, v := range in {
		out[i] = time.Month(v)
	}
	return out
}
