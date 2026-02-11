// Traffic pattern implementations for synthetic load generation
// Provides uniform, diurnal, poisson, and bursty arrival rate models
package synth

import (
	"fmt"
	"math"
	"time"

	"github.com/andrewh/motel/pkg/models"
)

// TrafficPattern determines the trace generation rate at any given elapsed time.
type TrafficPattern interface {
	Rate(elapsed time.Duration) float64 // traces per second
}

// NewTrafficPattern creates a TrafficPattern from configuration.
func NewTrafficPattern(cfg TrafficConfig) (TrafficPattern, error) {
	rate, err := models.NewRate(cfg.Rate)
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
		return &DiurnalPattern{BaseRate: baseRate}, nil
	case "poisson":
		return &PoissonPattern{BaseRate: baseRate}, nil
	case "bursty":
		return &BurstyPattern{
			BaseRate:        baseRate,
			BurstMultiplier: 5,
			BurstInterval:   5 * time.Minute,
			BurstDuration:   30 * time.Second,
		}, nil
	default:
		return nil, fmt.Errorf("unknown traffic pattern %q, supported: uniform, diurnal, poisson, bursty", pattern)
	}
}

// UniformPattern generates a constant rate.
type UniformPattern struct {
	BaseRate float64
}

func (p *UniformPattern) Rate(_ time.Duration) float64 {
	return p.BaseRate
}

// DiurnalPattern models a 24-hour day/night cycle using a sine wave.
// Peak rate is 1.5x base, trough is 0.5x base, with period of 24 hours.
type DiurnalPattern struct {
	BaseRate float64
}

func (p *DiurnalPattern) Rate(elapsed time.Duration) float64 {
	// Sine wave: 0.5 * amplitude oscillation around the base rate
	// Period is 24 hours, phase shifted so peak is at the 12-hour mark (midday in simulated time)
	hours := elapsed.Hours()
	factor := 1.0 + 0.5*math.Sin(2*math.Pi*(hours-6)/24)
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
