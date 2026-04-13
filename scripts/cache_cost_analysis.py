#!/usr/bin/env python3
"""
Cache cost analysis from YesMem proxy logs.
Parses actual cache read/write/uncached data per request,
groups by thread ID, computes time gaps, and simulates
costs under different cache strategies.

Usage: python3 scripts/cache_cost_analysis.py [--day YYYY/MM/DD] [--all-april]
"""
import re
import sys
from datetime import datetime
from collections import defaultdict

LOG_PATH = "/home/chief/.claude/yesmem/logs/proxy.log"

# Anthropic Prompt Cache Pricing (Opus 4.6, $/MTok)
PRICE_READ = 0.50
PRICE_WRITE_5M = 6.25
PRICE_WRITE_1H = 10.00
PRICE_UNCACHED = 5.00
PRICE_OUTPUT = 25.00

# Keepalive ping cost: pure read at average prefix size
# Estimated from logs — ~140k average prefix → $0.07/ping
PING_READ_TOKENS = 140_000

def parse_log(log_path, target_date=None):
    """Parse proxy log in single pass. Handles req_num resets across proxy restarts
    by tracking TID per req_num and emitting events sequentially."""
    cache_re = re.compile(
        r'\[proxy\] (\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}) '
        r'\[req (\d+)\] \[req \d+\] '
        r'in=(\d+) out=(\d+) \| '
        r'cache: (\d+)k read, (\d+)k write, (\d+)k uncached \((\d+)% hit\)'
    )
    tid_re = re.compile(
        r'\[proxy\] (\d{4}/\d{2}/\d{2} \d{2}:\d{2}:\d{2}) '
        r'(?:\x1b\[\d+m|\[[\d;]*m)*'  # skip ANSI escape codes
        r'\[req (\d+) (?:memory|yesmem) tid=([a-f0-9][-a-f0-9]*[a-f0-9])\]'
    )

    # Single-pass: TID lines come before cache lines for same req_num.
    # Track current TID per req_num; emit on cache line.
    current_tids = {}  # req_num -> tid (overwritten each time, valid within one proxy run)
    events = []        # list of {timestamp, tid, in, out, read, write, uncached, hit_pct}

    with open(log_path, 'rb') as f:
        for raw_line in f:
            try:
                line = raw_line.decode('utf-8', errors='replace')
            except:
                continue

            if target_date and target_date not in line:
                continue

            m = tid_re.search(line)
            if m:
                _, req_num, tid = m.groups()
                current_tids[int(req_num)] = tid
                continue

            m = cache_re.search(line)
            if m:
                ts_str, req_num, in_tok, out_tok, read_k, write_k, uncached_k, hit_pct = m.groups()
                req_num = int(req_num)
                tid = current_tids.pop(req_num, None)
                events.append({
                    'timestamp': datetime.strptime(ts_str, '%Y/%m/%d %H:%M:%S'),
                    'date': ts_str[:10],
                    'tid': tid,
                    'in_tokens': int(in_tok),
                    'out_tokens': int(out_tok),
                    'read_tokens': int(read_k) * 1000,
                    'write_tokens': int(write_k) * 1000,
                    'uncached_tokens': int(uncached_k) * 1000,
                    'hit_pct': int(hit_pct),
                })

    return events


def group_by_thread(events):
    """Group events by thread ID, sorted by time."""
    threads = defaultdict(list)
    unmatched = 0
    for event in events:
        tid = event.get('tid')
        if tid:
            threads[tid].append(event)
        else:
            unmatched += 1

    # Sort each thread's events by timestamp
    for tid in threads:
        threads[tid].sort(key=lambda e: e['timestamp'])

    return dict(threads), unmatched


def analyze_gaps(thread_events):
    """Calculate time gaps between consecutive requests in a thread."""
    gaps = []
    for i in range(1, len(thread_events)):
        prev = thread_events[i - 1]['timestamp']
        curr = thread_events[i]['timestamp']
        gap_seconds = (curr - prev).total_seconds()
        gaps.append({
            'gap_seconds': gap_seconds,
            'write_tokens': thread_events[i]['write_tokens'],
            'read_tokens': thread_events[i]['read_tokens'],
            'uncached_tokens': thread_events[i]['uncached_tokens'],
            'total_in': thread_events[i]['in_tokens'],
        })
    return gaps


