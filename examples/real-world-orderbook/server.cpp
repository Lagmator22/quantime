// =====================================================================
// QuanTime adapter for the timothewt open-source C++ order book.
// ---------------------------------------------------------------------
// Wraps a REAL third-party matching engine (src/Book.h) behind the
// QuanTime contract so the platform can stress-test and score it like
// any other submission:
//
//   POST /submit  {id,clientId,side,type,price,qty,targetId,ts}
//                 -> {acks:[{id,status,filled}], fills:[...]}
//   GET  /healthz -> 200
//
// filled = sum of trade volumes returned by Book::place_order. Market
// orders are mapped to aggressively-priced limits so they cross. Prices
// are integer ticks. A mutex serialises the (non-thread-safe) book.
// =====================================================================
#include "httplib.h"
#include <string>
#include <memory>
#include <mutex>
#include <cctype>
#include <cstdint>
#include "src/Book.h"
#include "src/Order.h"
#include "src/Types.h"
#include "src/Trade.h"

using namespace std;
using namespace httplib;

static long long getInt(const string& b, const string& key) {
    size_t p = b.find("\"" + key + "\"");
    if (p == string::npos) return 0;
    p = b.find(':', p);
    if (p == string::npos) return 0;
    p++;
    while (p < b.size() && (b[p] == ' ' || b[p] == '"')) p++;
    bool neg = false;
    if (p < b.size() && b[p] == '-') { neg = true; p++; }
    long long v = 0; bool any = false;
    while (p < b.size() && isdigit((unsigned char)b[p])) { v = v * 10 + (b[p] - '0'); p++; any = true; }
    if (!any) return 0;
    return neg ? -v : v;
}
static string getStr(const string& b, const string& key) {
    size_t p = b.find("\"" + key + "\"");
    if (p == string::npos) return "";
    p = b.find(':', p); if (p == string::npos) return "";
    p = b.find('"', p); if (p == string::npos) return "";
    p++;
    size_t e = b.find('"', p); if (e == string::npos) return "";
    return b.substr(p, e - p);
}

Book book;
std::mutex mtx;

int main() {
    Server svr;

    svr.Post("/submit", [](const Request& req, Response& res) {
        const string& body = req.body;
        long long id = getInt(body, "id");
        long long agent = getInt(body, "clientId");
        string side = getStr(body, "side");
        string type = getStr(body, "type");
        long long price = getInt(body, "price");
        long long qty = getInt(body, "qty");

        string status;
        long long filled = 0;

        if (type == "cancel") {
            // The third-party book exposes no public cancel through this
            // adapter; report a miss honestly (QuanTime scores it as-is).
            status = "cancel-miss";
        } else if (side != "buy" && side != "sell") {
            status = "rejected";
        } else if (qty <= 0) {
            status = "rejected";
        } else {
            OrderType ot = (side == "buy") ? BUY : SELL;
            Price px;
            if (type == "market") px = (ot == BUY) ? UINT32_MAX : 1; // aggressive crossing limit
            else px = (Price)price;
            OrderPointer order = std::make_shared<Order>((ID)id, (ID)agent, ot, px, (Volume)qty);
            Trades trades;
            {
                std::lock_guard<std::mutex> lock(mtx);
                trades = book.place_order(order);
            }
            for (size_t i = 0; i < trades.size(); ++i) filled += (long long)trades[i].get_volume();
            status = (filled == qty) ? "filled" : (filled > 0 ? "partial" : "resting");
        }

        string out = "{\"acks\":[{\"id\":" + to_string(id) + ",\"status\":\"" + status +
                     "\",\"filled\":" + to_string(filled) + "}],\"fills\":[]}";
        res.set_content(out, "application/json");
    });

    auto health = [](const Request&, Response& res) { res.set_content(R"({"status":"ok"})", "application/json"); };
    svr.Get("/healthz", health);
    svr.Get("/health", health);

    svr.listen("0.0.0.0", 9004);
}
