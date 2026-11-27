// Package web serves the browser UI and a JSON API over the simulator,
// using only the Go standard library.
package web

import (
	"context"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/awhsnap/market-sim/engine"
	"github.com/awhsnap/market-sim/sim"
)

//go:embed static
var staticFS embed.FS

// Limits that keep a public deployment from being pushed into overflow or
// out of memory.
const (
	MaxParticipants = 200
	MaxPrice        = 1_000_000_00 // $1,000,000.00 in cents
	MaxQty          = 1_000_000
	MaxCash         = 1_000_000_000_00 // $1B in cents
	maxBody         = 64 << 10
	streamEvery     = 150 * time.Millisecond
)

// Options configures the server.
type Options struct {
	// Password, when set, protects every page and API call with HTTP basic
	// auth (any username).
	Password string
	// Sim is the configuration used for the simulator and every reset.
	Sim sim.Config
	// Autoplay starts the simulator ticking immediately.
	Autoplay bool
}

// Server owns the current simulator and serves HTTP.
type Server struct {
	opts Options

	mu      sync.RWMutex
	sim     *sim.Simulator
	stopRun context.CancelFunc
	resetCh chan struct{} // closed when the simulator is replaced

	mux *http.ServeMux
}

// New creates a server with a fresh simulator and the default participants.
func New(opts Options) *Server {
	s := &Server{opts: opts, mux: http.NewServeMux()}
	s.reset()
	s.routes()
	return s
}

// Close stops the simulator loop.
func (s *Server) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.stopRun != nil {
		s.stopRun()
	}
}

// reset replaces the simulator with a fresh one.
func (s *Server) reset() {
	fresh := sim.New(s.opts.Sim)
	for _, c := range sim.DefaultParticipants(s.opts.Sim.StartPrice) {
		fresh.AddParticipant(c)
	}
	ctx, cancel := context.WithCancel(context.Background())
	go fresh.Run(ctx)

	s.mu.Lock()
	wasRunning := s.opts.Autoplay
	if s.sim != nil {
		wasRunning = s.sim.Running()
		s.stopRun()
		close(s.resetCh)
	}
	s.sim, s.stopRun, s.resetCh = fresh, cancel, make(chan struct{})
	s.mu.Unlock()

	if wasRunning {
		fresh.Play()
	}
}

