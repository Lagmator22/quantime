// =====================================================================
// QuanTime adapter for Liquibook — Object Computing's canonical
// open-source C++ matching engine (github.com/objectcomputing/liquibook,
// ~1.5k stars, "the heart of every financial exchange", 2M+ ops/sec).
// ---------------------------------------------------------------------
// Wraps Liquibook's SimpleOrderBook behind the QuanTime contract so the
// platform can stress-test and score a real production-grade engine:
//
//   POST /submit  {id,clientId,side,type,price,qty,targetId}
//                 -> {acks:[{id,status,filled}], fills:[...]}
//   GET  /healthz -> 200
//
// filled = sum of on_fill quantities for the inbound order. Market orders
// map to Liquibook's price=0 (MARKET_ORDER_PRICE). Prices are integer
// ticks. A mutex serialises the (non-thread-safe) book.
// =====================================================================
#include "httplib.h"
#include <string>
#include <unordered_map>
#include <mutex>
#include <cctype>
#include <cstdint>
#include "simple/simple_order.h"
#include "simple/simple_order_book.h"

using namespace httplib;
using liquibook::simple::SimpleOrder;
typedef liquibook::simple::SimpleOrderBook<> Book;

static long long getInt(const std::string& b, const std::string& key) {
    size_t p = b.find("\"" + key + "\"");
    if (p == std::string::npos) return 0;
    p = b.find(':', p);
    if (p == std::string::npos) return 0;
    p++;
    while (p < b.size() && (b[p] == ' ' || b[p] == '"')) p++;
    bool neg = false;
    if (p < b.size() && b[p] == '-') { neg = true; p++; }
    long long v = 0; bool any = false;
    while (p < b.size() && isdigit((unsigned char)b[p])) { v = v * 10 + (b[p] - '0'); p++; any = true; }
    if (!any) return 0;
    return neg ? -v : v;
}
static std::string getStr(const std::string& b, const std::string& key) {
    size_t p = b.find("\"" + key + "\"");
    if (p == std::string::npos) return "";
    p = b.find(':', p); if (p == std::string::npos) return "";
    p = b.find('"', p); if (p == std::string::npos) return "";
    p++;
    size_t e = b.find('"', p); if (e == std::string::npos) return "";
    return b.substr(p, e - p);
}

// Captures the inbound (aggressor) order's total filled quantity.
struct FillCapture : public liquibook::book::OrderListener<SimpleOrder*> {
    SimpleOrder* inbound = nullptr;
    uint64_t filled = 0;
    void on_accept(SimpleOrder* const& o) override { (void)o; }
    void on_reject(SimpleOrder* const& o, const char* r) override { (void)o; (void)r; }
    void on_fill(SimpleOrder* const& o, SimpleOrder* const& m,
                 liquibook::book::Quantity q, liquibook::book::Price p) override {
        (void)m; (void)p;
        if (o == inbound) filled += q;
    }
    void on_cancel(SimpleOrder* const& o) override { (void)o; }
    void on_cancel_reject(SimpleOrder* const& o, const char* r) override { (void)o; (void)r; }
    void on_replace(SimpleOrder* const& o, const int64_t& d, liquibook::book::Price np) override { (void)o; (void)d; (void)np; }
    void on_replace_reject(SimpleOrder* const& o, const char* r) override { (void)o; (void)r; }
};

Book book;
FillCapture listener;
std::mutex mtx;
std::unordered_map<int64_t, SimpleOrder*> byId; // id -> order (cancel lookup + lifetime)

int main() {
    book.set_order_listener(&listener);
    Server svr;

    svr.Post("/submit", [](const Request& req, Response& res) {
        const std::string& b = req.body;
        long long id = getInt(b, "id");
        std::string side = getStr(b, "side");
        std::string type = getStr(b, "type");
        long long price = getInt(b, "price");
        long long qty = getInt(b, "qty");
        long long target = getInt(b, "targetId");

        std::string status;
        long long filled = 0;
        {
            std::lock_guard<std::mutex> lk(mtx);
            if (type == "cancel") {
                auto it = byId.find(target);
                if (it != byId.end()) { book.cancel(it->second); status = "cancelled"; }
                else status = "cancel-miss";
            } else if (side != "buy" && side != "sell") {
                status = "rejected";
            } else if (qty <= 0) {
                status = "rejected";
            } else {
                bool isBuy = (side == "buy");
                uint32_t px = (type == "market") ? 0 : (uint32_t)price; // 0 = MARKET_ORDER_PRICE
                SimpleOrder* o = new SimpleOrder(isBuy, px, (uint32_t)qty);
                listener.inbound = o;
                listener.filled = 0;
                book.add(o, 0);
                filled = (long long)listener.filled;
                byId[id] = o; // keep alive while resting (bounded leak per run)
                status = (filled == qty) ? "filled" : (filled > 0 ? "partial" : (px == 0 ? "unfilled" : "resting"));
            }
        }
        std::string out = "{\"acks\":[{\"id\":" + std::to_string(id) + ",\"status\":\"" + status +
                          "\",\"filled\":" + std::to_string(filled) + "}],\"fills\":[]}";
        res.set_content(out, "application/json");
    });

    auto health = [](const Request&, Response& res) { res.set_content("{\"status\":\"ok\"}", "application/json"); };
    svr.Get("/healthz", health);
    svr.Get("/health", health);

    svr.listen("0.0.0.0", 9005);
}
