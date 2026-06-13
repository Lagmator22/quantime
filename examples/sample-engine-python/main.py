from fastapi import FastAPI, HTTPException
from pydantic import BaseModel
import uvicorn
import time

app = FastAPI()

class Order(BaseModel):
    id: str
    symbol: str
    side: str
    price: int
    quantity: int

class Orderbook:
    def __init__(self):
        self.bids = []
        self.asks = []

    def add_order(self, order: Order):
        if order.side == "buy":
            self.bids.append(order)
            self.bids.sort(key=lambda x: x.price, reverse=True)
        else:
            self.asks.append(order)
            self.asks.sort(key=lambda x: x.price)
        self.match()

    def match(self):
        while self.bids and self.asks and self.bids[0].price >= self.asks[0].price:
            bid = self.bids[0]
            ask = self.asks[0]
            match_qty = min(bid.quantity, ask.quantity)
            bid.quantity -= match_qty
            ask.quantity -= match_qty
            if bid.quantity == 0:
                self.bids.pop(0)
            if ask.quantity == 0:
                self.asks.pop(0)

book = Orderbook()

@app.post("/submit")
async def submit_order(order: Order):
    book.add_order(order)
    return {"status": "ok", "order_id": order.id}

@app.get("/health")
async def health():
    return {"status": "ok"}

if __name__ == "__main__":
    uvicorn.run(app, host="0.0.0.0", port=9003)
