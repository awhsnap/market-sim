// Package sim drives the engine: participants with their own trading
// logic and a tick loop that can be played, paused and sped up while
// participants are added or removed. There is no external price feed: the
// price is whatever participants and the market maker trade at.
package sim

import (
	"context"
	"errors"
	"math/rand"
	"sync"
	"time"

	"github.com/awhsnap/market-sim/engine"
)

// Config configures a Simulator.
type Config struct {
	Seed       int64         // 0 picks a time-based seed
	StartPrice int64         // cents; opening reference price, default 10000 ($100.00)
	Interval   time.Duration // time between ticks when running; default 250ms
	HistoryLen int           // ticks of price history kept; default 5000
}

func (c Config) withDefaults() Config {
	if c.Seed == 0 {
		c.Seed = time.Now().UnixNano()
	}
	if c.StartPrice <= 0 {
		c.StartPrice = 10_000
	}
	if c.Interval <= 0 {
		c.Interval = 250 * time.Millisecond
	}
	if c.HistoryLen <= 0 {
		c.HistoryLen = 5000
	}
	return c
}

// ParticipantConfig describes a participant to add.
type ParticipantConfig struct {
	Name       string            `json:"name"`
	Strategy   StrategyKind      `json:"strategy"`
	Permission engine.Permission `json:"permission"`
	Cash       int64             `json:"cash"`   // cents
	Shares     int64             `json:"shares"` // starting inventory
	Params     Params            `json:"params"`
}

// Participant is a trader with its own engine account and strategy.
type Participant struct {
	ID        int64 // engine account ID
	Name      string
	Strategy  Strategy
	Params    Params
	Paused    bool
	placedAt  map[int64]int64 // open order ID -> tick placed
	Submitted int64
	Rejected  int64
}

// ParticipantInfo is a read-only snapshot of a participant.
type ParticipantInfo struct {
	ID         int64             `json:"id"`
	Name       string            `json:"name"`
	Strategy   StrategyKind      `json:"strategy"`
	Permission engine.Permission `json:"permission"`
	Params     Params            `json:"params"`
	Paused     bool              `json:"paused"`
	Submitted  int64             `json:"submitted"`
	Rejected   int64             `json:"rejected"`
}

// Point is one tick of market history.
type Point struct {
	Tick    int64     `json:"tick"`
	Time    time.Time `json:"time"`
	Open    int64     `json:"open"` // first trade this tick, else previous close
	High    int64     `json:"high"`
	Low     int64     `json:"low"`
	Close   int64     `json:"close"` // mark at end of tick
	BestBid int64     `json:"bestBid"`
	BestAsk int64     `json:"bestAsk"`
	Volume  int64     `json:"volume"`
	Trades  int       `json:"trades"`
}

// ErrUnknownParticipant is returned for an ID that is not a participant.
var ErrUnknownParticipant = errors.New("unknown participant")

// Simulator owns an engine, the market maker account and the participants.
type Simulator struct {
	mu sync.Mutex

	cfg          Config
	eng          *engine.Engine
	mmID         int64
	rng          *rand.Rand
	tick         int64
	participants []*Participant
	history      []Point
	lastTradeID  int64
	running      bool
	interval     time.Duration

	subMu sync.Mutex
	subs  map[chan Point]struct{}
}

// New creates a simulator with an empty book and a market maker account.
func New(cfg Config) *Simulator {
	cfg = cfg.withDefaults()
	eng := engine.New(engine.Config{ReferencePrice: cfg.StartPrice})
	s := &Simulator{
		cfg:      cfg,
		eng:      eng,
		rng:      rand.New(rand.NewSource(cfg.Seed)),
		interval: cfg.Interval,
		subs:     make(map[chan Point]struct{}),
	}
	s.mmID = eng.AddAccount(engine.AccountConfig{Name: "Market Maker", Unlimited: true})
	return s
}

// Engine returns the underlying exchange, for placing market maker orders
// and reading the book.
func (s *Simulator) Engine() *engine.Engine { return s.eng }

// MarketMakerID is the engine account the user trades as.
func (s *Simulator) MarketMakerID() int64 { return s.mmID }

