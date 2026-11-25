package engine

import (
	"sort"
	"sync"
	"time"
)

// DefaultTradeHistory is how many trades the engine keeps in memory.
const DefaultTradeHistory = 50_000

// Config configures a new Engine.
type Config struct {
	// ReferencePrice is the mark used before any trade happens and the
	// cost basis for starting inventory. In cents.
	ReferencePrice int64
	// TradeHistory caps the in-memory trade log. Defaults to DefaultTradeHistory.
	TradeHistory int
	// Now overrides the clock, for tests.
	Now func() time.Time
}

// Engine is a single-asset exchange.
type Engine struct {
	mu sync.Mutex

	cfg      Config
	accounts map[int64]*Account
	acctIDs  []int64
	orders   map[int64]*Order // open orders only
	bids     bookSide
	asks     bookSide
	trades   []Trade

	lastPrice int64
	nextAcct  int64
	nextOrder int64
	nextTrade int64
	seq       int64
}

// New creates an engine.
func New(cfg Config) *Engine {
	if cfg.TradeHistory <= 0 {
		cfg.TradeHistory = DefaultTradeHistory
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Engine{
		cfg:      cfg,
		accounts: make(map[int64]*Account),
		orders:   make(map[int64]*Order),
		bids:     bookSide{side: Buy},
		asks:     bookSide{side: Sell},
	}
}

// AddAccount registers an account and returns its ID.
func (e *Engine) AddAccount(c AccountConfig) int64 {
	e.mu.Lock()
	defer e.mu.Unlock()

	e.nextAcct++
	price := c.OpeningPrice
	if price == 0 {
		price = e.markLocked()
	}
	a := &Account{
		ID:          e.nextAcct,
		Name:        c.Name,
		Permission:  c.Permission,
		Unlimited:   c.Unlimited,
		Cash:        c.Cash,
		StartCash:   c.Cash,
		StartShares: c.Shares,
		StartPrice:  price,
	}
	if c.Shares != 0 {
		a.Lots = []Lot{{Qty: c.Shares, Price: price, Opened: e.cfg.Now()}}
	}
	e.accounts[a.ID] = a
	e.acctIDs = append(e.acctIDs, a.ID)
	return a.ID
}

// RemoveAccount cancels the account's open orders and removes it. Its
// trades stay in the trade log.
func (e *Engine) RemoveAccount(id int64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if _, ok := e.accounts[id]; !ok {
		return ErrUnknownAccount
	}
	for _, o := range e.openOrdersLocked(id) {
		e.cancelLocked(o)
	}
	delete(e.accounts, id)
	for i, v := range e.acctIDs {
		if v == id {
			e.acctIDs = append(e.acctIDs[:i], e.acctIDs[i+1:]...)
			break
		}
	}
	return nil
}

// SetPermission changes which sides an account may trade. Open orders on a
// side that is no longer allowed are cancelled.
func (e *Engine) SetPermission(id int64, p Permission) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.accounts[id]
	if !ok {
		return ErrUnknownAccount
	}
	a.Permission = p
	for _, o := range e.openOrdersLocked(id) {
		if !p.Allows(o.Side) {
			e.cancelLocked(o)
		}
	}
	return nil
}

