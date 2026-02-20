// Traffic pattern implementations for synthetic load generation
// Provides uniform, diurnal, poisson, bursty, and custom arrival rate models
package synth

import (
	"cmp"
	"fmt"
	"math"
	"slices"
	"time"

)

const (
	defaultBurstMultiplier  = 5.0
	defaultBurstInterval    = 5 * time.Minute
	defaultBurstDuration    = 30 * time.Second
	defaultPeakMultiplier   = 1.5
	defaultTroughMultiplier = 0.5
	defaultDiurnalPeriod    = 24 * time.Hour
)

// TrafficPattern determines the trace generation rate at any given elapsed time.
type TrafficPattern interface {
	Rate(elapsed time.Duration) float64 // traces per second
}

// NewTrafficPattern creates a TrafficPattern from configuration.
func NewTrafficPattern(cfg TrafficConfig) (TrafficPattern, error) {
	base, err := newBasePattern(cfg)
	if err != nil {
		return nil, err
	}

	if cfg.Overlay == nil {
		return base, nil
	}

	overlayPattern, err := newBasePattern(*cfg.Overlay)
	if err != nil {
		return nil, fmt.Errorf("overlay: %w", err)
	}

	overlayRate, err := ParseRate(cfg.Overlay.Rate)
	if err != nil {
		return nil, fmt.Errorf("overlay: invalid traffic rate: %w", err)
	}
	overlayBaseRate := float64(overlayRate.Count()) / overlayRate.Period().Seconds()

	return &compositePattern{
		Base:            base,
		Overlay:         overlayPattern,
		OverlayBaseRate: overlayBaseRate,
	}, nil
}

func newBasePattern(cfg TrafficConfig) (TrafficPattern, error) {
	rate, err := ParseRate(cfg.Rate)
	if err != nil {
		return nil, fmt.Errorf("invalid traffic rate: %w", err)
	}

	baseRate := float64(rate.Count()) / rate.Period().Seconds()

	pattern := cfg.Pattern
	if pattern == "" {
		pattern = "uniform"
	}

	switch pattern {
	case "uniform":
		return &UniformPattern{BaseRate: baseRate}, nil
	case "diurnal":
		return newDiurnalPattern(baseRate, cfg)
	case "poisson":
		return &PoissonPattern{BaseRate: baseRate}, nil
	case "bursty":
		return newBurstyPattern(baseRate, cfg)
	case "custom":
		return newCustomPattern(baseRate, cfg)
	default:
		return nil, fmt.Errorf("unknown traffic pattern %q, supported: uniform, diurnal, poisson, bursty, custom", pattern)
	}
}

func newBurstyPattern(baseRate float64, cfg TrafficConfig) (*BurstyPattern, error) {
	multiplier := defaultBurstMultiplier
	if cfg.BurstMultiplier != 0 {
		if cfg.BurstMultiplier < 0 {
			return nil, fmt.Errorf("burst_multiplier must be positive, got %g", cfg.BurstMultiplier)
		}
		multiplier = cfg.BurstMultiplier
	}

	interval := defaultBurstInterval
	if cfg.BurstInterval != "" {
		d, err := time.ParseDuration(cfg.BurstInterval)
		if err != nil {
			return nil, fmt.Errorf("invalid burst_interval %q: %w", cfg.BurstInterval, err)
		}
		interval = d
	}
	if interval <= 0 {
		return nil, fmt.Errorf("burst_interval must be positive, got %s", interval)
	}

	duration := defaultBurstDuration
	if cfg.BurstDuration != "" {
		d, err := time.ParseDuration(cfg.BurstDuration)
		if err != nil {
			return nil, fmt.Errorf("invalid burst_duration %q: %w", cfg.BurstDuration, err)
		}
		duration = d
	}
	if duration <= 0 {
		return nil, fmt.Errorf("burst_duration must be positive, got %s", duration)
	}
	if duration >= interval {
		return nil, fmt.Errorf("burst_duration (%s) must be less than burst_interval (%s)", duration, interval)
	}

	return &BurstyPattern{
		BaseRate:        baseRate,
		BurstMultiplier: multiplier,
		BurstInterval:   interval,
		BurstDuration:   duration,
	}, nil
}

