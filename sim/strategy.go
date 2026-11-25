package sim

import (
	"fmt"
	"math"
	"math/rand"

	"github.com/awhsnap/market-sim/engine"
)

// StrategyKind names a built-in participant logic.
type StrategyKind string

const (
	// Random places orders on a random side around the current price.
	Random StrategyKind = "random"
	// Momentum buys when the short moving average is above the long one
	// and sells when it is below.
	Momentum StrategyKind = "momentum"
	// MeanReversion buys when price is below its long moving average and
	// sells when it is above.
	MeanReversion StrategyKind = "meanrev"
)

// StrategyKinds lists the built-in strategies.
var StrategyKinds = []StrategyKind{Random, Momentum, MeanReversion}

// Params tune a participant's behaviour. Zero fields take defaults.
type Params struct {
	// Activity is the chance of acting on a tick, 0 to 1.
	Activity float64 `json:"activity"`
	// Aggression is the chance an order is a market order rather than a limit.
	Aggression float64 `json:"aggression"`
	// MaxQty caps the size of a single order.
	MaxQty int64 `json:"maxQty"`
	// Spread is how far from the reference price limit orders are placed,
	// as a fraction (0.002 = 0.2%).
	Spread float64 `json:"spread"`
	// Threshold is the signal strength needed to trade for momentum and
	// mean reversion, as a fraction of price.
	Threshold float64 `json:"threshold"`
	// Lookback is the long moving-average window, in ticks.
	Lookback int `json:"lookback"`
	// OrderTTL cancels resting orders older than this many ticks.
	OrderTTL int64 `json:"orderTTL"`
}

// DefaultParams returns sensible defaults for a strategy.
func DefaultParams(k StrategyKind) Params {
	p := Params{Activity: 0.5, Aggression: 0.2, MaxQty: 10, Spread: 0.003, Threshold: 0.002, Lookback: 20, OrderTTL: 20}
	switch k {
	case Momentum:
		p.Activity, p.Aggression, p.Threshold = 0.4, 0.5, 0.001
	case MeanReversion:
		p.Activity, p.Aggression, p.Threshold, p.Lookback = 0.5, 0.3, 0.004, 50
	}
	return p
}

// withDefaults fills unset fields. An all-zero Params takes every default;
// otherwise Aggression is kept as given, since zero (limit orders only) is
// a meaningful choice.
func (p Params) withDefaults(k StrategyKind) Params {
	d := DefaultParams(k)
	if p == (Params{}) {
		return d
	}
	if p.Activity <= 0 {
		p.Activity = d.Activity
	}
	if p.Aggression < 0 {
		p.Aggression = 0
	}
	if p.MaxQty <= 0 {
		p.MaxQty = d.MaxQty
	}
	if p.Spread <= 0 {
		p.Spread = d.Spread
	}
	if p.Threshold <= 0 {
		p.Threshold = d.Threshold
	}
	if p.Lookback < 2 {
		p.Lookback = d.Lookback
	}
	if p.OrderTTL <= 0 {
		p.OrderTTL = d.OrderTTL
	}
	p.Activity = math.Min(p.Activity, 1)
	p.Aggression = math.Min(p.Aggression, 1)
	return p
}

// View is what a participant can see when deciding. Everything in it comes
// from trading activity: there is no hidden "true" price.
type View struct {
	Tick       int64
	Mark       int64 // last trade, else mid, else the starting price
	BestBid    int64 // 0 when empty
	BestAsk    int64 // 0 when empty
	History    []int64
	Permission engine.Permission
	AvailCash  int64
	AvailShare int64
}

// Strategy turns a view into orders.
type Strategy interface {
	Kind() StrategyKind
	Decide(v View, p Params, rng *rand.Rand) []engine.OrderRequest
}

// NewStrategy returns the built-in strategy for k.
func NewStrategy(k StrategyKind) (Strategy, error) {
	switch k {
	case Random:
		return randomStrategy{}, nil
	case Momentum:
		return momentumStrategy{}, nil
	case MeanReversion:
		return meanRevStrategy{}, nil
	}
	return nil, fmt.Errorf("unknown strategy %q", k)
}

