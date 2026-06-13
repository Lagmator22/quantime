package main
import ("encoding/json"; "net/http"; "time")
func main() {
    http.HandleFunc("/order", func(w http.ResponseWriter, r *http.Request) {
        start := time.Now()
        json.NewEncoder(w).Encode(map[string]interface{}{"latencyNs": time.Since(start).Nanoseconds(), "fills": []interface{}{}})
    })
    http.ListenAndServe(":9001", nil)
}