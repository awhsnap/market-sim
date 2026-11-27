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
	ID        int64       `json:"id"`
	AccountID int64       `json:"accountID"`
	Side      Side        `json:"side"`
	Type      OrderType   `json:"type"`
	Price     int64       `json:"price"` // limit price in cents; 0 for market orders
	Qty       int64       `json:"qty"`   // original quantity
	Remaining int64       `json:"remaining"`
	Status    OrderStatus `json:"status"`
	Created   time.Time   `json:"created"`
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
	ID        int64     `json:"id"`
	Price     int64     `json:"price"`
	Qty       int64     `json:"qty"`
	BuyerID   int64     `json:"buyerID"`
	SellerID  int64     `json:"sellerID"`
	BuyOrder  int64     `json:"buyOrder"`
	SellOrder int64     `json:"sellOrder"`
	Aggressor Side      `json:"aggressor"`
	Time      time.Time `json:"time"`
}

// Lot is one open position lot. Qty is positive for long, negative for short.
type Lot struct {
	Qty    int64     `json:"qty"`
	Price  int64     `json:"price"`
	Opened time.Time `json:"opened"`
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
	ErrInvalidPermission  = errors.New("permission must be buy_and_sell, buy_only or sell_only")
)

// Text encoding so the enums read as words in JSON.

func (s Side) MarshalText() ([]byte, error) { return []byte(s.String()), nil }

func (s *Side) UnmarshalText(b []byte) error {
	switch string(b) {
	case "buy":
		*s = Buy
	case "sell":
		*s = Sell
	default:
		return ErrInvalidSide
	}
	return nil
}

func (t OrderType) MarshalText() ([]byte, error) { return []byte(t.String()), nil }

func (t *OrderType) UnmarshalText(b []byte) error {
	switch string(b) {
	case "limit":
		*t = Limit
	case "market":
		*t = Market
	default:
		return ErrInvalidType
	}
	return nil
}

func (p Permission) MarshalText() ([]byte, error) { return []byte(p.String()), nil }

func (p *Permission) UnmarshalText(b []byte) error {
	switch string(b) {
	case "buy_and_sell", "":
		*p = BuyAndSell
	case "buy_only":
		*p = BuyOnly
	case "sell_only":
		*p = SellOnly
	default:
		return ErrInvalidPermission
	}
	return nil
}

func (s OrderStatus) MarshalText() ([]byte, error) { return []byte(s.String()), nil }
