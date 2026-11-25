package engine

import (
	"errors"
	"testing"
	"time"
)

func newTestEngine() *Engine {
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	return New(Config{ReferencePrice: 10_000, Now: func() time.Time { return t0 }})
}

func mustSubmit(t *testing.T, e *Engine, r OrderRequest) (Order, []Trade) {
	t.Helper()
	o, tr, err := e.Submit(r)
	if err != nil {
		t.Fatalf("submit %+v: %v", r, err)
	}
	return o, tr
}

func lim(acct int64, s Side, price, qty int64) OrderRequest {
	return OrderRequest{AccountID: acct, Side: s, Type: Limit, Price: price, Qty: qty}
}

func mkt(acct int64, s Side, qty int64) OrderRequest {
	return OrderRequest{AccountID: acct, Side: s, Type: Market, Qty: qty}
}

func TestPriceTimePriority(t *testing.T) {
	e := newTestEngine()
	mm := e.AddAccount(AccountConfig{Name: "mm", Unlimited: true})
	a := e.AddAccount(AccountConfig{Name: "a", Unlimited: true})
	b := e.AddAccount(AccountConfig{Name: "b", Unlimited: true})

	o1, _ := mustSubmit(t, e, lim(a, Sell, 101, 5))
	o2, _ := mustSubmit(t, e, lim(b, Sell, 100, 5)) // better price
	o3, _ := mustSubmit(t, e, lim(a, Sell, 100, 5)) // same price, later

	_, trades := mustSubmit(t, e, lim(mm, Buy, 101, 12))
	if len(trades) != 3 {
		t.Fatalf("want 3 trades, got %d", len(trades))
	}
	want := []struct{ order, price, qty int64 }{
		{o2.ID, 100, 5}, {o3.ID, 100, 5}, {o1.ID, 101, 2},
	}
	for i, w := range want {
		tr := trades[i]
		if tr.SellOrder != w.order || tr.Price != w.price || tr.Qty != w.qty {
			t.Errorf("trade %d = %+v, want order %d @%d x%d", i, tr, w.order, w.price, w.qty)
		}
	}
	_, asks := e.Depth(0)
	if len(asks) != 1 || asks[0].Price != 101 || asks[0].Qty != 3 {
		t.Errorf("asks after = %+v", asks)
	}
	if e.LastPrice() != 101 {
		t.Errorf("last price = %d", e.LastPrice())
	}
}

func TestLimitRestsWhenNotMarketable(t *testing.T) {
	e := newTestEngine()
	a := e.AddAccount(AccountConfig{Unlimited: true})
	b := e.AddAccount(AccountConfig{Unlimited: true})
	mustSubmit(t, e, lim(a, Sell, 105, 1))
	o, tr := mustSubmit(t, e, lim(b, Buy, 104, 1))
	if len(tr) != 0 || o.Status != Open {
		t.Fatalf("expected resting order, got %v trades, status %v", len(tr), o.Status)
	}
	if bb, _ := e.BestBid(); bb != 104 {
		t.Errorf("best bid %d", bb)
	}
	if ba, _ := e.BestAsk(); ba != 105 {
		t.Errorf("best ask %d", ba)
	}
	if m := e.Mark(); m != 104 {
		t.Errorf("mark with no trades should be mid, got %d", m)
	}
}

func TestMarketOrderWalksBookAndCancelsRemainder(t *testing.T) {
	e := newTestEngine()
	mm := e.AddAccount(AccountConfig{Unlimited: true})
	b := e.AddAccount(AccountConfig{Unlimited: true})
	mustSubmit(t, e, lim(mm, Buy, 99, 2))
	mustSubmit(t, e, lim(mm, Buy, 98, 2))

	o, trades := mustSubmit(t, e, mkt(b, Sell, 10))
	if len(trades) != 2 || trades[0].Price != 99 || trades[1].Price != 98 {
		t.Fatalf("trades = %+v", trades)
	}
	if o.Status != Cancelled || o.Remaining != 6 {
		t.Errorf("market order status %v remaining %d", o.Status, o.Remaining)
	}
	if bids, _ := e.Depth(0); len(bids) != 0 {
		t.Errorf("bids should be empty, got %+v", bids)
	}
}

