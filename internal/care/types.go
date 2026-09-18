package care

import (
	"time"

	"github.com/BartolottiLuca/plantation/internal/domain"
)

type Params struct {
	Location         domain.Location
	Kc               float64
	Substrate        domain.SubstrateKind
	MAD              float64
	BaseIntervalDays int
	MinIntervalDays  int
	MaxIntervalDays  int
	DormantMonths    []time.Month
	DormancyFactor   float64
	FExposure        float64
	FRain            float64
	InGround         bool
	PotDiameterMM    int
	AcquiredAt       *time.Time
}

type DayEnv struct {
	Date       domain.Date
	ET0MM      float64
	PrecipMM   float64
	PrecipProb float64
	TMinC      float64
	TMaxC      float64
	Observed   bool
	Estimated  bool
}

type IndoorDay struct {
	Date        domain.Date
	TempC       float64
	HumidityPct float64
}

type EnvSeries struct {
	Days            []DayEnv
	Indoor          []IndoorDay
	OutdoorStale    bool
	IndoorDataStale bool
}

type Status string

const (
	StatusOverdue  Status = "overdue"
	StatusDueToday Status = "due_today"
	StatusUpcoming Status = "upcoming"
)

type Due struct {
	Kind   domain.TaskKind
	On     domain.Date
	Status Status
}

const (
	ModeWaterBalance = "water_balance"
	ModeBaseInterval = "base_interval"
	ModeFixed        = "fixed"

	ClampMinInterval   = "min_interval"
	ClampMaxInterval   = "max_interval"
	ClampProjectionCap = "projection_cap"
)

type Explanation struct {
	Mode            string
	CapacityMM      float64
	DeficitMM       float64
	ThresholdMM     float64
	DepletionPct    float64
	MeanETcMMPerDay float64
	EffectiveRainMM float64
	Deferred        bool
	DeferredReason  string
	ClampedBy       string
	OutdoorStale    bool
	IndoorDataStale bool
	Summary         string
}
