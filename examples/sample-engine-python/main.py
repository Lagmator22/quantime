# =====================================================================
# SAMPLE DEVELOPER SUBMISSION · Python matching engine
# ---------------------------------------------------------------------
# A real price-time-priority CLOB that speaks the QuanTime contract, so
# the platform can benchmark engines written in ANY language behind a
# Dockerfile + POST /submit. Python is intentionally not as fast as the
# Go reference — QuanTime measures that speed difference honestly.
#
#   POST /submit  body {id,clientId,side,type,price,qty,targetId,ts}
#                 resp {acks:[{id,status,filled,...}], fills:[...]}
#   GET  /healthz -> 200   (the gateway sandbox polls this before deploy)
#
# Prices are integer ticks (price*100), matching the bot fleet + telemetry.
# =====================================================================
from fastapi import FastAPI, Request
import uvicorn
import time

app = FastAPI()


class Resting:
    __slots__ = ("id", "px", "qty", "seq")

    def __init__(self, oid, px, qty, seq):
        self.id, self.px, self.qty, self.seq = oid, px, qty, seq


class Book:
    """Price-time priority: best price first, FIFO (by seq) within a level."""

    def __init__(self):
        self.bids = []      # best (highest px) first
        self.asks = []      # best (lowest px) first
        self.by_id = {}     # id -> Resting (for cancel)
        self.seen = set()   # (clientId, id) dedup
        self.seq = 0

    def apply(self, o):
        otype = o.get("type", "limit")
        oid = int(o.get("id", 0) or 0)
        cid = int(o.get("clientId", 0) or 0)
        side = o.get("side", "")
        px = int(o.get("price", 0) or 0)
        qty = int(o.get("qty", 0) or 0)
        target = int(o.get("targetId", 0) or 0)

        if otype == "cancel":
            r = self.by_id.get(target)
            if r is None:
                return {"id": oid, "status": "cancel-miss", "targetId": target}, []
            self._remove(r)
            return {"id": oid, "status": "cancelled", "targetId": target}, []

        # Validate
        if side not in ("buy", "sell"):
            return {"id": oid, "status": "rejected", "reason": "bad-side"}, []
        if qty <= 0:
            return {"id": oid, "status": "rejected", "reason": "bad-qty"}, []
        if otype != "market" and px <= 0:
            return {"id": oid, "status": "rejected", "reason": "bad-price"}, []
        key = (cid, oid)
        if oid != 0 and key in self.seen:
            return {"id": oid, "status": "rejected", "reason": "duplicate-id"}, []
        if oid != 0:
            self.seen.add(key)

        filled = 0
        rem = qty
        fills = []
        opp = self.asks if side == "buy" else self.bids
        while opp and rem > 0:
            best = opp[0]
            if otype == "limit":
                if side == "buy" and best.px > px:
                    break
                if side == "sell" and best.px < px:
                    break
            take = rem if rem < best.qty else best.qty
            filled += take
            rem -= take
            best.qty -= take
            fills.append({"id": best.id, "price": best.px, "qty": take,
                          "takerId": oid, "makerId": best.id, "ts": time.time_ns()})
            if best.qty == 0:
                self._remove(best)

        # Residual rests (limit only; preserves price-time priority via seq)
        if rem > 0 and otype == "limit":
            self.seq += 1
            r = Resting(oid, px, rem, self.seq)
            self.by_id[oid] = r
            if side == "buy":
                self.bids.append(r)
                self.bids.sort(key=lambda x: (-x.px, x.seq))
            else:
                self.asks.append(r)
                self.asks.sort(key=lambda x: (x.px, x.seq))

        if filled == qty:
            status = "filled"
        elif filled > 0:
            status = "partial"
        else:
            status = "resting" if otype == "limit" else "unfilled"
        return {"id": oid, "status": status, "filled": filled, "remaining": rem}, fills

    def _remove(self, r):
        self.by_id.pop(r.id, None)
        if r in self.bids:
            self.bids.remove(r)
        if r in self.asks:
            self.asks.remove(r)


book = Book()


@app.post("/submit")
async def submit(request: Request):
    o = await request.json()
    ack, fills = book.apply(o)
    return {"acks": [ack], "fills": fills}


@app.get("/healthz")
async def healthz():
    return {"status": "ok"}


@app.get("/health")
async def health():
    return {"status": "ok"}


if __name__ == "__main__":
    uvicorn.run(app, host="0.0.0.0", port=9003)