// AddParticipant creates an account and starts trading it on the next tick.
func (s *Simulator) AddParticipant(c ParticipantConfig) (ParticipantInfo, error) {
	if c.Strategy == "" {
		c.Strategy = Random
	}
	strat, err := NewStrategy(c.Strategy)
	if err != nil {
		return ParticipantInfo{}, err
	}
	if c.Cash < 0 || c.Shares < 0 {
		return ParticipantInfo{}, errors.New("cash and shares must not be negative")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if c.Name == "" {
		c.Name = string(c.Strategy)
	}
	id := s.eng.AddAccount(engine.AccountConfig{
		Name:         c.Name,
		Cash:         c.Cash,
		Shares:       c.Shares,
		Permission:   c.Permission,
		OpeningPrice: s.eng.Mark(),
	})
	p := &Participant{
		ID:       id,
		Name:     c.Name,
		Strategy: strat,
		Params:   c.Params.withDefaults(c.Strategy),
		placedAt: make(map[int64]int64),
	}
	s.participants = append(s.participants, p)
	return s.infoLocked(p), nil
}

// RemoveParticipant cancels the participant's orders and removes it.
func (s *Simulator) RemoveParticipant(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.participants {
		if p.ID == id {
			s.participants = append(s.participants[:i], s.participants[i+1:]...)
			return s.eng.RemoveAccount(id)
		}
	}
	return ErrUnknownParticipant
}

// ParticipantUpdate changes a participant. Nil fields are left alone.
type ParticipantUpdate struct {
	Strategy   *StrategyKind      `json:"strategy,omitempty"`
	Permission *engine.Permission `json:"permission,omitempty"`
	Params     *Params            `json:"params,omitempty"`
	Paused     *bool              `json:"paused,omitempty"`
}

// UpdateParticipant applies u to participant id while the sim runs.
func (s *Simulator) UpdateParticipant(id int64, u ParticipantUpdate) (ParticipantInfo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	p := s.findLocked(id)
	if p == nil {
		return ParticipantInfo{}, ErrUnknownParticipant
	}
	if u.Strategy != nil {
		strat, err := NewStrategy(*u.Strategy)
		if err != nil {
			return ParticipantInfo{}, err
		}
		p.Strategy = strat
		if u.Params == nil {
			p.Params = p.Params.withDefaults(strat.Kind())
		}
	}
	if u.Params != nil {
		p.Params = u.Params.withDefaults(p.Strategy.Kind())
	}
	if u.Permission != nil {
		if err := s.eng.SetPermission(id, *u.Permission); err != nil {
			return ParticipantInfo{}, err
		}
	}
	if u.Paused != nil {
		p.Paused = *u.Paused
		if p.Paused {
			s.eng.CancelAll(id)
			clear(p.placedAt)
		}
	}
	return s.infoLocked(p), nil
}

// Participants returns snapshots of all participants.
func (s *Simulator) Participants() []ParticipantInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ParticipantInfo, 0, len(s.participants))
	for _, p := range s.participants {
		out = append(out, s.infoLocked(p))
	}
	return out
}

func (s *Simulator) findLocked(id int64) *Participant {
	for _, p := range s.participants {
		if p.ID == id {
			return p
		}
	}
	return nil
}

func (s *Simulator) infoLocked(p *Participant) ParticipantInfo {
	info := ParticipantInfo{
		ID: p.ID, Name: p.Name, Strategy: p.Strategy.Kind(), Params: p.Params,
		Paused: p.Paused, Submitted: p.Submitted, Rejected: p.Rejected,
	}
	if a, err := s.eng.Account(p.ID); err == nil {
		info.Permission = a.Permission
	}
	return info
}

// Step advances one tick: stale participant orders are cancelled, each active participant decides in random order, and a
// history point is recorded and published.
func (s *Simulator) Step() Point {
	s.mu.Lock()
	s.tick++
	history := s.closesLocked(200)

	order := s.rng.Perm(len(s.participants))
	for _, i := range order {
		p := s.participants[i]
		if p.Paused {
			continue
		}
		s.expireLocked(p)
		acct, err := s.eng.Account(p.ID)
		if err != nil {
			continue
		}
		bb, _ := s.eng.BestBid()
		ba, _ := s.eng.BestAsk()
		v := View{
			Tick:       s.tick,
			Mark:       s.eng.Mark(),
			BestBid:    bb,
			BestAsk:    ba,
			History:    history,
			Permission: acct.Permission,
			AvailCash:  acct.AvailableCash(),
			AvailShare: acct.AvailableShares(),
		}
		for _, r := range p.Strategy.Decide(v, p.Params, s.rng) {
			r.AccountID = p.ID
			p.Submitted++
			o, _, err := s.eng.Submit(r)
			if err != nil {
				p.Rejected++
				continue
			}
			if o.Status == engine.Open || o.Status == engine.PartiallyFilled {
				p.placedAt[o.ID] = s.tick
			}
		}
	}

	pt := s.recordLocked()
	s.mu.Unlock()
	s.publish(pt)
	return pt
}

