#include <iostream>
#include <vector>
#include <map>
#include <algorithm>
#include <string>

using namespace std;

enum class Side { BUY, SELL };

struct Order {
    int id;
    string symbol;
    Side side;
    double price;
    int quantity;
    int remaining;
};

class OrderBook {
private:
    // Uses a simple map to sort prices. 
    // BUY: greater<double> for highest bid first.
    map<double, vector<Order>, greater<double>> bids;
    // SELL: less<double> for lowest ask first.
    map<double, vector<Order>, less<double>> asks;

public:
    void addOrder(const Order& order) {
        Order o = order;
        o.remaining = o.quantity;
        matchOrder(o);
        
        if (o.remaining > 0) {
            if (o.side == Side::BUY) {
                bids[o.price].push_back(o);
            } else {
                asks[o.price].push_back(o);
            }
        }
    }

private:
    void matchOrder(Order& newOrder) {
        if (newOrder.side == Side::BUY) {
            auto it = asks.begin();
            while (it != asks.end() && it->first <= newOrder.price && newOrder.remaining > 0) {
                auto& orderList = it->second;
                for (auto o_it = orderList.begin(); o_it != orderList.end() && newOrder.remaining > 0;) {
                    int matchQty = min(newOrder.remaining, o_it->remaining);
                    newOrder.remaining -= matchQty;
                    o_it->remaining -= matchQty;

                    if (o_it->remaining == 0) {
                        o_it = orderList.erase(o_it);
                    } else {
                        ++o_it;
                    }
                }
                if (orderList.empty()) {
                    it = asks.erase(it);
                } else {
                    ++it;
                }
            }
        } else {
            auto it = bids.begin();
            while (it != bids.end() && it->first >= newOrder.price && newOrder.remaining > 0) {
                auto& orderList = it->second;
                for (auto o_it = orderList.begin(); o_it != orderList.end() && newOrder.remaining > 0;) {
                    int matchQty = min(newOrder.remaining, o_it->remaining);
                    newOrder.remaining -= matchQty;
                    o_it->remaining -= matchQty;

                    if (o_it->remaining == 0) {
                        o_it = orderList.erase(o_it);
                    } else {
                        ++o_it;
                    }
                }
                if (orderList.empty()) {
                    it = bids.erase(it);
                } else {
                    ++it;
                }
            }
        }
    }
};