func newDiurnalPattern(baseRate float64, cfg TrafficConfig) (*DiurnalPattern, error) {
	peak := defaultPeakMultiplier
	if cfg.PeakMultiplier != 0 {
		if cfg.PeakMultiplier < 0 {
			return nil, fmt.Errorf("peak_multiplier must be positive, got %g", cfg.PeakMultiplier)
		}
		peak = cfg.PeakMultiplier
	}

	trough := defaultTroughMultiplier
	if cfg.TroughMultiplier != 0 {
		if cfg.TroughMultiplier < 0 {
			return nil, fmt.Errorf("trough_multiplier must not be negative, got %g", cfg.TroughMultiplier)
		}
		trough = cfg.TroughMultiplier
	}

	if peak < trough {
		return nil, fmt.Errorf("peak_multiplier (%g) must be >= trough_multiplier (%g)", peak, trough)
	}

	period := defaultDiurnalPeriod
	if cfg.Period != "" {
		d, err := time.ParseDuration(cfg.Period)
		if err != nil {
			return nil, fmt.Errorf("invalid period %q: %w", cfg.Period, err)
		}
		period = d
	}
	if period <= 0 {
		return nil, fmt.Errorf("period must be positive, got %s", period)
	}

	return &DiurnalPattern{
		BaseRate:         baseRate,
		PeakMultiplier:   peak,
		TroughMultiplier: trough,
		Period:           period,
	}, nil
}

// UniformPattern generates a constant rate.
type UniformPattern struct {
	BaseRate float64
}

func (p *UniformPattern) Rate(_ time.Duration) float64 {
	return p.BaseRate
}

// DiurnalPattern models a day/night cycle using a sine wave oscillating between
// trough and peak multipliers over a configurable period.
type DiurnalPattern struct {
	BaseRate         float64
	PeakMultiplier   float64
	TroughMultiplier float64
	Period           time.Duration
}

func (p *DiurnalPattern) Rate(elapsed time.Duration) float64 {
	mid := (p.PeakMultiplier + p.TroughMultiplier) / 2
	amplitude := (p.PeakMultiplier - p.TroughMultiplier) / 2
	periodHours := p.Period.Hours()
	hours := elapsed.Hours()
	factor := mid + amplitude*math.Sin(2*math.Pi*(hours-periodHours/4)/periodHours)
	return p.BaseRate * factor
}

// PoissonPattern generates a constant mean rate (Poisson arrivals are modelled
// in the engine's scheduling, not in the rate curve).
type PoissonPattern struct {
	BaseRate float64
}

func (p *PoissonPattern) Rate(_ time.Duration) float64 {
	return p.BaseRate
}

// BurstyPattern alternates between a base rate and periodic high-rate bursts.
type BurstyPattern struct {
	BaseRate        float64
	BurstMultiplier float64
	BurstInterval   time.Duration
	BurstDuration   time.Duration
}

func (p *BurstyPattern) Rate(elapsed time.Duration) float64 {
	cyclePos := elapsed % p.BurstInterval
	if cyclePos < p.BurstDuration {
		return p.BaseRate * p.BurstMultiplier
	}
	return p.BaseRate
}

// segment is a parsed time-bounded rate used by customPattern.
type segment struct {
	Until time.Duration
	Rate  float64
}

// customPattern steps through explicit time segments, falling back to the base
// rate once elapsed time exceeds all defined segments.
type customPattern struct {
	BaseRate float64
	Segments []segment
}

func (p *customPattern) Rate(elapsed time.Duration) float64 {
	for _, seg := range p.Segments {
		if elapsed < seg.Until {
			return seg.Rate
		}
	}
	return p.BaseRate
}

// compositePattern layers an overlay pattern on top of a base pattern. The
// overlay modulates the base rate by the ratio of overlay rate to overlay base.
type compositePattern struct {
	Base            TrafficPattern
	Overlay         TrafficPattern
	OverlayBaseRate float64
}

func (p *compositePattern) Rate(elapsed time.Duration) float64 {
	if p.OverlayBaseRate == 0 {
		return p.Base.Rate(elapsed)
	}
	factor := p.Overlay.Rate(elapsed) / p.OverlayBaseRate
	return p.Base.Rate(elapsed) * factor
}

func newCustomPattern(baseRate float64, cfg TrafficConfig) (*customPattern, error) {
	if len(cfg.Segments) == 0 {
		return nil, fmt.Errorf("custom pattern requires at least one segment in segments")
	}

	segments := make([]segment, 0, len(cfg.Segments))
	for i, sc := range cfg.Segments {
		until, err := time.ParseDuration(sc.Until)
		if err != nil {
			return nil, fmt.Errorf("segment %d: invalid until %q: %w", i, sc.Until, err)
		}

		rate, err := ParseRate(sc.Rate)
		if err != nil {
			return nil, fmt.Errorf("segment %d: invalid rate %q: %w", i, sc.Rate, err)
		}

		segments = append(segments, segment{
			Until: until,
			Rate:  float64(rate.Count()) / rate.Period().Seconds(),
		})
	}

	slices.SortFunc(segments, func(a, b segment) int {
		return cmp.Compare(a.Until, b.Until)
	})

	for i := 1; i < len(segments); i++ {
		if segments[i].Until == segments[i-1].Until {
			return nil, fmt.Errorf("duplicate segment until value %s", segments[i].Until)
		}
	}

	return &customPattern{
		BaseRate: baseRate,
		Segments: segments,
	}, nil
}