func TestFIFOLotsLong(t *testing.T) {
	e := newTestEngine()
	mm := e.AddAccount(AccountConfig{Unlimited: true})
	p := e.AddAccount(AccountConfig{Cash: 1_000_000})

	// Buy 10 @100, then 10 @110.
	mustSubmit(t, e, lim(mm, Sell, 100, 10))
	mustSubmit(t, e, mkt(p, Buy, 10))
	mustSubmit(t, e, lim(mm, Sell, 110, 10))
	mustSubmit(t, e, mkt(p, Buy, 10))

	// Sell 15 @120: closes all of lot 1 and 5 of lot 2 (FIFO).
	mustSubmit(t, e, lim(mm, Buy, 120, 15))
	mustSubmit(t, e, mkt(p, Sell, 15))

	acc, _ := e.Account(p)
	wantRealized := int64(10*(120-100) + 5*(120-110))
	if acc.Realized != wantRealized {
		t.Errorf("realized = %d, want %d", acc.Realized, wantRealized)
	}
	if len(acc.Lots) != 1 || acc.Lots[0].Qty != 5 || acc.Lots[0].Price != 110 {
		t.Errorf("lots = %+v, want one lot 5@110", acc.Lots)
	}
	wantCash := int64(1_000_000 - 10*100 - 10*110 + 15*120)
	if acc.Cash != wantCash {
		t.Errorf("cash = %d, want %d", acc.Cash, wantCash)
	}

	// Market maker is the mirror image: short lots closed FIFO.
	m, _ := e.Account(mm)
	if m.Realized != -wantRealized {
		t.Errorf("mm realized = %d, want %d", m.Realized, -wantRealized)
	}
	if m.Position() != -5 {
		t.Errorf("mm position = %d", m.Position())
	}
}

func TestFIFOFlipThroughZero(t *testing.T) {
	a := &Account{Unlimited: true}
	now := time.Now()
	a.applyFill(Buy, 3, 100, now)
	a.applyFill(Sell, 5, 90, now) // close 3 long (-30), open 2 short @90
	if a.Realized != -30 {
		t.Errorf("realized = %d", a.Realized)
	}
	if len(a.Lots) != 1 || a.Lots[0].Qty != -2 || a.Lots[0].Price != 90 {
		t.Fatalf("lots = %+v", a.Lots)
	}
	a.applyFill(Buy, 2, 80, now) // cover short: +20
	if a.Realized != -10 || len(a.Lots) != 0 {
		t.Errorf("realized %d lots %+v", a.Realized, a.Lots)
	}
	if a.Unrealized(123) != 0 || a.AvgPrice() != 0 {
		t.Errorf("flat account should have no open P&L")
	}
}

func TestPermissions(t *testing.T) {
	e := newTestEngine()
	buyer := e.AddAccount(AccountConfig{Cash: 100_000, Permission: BuyOnly})
	seller := e.AddAccount(AccountConfig{Shares: 10, Permission: SellOnly})

	if _, _, err := e.Submit(lim(buyer, Sell, 100, 1)); !errors.Is(err, ErrSideNotPermitted) {
		t.Errorf("buy-only selling: err = %v", err)
	}
	if _, _, err := e.Submit(lim(seller, Buy, 100, 1)); !errors.Is(err, ErrSideNotPermitted) {
		t.Errorf("sell-only buying: err = %v", err)
	}
	mustSubmit(t, e, lim(seller, Sell, 100, 4))
	_, tr := mustSubmit(t, e, lim(buyer, Buy, 100, 4))
	if len(tr) != 1 {
		t.Fatalf("participants should trade with each other, got %d trades", len(tr))
	}

	// Switching to buy-only cancels the seller's open sell orders.
	mustSubmit(t, e, lim(seller, Sell, 105, 2))
	if err := e.SetPermission(seller, BuyOnly); err != nil {
		t.Fatal(err)
	}
	if open := e.OpenOrders(seller); len(open) != 0 {
		t.Errorf("open orders after permission change: %+v", open)
	}
}