// Submit places an order. Limit orders that do not fully match rest in the
// book. Market orders never rest: any unfilled quantity is cancelled. The
// returned order reflects its state after matching.
func (e *Engine) Submit(r OrderRequest) (Order, []Trade, error) {
	e.mu.Lock()
	defer e.mu.Unlock()

	if r.Qty <= 0 {
		return Order{}, nil, ErrInvalidQty
	}
	if r.Side != Buy && r.Side != Sell {
		return Order{}, nil, ErrInvalidSide
	}
	switch r.Type {
	case Limit:
		if r.Price <= 0 {
			return Order{}, nil, ErrInvalidPrice
		}
	case Market:
		r.Price = 0
	default:
		return Order{}, nil, ErrInvalidType
	}
	a, ok := e.accounts[r.AccountID]
	if !ok {
		return Order{}, nil, ErrUnknownAccount
	}
	if !a.Permission.Allows(r.Side) {
		return Order{}, nil, ErrSideNotPermitted
	}

	// Pre-trade checks for capital-limited accounts. Limit orders reserve
	// their full worst-case cost up front. Market sells must be covered by
	// free inventory; market buys are capped by free cash while matching.
	if !a.Unlimited {
		switch {
		case r.Side == Buy && r.Type == Limit:
			if r.Qty*r.Price > a.AvailableCash() {
				return Order{}, nil, ErrInsufficientCash
			}
		case r.Side == Buy && r.Type == Market:
			if a.AvailableCash() <= 0 {
				return Order{}, nil, ErrInsufficientCash
			}
		case r.Side == Sell:
			if r.Qty > a.AvailableShares() {
				return Order{}, nil, ErrInsufficientShares
			}
		}
	}

	now := e.cfg.Now()
	e.nextOrder++
	e.seq++
	o := &Order{
		ID:        e.nextOrder,
		AccountID: a.ID,
		Side:      r.Side,
		Type:      r.Type,
		Price:     r.Price,
		Qty:       r.Qty,
		Remaining: r.Qty,
		Status:    Open,
		Created:   now,
		seq:       e.seq,
	}
	if !a.Unlimited && o.Type == Limit {
		e.reserve(a, o, o.Remaining)
	}

	trades := e.match(o, a, now)

	switch {
	case o.Remaining == 0:
		o.Status = Filled
	case o.Type == Market:
		o.Status = Cancelled
	default:
		if o.Remaining < o.Qty {
			o.Status = PartiallyFilled
		}
		e.orders[o.ID] = o
		e.side(o.Side).add(o)
	}
	return *o, trades, nil
}

// match crosses an incoming order against the opposite side of the book.
func (e *Engine) match(o *Order, a *Account, now time.Time) []Trade {
	var trades []Trade
	book := e.side(o.Side.Opposite())
	for o.Remaining > 0 {
		best := book.best()
		if best == nil {
			break
		}
		if o.Type == Limit && !crosses(o, best.price) {
			break
		}
		resting := best.orders[0]
		ra := e.accounts[resting.AccountID]

		// Self-trade prevention: cancel the older resting order.
		if resting.AccountID == o.AccountID {
			e.cancelLocked(resting)
			continue
		}

		qty := min(o.Remaining, resting.Remaining)
		price := resting.Price
		if o.Type == Market && o.Side == Buy && !a.Unlimited {
			afford := a.AvailableCash() / price
			if afford <= 0 {
				break
			}
			qty = min(qty, afford)
		}

		e.fill(a, o, qty, price, now)
		e.fill(ra, resting, qty, price, now)
		if resting.Remaining == 0 {
			resting.Status = Filled
			book.popFront()
			delete(e.orders, resting.ID)
		} else {
			resting.Status = PartiallyFilled
		}

		t := e.recordTrade(o, resting, qty, price, now)
		trades = append(trades, t)
	}
	return trades
}

func crosses(o *Order, restingPrice int64) bool {
	if o.Side == Buy {
		return o.Price >= restingPrice
	}
	return o.Price <= restingPrice
}

// fill applies an execution to one side's order and account.
func (e *Engine) fill(a *Account, o *Order, qty, price int64, now time.Time) {
	o.Remaining -= qty
	if a == nil {
		return
	}
	if !a.Unlimited && o.Type == Limit {
		e.release(a, o, qty)
	}
	a.applyFill(o.Side, qty, price, now)
}

func (e *Engine) reserve(a *Account, o *Order, qty int64) {
	if o.Side == Buy {
		a.reservedCash += qty * o.Price
	} else {
		a.reservedShares += qty
	}
}

func (e *Engine) release(a *Account, o *Order, qty int64) {
	if o.Side == Buy {
		a.reservedCash -= qty * o.Price
	} else {
		a.reservedShares -= qty
	}
}

func (e *Engine) recordTrade(in, resting *Order, qty, price int64, now time.Time) Trade {
	e.nextTrade++
	t := Trade{ID: e.nextTrade, Price: price, Qty: qty, Aggressor: in.Side, Time: now}
	if in.Side == Buy {
		t.BuyerID, t.BuyOrder = in.AccountID, in.ID
		t.SellerID, t.SellOrder = resting.AccountID, resting.ID
	} else {
		t.SellerID, t.SellOrder = in.AccountID, in.ID
		t.BuyerID, t.BuyOrder = resting.AccountID, resting.ID
	}
	e.trades = append(e.trades, t)
	if over := len(e.trades) - e.cfg.TradeHistory; over > 0 {
		e.trades = append(e.trades[:0:0], e.trades[over:]...)
	}
	e.lastPrice = price
	return t
}