type randomStrategy struct{}

func (randomStrategy) Kind() StrategyKind { return Random }

func (randomStrategy) Decide(v View, p Params, rng *rand.Rand) []engine.OrderRequest {
	if rng.Float64() >= p.Activity {
		return nil
	}
	side := engine.Buy
	if rng.Intn(2) == 0 {
		side = engine.Sell
	}
	if !v.Permission.Allows(side) {
		side = side.Opposite()
	}
	return order(v, p, rng, side, v.ref(), rng.Float64() < p.Aggression)
}

type momentumStrategy struct{}

func (momentumStrategy) Kind() StrategyKind { return Momentum }

func (momentumStrategy) Decide(v View, p Params, rng *rand.Rand) []engine.OrderRequest {
	if len(v.History) < p.Lookback || rng.Float64() >= p.Activity {
		return nil
	}
	long := mean(v.History[len(v.History)-p.Lookback:])
	short := mean(v.History[len(v.History)-max(p.Lookback/4, 1):])
	if long == 0 {
		return nil
	}
	signal := (short - long) / long
	switch {
	case signal > p.Threshold:
		return order(v, p, rng, engine.Buy, v.ref(), rng.Float64() < p.Aggression)
	case signal < -p.Threshold:
		return order(v, p, rng, engine.Sell, v.ref(), rng.Float64() < p.Aggression)
	}
	return nil
}

type meanRevStrategy struct{}

func (meanRevStrategy) Kind() StrategyKind { return MeanReversion }

func (meanRevStrategy) Decide(v View, p Params, rng *rand.Rand) []engine.OrderRequest {
	if len(v.History) < p.Lookback || rng.Float64() >= p.Activity {
		return nil
	}
	avg := mean(v.History[len(v.History)-p.Lookback:])
	if avg == 0 {
		return nil
	}
	gap := (avg - float64(v.Mark)) / avg
	// The further price is from its average, the more likely to cross the spread.
	aggressive := rng.Float64() < math.Min(1, p.Aggression*math.Abs(gap)/p.Threshold)
	ref := int64(math.Round(avg))
	switch {
	case gap > p.Threshold:
		return order(v, p, rng, engine.Buy, min(ref, v.ref()), aggressive)
	case gap < -p.Threshold:
		return order(v, p, rng, engine.Sell, max(ref, v.ref()), aggressive)
	}
	return nil
}

// ref is the price participants quote around: the book mid when both
// sides exist, otherwise the mark.
func (v View) ref() int64 {
	if v.BestBid > 0 && v.BestAsk > 0 {
		return (v.BestBid + v.BestAsk) / 2
	}
	return v.Mark
}

// order builds one market or limit order on side, sized randomly up to
// MaxQty and capped by what the participant can afford or deliver. Limit
// orders sit a random distance (up to Spread) on the passive side of ref.
func order(v View, p Params, rng *rand.Rand, side engine.Side, ref int64, market bool) []engine.OrderRequest {
	if !v.Permission.Allows(side) || ref <= 0 {
		return nil
	}
	qty := 1 + rng.Int63n(p.MaxQty)
	r := engine.OrderRequest{Side: side, Type: engine.Limit}

	price := ref
	if market {
		r.Type = engine.Market
		// Size a market buy against the worst price we expect to pay.
		if v.BestAsk > 0 {
			price = v.BestAsk
		}
	} else {
		off := 1 - rng.Float64()*p.Spread
		if side == engine.Sell {
			off = 1 + rng.Float64()*p.Spread
		}
		price = max(1, int64(math.Round(float64(ref)*off)))
		r.Price = price
	}

	if side == engine.Buy {
		qty = min(qty, v.AvailCash/max(price, 1))
	} else {
		qty = min(qty, v.AvailShare)
	}
	if qty <= 0 {
		return nil
	}
	r.Qty = qty
	return []engine.OrderRequest{r}
}

func mean(xs []int64) float64 {
	if len(xs) == 0 {
		return 0
	}
	var s int64
	for _, x := range xs {
		s += x
	}
	return float64(s) / float64(len(xs))
}
