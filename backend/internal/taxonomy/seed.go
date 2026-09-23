package taxonomy

import (
	"context"
	"errors"
	"fmt"
	"log"
)

// SeedData is the Irish-agriculture starter taxonomy (ticket 06: "seeded
// for Irish agriculture — counties; dairy/beef/tillage profile"). Curated
// keywords make the classifier useful from the first upload: an Irish herd
// registry mentioning "Friesian" or "Teagasc" lands in the right facets
// without the admin hand-tuning the vocabulary.
var SeedData = []Term{
	// Crop / livestock profile: the dairy, beef, and tillage spine of
	// Irish farming (Teagasc's three dominant systems).
	{Category: CategoryCrop, Value: "dairy", Label: "Dairy", Keywords: []string{"dairy", "milk", "herd", "friesian", "holstein", "milking", "creamery"}},
	{Category: CategoryCrop, Value: "beef", Label: "Beef", Keywords: []string{"beef", "angus", "hereford", "suckler", "weanling", "store cattle"}},
	{Category: CategoryCrop, Value: "tillage", Label: "Tillage", Keywords: []string{"tillage", "cereal", "wheat", "barley", "oats", "spring barley", "winter wheat"}},
	{Category: CategoryCrop, Value: "sheep", Label: "Sheep", Keywords: []string{"sheep", "lamb", "ewe", "flock", "hogget"}},
	{Category: CategoryCrop, Value: "horticulture", Label: "Horticulture", Keywords: []string{"horticulture", "vegetable", "fruit", "potato", "glasshouse"}},

	// Region: the eight counties of the pilot region. Values are the
	// slug forms the catalog facets filter on.
	{Category: CategoryRegion, Value: "co-galway", Label: "County Galway", Keywords: []string{"galway"}},
	{Category: CategoryRegion, Value: "co-mayo", Label: "County Mayo", Keywords: []string{"mayo"}},
	{Category: CategoryRegion, Value: "co-clare", Label: "County Clare", Keywords: []string{"clare"}},
	{Category: CategoryRegion, Value: "co-cork", Label: "County Cork", Keywords: []string{"cork"}},
	{Category: CategoryRegion, Value: "co-kerry", Label: "County Kerry", Keywords: []string{"kerry"}},
	{Category: CategoryRegion, Value: "co-tipperary", Label: "County Tipperary", Keywords: []string{"tipperary"}},
	{Category: CategoryRegion, Value: "co-kilkenny", Label: "County Kilkenny", Keywords: []string{"kilkenny"}},
	{Category: CategoryRegion, Value: "co-donegal", Label: "County Donegal", Keywords: []string{"donegal"}},

	// Growth stage: the grass and crop calendar's shared vocabulary.
	{Category: CategoryGrowthStage, Value: "establishment", Label: "Establishment", Keywords: []string{"sowing", "planting", "drilling", "germination"}},
	{Category: CategoryGrowthStage, Value: "vegetative", Label: "Vegetative", Keywords: []string{"vegetative", "tillering", "leaf expansion", "regrowth"}},
	{Category: CategoryGrowthStage, Value: "reproductive", Label: "Reproductive", Keywords: []string{"flowering", "heading", "ear emergence", "silage cutting"}},
	{Category: CategoryGrowthStage, Value: "harvest", Label: "Harvest", Keywords: []string{"harvest", "combining", "lifting"}},

	// Intervention: what was applied or done.
	{Category: CategoryIntervention, Value: "fertiliser", Label: "Fertiliser", Keywords: []string{"fertiliser", "fertilizer", "urea", "slurry", "manure", "npk"}},
	{Category: CategoryIntervention, Value: "pesticide", Label: "Pesticide", Keywords: []string{"pesticide", "herbicide", "fungicide", "insecticide", "spray"}},
	{Category: CategoryIntervention, Value: "grazing", Label: "Grazing", Keywords: []string{"grazing", "paddock", "rotation", "stocking rate", "grass cover"}},
	{Category: CategoryIntervention, Value: "irrigation", Label: "Irrigation", Keywords: []string{"irrigation", "watering", "rain gun"}},
	{Category: CategoryIntervention, Value: "veterinary", Label: "Veterinary", Keywords: []string{"veterinary", "vaccine", "drench", "tb test", "dosing"}},

	// Outcome: what was measured.
	{Category: CategoryOutcome, Value: "yield", Label: "Yield", Keywords: []string{"yield", "kg/ha", "tonnes", "litres", "milk yield"}},
	{Category: CategoryOutcome, Value: "quality", Label: "Quality", Keywords: []string{"quality", "protein", "butterfat", "starch", "dry matter"}},
	{Category: CategoryOutcome, Value: "soil-health", Label: "Soil health", Keywords: []string{"soil", "soil sample", "organic matter", "compaction", "drainage"}},
	{Category: CategoryOutcome, Value: "biodiversity", Label: "Biodiversity", Keywords: []string{"biodiversity", "species", "hedgerow", "pollinator", "bird"}},

	// Data type: what shape of data the asset holds.
	{Category: CategoryDataType, Value: "sensor", Label: "Sensor data", Keywords: []string{"sensor", "telemetry", "weather station"}},
	{Category: CategoryDataType, Value: "survey", Label: "Survey responses", Keywords: []string{"survey", "questionnaire", "interview"}},
	{Category: CategoryDataType, Value: "registry", Label: "Registry", Keywords: []string{"registry", "register", "herd book", "records"}},
	{Category: CategoryDataType, Value: "imagery", Label: "Imagery", Keywords: []string{"imagery", "satellite", "drone", "aerial", "photo"}},
	{Category: CategoryDataType, Value: "financial", Label: "Financial", Keywords: []string{"financial", "accounts", "costs", "invoices"}},
}

// SeedResult reports what one seeding pass did.
type SeedResult struct {
	Created int
	Skipped int
}

// Seed installs SeedData into the store idempotently: an existing
// (category, value) pair is skipped, so restarts and concurrent boots are
// no-ops — the same contract as the membership bootstrap.
func Seed(ctx context.Context, store Store) (SeedResult, error) {
	var res SeedResult
	for _, t := range SeedData {
		term := t // copy: CreateTerm mutates through the pointer
		err := store.CreateTerm(ctx, &term)
		switch {
		case err == nil:
			res.Created++
		case isExists(err):
			res.Skipped++
		default:
			return res, fmt.Errorf("taxonomy: seed %s/%s: %w", t.Category, t.Value, err)
		}
	}
	return res, nil
}

// SeedIfEmpty seeds only when the store holds no terms at all — the boot
// path for the pilot: first boot loads the vocabulary, later boots leave
// the admin's edits (new terms, removed defaults) alone.
func SeedIfEmpty(ctx context.Context, store Store) (SeedResult, error) {
	existing, err := store.Terms(ctx)
	if err != nil {
		return SeedResult{}, fmt.Errorf("taxonomy: check before seeding: %w", err)
	}
	if len(existing) > 0 {
		return SeedResult{Skipped: len(existing)}, nil
	}
	res, err := Seed(ctx, store)
	if err != nil {
		return SeedResult{}, err
	}
	if res.Created > 0 {
		log.Printf("taxonomy: seeded %d Irish-agriculture terms", res.Created)
	}
	return res, nil
}

// isExists reports whether err carries ErrExists (the postgres layer wraps
// uniqueness violations in it).
func isExists(err error) bool {
	return errors.Is(err, ErrExists)
}