func (e *Engine) side(s Side) *bookSide {
	if s == Buy {
		return &e.bids
	}
	return &e.asks
}

// Cancel cancels an open order owned by accountID.
func (e *Engine) Cancel(accountID, orderID int64) (Order, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	o, ok := e.orders[orderID]
	if !ok {
		return Order{}, ErrUnknownOrder
	}
	if o.AccountID != accountID {
		return Order{}, ErrUnknownOrder
	}
	e.cancelLocked(o)
	return *o, nil
}

// CancelAll cancels every open order for an account and returns how many.
func (e *Engine) CancelAll(accountID int64) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	open := e.openOrdersLocked(accountID)
	for _, o := range open {
		e.cancelLocked(o)
	}
	return len(open)
}

func (e *Engine) cancelLocked(o *Order) {
	if o.Status == Filled || o.Status == Cancelled {
		return
	}
	e.side(o.Side).remove(o)
	delete(e.orders, o.ID)
	if a, ok := e.accounts[o.AccountID]; ok && !a.Unlimited && o.Type == Limit {
		e.release(a, o, o.Remaining)
	}
	o.Status = Cancelled
}

func (e *Engine) openOrdersLocked(accountID int64) []*Order {
	var out []*Order
	for _, o := range e.orders {
		if o.AccountID == accountID {
			out = append(out, o)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].seq < out[j].seq })
	return out
}

// OpenOrders returns an account's resting orders, oldest first.
func (e *Engine) OpenOrders(accountID int64) []Order {
	e.mu.Lock()
	defer e.mu.Unlock()
	open := e.openOrdersLocked(accountID)
	out := make([]Order, len(open))
	for i, o := range open {
		out[i] = *o
	}
	return out
}

// Account returns a copy of an account.
func (e *Engine) Account(id int64) (Account, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.accounts[id]
	if !ok {
		return Account{}, ErrUnknownAccount
	}
	return copyAccount(a), nil
}

// Accounts returns copies of all accounts in creation order.
func (e *Engine) Accounts() []Account {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Account, 0, len(e.acctIDs))
	for _, id := range e.acctIDs {
		out = append(out, copyAccount(e.accounts[id]))
	}
	return out
}

func copyAccount(a *Account) Account {
	c := *a
	c.Lots = append([]Lot(nil), a.Lots...)
	return c
}

// Depth returns up to n aggregated levels per side, best first. n <= 0
// returns every level.
func (e *Engine) Depth(n int) (bids, asks []DepthLevel) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.bids.depth(n), e.asks.depth(n)
}

// BestBid returns the best bid price and whether one exists.
func (e *Engine) BestBid() (int64, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if l := e.bids.best(); l != nil {
		return l.price, true
	}
	return 0, false
}

// BestAsk returns the best ask price and whether one exists.
func (e *Engine) BestAsk() (int64, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if l := e.asks.best(); l != nil {
		return l.price, true
	}
	return 0, false
}

// LastPrice returns the most recent trade price, or 0 before any trade.
func (e *Engine) LastPrice() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.lastPrice
}

// Mark returns the price used to value positions: last trade, else the
// book mid, else the reference price.
func (e *Engine) Mark() int64 {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.markLocked()
}

func (e *Engine) markLocked() int64 {
	if e.lastPrice > 0 {
		return e.lastPrice
	}
	b, a := e.bids.best(), e.asks.best()
	if b != nil && a != nil {
		return (b.price + a.price) / 2
	}
	return e.cfg.ReferencePrice
}

// TradesSince returns trades with ID greater than afterID, oldest first.
func (e *Engine) TradesSince(afterID int64) []Trade {
	e.mu.Lock()
	defer e.mu.Unlock()
	i := sort.Search(len(e.trades), func(i int) bool { return e.trades[i].ID > afterID })
	return append([]Trade(nil), e.trades[i:]...)
}
