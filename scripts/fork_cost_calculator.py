#!/usr/bin/env python3
"""Forked Agent Token Cost Calculator.

Berechnet die Kosten eines Forked Agent Calls basierend auf
Anthropic Prompt Caching Pricing (Stand März 2026).
"""

# Anthropic Pricing per Million Tokens (USD)
PRICING = {
    "claude-sonnet-4-6": {
        "input":          3.00,
        "cache_read":     0.30,   # 10% of input
        "cache_write":    3.75,   # 125% of input
        "output":        15.00,
    },
    "claude-haiku-4-5": {
        "input":          0.80,
        "cache_read":     0.08,
        "cache_write":    1.00,
        "output":         4.00,
    },
    "claude-opus-4-6": {
        "input":         15.00,
        "cache_read":     1.50,
        "cache_write":   18.75,
        "output":        75.00,
    },
}

def calc_cost(model, cache_read_tokens, new_input_tokens, output_tokens):
    p = PRICING[model]
    cache_read  = (cache_read_tokens / 1_000_000) * p["cache_read"]
    new_input   = (new_input_tokens / 1_000_000) * p["input"]
    output      = (output_tokens / 1_000_000) * p["output"]
    total       = cache_read + new_input + output
    return cache_read, new_input, output, total

def fmt_cost(usd):
    cents = usd * 100
    if cents >= 1:
        return f"${usd:.4f} ({cents:.2f}¢)"
    elif cents >= 0.01:
        return f"${usd:.6f} ({cents:.4f}¢)"
    else:
        return f"${usd:.8f} ({cents:.6f}¢)"

def print_scenario(label, model, context_k, append_k, output_k):
    cache_read_tokens = context_k * 1000
    new_input_tokens  = append_k * 1000
    output_tokens     = output_k * 1000

    cr, ni, out, total = calc_cost(model, cache_read_tokens, new_input_tokens, output_tokens)

    print(f"\n{'='*60}")
    print(f"  {label}")
    print(f"  Model: {model}")
    print(f"  Context: {context_k}k (cache read) + {append_k}k (new) → {output_k}k output")
    print(f"{'='*60}")
    print(f"  Cache Read:  {cache_read_tokens:>8,} tok × ${PRICING[model]['cache_read']:.2f}/MTok = {fmt_cost(cr)}")
    print(f"  New Input:   {new_input_tokens:>8,} tok × ${PRICING[model]['input']:.2f}/MTok = {fmt_cost(ni)}")
    print(f"  Output:      {output_tokens:>8,} tok × ${PRICING[model]['output']:.2f}/MTok = {fmt_cost(out)}")
    print(f"  ─────────────────────────────────────────")
    print(f"  TOTAL: {fmt_cost(total)}")
    return total

if __name__ == "__main__":
    print("╔══════════════════════════════════════════════════════════╗")
    print("║        Forked Agent Token Cost Calculator               ║")
    print("╚══════════════════════════════════════════════════════════╝")

    # --- Forked Agent Scenarios ---
    costs = {}

    costs["fork_50k"] = print_scenario(
        "Forked Agent — 50k Konversation",
        "claude-sonnet-4-6", context_k=50, append_k=2, output_k=2)

    costs["fork_100k"] = print_scenario(
        "Forked Agent — 100k Konversation",
        "claude-sonnet-4-6", context_k=100, append_k=2, output_k=2)

    costs["fork_200k"] = print_scenario(
        "Forked Agent — 200k Konversation",
        "claude-sonnet-4-6", context_k=200, append_k=2, output_k=2)

    # --- Vergleich: Reflection (aktuell) ---
    costs["reflection"] = print_scenario(
        "Reflection (aktuell) — kein Cache",
        "claude-sonnet-4-6", context_k=0, append_k=6, output_k=1)

    # --- Vergleich: Voller Replay ---
    costs["replay_100k"] = print_scenario(
        "Voller Replay — 100k ohne Cache",
        "claude-sonnet-4-6", context_k=0, append_k=100, output_k=2)

    # --- Haiku Forks (günstiger) ---
    costs["fork_haiku_100k"] = print_scenario(
        "Forked Agent (Haiku) — 100k Konversation",
        "claude-haiku-4-5", context_k=100, append_k=2, output_k=2)

    # --- Session-Hochrechnung ---
    print(f"\n{'='*60}")
    print(f"  Session-Hochrechnung (Sonnet, 100k Kontext)")
    print(f"{'='*60}")
    for n_forks in [10, 20, 50, 100]:
        session_cost = costs["fork_100k"] * n_forks
        print(f"  {n_forks:>3} Forks/Session: {fmt_cost(session_cost)}")

    print(f"\n  Zum Vergleich:")
    print(f"  1 Reflection:     {fmt_cost(costs['reflection'])}")
    print(f"  1 voller Replay:  {fmt_cost(costs['replay_100k'])}")

    # --- Break-even ---
    print(f"\n{'='*60}")
    print(f"  Break-Even: Wie viele Forks kosten so viel wie 1 Reflection?")
    print(f"{'='*60}")
    n_breakeven = costs["reflection"] / costs["fork_100k"]
    print(f"  {n_breakeven:.0f} Forked Agent Calls (100k) = 1 Reflection Call")
    print(f"  → Wir können {n_breakeven:.0f}x öfter forken für das gleiche Geld")
