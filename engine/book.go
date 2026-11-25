package engine

import "sort"

// level is one price level: resting orders in time priority.
type level struct {
	price  int64
	orders []*Order
}

func (l *level) qty() int64 {
	var q int64
	for _, o := range l.orders {
		q += o.Remaining
	}
	return q
}

// bookSide keeps price levels sorted best-first: descending for bids,
// ascending for asks.
type bookSide struct {
	side   Side
	levels []*level
}

// better reports whether price a ranks ahead of price b on this side.
func (b *bookSide) better(a, c int64) bool {
	if b.side == Buy {
		return a > c
	}
	return a < c
}

func (b *bookSide) best() *level {
	if len(b.levels) == 0 {
		return nil
	}
	return b.levels[0]
}

func (b *bookSide) add(o *Order) {
	i := sort.Search(len(b.levels), func(i int) bool {
		return !b.better(b.levels[i].price, o.Price)
	})
	if i < len(b.levels) && b.levels[i].price == o.Price {
		b.levels[i].orders = append(b.levels[i].orders, o)
		return
	}
	b.levels = append(b.levels, nil)
	copy(b.levels[i+1:], b.levels[i:])
	b.levels[i] = &level{price: o.Price, orders: []*Order{o}}
}

// remove deletes an order from its level, dropping the level if empty.
func (b *bookSide) remove(o *Order) bool {
	for li, l := range b.levels {
		if l.price != o.Price {
			continue
		}
		for oi, r := range l.orders {
			if r.ID != o.ID {
				continue
			}
			l.orders = append(l.orders[:oi], l.orders[oi+1:]...)
			if len(l.orders) == 0 {
				b.levels = append(b.levels[:li], b.levels[li+1:]...)
			}
			return true
		}
		return false
	}
	return false
}

// popFront removes the head order of the best level.
func (b *bookSide) popFront() {
	l := b.levels[0]
	l.orders = l.orders[1:]
	if len(l.orders) == 0 {
		b.levels = b.levels[1:]
	}
}

// DepthLevel is an aggregated price level for display.
type DepthLevel struct {
	Price  int64
	Qty    int64
	Orders int
}

func (b *bookSide) depth(n int) []DepthLevel {
	if n <= 0 || n > len(b.levels) {
		n = len(b.levels)
	}
	out := make([]DepthLevel, 0, n)
	for _, l := range b.levels[:n] {
		out = append(out, DepthLevel{Price: l.price, Qty: l.qty(), Orders: len(l.orders)})
	}
	return out
}
