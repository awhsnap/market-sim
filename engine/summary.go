package engine

// AccountSummary is an account valued at the current mark.
type AccountSummary struct {
	ID         int64
	Name       string
	Permission Permission
	Unlimited  bool
	Cash       int64
	Position   int64
	AvgPrice   int64
	Equity     int64 // cash + position * mark
	Realized   int64
	Unrealized int64
	PnL        int64 // trading P&L: equity now minus starting cash and shares, both valued at mark
}

// Summary is the whole market valued at one mark price.
type Summary struct {
	Mark int64

	// Participants are all capital-limited accounts.
	Participants      []AccountSummary
	ParticipantEquity int64 // total participant capital at mark
	ParticipantPnL    int64
	ParticipantPos    int64 // net participant position

	// MarketMakers are unlimited accounts (normally exactly one).
	MarketMakers []AccountSummary
	MakerPnL     int64
	MakerPos     int64
}

// Summarize values every account at the current mark. Because every trade
// has a buyer and a seller, ParticipantPnL + MakerPnL is always zero once
// starting inventory is valued at the same mark.
func (e *Engine) Summarize() Summary {
	e.mu.Lock()
	defer e.mu.Unlock()

	mark := e.markLocked()
	s := Summary{Mark: mark}
	for _, id := range e.acctIDs {
		a := e.accounts[id]
		as := AccountSummary{
			ID:         a.ID,
			Name:       a.Name,
			Permission: a.Permission,
			Unlimited:  a.Unlimited,
			Cash:       a.Cash,
			Position:   a.Position(),
			AvgPrice:   a.AvgPrice(),
			Equity:     a.Equity(mark),
			Realized:   a.Realized,
			Unrealized: a.Unrealized(mark),
		}
		// Start equity valued at today's mark so price drift on starting
		// inventory is not counted as trading profit.
		as.PnL = as.Equity - (a.StartCash + a.StartShares*mark)
		if a.Unlimited {
			s.MarketMakers = append(s.MarketMakers, as)
			s.MakerPnL += as.PnL
			s.MakerPos += as.Position
		} else {
			s.Participants = append(s.Participants, as)
			s.ParticipantEquity += as.Equity
			s.ParticipantPnL += as.PnL
			s.ParticipantPos += as.Position
		}
	}
	return s
}