// expireLocked cancels a participant's orders older than its OrderTTL and
// forgets orders that have since filled.
func (s *Simulator) expireLocked(p *Participant) {
	for id, placed := range p.placedAt {
		if s.tick-placed < p.Params.OrderTTL {
			continue
		}
		s.eng.Cancel(p.ID, id) // already filled or cancelled is fine
		delete(p.placedAt, id)
	}
}

func (s *Simulator) recordLocked() Point {
	trades := s.eng.TradesSince(s.lastTradeID)
	mark := s.eng.Mark()
	prevClose := mark
	if n := len(s.history); n > 0 {
		prevClose = s.history[n-1].Close
	}
	pt := Point{
		Tick: s.tick, Time: time.Now(),
		Open: prevClose, High: prevClose, Low: prevClose, Close: mark,
		Trades: len(trades),
	}
	if len(trades) > 0 {
		pt.Open, pt.High, pt.Low = trades[0].Price, trades[0].Price, trades[0].Price
		for _, t := range trades {
			pt.High = max(pt.High, t.Price)
			pt.Low = min(pt.Low, t.Price)
			pt.Volume += t.Qty
		}
		s.lastTradeID = trades[len(trades)-1].ID
	}
	pt.High = max(pt.High, pt.Close)
	pt.Low = min(pt.Low, pt.Close)
	pt.BestBid, _ = s.eng.BestBid()
	pt.BestAsk, _ = s.eng.BestAsk()

	s.history = append(s.history, pt)
	if over := len(s.history) - s.cfg.HistoryLen; over > 0 {
		s.history = append(s.history[:0:0], s.history[over:]...)
	}
	return pt
}

func (s *Simulator) closesLocked(n int) []int64 {
	start := max(len(s.history)-n, 0)
	out := make([]int64, 0, len(s.history)-start)
	for _, p := range s.history[start:] {
		out = append(out, p.Close)
	}
	return out
}

// History returns up to n most recent points, oldest first. n <= 0 returns all.
func (s *Simulator) History(n int) []Point {
	s.mu.Lock()
	defer s.mu.Unlock()
	start := 0
	if n > 0 && n < len(s.history) {
		start = len(s.history) - n
	}
	return append([]Point(nil), s.history[start:]...)
}

// Tick returns the current tick number.
func (s *Simulator) Tick() int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.tick
}

// Play starts ticking when Run is active.
func (s *Simulator) Play() { s.setRunning(true) }

// Pause stops ticking; Step still works for single-stepping.
func (s *Simulator) Pause() { s.setRunning(false) }

func (s *Simulator) setRunning(v bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.running = v
}

// Running reports whether the sim is playing.
func (s *Simulator) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// SetInterval changes the time between ticks (minimum 10ms).
func (s *Simulator) SetInterval(d time.Duration) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.interval = max(d, 10*time.Millisecond)
}

// Interval returns the time between ticks.
func (s *Simulator) Interval() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.interval
}

// Run ticks while playing until ctx is done.
func (s *Simulator) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(s.Interval()):
			if s.Running() {
				s.Step()
			}
		}
	}
}

// Subscribe returns a channel that receives every new point and a func to
// stop. Slow subscribers miss points rather than block the sim.
func (s *Simulator) Subscribe() (<-chan Point, func()) {
	ch := make(chan Point, 64)
	s.subMu.Lock()
	s.subs[ch] = struct{}{}
	s.subMu.Unlock()
	return ch, func() {
		s.subMu.Lock()
		if _, ok := s.subs[ch]; ok {
			delete(s.subs, ch)
			close(ch)
		}
		s.subMu.Unlock()
	}
}

func (s *Simulator) publish(p Point) {
	s.subMu.Lock()
	defer s.subMu.Unlock()
	for ch := range s.subs {
		select {
		case ch <- p:
		default:
		}
	}
}

// DefaultParticipants is a starter population: noise traders on both
// sides, a trend follower, a mean reverter, and one buy-only and one
// sell-only account.
func DefaultParticipants(price int64) []ParticipantConfig {
	cash := 100 * price // enough for about 100 shares
	return []ParticipantConfig{
		{Name: "Noise 1", Strategy: Random, Cash: cash, Shares: 50},
		{Name: "Noise 2", Strategy: Random, Cash: cash, Shares: 50},
		{Name: "Noise 3", Strategy: Random, Cash: cash, Shares: 50},
		{Name: "Trend", Strategy: Momentum, Cash: cash, Shares: 50},
		{Name: "Mean Reversion", Strategy: MeanReversion, Cash: cash, Shares: 50},
		{Name: "Accumulator", Strategy: Random, Permission: engine.BuyOnly, Cash: cash},
		{Name: "Distributor", Strategy: Random, Permission: engine.SellOnly, Shares: 100},
	}
}
