package web

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/BartolottiLuca/plantation/internal/catalog"
	"github.com/BartolottiLuca/plantation/internal/domain"
	"github.com/BartolottiLuca/plantation/internal/species"
	"github.com/BartolottiLuca/plantation/internal/store"
)

type speciesListRow struct {
	Slug       string
	CommonName string
	Scientific string
	Origin     string
	Retired    bool
	Tasks      int
}

type speciesListData struct {
	page
	Species []speciesListRow
}

type askData struct {
	page
	Name      string
	LabelText string
	Notes     string
	Error     string
}

// draftInfo is what the review screen shows above the form so the numbers are
// never mistaken for curated data.
type draftInfo struct {
	IdentifiedAs string
	Confidence   string
	LowConfid    bool
	Alternatives []string
	Reasoning    string
	Model        string
	Retried      bool
}

type speciesFormData struct {
	page
	Heading    string
	Action     string
	Submit     string
	Edit       bool
	Form       speciesForm
	Draft      *draftInfo
	Collision  *domain.Species
	Disabled   bool
	Kinds      []domain.TaskKind
	Substrates []domain.SubstrateKind
}

type draftStatusData struct {
	page
	ID       string
	Name     string
	Failed   bool
	Message  string
	Retry    string
	Manual   string
	Existing *domain.Species
}

// draftingEnabled reports whether a real drafter is wired. A NoopDrafter is the
// disabled feature, so it counts as not enabled.
func (s *Server) draftingEnabled() bool {
	if s.Drafter == nil {
		return false
	}
	_, noop := s.Drafter.(species.NoopDrafter)
	return !noop
}

func (s *Server) speciesList(w http.ResponseWriter, r *http.Request) {
	all, err := s.Species.List(r.Context())
	if err != nil {
		s.internal(w, "listing species", err)
		return
	}
	rows := make([]speciesListRow, 0, len(all))
	for _, sp := range all {
		rows = append(rows, speciesListRow{
			Slug:       sp.Slug,
			CommonName: sp.CommonName,
			Scientific: sp.ScientificName,
			Origin:     originLabel(sp.Origin),
			Retired:    sp.Retired,
			Tasks:      len(sp.Tasks),
		})
	}
	sort.Slice(rows, func(i, j int) bool { return strings.ToLower(rows[i].CommonName) < strings.ToLower(rows[j].CommonName) })
	s.render(w, http.StatusOK, "species_list.html", speciesListData{page: s.page(r, "species"), Species: rows})
}

func originLabel(o domain.SpeciesOrigin) string {
	switch o {
	case domain.OriginAI:
		return "Drafted by AI"
	default:
		return "Added by hand"
	}
}

func (s *Server) newSpeciesGET(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("manual") == "1" || !s.draftingEnabled() {
		f := blankSpeciesForm()
		f.CommonName = strings.TrimSpace(q.Get("name"))
		s.renderSpeciesForm(w, r, http.StatusOK, speciesFormData{
			Heading:  "Add a species",
			Action:   "/species/new",
			Submit:   "Add species",
			Form:     f,
			Disabled: !s.draftingEnabled(),
		})
		return
	}
	s.render(w, http.StatusOK, "species_ask.html", askData{
		page: s.page(r, "add"),
		Name: strings.TrimSpace(q.Get("name")),
	})
}

func (s *Server) draftPOST(w http.ResponseWriter, r *http.Request) {
	if !s.draftingEnabled() {
		s.redirect(w, r, "/species/new?manual=1")
		return
	}
	req := species.Request{
		Name:      strings.TrimSpace(r.FormValue("name")),
		LabelText: strings.TrimSpace(r.FormValue("label_text")),
		Notes:     strings.TrimSpace(r.FormValue("notes")),
	}
	ask := askData{page: s.page(r, "add"), Name: req.Name, LabelText: req.LabelText, Notes: req.Notes}
	if err := req.Validate(); err != nil {
		ask.Error = capitalise(err.Error()) + "."
		s.render(w, http.StatusBadRequest, "species_ask.html", ask)
		return
	}
	if !s.draftLimit.allow(s.Clock.Now()) {
		ask.Error = "That is a lot of drafts for one hour. Try again a little later, or fill the species in by hand."
		s.render(w, http.StatusTooManyRequests, "species_ask.html", ask)
		return
	}
	id, err := s.drafts.start(s.Clock.Now(), req, s.draftAndSave(req))
	if err != nil {
		s.internal(w, "starting species draft", err)
		return
	}
	s.redirect(w, r, "/drafts/"+id)
}

// saveTimeout bounds the one insert that follows a finished draft.
const saveTimeout = 10 * time.Second

