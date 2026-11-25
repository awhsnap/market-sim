package sim

import (
	"context"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/awhsnap/market-sim/engine"
)

func newSim(t *testing.T) *Simulator {
	t.Helper()
	return New(Config{Seed: 42, StartPrice: 10_000})
}

func addDefaults(t *testing.T, s *Simulator) {
	t.Helper()
	for _, c := range DefaultParticipants(10_000) {
		if _, err := s.AddParticipant(c); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPriceComesFromTrading(t *testing.T) {
	s := newSim(t)
	addDefaults(t, s)
	for i := 0; i < 1000; i++ {
		s.Step()
	}
	h := s.History(0)
	if len(h) != 1000 {
		t.Fatalf("history len %d", len(h))
	}
	var trades int
	lo, hi := h[0].Close, h[0].Close
	for _, p := range h {
		trades += p.Trades
		lo, hi = min(lo, p.Low), max(hi, p.High)
		if p.Low > p.Open || p.Low > p.Close || p.High < p.Open || p.High < p.Close {
			t.Fatalf("bad candle %+v", p)
		}
	}
	if trades == 0 {
		t.Fatal("participants never traded")
	}
	if lo == hi {
		t.Fatal("price never moved")
	}
	// Every history close is a traded price or derived from the book, never
	// an exogenous value: the last close equals the engine mark.
	if h[len(h)-1].Close != s.Engine().Mark() {
		t.Errorf("last close %d != engine mark %d", h[len(h)-1].Close, s.Engine().Mark())
	}
	t.Logf("1000 ticks: %d trades, range %d..%d", trades, lo, hi)
}

func TestBuyPressureLiftsPrice(t *testing.T) {
	s := newSim(t)
	eng := s.Engine()
	mm := s.MarketMakerID()
	// Market maker ladder of offers from 100.00 to 104.90.
	for i := int64(0); i < 50; i++ {
		if _, _, err := eng.Submit(engine.OrderRequest{AccountID: mm, Side: engine.Sell, Type: engine.Limit, Price: 10_000 + i*10, Qty: 5}); err != nil {
			t.Fatal(err)
		}
	}
	_, err := s.AddParticipant(ParticipantConfig{
		Name: "buyer", Strategy: Random, Permission: engine.BuyOnly, Cash: 10_000_000,
		Params: Params{Activity: 1, Aggression: 1, MaxQty: 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 30; i++ {
		s.Step()
	}
	if last := eng.LastPrice(); last <= 10_000 {
		t.Errorf("buy-only market orders should lift price above 100.00, last = %d", last)
	}
	sum := eng.Summarize()
	if sum.MakerPos >= 0 {
		t.Errorf("market maker should be short after selling, pos = %d", sum.MakerPos)
	}
}

func TestParticipantsStayWithinLimits(t *testing.T) {
	s := newSim(t)
	addDefaults(t, s)
	for i := 0; i < 500; i++ {
		s.Step()
	}
	sum := s.Engine().Summarize()
	for _, p := range sum.Participants {
		if p.Cash < 0 || p.Position < 0 {
			t.Errorf("%s went negative: cash %d pos %d", p.Name, p.Cash, p.Position)
		}
	}
	if got := sum.ParticipantPnL + sum.MakerPnL; got != 0 {
		t.Errorf("market should be zero-sum, got %d", got)
	}
	// Buy-only and sell-only accounts only ever moved one way.
	for _, a := range s.Engine().Accounts() {
		switch a.Permission {
		case engine.BuyOnly:
			if a.Position() < a.StartShares {
				t.Errorf("%s is buy only but position fell to %d", a.Name, a.Position())
			}
		case engine.SellOnly:
			if a.Position() > a.StartShares || a.Cash < a.StartCash {
				t.Errorf("%s is sell only but bought: pos %d cash %d", a.Name, a.Position(), a.Cash)
			}
		}
	}
}

func TestStrategiesRespectPermission(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	hist := make([]int64, 100)
	for i := range hist {
		hist[i] = 10_000 + int64(i%7)*50 - int64(i)*3 // wiggly downtrend
	}
	for _, k := range StrategyKinds {
		strat, _ := NewStrategy(k)
		p := DefaultParams(k)
		p.Activity = 1
		for _, perm := range []engine.Permission{engine.BuyOnly, engine.SellOnly} {
			v := View{Mark: 9_700, BestBid: 9_690, BestAsk: 9_710, History: hist, Permission: perm, AvailCash: 1_000_000, AvailShare: 100}
			for i := 0; i < 200; i++ {
				for _, r := range strat.Decide(v, p, rng) {
					if !perm.Allows(r.Side) {
						t.Fatalf("%s with %s placed a %s", k, perm, r.Side)
					}
					if r.Qty <= 0 || (r.Type == engine.Limit && r.Price <= 0) {
						t.Fatalf("%s produced bad order %+v", k, r)
					}
				}
			}
		}
	}
}

func TestOrdersExpire(t *testing.T) {
	s := newSim(t)
	info, _ := s.AddParticipant(ParticipantConfig{
		Strategy: Random, Cash: 1_000_000, Shares: 100,
		Params: Params{Activity: 1, Aggression: 0, OrderTTL: 3, MaxQty: 1},
	})
	for i := 0; i < 20; i++ {
		s.Step()
	}
	if n := len(s.Engine().OpenOrders(info.ID)); n > 3 {
		t.Errorf("open orders %d, want at most TTL (3)", n)
	}
}

func TestUpdateParticipant(t *testing.T) {
	s := newSim(t)
	info, _ := s.AddParticipant(ParticipantConfig{Strategy: Random, Cash: 1_000_000, Shares: 100, Params: Params{Activity: 1}})
	for i := 0; i < 5; i++ {
		s.Step()
	}

	sellOnly := engine.SellOnly
	mom := Momentum
	got, err := s.UpdateParticipant(info.ID, ParticipantUpdate{Permission: &sellOnly, Strategy: &mom})
	if err != nil {
		t.Fatal(err)
	}
	if got.Permission != engine.SellOnly || got.Strategy != Momentum {
		t.Errorf("update not applied: %+v", got)
	}
	for _, o := range s.Engine().OpenOrders(info.ID) {
		if o.Side == engine.Buy {
			t.Errorf("buy order survived switch to sell only: %+v", o)
		}
	}

	paused := true
	if _, err := s.UpdateParticipant(info.ID, ParticipantUpdate{Paused: &paused}); err != nil {
		t.Fatal(err)
	}
	if n := len(s.Engine().OpenOrders(info.ID)); n != 0 {
		t.Errorf("pausing should cancel orders, %d left", n)
	}
	before := s.Participants()[0].Submitted
	s.Step()
	if s.Participants()[0].Submitted != before {
		t.Error("paused participant still trading")
	}

	bad := StrategyKind("nope")
	if _, err := s.UpdateParticipant(info.ID, ParticipantUpdate{Strategy: &bad}); err == nil {
		t.Error("expected error for unknown strategy")
	}
	if _, err := s.UpdateParticipant(999, ParticipantUpdate{}); err != ErrUnknownParticipant {
		t.Errorf("err = %v", err)
	}
}

func TestAddRemoveWhileRunning(t *testing.T) {
	s := New(Config{Seed: 7, Interval: time.Millisecond})
	s.SetInterval(time.Millisecond) // clamps to 10ms
	if s.Interval() != 10*time.Millisecond {
		t.Errorf("interval %v", s.Interval())
	}
	addDefaults(t, s)
	ch, stop := s.Subscribe()
	defer stop()

	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); s.Run(ctx) }()
	s.Play()

	for i := 0; i < 10; i++ {
		info, err := s.AddParticipant(ParticipantConfig{Strategy: StrategyKinds[i%3], Cash: 500_000, Shares: 20})
		if err != nil {
			t.Fatal(err)
		}
		<-ch // let at least one tick run with it
		if err := s.RemoveParticipant(info.ID); err != nil {
			t.Fatal(err)
		}
	}
	s.Pause()
	cancel()
	wg.Wait()

	if s.Running() {
		t.Error("still running after pause")
	}
	if s.Tick() < 10 {
		t.Errorf("only %d ticks ran", s.Tick())
	}
	if len(s.Participants()) != len(DefaultParticipants(0)) {
		t.Errorf("participants = %d", len(s.Participants()))
	}
	if err := s.RemoveParticipant(12345); err != ErrUnknownParticipant {
		t.Errorf("err = %v", err)
	}
}

func TestAddParticipantValidation(t *testing.T) {
	s := newSim(t)
	if _, err := s.AddParticipant(ParticipantConfig{Strategy: "bogus"}); err == nil {
		t.Error("unknown strategy accepted")
	}
	if _, err := s.AddParticipant(ParticipantConfig{Cash: -1}); err == nil {
		t.Error("negative cash accepted")
	}
	info, err := s.AddParticipant(ParticipantConfig{})
	if err != nil || info.Strategy != Random || info.Name != "random" {
		t.Errorf("defaults: %+v %v", info, err)
	}
}
