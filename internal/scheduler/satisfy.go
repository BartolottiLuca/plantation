package scheduler

import (
	"github.com/BartolottiLuca/plantation/internal/climate/tado"
	"github.com/BartolottiLuca/plantation/internal/notify"
	"github.com/BartolottiLuca/plantation/internal/store"
	"github.com/BartolottiLuca/plantation/internal/weather"
)

var (
	_ Locker         = PoolLocker{}
	_ WeatherSource  = (*weather.Service)(nil)
	_ WeatherAge     = (*store.WeatherRepo)(nil)
	_ Sampler        = (*tado.Sampler)(nil)
	_ TokenRepo      = (*store.TadoTokenRepo)(nil)
	_ TokenRefresher = (*tado.Client)(nil)
	_ PlantLister    = (*store.PlantRepo)(nil)
	_ EventSource    = (*store.CareEventRepo)(nil)
	_ TaskLister     = (*store.CareTaskRepo)(nil)
	_ ClimateSource  = (*store.ClimateRepo)(nil)
	_ Notifier       = (*notify.OutboxNotifier)(nil)
	_ Sweeper        = (*notify.OutboxNotifier)(nil)
)