// errSaveFailed marks a draft that was fine but could not be stored, so the
// page does not blame the drafting service for a database problem.
var errSaveFailed = errors.New("saving the drafted species failed")

// draftAndSave is the whole background job: draft, and if the draft is clean,
// store it straight away. A draft is not shown for confirmation first; it is
// only shown when it cannot be stored as it is (problems that survived the
// model's correction round), and an existing species is never overwritten.
func (s *Server) draftAndSave(req species.Request) func(context.Context) draftOutcome {
	drafter, repo := s.Drafter, s.Species
	return func(ctx context.Context) draftOutcome {
		res, err := drafter.Draft(ctx, req)
		if err != nil {
			return draftOutcome{State: draftFailed, Err: err}
		}
		if len(res.Problems) > 0 {
			return draftOutcome{State: draftNeedsReview, Result: res}
		}

		ctx, cancel := context.WithTimeout(ctx, saveTimeout)
		defer cancel()
		err = repo.Create(ctx, res.Species)
		switch {
		case err == nil:
			slog.Info("drafted species saved", "slug", res.Species.Slug, "model", res.Model)
			return draftOutcome{State: draftSaved, Result: res}
		case errors.Is(err, store.ErrConflict):
			existing, gerr := repo.Get(ctx, res.Species.Slug)
			if gerr == nil {
				return draftOutcome{State: draftExists, Result: res, Existing: &existing}
			}
			err = gerr
		}
		slog.Error("saving drafted species", "slug", res.Species.Slug, "err", err)
		return draftOutcome{State: draftFailed, Err: fmt.Errorf("%w: %w", errSaveFailed, err)}
	}
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func (s *Server) draftGET(w http.ResponseWriter, r *http.Request) {
	job, ok := s.drafts.get(r.PathValue("id"))
	if !ok {
		s.notFound(w)
		return
	}
	if job.State != draftPending && isHTMX(r) {
		// The polling fragment is done: send the whole page somewhere better.
		dest := "/drafts/" + job.ID
		if job.State == draftSaved {
			dest = savedDest(job.Result.Species.Slug)
		}
		s.redirect(w, r, dest)
		return
	}
	switch job.State {
	case draftPending:
		data := draftStatusData{page: s.page(r, "add"), ID: job.ID, Name: job.Request.Name}
		if isHTMX(r) {
			s.renderPartial(w, http.StatusOK, "species_drafting.html", "draft-pending", data)
			return
		}
		s.render(w, http.StatusOK, "species_drafting.html", data)
	case draftSaved:
		s.redirect(w, r, savedDest(job.Result.Species.Slug))
	case draftNeedsReview:
		s.renderReview(w, r, job)
	case draftExists:
		s.render(w, http.StatusOK, "species_drafting.html", draftStatusData{
			page:     s.page(r, "add"),
			ID:       job.ID,
			Name:     job.Request.Name,
			Existing: job.Existing,
		})
	default:
		slog.Warn("species draft failed", "err", job.Err)
		s.render(w, http.StatusOK, "species_drafting.html", draftStatusData{
			page:    s.page(r, "add"),
			ID:      job.ID,
			Name:    job.Request.Name,
			Failed:  true,
			Message: draftFailureMessage(job.Err),
			Retry:   "/species/new?name=" + queryEscape(job.Request.Name),
			Manual:  "/species/new?manual=1&name=" + queryEscape(job.Request.Name),
		})
	}
}

// savedDest is where a saved draft sends you: straight on to adding the plant,
// with a note that the species was added without review.
func savedDest(slug string) string {
	return "/plants/new?species=" + queryEscape(slug) + "&drafted=1"
}

// draftFailureMessage picks what a person is told. It never quotes the error:
// SDK errors can carry request details that do not belong on a page.
func draftFailureMessage(err error) string {
	switch {
	case errors.Is(err, errSaveFailed):
		return "The species was drafted but could not be saved. Try again in a moment, or fill it in by hand."
	case errors.Is(err, species.ErrRateLimited):
		return "The AI service is rate limiting requests right now. Try again in a minute, or fill the species in by hand."
	case errors.Is(err, species.ErrNoQuota):
		return "The AI account has run out of credit, so drafting is unavailable until it is topped up. Fill the species in by hand."
	case errors.Is(err, species.ErrRefused):
		return "The AI declined to draft this one. Try describing it differently, or fill the species in by hand."
	case errors.Is(err, species.ErrTruncated):
		return "The draft was cut off before it finished. Trying again usually works, or fill the species in by hand."
	case errors.Is(err, species.ErrDisabled):
		return "Drafting is not set up. Fill the species in by hand."
	default:
		return "The drafting service could not be reached. Try again shortly, or fill the species in by hand."
	}
}

func (s *Server) renderReview(w http.ResponseWriter, r *http.Request, job draftJob) {
	res := job.Result
	f := speciesFormFromDraft(res)
	info := &draftInfo{
		IdentifiedAs: res.Proposal.IdentifiedAs,
		Confidence:   string(res.Proposal.Confidence),
		LowConfid:    res.Proposal.Confidence == species.ConfidenceLow,
		Alternatives: res.Proposal.Alternatives,
		Reasoning:    res.Proposal.Reasoning,
		Model:        res.Model,
		Retried:      res.Retried,
	}
	s.renderSpeciesForm(w, r, http.StatusOK, speciesFormData{
		Heading: "Review the drafted species",
		Action:  "/species/new",
		Submit:  "Save species",
		Form:    f,
		Draft:   info,
	})
}

func (s *Server) createSpeciesPOST(w http.ResponseWriter, r *http.Request) {
	f := parseSpeciesForm(r)
	sp, rows := f.build()
	recomputed := f.reconcileIntervals(&sp)
	f.validate(sp, rows)

	data := speciesFormData{Heading: "Add a species", Action: "/species/new", Submit: "Add species", Form: f}
	if f.Origin == string(domain.OriginAI) {
		data.Heading, data.Submit = "Review the drafted species", "Save species"
	}
	if !f.ok() {
		data.Form = f
		s.renderSpeciesForm(w, r, http.StatusBadRequest, data)
		return
	}
	if recomputed {
		data.Form = f
		s.renderSpeciesForm(w, r, http.StatusOK, data)
		return
	}

	sp.Origin, sp.AIModel, sp.AIDraftedAt = f.provenance()
	if err := s.Species.Create(r.Context(), sp); err != nil {
		if errors.Is(err, store.ErrConflict) {
			existing, gerr := s.Species.Get(r.Context(), sp.Slug)
			if gerr == nil {
				f.Errors["slug"] = "A species with this slug already exists."
				data.Form, data.Collision = f, &existing
				s.renderSpeciesForm(w, r, http.StatusConflict, data)
				return
			}
		}
		s.internal(w, "creating species", err)
		return
	}
	s.redirect(w, r, "/plants/new?species="+queryEscape(sp.Slug))
}

func (s *Server) editSpeciesGET(w http.ResponseWriter, r *http.Request) {
	sp, err := s.Species.Get(r.Context(), r.PathValue("slug"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.notFound(w)
			return
		}
		s.internal(w, "loading species", err)
		return
	}
	s.renderSpeciesForm(w, r, http.StatusOK, editData(sp, speciesFormFrom(sp, true)))
}

func editData(sp domain.Species, f speciesForm) speciesFormData {
	return speciesFormData{
		Heading: "Edit " + sp.CommonName,
		Action:  "/species/" + sp.Slug + "/edit",
		Submit:  "Save",
		Edit:    true,
		Form:    f,
	}
}

func (s *Server) editSpeciesPOST(w http.ResponseWriter, r *http.Request) {
	current, err := s.Species.Get(r.Context(), r.PathValue("slug"))
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.notFound(w)
			return
		}
		s.internal(w, "loading species", err)
		return
	}
	f := parseSpeciesForm(r)
	f.Slug = current.Slug // identity comes from the path, never the form
	sp, rows := f.build()
	recomputed := f.reconcileIntervals(&sp)
	f.validate(sp, rows)

	data := editData(current, f)
	if !f.ok() {
		s.renderSpeciesForm(w, r, http.StatusBadRequest, data)
		return
	}
	if recomputed {
		s.renderSpeciesForm(w, r, http.StatusOK, data)
		return
	}
	if err := s.Species.Update(r.Context(), sp); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			s.notFound(w)
			return
		}
		s.internal(w, "updating species", err)
		return
	}
	s.redirect(w, r, "/species")
}

func (s *Server) renderSpeciesForm(w http.ResponseWriter, r *http.Request, status int, d speciesFormData) {
	nav := "add"
	if d.Edit {
		nav = "species"
	}
	d.page = s.page(r, nav)
	d.Kinds = catalog.SpeciesTaskKinds()
	d.Substrates = []domain.SubstrateKind{domain.Peat, domain.Cactus, domain.Coir}
	if d.Form.Errors == nil {
		d.Form.Errors = map[string]string{}
	}
	s.render(w, status, "species_form.html", d)
}

func queryEscape(s string) string { return url.QueryEscape(s) }