func TestCashAndInventoryChecks(t *testing.T) {
	e := newTestEngine()
	mm := e.AddAccount(AccountConfig{Unlimited: true})
	p := e.AddAccount(AccountConfig{Cash: 1_000, Shares: 3})

	if _, _, err := e.Submit(lim(p, Buy, 100, 11)); !errors.Is(err, ErrInsufficientCash) {
		t.Errorf("over-cash limit buy: err = %v", err)
	}
	// Reserve 600 of 1000, then 500 more should fail.
	mustSubmit(t, e, lim(p, Buy, 100, 6))
	if _, _, err := e.Submit(lim(p, Buy, 100, 5)); !errors.Is(err, ErrInsufficientCash) {
		t.Errorf("reservation not enforced: err = %v", err)
	}
	if _, _, err := e.Submit(lim(p, Sell, 200, 4)); !errors.Is(err, ErrInsufficientShares) {
		t.Errorf("short sell by participant: err = %v", err)
	}
	mustSubmit(t, e, lim(p, Sell, 200, 3))
	if _, _, err := e.Submit(mkt(p, Sell, 1)); !errors.Is(err, ErrInsufficientShares) {
		t.Errorf("reserved shares sold twice: err = %v", err)
	}

	// Cancelling releases reservations.
	e.CancelAll(p)
	acc, _ := e.Account(p)
	if acc.AvailableCash() != 1_000 || acc.AvailableShares() != 3 {
		t.Errorf("after cancel: cash %d shares %d", acc.AvailableCash(), acc.AvailableShares())
	}

	// Market buy is capped by cash: 1000 cash at 300 buys 3, not 5.
	mustSubmit(t, e, lim(mm, Sell, 300, 5))
	o, _ := mustSubmit(t, e, mkt(p, Buy, 5))
	if o.Filled() != 3 {
		t.Errorf("market buy filled %d, want 3", o.Filled())
	}
	acc, _ = e.Account(p)
	if acc.Cash != 100 {
		t.Errorf("cash = %d, want 100", acc.Cash)
	}
}

func TestLimitBuyFilledBelowLimitRefundsReservation(t *testing.T) {
	e := newTestEngine()
	mm := e.AddAccount(AccountConfig{Unlimited: true})
	p := e.AddAccount(AccountConfig{Cash: 1_000})
	mustSubmit(t, e, lim(mm, Sell, 80, 10))
	mustSubmit(t, e, lim(p, Buy, 100, 10)) // fills at 80
	acc, _ := e.Account(p)
	if acc.Cash != 200 || acc.AvailableCash() != 200 {
		t.Errorf("cash %d available %d, want 200/200", acc.Cash, acc.AvailableCash())
	}
}

func TestSelfTradePrevention(t *testing.T) {
	e := newTestEngine()
	mm := e.AddAccount(AccountConfig{Unlimited: true})
	other := e.AddAccount(AccountConfig{Unlimited: true})
	own, _ := mustSubmit(t, e, lim(mm, Sell, 100, 5))
	mustSubmit(t, e, lim(other, Sell, 101, 5))

	_, trades := mustSubmit(t, e, lim(mm, Buy, 101, 5))
	if len(trades) != 1 || trades[0].SellerID != other {
		t.Fatalf("trades = %+v", trades)
	}
	if _, err := e.Cancel(mm, own.ID); !errors.Is(err, ErrUnknownOrder) {
		t.Errorf("own resting order should already be cancelled, err = %v", err)
	}
}

func TestCancelOwnership(t *testing.T) {
	e := newTestEngine()
	a := e.AddAccount(AccountConfig{Unlimited: true})
	b := e.AddAccount(AccountConfig{Unlimited: true})
	o, _ := mustSubmit(t, e, lim(a, Buy, 100, 1))
	if _, err := e.Cancel(b, o.ID); !errors.Is(err, ErrUnknownOrder) {
		t.Errorf("cancel by non-owner: err = %v", err)
	}
	c, err := e.Cancel(a, o.ID)
	if err != nil || c.Status != Cancelled {
		t.Errorf("cancel: %+v %v", c, err)
	}
}