def compute_actual_cost(events):
    """Compute actual cost from logged cache data."""
    total_read = sum(e['read_tokens'] for e in events)
    total_write = sum(e['write_tokens'] for e in events)
    total_uncached = sum(e['uncached_tokens'] for e in events)
    total_output = sum(e['out_tokens'] for e in events)

    cost_read = total_read / 1_000_000 * PRICE_READ
    cost_write = total_write / 1_000_000 * PRICE_WRITE_5M
    cost_uncached = total_uncached / 1_000_000 * PRICE_UNCACHED
    cost_output = total_output / 1_000_000 * PRICE_OUTPUT

    return {
        'read_tokens': total_read,
        'write_tokens': total_write,
        'uncached_tokens': total_uncached,
        'output_tokens': total_output,
        'cost_read': cost_read,
        'cost_write': cost_write,
        'cost_uncached': cost_uncached,
        'cost_output': cost_output,
        'cost_total': cost_read + cost_write + cost_uncached + cost_output,
        'cost_input_only': cost_read + cost_write + cost_uncached,
    }


def simulate_strategy(thread_events, ttl_seconds, keepalive_pings, ping_interval=270):
    """
    Simulate cache costs using delta-based model (not logged read/write values).

    Physical model per request:
    - delta = in_tokens[i] - in_tokens[i-1] = conversation growth since last turn
    - prefix = in_tokens[i] - delta = reusable cached content
    - Cache HIT: prefix is READ ($0.50/MTok), delta is WRITE ($6.25 or $10/MTok)
    - Cache MISS: everything is WRITE (full rewrite)

    This avoids the bug where logged write values from actual busts
    would be reused in "what if keepalive bridged this" scenarios.
    """
    if ttl_seconds == 300:
        write_price = PRICE_WRITE_5M
    else:
        write_price = PRICE_WRITE_1H

    effective_ttl = ttl_seconds + (keepalive_pings * ping_interval)

    total_cost = 0
    total_output_cost = 0
    cache_busts = 0
    ping_count = 0
    gaps_bridged = 0
    total_read_tokens = 0
    total_write_tokens = 0

    for events in thread_events.values():
        for i, event in enumerate(events):
            in_tok = event['in_tokens']
            out_tok = event['out_tokens']
            total_output_cost += out_tok / 1_000_000 * PRICE_OUTPUT

            if i == 0:
                # First request: always full write
                total_write_tokens += in_tok
                total_cost += in_tok / 1_000_000 * write_price
                continue

            # Delta: how much the conversation grew since last request
            prev_in = events[i - 1]['in_tokens']
            delta = max(0, in_tok - prev_in)
            # If negative (compaction shrank it), treat entire request as new content
            if in_tok <= prev_in:
                delta = in_tok  # compaction: can't reuse prefix
            prefix = in_tok - delta

            gap = (event['timestamp'] - events[i - 1]['timestamp']).total_seconds()

            cache_alive = False
            if gap <= ttl_seconds:
                cache_alive = True
            elif gap <= effective_ttl and keepalive_pings > 0:
                pings_needed = int((gap - ttl_seconds) / ping_interval) + 1
                pings_needed = min(pings_needed, keepalive_pings)
                ping_count += pings_needed
                gaps_bridged += 1
                cache_alive = True

            if cache_alive:
                # Cache hit: prefix is read, delta is written
                read_cost = prefix / 1_000_000 * PRICE_READ
                write_cost = delta / 1_000_000 * write_price
                total_read_tokens += prefix
                total_write_tokens += delta
                total_cost += read_cost + write_cost
            else:
                # Cache miss: full rewrite
                cache_busts += 1
                total_write_tokens += in_tok
                total_cost += in_tok / 1_000_000 * write_price

    cost_pings = ping_count * (PING_READ_TOKENS / 1_000_000 * PRICE_READ)

    return {
        'read_tokens': total_read_tokens,
        'write_tokens': total_write_tokens,
        'cost_input': total_cost + cost_pings,
        'cost_output': total_output_cost,
        'cost_pings': cost_pings,
        'cost_total': total_cost + cost_pings + total_output_cost,
        'cache_busts': cache_busts,
        'ping_count': ping_count,
        'gaps_bridged': gaps_bridged,
    }


