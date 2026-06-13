from aiohttp import web
import time
async def handle(request):
    start = time.time()
    return web.json_response({"latencyNs": int((time.time()-start)*1e9), "fills": []})
app = web.Application()
app.add_routes([web.post('/order', handle)])
web.run_app(app, port=9001)