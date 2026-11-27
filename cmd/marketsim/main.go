// Command marketsim serves the market simulation in the browser.
//
// Environment:
//
//	PORT                 listen port (default 8080)
//	MARKETSIM_PASSWORD   if set, require this password (HTTP basic auth)
//	MARKETSIM_AUTOPLAY   "false" to start paused (default: playing)
package main

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/awhsnap/market-sim/sim"
	"github.com/awhsnap/market-sim/web"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}
	srv := web.New(web.Options{
		Password: os.Getenv("MARKETSIM_PASSWORD"),
		Autoplay: os.Getenv("MARKETSIM_AUTOPLAY") != "false",
		Sim:      sim.Config{StartPrice: 10_000},
	})
	defer srv.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	hs := &http.Server{
		Addr:              ":" + port,
		Handler:           srv,
		ReadHeaderTimeout: 10 * time.Second,
		// Request contexts end on shutdown so live streams close promptly.
		BaseContext: func(net.Listener) context.Context { return ctx },
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		hs.Shutdown(shutdown)
	}()

	log.Printf("market-sim listening on :%s", port)
	if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
}