func TestRemoveAccountCancelsOrders(t *testing.T) {
	e := newTestEngine()
	a := e.AddAccount(AccountConfig{Cash: 10_000})
	mustSubmit(t, e, lim(a, Buy, 100, 5))
	if err := e.RemoveAccount(a); err != nil {
		t.Fatal(err)
	}
	if bids, _ := e.Depth(0); len(bids) != 0 {
		t.Errorf("bids remain after removing account: %+v", bids)
	}
	if _, err := e.Account(a); !errors.Is(err, ErrUnknownAccount) {
		t.Errorf("err = %v", err)
	}
}

func TestSummaryIsZeroSum(t *testing.T) {
	e := newTestEngine()
	mm := e.AddAccount(AccountConfig{Name: "you", Unlimited: true})
	p1 := e.AddAccount(AccountConfig{Name: "p1", Cash: 1_000_000})
	p2 := e.AddAccount(AccountConfig{Name: "p2", Cash: 500_000, Shares: 50})

	mustSubmit(t, e, lim(mm, Sell, 10_100, 20))
	mustSubmit(t, e, mkt(p1, Buy, 15))
	mustSubmit(t, e, lim(p2, Sell, 10_050, 10))
	mustSubmit(t, e, mkt(mm, Buy, 10))
	mustSubmit(t, e, lim(p1, Buy, 10_000, 5))
	mustSubmit(t, e, mkt(p2, Sell, 5))

	s := e.Summarize()
	if s.Mark != 10_000 {
		t.Errorf("mark = %d", s.Mark)
	}
	if got := s.ParticipantPnL + s.MakerPnL; got != 0 {
		t.Errorf("participant pnl %d + maker pnl %d = %d, want 0", s.ParticipantPnL, s.MakerPnL, got)
	}
	if s.ParticipantPos+s.MakerPos != 50 {
		t.Errorf("net position should equal starting inventory 50, got %d", s.ParticipantPos+s.MakerPos)
	}
	if len(s.MarketMakers) != 1 || len(s.Participants) != 2 {
		t.Errorf("grouping wrong: %d makers, %d participants", len(s.MarketMakers), len(s.Participants))
	}
	// MM sold 15 @101.00 and bought 10 @100.50: short 5, marked at 100.00.
	wantMM := int64(15*10_100 - 10*10_050 - 5*10_000)
	if s.MakerPnL != wantMM {
		t.Errorf("maker pnl = %d, want %d", s.MakerPnL, wantMM)
	}
}

func TestInvalidOrders(t *testing.T) {
	e := newTestEngine()
	a := e.AddAccount(AccountConfig{Unlimited: true})
	cases := []struct {
		r   OrderRequest
		err error
	}{
		{lim(a, Buy, 100, 0), ErrInvalidQty},
		{lim(a, Buy, 0, 1), ErrInvalidPrice},
		{OrderRequest{AccountID: a, Side: 9, Type: Limit, Price: 1, Qty: 1}, ErrInvalidSide},
		{OrderRequest{AccountID: a, Side: Buy, Type: 9, Price: 1, Qty: 1}, ErrInvalidType},
		{lim(999, Buy, 100, 1), ErrUnknownAccount},
	}
	for _, c := range cases {
		if _, _, err := e.Submit(c.r); !errors.Is(err, c.err) {
			t.Errorf("%+v: err = %v, want %v", c.r, err, c.err)
		}
	}
}

func TestTradesSinceAndHistoryCap(t *testing.T) {
	t0 := time.Now()
	e := New(Config{ReferencePrice: 100, TradeHistory: 3, Now: func() time.Time { return t0 }})
	a := e.AddAccount(AccountConfig{Unlimited: true})
	b := e.AddAccount(AccountConfig{Unlimited: true})
	for i := 0; i < 5; i++ {
		mustSubmit(t, e, lim(a, Sell, 100, 1))
		mustSubmit(t, e, mkt(b, Buy, 1))
	}
	all := e.TradesSince(0)
	if len(all) != 3 || all[0].ID != 3 {
		t.Fatalf("history = %+v", all)
	}
	if got := e.TradesSince(4); len(got) != 1 || got[0].ID != 5 {
		t.Errorf("since 4 = %+v", got)
	}
}
