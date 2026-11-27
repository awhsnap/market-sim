package web

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/awhsnap/market-sim/sim"
)

func newTestServer(t *testing.T, opts Options) *httptest.Server {
	t.Helper()
	if opts.Sim.Seed == 0 {
		opts.Sim.Seed = 1
	}
	s := New(opts)
	ts := httptest.NewServer(s)
	t.Cleanup(func() { ts.Close(); s.Close() })
	return ts
}

func do(t *testing.T, ts *httptest.Server, method, path, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(method, ts.URL+path, strings.NewReader(body))
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func TestStaticAndHealth(t *testing.T) {
	ts := newTestServer(t, Options{})
	for _, p := range []string{"/", "/app.js", "/style.css", "/healthz"} {
		res, err := http.Get(ts.URL + p)
		if err != nil || res.StatusCode != 200 {
			t.Errorf("GET %s: %v %v", p, err, res.StatusCode)
		}
		res.Body.Close()
	}
}

func TestMarketMakerOrdersAndControl(t *testing.T) {
	ts := newTestServer(t, Options{})

	code, st := do(t, ts, "POST", "/api/control", `{"action":"step","steps":50}`)
	if code != 200 || st["tick"].(float64) != 50 {
		t.Fatalf("step: %d %v", code, st["tick"])
	}

	code, res := do(t, ts, "POST", "/api/orders", `{"side":"sell","type":"limit","price":20000,"qty":5}`)
	if code != 200 {
		t.Fatalf("place: %d %v", code, res)
	}
	order := res["order"].(map[string]any)
	if order["status"] != "open" {
		t.Errorf("order status %v", order["status"])
	}
	id := int(order["id"].(float64))
	mm := res["state"].(map[string]any)["makerOrders"].([]any)
	if len(mm) != 1 {
		t.Fatalf("maker orders %v", mm)
	}

	code, _ = do(t, ts, "DELETE", "/api/orders/"+itoa(id), "")
	if code != 200 {
		t.Errorf("cancel: %d", code)
	}
	code, _ = do(t, ts, "DELETE", "/api/orders/"+itoa(id), "")
	if code != 404 {
		t.Errorf("second cancel: %d", code)
	}

	// Market buy as market maker trades against participant offers.
	code, res = do(t, ts, "POST", "/api/orders", `{"side":"buy","type":"market","qty":3}`)
	if code != 200 || len(res["trades"].([]any)) == 0 {
		t.Errorf("market buy: %d %v", code, res["trades"])
	}

	for _, bad := range []string{
		`{"side":"up","type":"limit","price":1,"qty":1}`,
		`{"side":"buy","type":"limit","price":0,"qty":1}`,
		`{"side":"buy","type":"limit","price":1,"qty":99999999}`,
		`{"side":"buy","type":"limit","price":1,"qty":1,"extra":true}`,
	} {
		if code, _ := do(t, ts, "POST", "/api/orders", bad); code != 400 {
			t.Errorf("%s: code %d", bad, code)
		}
	}

	if code, _ := do(t, ts, "POST", "/api/control", `{"action":"fly"}`); code != 400 {
		t.Errorf("bad action: %d", code)
	}
	code, st = do(t, ts, "POST", "/api/control", `{"action":"reset"}`)
	if code != 200 || st["tick"].(float64) != 0 {
		t.Errorf("reset: %d %v", code, st["tick"])
	}
}

func TestParticipantsAPI(t *testing.T) {
	ts := newTestServer(t, Options{})
	code, st := do(t, ts, "POST", "/api/participants",
		`{"name":"Whale","strategy":"momentum","permission":"buy_only","cash":100000000,"params":{"activity":0.9,"aggression":0.5,"maxQty":20}}`)
	if code != 200 {
		t.Fatalf("add: %d %v", code, st)
	}
	ps := st["participants"].([]any)
	last := ps[len(ps)-1].(map[string]any)["info"].(map[string]any)
	if last["name"] != "Whale" || last["permission"] != "buy_only" || last["strategy"] != "momentum" {
		t.Fatalf("added %v", last)
	}
	id := itoa(int(last["id"].(float64)))

	code, st = do(t, ts, "PATCH", "/api/participants/"+id, `{"permission":"sell_only","paused":true}`)
	if code != 200 {
		t.Fatalf("patch: %d %v", code, st)
	}
	if code, _ := do(t, ts, "PATCH", "/api/participants/"+id, `{"permission":"sideways"}`); code != 400 {
		t.Errorf("bad permission: %d", code)
	}
	if code, _ := do(t, ts, "POST", "/api/participants", `{"strategy":"psychic"}`); code != 400 {
		t.Errorf("bad strategy: %d", code)
	}
	if code, _ := do(t, ts, "DELETE", "/api/participants/"+id, ""); code != 200 {
		t.Errorf("delete: %d", code)
	}
	if code, _ := do(t, ts, "DELETE", "/api/participants/"+id, ""); code != 404 {
		t.Errorf("delete again: %d", code)
	}
}

func TestPassword(t *testing.T) {
	ts := newTestServer(t, Options{Password: "hunter2"})
	res, _ := http.Get(ts.URL + "/api/state")
	res.Body.Close()
	if res.StatusCode != 401 {
		t.Errorf("no auth: %d", res.StatusCode)
	}
	req, _ := http.NewRequest("GET", ts.URL+"/api/state", nil)
	req.SetBasicAuth("anyone", "hunter2")
	res, _ = http.DefaultClient.Do(req)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Errorf("with auth: %d", res.StatusCode)
	}
	res, _ = http.Get(ts.URL + "/healthz")
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Errorf("healthz should be open: %d", res.StatusCode)
	}
}

func TestStream(t *testing.T) {
	ts := newTestServer(t, Options{Autoplay: true, Sim: sim.Config{Seed: 3, Interval: 10 * time.Millisecond}})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", ts.URL+"/api/stream", nil)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}

	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	gotTick := false
	resetSent := false
	for sc.Scan() {
		line := sc.Text()
		if line == "event: tick" && !gotTick {
			gotTick = true
			// A reset should be announced on the open stream.
			go http.Post(ts.URL+"/api/control", "application/json", strings.NewReader(`{"action":"reset"}`))
			resetSent = true
		}
		if line == "event: reset" && resetSent {
			return
		}
	}
	t.Fatalf("stream ended early: tick=%v reset=%v err=%v", gotTick, resetSent, sc.Err())
}

func itoa(n int) string {
	b, _ := json.Marshal(n)
	return string(b)
}