def gap_distribution(thread_events):
    """Categorize gaps across all threads."""
    buckets = {
        '0-10s': (0, 10),
        '10-30s': (10, 30),
        '30s-1m': (30, 60),
        '1-5m': (60, 300),
        '5-15m': (300, 900),
        '15-30m': (900, 1800),
        '30-60m': (1800, 3600),
        '1-2h': (3600, 7200),
        '>2h': (7200, float('inf')),
    }

    counts = {k: 0 for k in buckets}
    write_tokens = {k: 0 for k in buckets}

    for events in thread_events.values():
        for i in range(1, len(events)):
            gap = (events[i]['timestamp'] - events[i - 1]['timestamp']).total_seconds()
            wt = events[i]['write_tokens']
            for label, (lo, hi) in buckets.items():
                if lo <= gap < hi:
                    counts[label] += 1
                    write_tokens[label] += wt
                    break

    return counts, write_tokens


def main():
    import argparse
    parser = argparse.ArgumentParser(description='Cache cost analysis from proxy logs')
    parser.add_argument('--day', help='Target date YYYY/MM/DD')
    parser.add_argument('--all-april', action='store_true', help='Analyze all April days')
    parser.add_argument('--all', action='store_true', help='Analyze entire log')
    parser.add_argument('--min-reqs', type=int, default=5, help='Min requests per thread to include')
    parser.add_argument('--no-subagents', action='store_true', help='Exclude subagent threads (<50 reqs AND <1h duration)')
    args = parser.parse_args()

    target_date = args.day
    if args.all_april:
        target_date = '2026/04'
    elif args.all:
        target_date = None

    print(f"Parsing {LOG_PATH}...")
    events = parse_log(LOG_PATH, target_date)
    print(f"  Total events: {len(events)}")
    with_tid = sum(1 for e in events if e.get('tid'))
    print(f"  Events with TID: {with_tid}")

    threads, unmatched = group_by_thread(events)
    print(f"  Threads: {len(threads)} ({unmatched} requests without TID)")

    # Filter subagents: <50 reqs AND <1h duration
    if args.no_subagents:
        before = len(threads)
        filtered = {}
        for tid, evts in threads.items():
            duration_h = (evts[-1]['timestamp'] - evts[0]['timestamp']).total_seconds() / 3600 if len(evts) > 1 else 0
            if len(evts) >= 50 or duration_h >= 1:
                filtered[tid] = evts
        threads = filtered
        print(f"  After subagent filter: {len(threads)} threads (removed {before - len(threads)} subagent threads)")

    # Filter by min requests
    threads = {tid: evts for tid, evts in threads.items() if len(evts) >= args.min_reqs}
    total_reqs = sum(len(e) for e in threads.values())
    print(f"  Threads with >= {args.min_reqs} reqs: {len(threads)} ({total_reqs} requests)")

    # --- Actual costs (TID-matched events only, excludes subagents) ---
    tid_events = [e for events_list in threads.values() for e in events_list]
    print(f"\n{'='*70}")
    print(f"ACTUAL COSTS (TID-matched sessions only, {len(tid_events)} events)")
    print(f"{'='*70}")

    all_events = tid_events
    actual = compute_actual_cost(all_events)

    print(f"  Read:      {actual['read_tokens']/1e6:8.1f}M tokens  ${actual['cost_read']:8.2f}")
    print(f"  Write:     {actual['write_tokens']/1e6:8.1f}M tokens  ${actual['cost_write']:8.2f}")
    print(f"  Uncached:  {actual['uncached_tokens']/1e6:8.1f}M tokens  ${actual['cost_uncached']:8.2f}")
    print(f"  Output:    {actual['output_tokens']/1e6:8.1f}M tokens  ${actual['cost_output']:8.2f}")
    print(f"  ---")
    print(f"  Input cost: ${actual['cost_input_only']:8.2f}")
    print(f"  Total cost: ${actual['cost_total']:8.2f}")

    # --- Gap distribution ---
    print(f"\n{'='*70}")
    print(f"GAP DISTRIBUTION (time between consecutive requests per thread)")
    print(f"{'='*70}")

    gap_counts, gap_writes = gap_distribution(threads)
    total_gaps = sum(gap_counts.values())
    for label in gap_counts:
        c = gap_counts[label]
        w = gap_writes[label]
        pct = (c / total_gaps * 100) if total_gaps > 0 else 0
        print(f"  {label:>8s}: {c:5d} gaps ({pct:5.1f}%)  write: {w/1e6:.1f}M tokens")

    # Count gaps that would cause cache miss at 5min
    gaps_over_5m = sum(v for k, v in gap_counts.items() if k in ['5-15m', '15-30m', '30-60m', '1-2h', '>2h'])
    gaps_over_1h = sum(v for k, v in gap_counts.items() if k in ['1-2h', '>2h'])
    print(f"\n  Gaps > 5min: {gaps_over_5m} ({gaps_over_5m/total_gaps*100:.1f}% of all gaps)")
    print(f"  Gaps > 1h:   {gaps_over_1h} ({gaps_over_1h/total_gaps*100:.1f}% of all gaps)")

    # --- Strategy comparison ---
    print(f"\n{'='*70}")
    print(f"STRATEGY COMPARISON (simulated)")
    print(f"{'='*70}")

    strategies = [
        ("5min no keepalive",    300,  0),
        ("5min + 3 pings (~14m)", 300,  3),
        ("5min + 5 pings (~24m)", 300,  5),
        ("5min + 6 pings (~27m)", 300,  6),
        ("5min + 9 pings (~40m)", 300,  9),
        ("5min + 12 pings (~54m)", 300, 12),
        ("1h no keepalive",      3600, 0),
        ("1h + 6 pings",         3600, 6),
    ]

    results = []
    for name, ttl, pings in strategies:
        sim = simulate_strategy(threads, ttl, pings)
        results.append((name, sim))

    # Sort by input cost
    results.sort(key=lambda r: r[1]['cost_input'])

    print(f"\n  {'Strategy':<26s} {'Input Cost':>10s} {'Busts':>6s} {'Pings':>6s} {'Bridged':>8s} {'vs Best':>10s}")
    print(f"  {'-'*26} {'-'*10} {'-'*6} {'-'*6} {'-'*8} {'-'*10}")
    best_cost = results[0][1]['cost_input']
    for name, sim in results:
        delta = sim['cost_input'] - best_cost
        delta_str = f"+${delta:.2f}" if delta > 0 else "baseline"
        print(f"  {name:<26s} ${sim['cost_input']:>8.2f} {sim['cache_busts']:>6d} {sim['ping_count']:>6d} {sim['gaps_bridged']:>8d} {delta_str:>10s}")

    # --- Per-thread breakdown (top 10 by request count) ---
    print(f"\n{'='*70}")
    print(f"PER-THREAD BREAKDOWN (top 10 by request count)")
    print(f"{'='*70}")

    sorted_threads = sorted(threads.items(), key=lambda x: len(x[1]), reverse=True)[:10]
    print(f"\n  {'TID':<18s} {'Reqs':>5s} {'Read':>8s} {'Write':>8s} {'Gaps>5m':>8s} {'Gaps>1h':>8s} {'Duration':>10s}")
    print(f"  {'-'*18} {'-'*5} {'-'*8} {'-'*8} {'-'*8} {'-'*8} {'-'*10}")

    for tid, events in sorted_threads:
        total_read = sum(e['read_tokens'] for e in events) / 1e6
        total_write = sum(e['write_tokens'] for e in events) / 1e6
        gaps_5m = 0
        gaps_1h = 0
        for i in range(1, len(events)):
            gap = (events[i]['timestamp'] - events[i - 1]['timestamp']).total_seconds()
            if gap > 300:
                gaps_5m += 1
            if gap > 3600:
                gaps_1h += 1
        duration = events[-1]['timestamp'] - events[0]['timestamp']
        hours = duration.total_seconds() / 3600
        print(f"  {tid[:16]:<18s} {len(events):>5d} {total_read:>7.1f}M {total_write:>7.1f}M {gaps_5m:>8d} {gaps_1h:>8d} {hours:>8.1f}h")

    # --- Per-day breakdown ---
    if not args.day:
        print(f"\n{'='*70}")
        print(f"PER-DAY BREAKDOWN")
        print(f"{'='*70}")

        day_events = defaultdict(list)
        for events in threads.values():
            for e in events:
                day_events[e['date']].append(e)

        print(f"\n  {'Date':<12s} {'Reqs':>6s} {'Read':>8s} {'Write':>8s} {'Uncached':>9s} {'Input$':>9s}")
        print(f"  {'-'*12} {'-'*6} {'-'*8} {'-'*8} {'-'*9} {'-'*9}")

        for date in sorted(day_events.keys()):
            evts = day_events[date]
            read = sum(e['read_tokens'] for e in evts) / 1e6
            write = sum(e['write_tokens'] for e in evts) / 1e6
            uncached = sum(e['uncached_tokens'] for e in evts) / 1e6
            cost = read * PRICE_READ + write * PRICE_WRITE_5M + uncached * PRICE_UNCACHED
            print(f"  {date:<12s} {len(evts):>6d} {read:>7.1f}M {write:>7.1f}M {uncached:>8.1f}M ${cost:>8.2f}")


if __name__ == '__main__':
    main()
