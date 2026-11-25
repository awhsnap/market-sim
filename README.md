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

## Development

```sh
go test ./...
```

Prices are integer cents and quantities are whole units, so the engine never uses floating point.
