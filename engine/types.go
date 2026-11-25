// Package engine implements a single-asset exchange: a central limit order
// book with price-time priority, accounts with cash and FIFO position lots,
// and a market maker account with unlimited capital.
//
// Prices are integer cents and quantities are whole units. All exported
// Engine methods are safe for concurrent use.
package engine

import (
	"errors"
	"time"
)

// Side is the direction of an order or trade.
type Side int

const (
	Buy Side = iota + 1
	Sell
)

func (s Side) String() string {
	switch s {
	case Buy:
		return "buy"
	case Sell:
		return "sell"
	}
	return "unknown"
}

// Opposite returns the other side.
func (s Side) Opposite() Side {
	if s == Buy {
		return Sell
	}
	return Buy
}

// OrderType is limit or market.
type OrderType int

const (
	Limit OrderType = iota + 1
	Market
)

func (t OrderType) String() string {
	switch t {
	case Limit:
		return "limit"
	case Market:
		return "market"
	}
	return "unknown"
}

// Permission restricts which sides an account may trade.
type Permission int

const (
	BuyAndSell Permission = iota
	BuyOnly
	SellOnly
)

func (p Permission) String() string {
	switch p {
	case BuyOnly:
		return "buy_only"
	case SellOnly:
		return "sell_only"
	}
	return "buy_and_sell"
}

// Allows reports whether the permission lets an account trade on side s.
func (p Permission) Allows(s Side) bool {
	switch p {
	case BuyOnly:
		return s == Buy
	case SellOnly:
		return s == Sell
	}
	return true
}

// OrderStatus is the lifecycle state of an order.
type OrderStatus int

const (
	Open OrderStatus = iota + 1
	PartiallyFilled
	Filled
	Cancelled
)

func (s OrderStatus) String() string {
	switch s {
	case Open:
		return "open"
	case PartiallyFilled:
		return "partially_filled"
	case Filled:
		return "filled"
	case Cancelled:
		return "cancelled"
	}
	return "unknown"
}

// Order is an order as tracked by the engine. Values returned from the
// engine are copies.
type Order struct {
	ID        int64
	AccountID int64
	Side      Side
	Type      OrderType
	Price     int64 // limit price in cents; 0 for market orders
	Qty       int64 // original quantity
	Remaining int64
	Status    OrderStatus
	Created   time.Time
	seq       int64
}

// Filled is the quantity executed so far.
func (o Order) Filled() int64 { return o.Qty - o.Remaining }

// OrderRequest is what a caller submits.
type OrderRequest struct {
	AccountID int64
	Side      Side
	Type      OrderType
	Price     int64 // required for limit orders, ignored for market orders
	Qty       int64
}

// Trade is a single execution between a resting order and an incoming one.
type Trade struct {
	ID        int64
	Price     int64
	Qty       int64
	BuyerID   int64
	SellerID  int64
	BuyOrder  int64
	SellOrder int64
	Aggressor Side
	Time      time.Time
}

// Lot is one open position lot. Qty is positive for long, negative for short.
type Lot struct {
	Qty    int64
	Price  int64
	Opened time.Time
}

// Errors returned by the engine.
var (
	ErrUnknownAccount     = errors.New("unknown account")
	ErrUnknownOrder       = errors.New("unknown order")
	ErrInvalidQty         = errors.New("quantity must be positive")
	ErrInvalidPrice       = errors.New("limit price must be positive")
	ErrInvalidSide        = errors.New("invalid side")
	ErrInvalidType        = errors.New("invalid order type")
	ErrSideNotPermitted   = errors.New("account is not permitted to trade this side")
	ErrInsufficientCash   = errors.New("insufficient cash")
	ErrInsufficientShares = errors.New("insufficient shares")
	ErrOrderClosed        = errors.New("order is no longer open")
)
