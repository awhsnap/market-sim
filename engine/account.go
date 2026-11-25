package engine

import "time"

// AccountConfig describes a new account.
type AccountConfig struct {
	Name       string
	Cash       int64 // starting cash in cents
	Shares     int64 // starting long inventory (opened at OpeningPrice)
	Permission Permission
	// Unlimited accounts skip all cash and inventory checks and may go
	// short. This is how the market maker is modelled.
	Unlimited bool
	// OpeningPrice is the cost basis for starting Shares. Defaults to the
	// engine's reference price.
	OpeningPrice int64
}

// Account holds cash, FIFO position lots and reservations for open orders.
type Account struct {
	ID          int64
	Name        string
	Permission  Permission
	Unlimited   bool
	Cash        int64
	StartCash   int64
	StartShares int64
	StartPrice  int64
	Realized    int64
	Lots        []Lot

	reservedCash   int64 // cash locked by open buy orders
	reservedShares int64 // shares locked by open sell orders
}

// Position returns the net quantity across all lots.
func (a *Account) Position() int64 {
	var n int64
	for _, l := range a.Lots {
		n += l.Qty
	}
	return n
}

// CostBasis returns the summed cost of open lots (signed like Qty).
func (a *Account) CostBasis() int64 {
	var c int64
	for _, l := range a.Lots {
		c += l.Qty * l.Price
	}
	return c
}

// AvgPrice returns the average open price of the position, or 0 when flat.
func (a *Account) AvgPrice() int64 {
	pos := a.Position()
	if pos == 0 {
		return 0
	}
	return a.CostBasis() / pos
}

// Unrealized returns open profit or loss at mark.
func (a *Account) Unrealized(mark int64) int64 {
	return a.Position()*mark - a.CostBasis()
}

// Equity returns cash plus position marked at mark.
func (a *Account) Equity(mark int64) int64 {
	return a.Cash + a.Position()*mark
}

// AvailableCash is cash not locked by open buy orders.
func (a *Account) AvailableCash() int64 { return a.Cash - a.reservedCash }

// AvailableShares is long inventory not locked by open sell orders.
func (a *Account) AvailableShares() int64 {
	pos := a.Position()
	if pos < 0 {
		return 0
	}
	return pos - a.reservedShares
}

// applyFill books an execution: moves cash and updates FIFO lots, closing
// the oldest opposite lots first and realizing their profit or loss.
func (a *Account) applyFill(side Side, qty, price int64, at time.Time) {
	signed := qty
	if side == Sell {
		signed = -qty
		a.Cash += qty * price
	} else {
		a.Cash -= qty * price
	}

	for signed != 0 && len(a.Lots) > 0 && sign(a.Lots[0].Qty) != sign(signed) {
		lot := &a.Lots[0]
		closeQty := min(abs(lot.Qty), abs(signed))
		// Long lot closed by a sell: (price - open) per unit.
		// Short lot closed by a buy: (open - price) per unit.
		a.Realized += closeQty * (price - lot.Price) * sign(lot.Qty)
		lot.Qty -= closeQty * sign(lot.Qty)
		signed -= closeQty * sign(signed)
		if lot.Qty == 0 {
			a.Lots = a.Lots[1:]
		}
	}
	if signed != 0 {
		a.Lots = append(a.Lots, Lot{Qty: signed, Price: price, Opened: at})
	}
}

func sign(x int64) int64 {
	switch {
	case x > 0:
		return 1
	case x < 0:
		return -1
	}
	return 0
}

func abs(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}
