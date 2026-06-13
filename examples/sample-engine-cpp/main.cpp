#include "httplib.h"
#include <iostream>
#include <string>
#include <vector>
#include <map>
#include <algorithm>

using namespace std;
using namespace httplib;

struct Order {
    string id;
    string symbol;
    string side;
    int price;
    int quantity;
};

class Orderbook {
public:
    vector<Order> bids;
    vector<Order> asks;

    void add_order(const Order& order) {
        if (order.side == "buy") {
            bids.push_back(order);
            sort(bids.begin(), bids.end(), [](const Order& a, const Order& b) {
                return a.price > b.price;
            });
        } else {
            asks.push_back(order);
            sort(asks.begin(), asks.end(), [](const Order& a, const Order& b) {
                return a.price < b.price;
            });
        }
        match();
    }

    void match() {
        while (!bids.empty() && !asks.empty() && bids.front().price >= asks.front().price) {
            auto& bid = bids.front();
            auto& ask = asks.front();
            int match_qty = min(bid.quantity, ask.quantity);
            bid.quantity -= match_qty;
            ask.quantity -= match_qty;
            
            if (bid.quantity == 0) bids.erase(bids.begin());
            if (ask.quantity == 0) asks.erase(asks.begin());
        }
    }
};

Orderbook book;

int main() {
    Server svr;

    svr.Post("/submit", [](const Request& req, Response& res) {
        // Very basic json parsing for performance simulation
        string body = req.body;
        Order order;
        order.id = "1";
        order.symbol = "AAPL";
        order.side = body.find("\"side\":\"buy\"") != string::npos ? "buy" : "sell";
        order.price = 100; // Mock parsing
        order.quantity = 10;
        
        book.add_order(order);
        
        res.set_content(R"({"status":"ok"})", "application/json");
    });

    svr.Get("/health", [](const Request& req, Response& res) {
        res.set_content(R"({"status":"ok"})", "application/json");
    });

    cout << "C++ Engine listening on port 9002..." << endl;
    svr.listen("0.0.0.0", 9002);
}