func (s *Server) current() (*sim.Simulator, chan struct{}) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.sim, s.resetCh
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/healthz" && s.opts.Password != "" {
		_, pw, ok := r.BasicAuth()
		if !ok || subtle.ConstantTimeCompare([]byte(pw), []byte(s.opts.Password)) != 1 {
			w.Header().Set("WWW-Authenticate", `Basic realm="market-sim"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}
	s.mux.ServeHTTP(w, r)
}

func (s *Server) routes() {
	static, _ := fs.Sub(staticFS, "static")
	s.mux.Handle("GET /", http.FileServerFS(static))
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { w.Write([]byte("ok")) })

	s.mux.HandleFunc("GET /api/state", s.handleState)
	s.mux.HandleFunc("GET /api/history", s.handleHistory)
	s.mux.HandleFunc("GET /api/stream", s.handleStream)
	s.mux.HandleFunc("POST /api/control", s.handleControl)

	s.mux.HandleFunc("POST /api/orders", s.handlePlaceOrder)
	s.mux.HandleFunc("DELETE /api/orders/{id}", s.handleCancelOrder)
	s.mux.HandleFunc("DELETE /api/orders", s.handleCancelAll)

	s.mux.HandleFunc("POST /api/participants", s.handleAddParticipant)
	s.mux.HandleFunc("PATCH /api/participants/{id}", s.handleUpdateParticipant)
	s.mux.HandleFunc("DELETE /api/participants/{id}", s.handleRemoveParticipant)
}

// ---- state ----

type participantDTO struct {
	Info    sim.ParticipantInfo   `json:"info"`
	Account engine.AccountSummary `json:"account"`
	Open    int                   `json:"openOrders"`
}

type tradeDTO struct {
	engine.Trade
	Buyer  string `json:"buyer"`
	Seller string `json:"seller"`
}

type stateDTO struct {
	Tick         int64                  `json:"tick"`
	Running      bool                   `json:"running"`
	IntervalMs   int64                  `json:"intervalMs"`
	Mark         int64                  `json:"mark"`
	Last         int64                  `json:"last"`
	Bids         []engine.DepthLevel    `json:"bids"`
	Asks         []engine.DepthLevel    `json:"asks"`
	Maker        *engine.AccountSummary `json:"maker"`
	MakerID      int64                  `json:"makerID"`
	MakerLots    []engine.Lot           `json:"makerLots"`
	MakerOrders  []engine.Order         `json:"makerOrders"`
	Summary      engine.Summary         `json:"summary"`
	Participants []participantDTO       `json:"participants"`
	Trades       []tradeDTO             `json:"trades"`
	Strategies   []sim.StrategyKind     `json:"strategies"`
}

func buildState(sm *sim.Simulator) stateDTO {
	eng := sm.Engine()
	sum := eng.Summarize()
	bids, asks := eng.Depth(12)
	st := stateDTO{
		Tick:        sm.Tick(),
		Running:     sm.Running(),
		IntervalMs:  sm.Interval().Milliseconds(),
		Mark:        sum.Mark,
		Last:        eng.LastPrice(),
		Bids:        nonNil(bids),
		Asks:        nonNil(asks),
		MakerID:     sm.MarketMakerID(),
		MakerOrders: nonNil(eng.OpenOrders(sm.MarketMakerID())),
		Summary:     sum,
		Strategies:  sim.StrategyKinds,
	}

	names := map[int64]string{}
	byID := map[int64]engine.AccountSummary{}
	for _, a := range append(append([]engine.AccountSummary{}, sum.MarketMakers...), sum.Participants...) {
		names[a.ID] = a.Name
		byID[a.ID] = a
	}
	if m, ok := byID[sm.MarketMakerID()]; ok {
		st.Maker = &m
	}
	if a, err := eng.Account(sm.MarketMakerID()); err == nil {
		st.MakerLots = a.Lots
	}
	st.MakerLots = nonNil(st.MakerLots)
	for _, info := range sm.Participants() {
		st.Participants = append(st.Participants, participantDTO{
			Info: info, Account: byID[info.ID], Open: len(eng.OpenOrders(info.ID)),
		})
	}
	st.Participants = nonNil(st.Participants)

	const tape = 40
	last := eng.LastTradeID()
	trades := eng.TradesSince(max(last-tape, 0))
	for i := len(trades) - 1; i >= 0; i-- {
		t := trades[i]
		st.Trades = append(st.Trades, tradeDTO{Trade: t, Buyer: nameOr(names, t.BuyerID), Seller: nameOr(names, t.SellerID)})
	}
	st.Trades = nonNil(st.Trades)
	return st
}

func nameOr(names map[int64]string, id int64) string {
	if n, ok := names[id]; ok {
		return n
	}
	return "(removed)"
}

func nonNil[T any](xs []T) []T {
	if xs == nil {
		return []T{}
	}
	return xs
}

func (s *Server) handleState(w http.ResponseWriter, _ *http.Request) {
	sm, _ := s.current()
	writeJSON(w, http.StatusOK, buildState(sm))
}

func (s *Server) handleHistory(w http.ResponseWriter, r *http.Request) {
	sm, _ := s.current()
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	if n <= 0 || n > 5000 {
		n = 1000
	}
	writeJSON(w, http.StatusOK, nonNil(sm.History(n)))
}

// handleStream sends server-sent events: "tick" with new history points
// and the current state, batched every streamEvery, and "reset" when the
// simulator is replaced.
func (s *Server) handleStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	send := func(event string, v any) bool {
		b, err := json.Marshal(v)
		if err != nil {
			return false
		}
		_, err = w.Write([]byte("event: " + event + "\ndata: " + string(b) + "\n\n"))
		flusher.Flush()
		return err == nil
	}

	ticker := time.NewTicker(streamEvery)
	defer ticker.Stop()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		sm, resetCh := s.current()
		points, unsubscribe := sm.Subscribe()
		var pending []sim.Point
		done := func() bool {
			for {
				select {
				case <-r.Context().Done():
					return true
				case <-resetCh:
					return !send("reset", struct{}{})
				case p, ok := <-points:
					if !ok {
						return false
					}
					pending = append(pending, p)
				case <-ticker.C:
					if len(pending) == 0 {
						continue
					}
					ok := send("tick", map[string]any{"points": pending, "state": buildState(sm)})
					pending = nil
					if !ok {
						return true
					}
				case <-heartbeat.C:
					if _, err := w.Write([]byte(": ping\n\n")); err != nil {
						return true
					}
					flusher.Flush()
				}
			}
		}()
		unsubscribe()
		if done {
			return
		}
	}
}

// ---- control ----

type controlReq struct {
	Action     string `json:"action"` // play, pause, step, reset, speed
	IntervalMs int64  `json:"intervalMs"`
	Steps      int    `json:"steps"`
}

func (s *Server) handleControl(w http.ResponseWriter, r *http.Request) {
	var req controlReq
	if !readJSON(w, r, &req) {
		return
	}
	sm, _ := s.current()
	switch req.Action {
	case "play":
		sm.Play()
	case "pause":
		sm.Pause()
	case "step":
		n := min(max(req.Steps, 1), 1000)
		for i := 0; i < n; i++ {
			sm.Step()
		}
	case "speed":
		sm.SetInterval(time.Duration(min(max(req.IntervalMs, 10), 10_000)) * time.Millisecond)
	case "reset":
		s.reset()
		sm, _ = s.current()
	default:
		writeErr(w, http.StatusBadRequest, errors.New("action must be play, pause, step, speed or reset"))
		return
	}
	writeJSON(w, http.StatusOK, buildState(sm))
}

// ---- market maker orders ----

type orderReq struct {
	Side  engine.Side      `json:"side"`
	Type  engine.OrderType `json:"type"`
	Price int64            `json:"price"` // cents
	Qty   int64            `json:"qty"`
}

type orderResp struct {
	Order  engine.Order   `json:"order"`
	Trades []engine.Trade `json:"trades"`
	State  stateDTO       `json:"state"`
}

func (s *Server) handlePlaceOrder(w http.ResponseWriter, r *http.Request) {
	var req orderReq
	if !readJSON(w, r, &req) {
		return
	}
	if req.Qty > MaxQty || req.Price > MaxPrice {
		writeErr(w, http.StatusBadRequest, errors.New("price or quantity too large"))
		return
	}
	sm, _ := s.current()
	o, trades, err := sm.Engine().Submit(engine.OrderRequest{
		AccountID: sm.MarketMakerID(), Side: req.Side, Type: req.Type, Price: req.Price, Qty: req.Qty,
	})
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, orderResp{Order: o, Trades: nonNil(trades), State: buildState(sm)})
}

func (s *Server) handleCancelOrder(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad order id"))
		return
	}
	sm, _ := s.current()
	if _, err := sm.Engine().Cancel(sm.MarketMakerID(), id); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, buildState(sm))
}

func (s *Server) handleCancelAll(w http.ResponseWriter, _ *http.Request) {
	sm, _ := s.current()
	sm.Engine().CancelAll(sm.MarketMakerID())
	writeJSON(w, http.StatusOK, buildState(sm))
}

// ---- participants ----

func (s *Server) handleAddParticipant(w http.ResponseWriter, r *http.Request) {
	var req sim.ParticipantConfig
	if !readJSON(w, r, &req) {
		return
	}
	if req.Cash > MaxCash || req.Shares > MaxQty || req.Params.MaxQty > MaxQty || len(req.Name) > 40 {
		writeErr(w, http.StatusBadRequest, errors.New("cash, shares, order size or name too large"))
		return
	}
	sm, _ := s.current()
	if len(sm.Participants()) >= MaxParticipants {
		writeErr(w, http.StatusBadRequest, errors.New("too many participants"))
		return
	}
	if _, err := sm.AddParticipant(req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, buildState(sm))
}

func (s *Server) handleUpdateParticipant(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad participant id"))
		return
	}
	var req sim.ParticipantUpdate
	if !readJSON(w, r, &req) {
		return
	}
	if req.Params != nil && req.Params.MaxQty > MaxQty {
		writeErr(w, http.StatusBadRequest, errors.New("order size too large"))
		return
	}
	sm, _ := s.current()
	if _, err := sm.UpdateParticipant(id, req); err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, sim.ErrUnknownParticipant) {
			status = http.StatusNotFound
		}
		writeErr(w, status, err)
		return
	}
	writeJSON(w, http.StatusOK, buildState(sm))
}

func (s *Server) handleRemoveParticipant(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil {
		writeErr(w, http.StatusBadRequest, errors.New("bad participant id"))
		return
	}
	sm, _ := s.current()
	if err := sm.RemoveParticipant(id); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	writeJSON(w, http.StatusOK, buildState(sm))
}

// ---- helpers ----

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
