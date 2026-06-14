package bot

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/valyala/fasthttp"
)

// ReplayMarketData drives the submission engine with REAL market
// microstructure by streaming a LOBSTER-format message file
// (Time,Type,OrderID,Size,Price,Direction) as a single, ordered stream —
// instead of synthetic uniform-random bot flow. This is how quant teams test
// engines against realistic depth, cancel/replace storms, and crossed markets.
//
//   Type:      1 = new limit order, 2/3 = cancel/delete (4/5 executions are
//              skipped — the engine produces fills itself).
//   Direction: 1 = buy (bid), -1 = sell (ask).
//   Price:     LOBSTER units are $*10000; QuanTime ticks are $*100, so /100.
//
// Drop in a real LOBSTER (lobsterdata.com) or ITCH-derived message file at
// REPLAY_FILE to benchmark against an actual trading day. The stream loops
// (with a per-pass id offset to avoid dedup) until the run duration elapses.
func ReplayMarketData(ctx context.Context, nc *nats.Conn, runID, endpoint, file string) {
	cli := &fasthttp.Client{
		MaxConnsPerHost: 256,
		ReadTimeout:     500 * time.Millisecond,
		WriteTimeout:    500 * time.Millisecond,
	}
	url := endpoint + "/submit"
	const replayClientID = 9000 // single replay stream → one stable clientId

	total := 0
	for pass := 0; ; pass++ {
		select {
		case <-ctx.Done():
			fmt.Printf("[replay] run=%s streamed %d market-data messages (%d passes)\n", runID, total, pass)
			return
		default:
		}
		offset := int64(pass) * 10_000_000 // keep ids unique across passes

		f, err := os.Open(file)
		if err != nil {
			fmt.Printf("[replay] open %s: %v\n", file, err)
			return
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 1<<20), 1<<20)
		header := true
		for sc.Scan() {
			select {
			case <-ctx.Done():
				f.Close()
				fmt.Printf("[replay] run=%s streamed %d market-data messages\n", runID, total)
				return
			default:
			}
			line := sc.Text()
			if header {
				header = false
				if strings.HasPrefix(line, "Time") {
					continue
				}
			}
			cols := strings.Split(line, ",")
			if len(cols) < 6 {
				continue
			}
			etype, _ := strconv.Atoi(strings.TrimSpace(cols[1]))
			oid, _ := strconv.ParseInt(strings.TrimSpace(cols[2]), 10, 64)
			size, _ := strconv.ParseInt(strings.TrimSpace(cols[3]), 10, 64)
			priceL, _ := strconv.ParseInt(strings.TrimSpace(cols[4]), 10, 64)
			dir, _ := strconv.Atoi(strings.TrimSpace(cols[5]))

			var typ, side string
			switch etype {
			case 1:
				typ = "limit"
				if dir == 1 {
					side = "buy"
				} else {
					side = "sell"
				}
			case 2, 3:
				typ = "cancel"
			default:
				continue // executions / halts: engine generates these
			}
			id := oid + offset
			px := priceL / 100 // LOBSTER $*10000 → QuanTime tick $*100

			body, _ := json.Marshal(map[string]any{
				"id": id, "clientId": replayClientID, "side": side, "type": typ,
				"price": px, "qty": size, "targetId": id, "ts": time.Now().UnixNano(),
			})

			req := fasthttp.AcquireRequest()
			resp := fasthttp.AcquireResponse()
			req.Header.SetMethod(fasthttp.MethodPost)
			req.Header.SetContentType("application/json")
			req.SetRequestURI(url)
			req.SetBody(body)

			sendTS := time.Now().UnixNano()
			derr := cli.DoTimeout(req, resp, 500*time.Millisecond)
			ackTS := time.Now().UnixNano()
			status := ""
			var errStr *string
			if derr != nil {
				s := derr.Error()
				errStr = &s
			} else {
				var r struct {
					Acks []struct{ Status string } `json:"acks"`
				}
				if json.Unmarshal(resp.Body(), &r) == nil && len(r.Acks) > 0 {
					status = r.Acks[0].Status
				}
			}

			sample := map[string]any{
				"runId": runID, "botId": replayClientID, "orderId": id,
				"side": side, "type": typ, "priceX100": px, "qty": size,
				"sendTs": sendTS, "ackTs": ackTS, "latencyNs": ackTS - sendTS,
				"status": status, "err": errStr,
			}
			buf, _ := json.Marshal(sample)
			_ = nc.Publish(fmt.Sprintf("runs.%s.telemetry", runID), buf)

			fasthttp.ReleaseRequest(req)
			fasthttp.ReleaseResponse(resp)
			total++
		}
		f.Close()
	}
}
