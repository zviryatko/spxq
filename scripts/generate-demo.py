#!/usr/bin/env python3
"""Generate a deterministic, entirely synthetic SPX report for docs and smoke tests."""
import gzip
import json
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
functions = []
ids = {}
events = []
clock = 0
memory = 0
calls = 0


def emit(name, own, children=(), repeat=1, retained=0):
    global clock, memory, calls
    fid = ids.setdefault(name, len(ids))
    if fid == len(functions):
        functions.append(name)
    for _ in range(repeat):
        calls += 1
        events.append(f"{fid} 1 {clock} {memory}")
        clock += own // 2
        memory += retained
        for child in children:
            emit(**child)
        clock += own - own // 2
        events.append(f"{fid} 0 {clock} {memory}")


def call(name, own, children=(), repeat=1, retained=0):
    return dict(name=name, own=own, children=children, repeat=repeat, retained=retained)


emit("/srv/shop/public/index.php", 4000, [
    call(r"App\Kernel::handle", 3000, [
        call(r"App\Http\Middleware\Authenticate::handle", 8500),
        call(r"App\Routing\Router::dispatch", 2500, [
            call(r"App\Http\Controllers\ProductController::show", 8000, [
                call(r"App\Catalog\CatalogService::load", 19000, [
                    call(r"App\Search\SearchClient::query", 320000, retained=262144),
                    call(r"App\Database\ProductRepository::find", 2400, [
                        call(r"PDOStatement::execute", 11800),
                        call(r"App\Database\Hydrator::hydrate", 18, repeat=120, retained=128),
                    ], repeat=24),
                    call(r"App\Cache\RedisCache::get", 480, repeat=36),
                    call(r"App\Catalog\PriceCalculator::calculate", 800, repeat=24),
                    call(r"App\Catalog\Inventory::available", 1300, repeat=24),
                    call(r"App\Catalog\CategoryTree::ancestors", 640, repeat=24),
                    call(r"App\Catalog\ImageResolver::resolve", 720, repeat=24),
                    call(r"App\Catalog\AttributeBag::normalize", 340, repeat=24),
                    call(r"App\Catalog\ProductSerializer::serialize", 560, repeat=24),
                ], retained=131072),
                call(r"App\Recommendations\RecommendationClient::fetch", 218000, retained=32768),
                call(r"Twig\Environment::render", 12500, [
                    call("/srv/shop/templates/product/detail.php", 14000, [
                        call(r"Twig\Extension\CoreExtension::escape", 42, repeat=260),
                        call(r"App\View\CurrencyFormatter::format", 190, repeat=24),
                    ]),
                ], retained=65536),
                call(r"App\Http\Response::send", 18000),
                call(r"App\Telemetry\Metrics::flush", 9500),
            ]),
        ]),
        call(r"App\Http\Middleware\RequestLog::terminate", 2600),
    ]),
])
body = "[events]\n" + "\n".join(events) + "\n[functions]\n" + "\n".join(functions) + "\n"
metadata = dict(
    key="demo", exec_ts=1789049100, host_name="shop-web-01", cli=0,
    http_request_uri="/products/field-jacket", http_method="GET",
    http_host="shop.example", custom_metadata_str='{"route":"product.show","cache":"warm"}',
    wall_time_ms=clock / 1000, peak_memory_usage=memory,
    called_function_count=len(functions), call_count=calls, recorded_call_count=calls,
    enabled_metrics=["wt", "zm"],
)
(ROOT / "examples").mkdir(exist_ok=True)
(ROOT / "examples/demo.json").write_text(json.dumps(metadata, indent=2) + "\n")
(ROOT / "examples/demo.txt.gz").write_bytes(gzip.compress(body.encode(), mtime=0))
print(f"Synthetic demo: {calls:,} calls, {len(functions)} functions, {clock / 1e6:.3f}s")
