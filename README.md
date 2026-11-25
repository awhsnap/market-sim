# market-sim

A single-asset market simulation that runs in the browser.

- One asset traded through a central limit order book (limit and market orders, price-time priority).
- Participants with their own trading logic, optionally restricted to buy only or sell only, added on the fly.
- You act as the market maker with unlimited capital and can trade against anyone in the book.
- Open positions are tracked as FIFO lots with realized and unrealized profit or loss.
- Live price chart and a capital view of participants versus the market maker.

Built with Go (standard library only) and plain HTML, CSS and JavaScript. Deployed on fly.io.

## Layout

| Path | What it is |
| --- | --- |
| `engine/` | Order book, matching, accounts and FIFO position accounting |
| `sim/` | Participants and their strategies, and the tick loop that drives the market |

## How the price moves

There is no external price feed or random walk. The price is the last trade, and trades only happen when participants or the market maker cross the spread. Participants quote around the book mid (or the last trade when one side is empty), so buy pressure lifts the price and sell pressure pushes it down.

Built-in participant strategies:

- `random`: noise trader, random side each time it acts.
- `momentum`: buys when the short moving average is above the long one, sells when below.
- `meanrev`: buys when price is below its long moving average, sells when above.

Any participant can be buy only, sell only or both, and can be added, changed, paused or removed while the sim runs.

## Development

```sh
go test ./...
```

Prices are integer cents and quantities are whole units, so the engine never uses floating point.
