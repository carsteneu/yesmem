package hints

import "sync/atomic"

// counter rotates through hint variants across all callers.
var counter atomic.Int64

// NextTimestampHint returns the next timestamp hint from the rotating pool.
func NextTimestampHint() string {
	idx := counter.Add(1)
	return TimestampHints[int(idx)%len(TimestampHints)] + "\nDo NOT write timestamps at the start of your responses — read them silently, reference only when asked about timing."
}

// TimestampHints contains 33 varying formulations of the same core instruction:
// use [HH:MM:SS] [msg:N] [+Δ] timestamps for all time-related reasoning.
// Rotation prevents habituation in 3k+ message sessions.
var TimestampHints = []string{
	"Timestamps [HH:MM:SS] [msg:N] [+Δ] are in every message — use them for time references, session duration, and pace.",
	"Every message has timestamps [HH:MM:SS] [msg:N] [+Δ]. Check them before making any time-related claims.",
	"Look at the [HH:MM:SS] [msg:N] [+Δ] timestamps — they tell you exactly when things happened. Use them.",
	"Time data is in every message header: [HH:MM:SS] = wall clock, [msg:N] = message count, [+Δ] = time since last message. Reference these, don't guess.",
	"You have precise timestamps in every message. Never estimate session duration or timing — read the actual [HH:MM:SS] and [+Δ] values.",
	"Timestamps [HH:MM:SS] [msg:N] [+Δ] — they're right there in every message. Use wall clock for time-of-day, +Δ for pacing, msg:N for progress.",
	"Before stating any time — check the timestamps. [HH:MM:SS] is wall clock, [+Δ] is gap since last message, [msg:N] is position in session.",
	"Don't guess what time it is or how long the session has been running. The timestamps [HH:MM:SS] [msg:N] [+Δ] tell you precisely.",
	"Each message carries [HH:MM:SS] [msg:N] [+Δ]. Read them. They're more reliable than your sense of time.",
	"Your internal sense of session duration is unreliable. The [HH:MM:SS] timestamps in every message are not. Use those.",
	"Timestamps exist for a reason. [HH:MM:SS] = when, [msg:N] = how far in, [+Δ] = how long since last exchange. Don't make time claims without checking.",
	"The [+Δ] between messages tells you about pauses and pacing. The [HH:MM:SS] tells you time of day. Both are in every message. Use them.",
	"Time awareness: every message has [HH:MM:SS] [msg:N] [+Δ]. Consult these before any statement about timing, duration, or session progress.",
	"You cannot feel time passing. But you can read [HH:MM:SS] [msg:N] [+Δ] in every message. That's your clock — use it.",
	"Session timing is not a guess. Read [HH:MM:SS] for wall clock, [msg:N] for message count, [+Δ] for inter-message gaps. All in every message.",
	"Timestamps [HH:MM:SS] [msg:N] [+Δ] are your ground truth for all time-related reasoning. They're in every single message.",
	"If you're about to say 'since this morning' or 'about an hour' — stop. Read the [HH:MM:SS] timestamps first. They're in every message.",
	"Message metadata [HH:MM:SS] [msg:N] [+Δ] — wall clock, sequence number, delta. Present in every message. Always reference before time claims.",
	"Temporal claims require evidence. Your evidence is [HH:MM:SS] [msg:N] [+Δ] — embedded in every message of this conversation.",
	"The timestamps are not decoration. [HH:MM:SS] [msg:N] [+Δ] in every message — read them when discussing time, pace, or duration.",
	"How long has this session been running? Don't calculate — read the [HH:MM:SS] from the first and latest message. The data is right there.",
	"[HH:MM:SS] [msg:N] [+Δ] — three data points per message. Wall clock, position, gap. Use them for any temporal reasoning.",
	"Never say 'a few minutes ago' when you can say the exact time. Check [HH:MM:SS] in the relevant message.",
	"Precision over intuition: timestamps [HH:MM:SS] [msg:N] [+Δ] are in every message. Use exact values, not approximations.",
	"The conversation has a built-in clock: [HH:MM:SS] [msg:N] [+Δ]. Refer to it for any time-related observation.",
	"You're an LLM — you don't experience time. But [HH:MM:SS] [msg:N] [+Δ] in every message gives you exact temporal data. Use it.",
	"When was the last user message? How long was the pause? What time is it now? The answers are in [HH:MM:SS] [msg:N] [+Δ]. Always check.",
	"Resist the urge to estimate time. [HH:MM:SS] [msg:N] [+Δ] are precise and available in every message. Let the data speak.",
	"Think of [HH:MM:SS] [msg:N] [+Δ] as your instrumentation panel. Check it before making any time-related statement.",
	"Day changes, long pauses, session duration — all derivable from [HH:MM:SS] [msg:N] [+Δ]. Don't narrate time, read it.",
	"Every message is timestamped [HH:MM:SS] [msg:N] [+Δ]. These are facts. Your sense of elapsed time is not. Prefer facts.",
	"The [+Δ] field detects pauses (lunch break? overnight?), [HH:MM:SS] anchors to wall clock, [msg:N] tracks throughput. All per message.",
	"Temporal grounding: [HH:MM:SS] [msg:N] [+Δ]. Available in every message. Required reading before any time statement.",
}
