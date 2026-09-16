package catalog

import (
	"context"
	"reflect"
	"sort"
	"testing"

	"github.com/BartolottiLuca/plantation/internal/domain"
)

// fakeRepo behaves like store.SpeciesRepo would after a real UpsertAll: List
// reflects whatever the most recent UpsertAll wrote, keyed by slug.
type fakeRepo struct {
	bySlug        map[string]domain.Species
	lastUpsertArg []domain.Species
}

func newFakeRepo(seed ...domain.Species) *fakeRepo {
	r := &fakeRepo{bySlug: map[string]domain.Species{}}
	for _, s := range seed {
		r.bySlug[s.Slug] = s
	}
	return r
}

func (r *fakeRepo) List(_ context.Context) ([]domain.Species, error) {
	out := make([]domain.Species, 0, len(r.bySlug))
	for _, s := range r.bySlug {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out, nil
}

func (r *fakeRepo) UpsertAll(_ context.Context, species []domain.Species) error {
	r.lastUpsertArg = species
	for _, s := range species {
		r.bySlug[s.Slug] = s
	}
	return nil
}

func sortedBySlug(species []domain.Species) []domain.Species {
	out := append([]domain.Species(nil), species...)
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out
}

func TestUpsertTwiceIsANoOp(t *testing.T) {
	repo := newFakeRepo()
	loaded := []domain.Species{
		{Slug: "monstera-deliciosa", CommonName: "Monstera", BaseIntervalDays: 6},
		{Slug: "ficus-lyrata", CommonName: "Fiddle-leaf fig", BaseIntervalDays: 6},
	}

	if err := Upsert(context.Background(), repo, loaded); err != nil {
		t.Fatalf("first Upsert: %v", err)
	}
	first := sortedBySlug(repo.lastUpsertArg)

	if err := Upsert(context.Background(), repo, loaded); err != nil {
		t.Fatalf("second Upsert: %v", err)
	}
	second := sortedBySlug(repo.lastUpsertArg)

	if !reflect.DeepEqual(first, second) {
		t.Fatalf("second Upsert argument differs from first:\nfirst:  %+v\nsecond: %+v", first, second)
	}
	if len(second) != len(loaded) {
		t.Fatalf("got %d species after two no-op upserts, want %d", len(second), len(loaded))
	}
}

func TestUpsertRetiresSpeciesRemovedFromCatalog(t *testing.T) {
	existing := domain.Species{
		Slug:             "extinct-plantus",
		CommonName:       "No longer in the catalog",
		BaseIntervalDays: 9,
		Retired:          false,
	}
	repo := newFakeRepo(existing)
	loaded := []domain.Species{
		{Slug: "monstera-deliciosa", CommonName: "Monstera", BaseIntervalDays: 6},
	}

	if err := Upsert(context.Background(), repo, loaded); err != nil {
		t.Fatalf("Upsert: %v", err)
	}

	var gotExtinct *domain.Species
	for i := range repo.lastUpsertArg {
		if repo.lastUpsertArg[i].Slug == "extinct-plantus" {
			gotExtinct = &repo.lastUpsertArg[i]
		}
	}
	if gotExtinct == nil {
		t.Fatal("extinct-plantus was omitted from UpsertAll, want it passed through with Retired=true")
	}
	if !gotExtinct.Retired {
		t.Fatalf("extinct-plantus Retired = false, want true (never delete, never leave active)")
	}
	if gotExtinct.CommonName != existing.CommonName {
		t.Fatalf("extinct-plantus data was altered: got %+v, want CommonName preserved", gotExtinct)
	}

	if len(repo.lastUpsertArg) != 2 {
		t.Fatalf("got %d species in UpsertAll call, want 2 (1 loaded + 1 retired)", len(repo.lastUpsertArg))
	}
}

func TestUpsertListErrorPropagates(t *testing.T) {
	repo := &erroringListRepo{}
	err := Upsert(context.Background(), repo, nil)
	if err == nil {
		t.Fatal("want an error when List fails")
	}
	if repo.upsertAllCalled {
		t.Fatal("UpsertAll must not be called when List fails")
	}
}

type erroringListRepo struct {
	upsertAllCalled bool
}

func (r *erroringListRepo) List(_ context.Context) ([]domain.Species, error) {
	return nil, errListFailed
}

func (r *erroringListRepo) UpsertAll(_ context.Context, _ []domain.Species) error {
	r.upsertAllCalled = true
	return nil
}

type errListFailedType struct{}

func (errListFailedType) Error() string { return "list failed" }

var errListFailed error = errListFailedType{}
