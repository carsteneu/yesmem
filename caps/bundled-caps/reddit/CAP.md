---
name: reddit
description: "Reddit fetch + search + research bundle — fetch single posts (with comments + links), search across subreddits via RSS (www.reddit.com .rss, da old.reddit.com + .json fuer anonyme Requests seit 2026 gesperrt sind), multi-subreddit topic research with synthesis."
version: 10
tags: [reddit, fetch, search, research]
requires: [store]
scope: user
auto_active: true
---

## Purpose

Reddit fetch + search + research bundle. Sources sind die anonym erreichbaren RSS-Feeds auf www.reddit.com (/.rss): per-Post-Feed (Post + Kommentare), search-Feed und Listing-Feed. old.reddit.com steht seit ~Aug 2026 hinter einem Login-Gate (reason=lor2), www.reddit.com-HTML hinter JS-Challenge + hCaptcha, .json-API blockiert seit Mai 2026. Gegenueber HTML-Scraping fehlen Score und Kommentar-Tiefe (RSS liefert beides nicht). Wichtig: KEIN "Accept: application/atom+xml"-Header setzen (triggert bei reddit eine 429); Retry-Loop (3 Versuche) wegen aggressivem Rate-Limit.

## Scripts

### reddit_fetch
kind: tool

```js
async ({url, max_comments}) => {
  if (!url || typeof url !== 'string') return {error: 'url required (string)'};
  url = url.replace(/^reddit:/i, '').trim().replace(/\/+$/, '');
  if (!/^https?:\/\/(www\.|old\.)?reddit\.com\//i.test(url)) return {error: 'not a reddit URL', given: url};

  const path = url.replace(/^https?:\/\/(www\.|old\.)?reddit\.com/i, '');
  const rssUrl = 'https://www.reddit.com' + (path.endsWith('/.rss') ? path : path + '/.rss');
  const key = 'url:' + url;
  const UA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36";

  const fetchHtml = async () => {
    const curlCmd = `curl -sL -A ${JSON.stringify(UA)} -H "Accept-Language: en-US,en;q=0.9" --max-time 20 ${JSON.stringify(rssUrl)} | yesmem cap-blob-put --cap reddit --key ${JSON.stringify(key)}`;
    const putRes = await sh(curlCmd, 25000);
    if (!putRes || !putRes.includes('"status":"ok"')) return null;
    let rows = [];
    for (let i = 0; i < 50; i++) {
      const r = await mcp__yesmem__cap_store({capability: 'reddit', action: 'query', table: 'blobs', where: 'key=? AND chunk_idx=?', args: JSON.stringify([key, i]), limit: 1});
      const parsed = typeof r === 'string' ? JSON.parse(r) : r;
      const arr = Array.isArray(parsed) ? parsed : (parsed.rows || []);
      if (!arr.length) break;
      rows.push(arr[0]);
    }
    if (!rows.length) return '';
    return rows.map(x => x.data || '').join('');
  };

  let html = null;
  for (let attempt = 0; attempt < 3; attempt++) {
    html = await fetchHtml();
    if (html === null) return {error: 'cap-blob-put failed', detail: 'curl or blob pipe failed', url: rssUrl};
    if (html) break;
    if (attempt < 2) await sh('sleep 6', 8000);
  }
  if (html === '' || html.length < 500) return {error: 'empty feed (rate-limited or blocked?)', url: rssUrl, len: (html || '').length, hint: 'try again in ~60s'};

  // === PARSE ATOM ENTRIES (www.reddit.com per-post RSS) ===
  const cleanText = (text) => { for (let i = 0; i < 3; i++) text = text.replace(/<!\[CDATA\[|\]\]>/g, '').replace(/&lt;/g, '<').replace(/&gt;/g, '>').replace(/&amp;/g, '&').replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/&#x27;/g, "'").replace(/&#32;/g, ' ').replace(/&nbsp;/g, ' '); return text; };
  const stripTags = (text) => text.replace(/<!--\s*SC_(OFF|ON)\s*-->/g, '\n').replace(/<table>/g, ' ').replace(/<\/table>/g, ' ').replace(/<[^>]+>/g, ' ').replace(/\s+/g, ' ').trim();

  const entries = [];
  const er = /<entry>([\s\S]*?)<\/entry>/g;
  let em;
  while ((em = er.exec(html)) !== null) {
    const e = em[1];
    const g = (name) => { const mm = e.match(new RegExp('<' + name + '[^>]*>([\\s\\S]*?)</' + name + '>')); return mm ? mm[1] : ''; };
    const linkM = e.match(/<link href="([^"]*)"/);
    const catM = e.match(/<category [^>]*label="([^"]*)"/);
    const authorM = e.match(/<author><name>([^<]*)<\/name>/);
    entries.push({ id: g('id'), title: g('title'), link: linkM ? linkM[1] : '', subreddit: catM ? catM[1] : '', author: authorM ? authorM[1].replace(/^\/u\//, '') : '[deleted]', updated: g('updated'), content: g('content') });
  }
  if (!entries.length) return {error: 'could not parse any feed entries', url: rssUrl};

  const postE = entries.find(x => x.id.startsWith('t3_')) || entries[0];
  const postId = postE.id.replace(/^t3_/, '');
  const postFullname = 't3_' + postId;
  const permalink = postE.link || ('https://reddit.com/r/' + postE.subreddit.replace(/^r\//, '') + '/comments/' + postId + '/');
  const postBody = stripTags(cleanText(postE.content)).replace(/\s*submitted by\s+\/u\/[^]*$/i, '');
  const finalScore = 0;
  const postNumComments = entries.filter(x => x.id.startsWith('t1_')).length;
  const created_utc = Math.floor(new Date(postE.updated).getTime() / 1000) || 0;
  const fetchedAt = Math.floor(Date.now() / 1000);

  const cap = typeof max_comments === 'number' && max_comments > 0 ? max_comments : 0;
  const outputComments = [];
  const commentRows = [];
  for (const c of entries) {
    if (!c.id.startsWith('t1_')) continue;
    if (cap && outputComments.length >= cap) break;
    const body = stripTags(cleanText(c.content));
    if (!body) continue;
    outputComments.push({author: c.author, score: 0, depth: 0, body});
    commentRows.push({post_permalink: permalink, comment_id: c.id, depth: 0, author: c.author, score: 0, body, created_utc: Math.floor(new Date(c.updated).getTime() / 1000) || 0, parent_id: postFullname, fetched_at: fetchedAt});
  }

  // === LINK EXTRACTION ===
  const categorize = (u) => {
    const m = u.match(/^https?:\/\/([^\/?#:]+)/i);
    if (!m) return 'external';
    const host = m[1].toLowerCase();
    if (host === 'github.com' || host.endsWith('.github.com') || host === 'gist.github.com') return 'github';
    if (host === 'reddit.com' || host.endsWith('.reddit.com') || host === 'redd.it') return 'reddit';
    return 'external';
  };
  const linkSet = new Set();
  const linkRows = [];
  const urlRe = /https?:\/\/[^\s\)\]\>"'\<]+/g;
  const collect = (text, sourceKind, author, cid) => {
    if (!text) return;
    const m = text.match(urlRe);
    if (!m) return;
    for (const u of m) {
      const cleaned = u.replace(/[.,;:!?'")\]>]*$/, '');
      if (linkSet.has(cleaned)) continue;
      linkSet.add(cleaned);
      linkRows.push({post_permalink: permalink, target_url: cleaned, kind: categorize(cleaned), source_kind: sourceKind, source_author: author || '', source_comment_id: cid || '', fetched_at: fetchedAt});
    }
  };
  collect(postBody, 'post_body', postE.author, '');

  // === CAP_STORE PERSISTENCE ===
  await mcp__yesmem__cap_store({capability: 'reddit', action: 'create_table', table: 'posts', columns: JSON.stringify([{name: 'permalink', type: 'TEXT'}, {name: 'subreddit', type: 'TEXT'}, {name: 'author', type: 'TEXT'}, {name: 'title', type: 'TEXT'}, {name: 'body', type: 'TEXT'}, {name: 'score', type: 'INTEGER'}, {name: 'num_comments', type: 'INTEGER'}, {name: 'created_utc', type: 'INTEGER'}, {name: 'external_url', type: 'TEXT'}, {name: 'fetched_at', type: 'INTEGER'}])});
  await mcp__yesmem__cap_store({capability: 'reddit', action: 'create_table', table: 'comments', columns: JSON.stringify([{name: 'post_permalink', type: 'TEXT'}, {name: 'comment_id', type: 'TEXT'}, {name: 'depth', type: 'INTEGER'}, {name: 'author', type: 'TEXT'}, {name: 'score', type: 'INTEGER'}, {name: 'body', type: 'TEXT'}, {name: 'created_utc', type: 'INTEGER'}, {name: 'parent_id', type: 'TEXT'}, {name: 'fetched_at', type: 'INTEGER'}])});
  await mcp__yesmem__cap_store({capability: 'reddit', action: 'create_table', table: 'links', columns: JSON.stringify([{name: 'post_permalink', type: 'TEXT'}, {name: 'target_url', type: 'TEXT'}, {name: 'kind', type: 'TEXT'}, {name: 'source_kind', type: 'TEXT'}, {name: 'source_author', type: 'TEXT'}, {name: 'source_comment_id', type: 'TEXT'}, {name: 'fetched_at', type: 'INTEGER'}])});
  await mcp__yesmem__cap_store({capability: 'reddit', action: 'delete', table: 'posts', where: 'permalink=?', args: JSON.stringify([permalink])});
  await mcp__yesmem__cap_store({capability: 'reddit', action: 'delete', table: 'comments', where: 'post_permalink=?', args: JSON.stringify([permalink])});
  await mcp__yesmem__cap_store({capability: 'reddit', action: 'delete', table: 'links', where: 'post_permalink=?', args: JSON.stringify([permalink])});

  await mcp__yesmem__cap_store({capability: 'reddit', action: 'upsert', table: 'posts', data: JSON.stringify({permalink, subreddit: postE.subreddit.replace(/^r\//, ''), author: postE.author, title: cleanText(postE.title), body: postBody, score: finalScore, num_comments: postNumComments, created_utc, external_url: '', fetched_at: fetchedAt})});

  for (const row of commentRows) await mcp__yesmem__cap_store({capability: 'reddit', action: 'upsert', table: 'comments', data: JSON.stringify(row)});
  for (const row of linkRows) await mcp__yesmem__cap_store({capability: 'reddit', action: 'upsert', table: 'links', data: JSON.stringify(row)});

  return {
    post: {title: cleanText(postE.title), author: postE.author, score: 0, subreddit: postE.subreddit.replace(/^r\//, ''), permalink, body: postBody},
    comments: outputComments,
    links: Array.from(linkSet),
    stats: {comment_count: outputComments.length, link_count: linkSet.size, reported_comments: postNumComments},
    stored: {posts: 1, comments: commentRows.length, links: linkRows.length},
    source: 'www.reddit.com/.rss'
  };
}
```

### reddit_search
kind: tool

```js
async ({ query, limit = 25, sort = "relevance", t = "week", subreddit, after = "", classify = true }) => {
  const TAXONOMY = `- feature_announcement: announces new product/tool/version/feature release
- workflow_tip: shares productivity tips, workflow improvements, best practices, configurations
- bug_complaint: reports bugs, regressions, performance issues, quality drops
- meta_discussion: meta debate about AI direction, model comparisons, opinions
- tutorial_educational: tutorials, explanations, how-tos, educational content
- meme_joke: memes, jokes, humorous screenshots, lighthearted posts
- product_spam: cheap subscription sales, discount codes, referral spam, dropshipping
- other: doesn't clearly fit any of the above`;
  if (!query || typeof query !== "string") return { error: "query required (string)" };
  limit = Math.max(1, Math.min(100, (limit|0) || 25));
  const q = query.trim();
  const mListing = q.match(/^r\/([A-Za-z0-9_]+)\/(hot|top|new|rising|best|controversial)$/i);
  const mSubSearch = !mListing ? q.match(/^r\/([A-Za-z0-9_]+)\s*:\s*(.+)$/i) : null;
  let mode, sub = subreddit || "";
  if (mListing) { mode = "listing"; sub = mListing[1]; }
  else if (mSubSearch) { mode = "subreddit_search"; sub = mSubSearch[1]; }
  else if (sub) { mode = "subreddit_search"; }
  else { mode = "global_search"; }

  const UA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36";
  let url;
  if (mListing) {
    const type = mListing[2].toLowerCase();
    const tParam = (type === "top" || type === "controversial") ? `&t=${t}` : "";
    url = `https://www.reddit.com/r/${encodeURIComponent(sub)}/${type}/.rss?limit=${limit}${tParam}`;
  } else if (mSubSearch) {
    const term = mSubSearch[2].trim();
    url = `https://www.reddit.com/r/${encodeURIComponent(sub)}/search/.rss?q=${encodeURIComponent(term)}&restrict_sr=1&limit=${limit}&sort=${sort}&t=${t}`;
  } else if (sub) {
    url = `https://www.reddit.com/r/${encodeURIComponent(sub)}/search/.rss?q=${encodeURIComponent(q)}&restrict_sr=1&limit=${limit}&sort=${sort}&t=${t}`;
  } else {
    url = `https://www.reddit.com/search/.rss?q=${encodeURIComponent(q)}&limit=${limit}&sort=${sort}&t=${t}`;
  }

  const fetchedAt = Math.floor(Date.now()/1000);

  let html = "";
  let putFail = null;
  for (let attempt = 0; attempt < 3; attempt++) {
    const blobKey = `search:${fetchedAt}_${attempt}_${Math.random().toString(36).slice(2,8)}`;
    const curlCmd = `curl -sL -A ${JSON.stringify(UA)} -H "Accept-Language: en-US,en;q=0.9" --max-time 20 ${JSON.stringify(url)} | yesmem cap-blob-put --cap reddit --key ${JSON.stringify(blobKey)}`;
    const putRes = await sh(curlCmd, 25000);
    if (!putRes || !putRes.includes('"status":"ok"')) { putFail = String(putRes).slice(0,200); continue; }
    let rows = [];
    for (let i = 0; i < 50; i++) {
      const r = await mcp__yesmem__cap_store({capability: "reddit", action: "query", table: "blobs", where: "key=? AND chunk_idx=?", args: JSON.stringify([blobKey, i]), limit: 1});
      if (typeof r === "string" && /^Error/i.test(r)) { putFail = r.slice(0,200); break; }
      const parsed = typeof r === "string" ? JSON.parse(r) : r;
      const arr = Array.isArray(parsed) ? parsed : (parsed.rows || []);
      if (!arr.length) break;
      rows.push(arr[0]);
    }
    await mcp__yesmem__cap_store({capability: "reddit", action: "delete", table: "blobs", where: "key=?", args: JSON.stringify([blobKey])});
    if (rows.length) { html = rows.map(r => r.data || "").join(""); break; }
    if (attempt < 2) await sh('sleep 6', 8000);
  }
  if (putFail && !html) return { error: "cap-blob-put failed", detail: putFail, url };
  if (!html) return { error: "empty feed (rate-limited or blocked?)", url, hint: "try again in ~60s" };

  // === PARSE ATOM ENTRIES ===
  const cleanText = (text) => { for (let i = 0; i < 3; i++) text = text.replace(/<!\[CDATA\[|\]\]>/g, "").replace(/&lt;/g, "<").replace(/&gt;/g, ">").replace(/&amp;/g, "&").replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/&#x27;/g, "'").replace(/&nbsp;/g, " "); return text; };
  const stripTags = (text) => text.replace(/<!--\s*SC_(OFF|ON)\s*-->/g, "\n").replace(/<table>/g, " ").replace(/<\/table>/g, " ").replace(/<[^>]+>/g, " ").replace(/\s+/g, " ").trim();

  const posts = [];
  const seen = new Set();
  const er = /<entry>([\s\S]*?)<\/entry>/g;
  let em;
  while ((em = er.exec(html)) !== null) {
    const e = em[1];
    const g = (name) => { const mm = e.match(new RegExp('<' + name + '[^>]*>([\\s\\S]*?)</' + name + '>')); return mm ? mm[1] : ""; };
    const id = g("id");
    if (!id.startsWith("t3_") || seen.has(id)) continue;
    seen.add(id);
    const linkM = e.match(/<link href="([^"]*)"/);
    const catM = e.match(/<category [^>]*label="([^"]*)"/);
    const authorM = e.match(/<author><name>([^<]*)<\/name>/);
    const link = linkM ? linkM[1] : "";
    const topicDate = g("published") || g("updated");
    posts.push({
      permalink: link.replace(/^https?:\/\/(www\.|old\.)?reddit\.com/i, ""),
      title: cleanText(g("title")),
      subreddit: catM ? catM[1].replace(/^r\//, "") : "",
      author: authorM ? authorM[1].replace(/^\/u\//, "") : "[deleted]",
      score: 0, num_comments: 0, url: "", is_self: true,
      created_utc: topicDate ? Math.floor(new Date(topicDate).getTime() / 1000) : 0,
      body: stripTags(cleanText(g("content"))).substring(0, 500)
    });
  }

  // === CAP_STORE PERSISTENCE ===
  await mcp__yesmem__cap_store({capability: "reddit", action: "create_table", table: "listings", columns: JSON.stringify([{name: "query", type: "TEXT"},{name: "mode", type: "TEXT"},{name: "permalink", type: "TEXT"},{name: "title", type: "TEXT"},{name: "subreddit", type: "TEXT"},{name: "author", type: "TEXT"},{name: "score", type: "INTEGER"},{name: "num_comments", type: "INTEGER"},{name: "url", type: "TEXT"},{name: "created_utc", type: "INTEGER"},{name: "fetched_at", type: "INTEGER"}])});
  await mcp__yesmem__cap_store({capability: "reddit", action: "create_table", table: "categories", columns: JSON.stringify([{name: "permalink", type: "TEXT"},{name: "category", type: "TEXT"},{name: "confidence", type: "TEXT"},{name: "model", type: "TEXT"},{name: "classified_at", type: "INTEGER"}])});

  let classifications = {};
  let modelUsed = "", classifyErr = null;
  if (classify && posts.length > 0) {
    try {
      const instruction = `Classify each Reddit post into exactly one category. Taxonomy:\n${TAXONOMY}\n\nReturn STRICT JSON array only, no prose: [{"permalink":"<url>","category":"<name>","confidence":"high|med|low"}]. One entry per input post, same order.`;
      const postList = posts.map(p => `[${p.permalink}] (r/${p.subreddit}) ${p.title}`).join('\n');
      const resp = await haiku(instruction + '\n\nPosts:\n' + postList);
      const mm = resp.match(/\[[\s\S]*\]/);
      if (mm) {
        const arr = JSON.parse(mm[0]);
        for (const c of arr) {
          if (c?.permalink && c?.category) classifications[c.permalink] = {category: c.category, confidence: c.confidence || 'med'};
        }
        modelUsed = "haiku";
      } else { classifyErr = 'no json in haiku response'; }
    } catch(e) { classifyErr = 'haiku call fail: ' + String(e).slice(0,100); }
  }

  const outPosts = [];
  for (const p of posts) {
    const row = { query: q, mode, permalink: p.permalink, title: p.title, subreddit: p.subreddit, author: p.author, score: p.score, num_comments: p.num_comments, url: p.url, created_utc: p.created_utc, fetched_at: fetchedAt };
    await mcp__yesmem__cap_store({capability: "reddit", action: "upsert", table: "listings", data: JSON.stringify(row)});
    const cls = classifications[p.permalink];
    if (cls) {
      await mcp__yesmem__cap_store({capability: "reddit", action: "upsert", table: "categories", data: JSON.stringify({permalink: p.permalink, category: cls.category, confidence: cls.confidence, model: modelUsed, classified_at: fetchedAt})});
    }
    outPosts.push({...p, category: cls?.category || null, confidence: cls?.confidence || null, body: undefined});
  }

  return { query: q, mode, count: outPosts.length, posts: outPosts, stored: outPosts.length, classified: Object.keys(classifications).length, classify_error: classifyErr, after: "", source_url: url, source: "www.reddit.com/.rss" };
}
```

### reddit_research
kind: tool

```js
async ({ topic, subreddits, limit = 10, score_min = 2, fetch_top = 5, synthesize = true }) => {
    const subs = subreddits || ["ClaudeAI", "ChatGPTPro", "cursor", "CodingWithAI", "LocalLLaMA", "ExperiencedDevs", "mcp"];
    const queries = [topic, `${topic} frustration problem`, `${topic} wish feature`];
    const seen = new Set;
    const allPosts = [];
    for (const sub of subs) {
      for (const q of queries) {
        try {
          const r = await reddit_search({ query: q, subreddit: sub, sort: "relevance", t: "month", limit: Math.ceil(limit / subs.length) });
          if (r?.posts) {
            for (const p of r.posts) {
              const link = p.permalink ? `https://reddit.com${p.permalink}` : "";
              if (link && !seen.has(link) && (p.score || 0) >= score_min) {
                seen.add(link);
                allPosts.push({ url: link, title: p.title, score: p.score || 0, subreddit: p.subreddit, num_comments: p.num_comments || 0 });
              }
            }
          }
        } catch (e) {}
      }
    }
    allPosts.sort((a, b) => b.score - a.score);
    const topN = allPosts.slice(0, fetch_top);
    const fetched = [];
    for (const p of topN) {
      try {
        const detail = await reddit_fetch({ url: p.url, max_comments: 20 });
        const topComments = (detail?.comments || []).filter((c) => c.score > 3).sort((a, b) => b.score - a.score).slice(0, 8).map((c) => ({ author: c.author, score: c.score, body: (c.body || "").substring(0, 400), depth: c.depth || 0 }));
        const postData = {
          title: p.title,
          url: p.url,
          score: p.score,
          subreddit: p.subreddit,
          num_comments: p.num_comments,
          body: (detail?.post?.body || "").substring(0, 1000),
          top_comments: topComments,
          links: (detail?.links || []).slice(0, 10)
        };
        if (synthesize) {
          try {
            const classInput = `Title: ${postData.title}
Score: ${postData.score}
Body: ${postData.body.substring(0, 600)}
Top comments: ${topComments.slice(0, 4).map((c) => c.body.substring(0, 200)).join(" | ")}`;
            const cls = await haiku(`Classify this Reddit post about "${topic}". Return JSON only.

${classInput}`, {
              type: "object",
              properties: {
                category: { type: "string", description: "One of: pain_point, feature_request, workflow_tip, tool_comparison, showcase, discussion, other" },
                sentiment: { type: "string", description: "positive, negative, mixed, neutral" },
                relevance: { type: "number", description: "0.0-1.0 how relevant to the topic" },
                key_insight: { type: "string", description: "One sentence: the core takeaway" }
              },
              required: ["category", "sentiment", "relevance", "key_insight"],
              additionalProperties: false
            });
            postData.classification = cls;
          } catch (e) {
            postData.classification = { error: String(e) };
          }
        }
        fetched.push(postData);
      } catch (e) {
        fetched.push({ title: p.title, url: p.url, score: p.score, error: String(e) });
      }
    }
    let synthesis = null;
    if (synthesize && fetched.length > 0) {
      try {
        const synthInput = fetched.map((p, i) => `[${i + 1}] ${p.title} (${p.subreddit}, score:${p.score})
Category: ${p.classification?.category || "?"} | Sentiment: ${p.classification?.sentiment || "?"}
Insight: ${p.classification?.key_insight || "?"}
Body excerpt: ${(p.body || "").substring(0, 300)}`).join(`

`);
        synthesis = await haiku(`Analyze these ${fetched.length} Reddit posts about "${topic}". Return JSON only.

${synthInput}`, {
          type: "object",
          properties: {
            top_themes: { type: "array", items: { type: "object", properties: { theme: { type: "string" }, evidence_count: { type: "integer" }, description: { type: "string" } }, required: ["theme", "evidence_count", "description"], additionalProperties: false } },
            pain_points: { type: "array", items: { type: "string" } },
            feature_wishes: { type: "array", items: { type: "string" } },
            overall_sentiment: { type: "string" },
            wow_opportunities: { type: "array", items: { type: "string" }, description: "What would make users say WOW based on what they are asking for" }
          },
          required: ["top_themes", "pain_points", "feature_wishes", "overall_sentiment", "wow_opportunities"],
          additionalProperties: false
        });
      } catch (e) {
        synthesis = { error: String(e) };
      }
    }
    const result = {
      topic,
      searched_subreddits: subs,
      total_candidates: allPosts.length,
      fetched_count: fetched.length,
      score_min,
      synthesis,
      posts: fetched,
      candidate_list: allPosts.slice(fetch_top, fetch_top + 15).map((p) => ({ title: p.title, url: p.url, score: p.score, subreddit: p.subreddit }))
    };
    try {
      await cap_save_analysis({
        cap:"reddit",
        source_table: "posts",
        instruction: `Research: ${topic}`,
        summary: JSON.stringify({ synthesis, post_count: fetched.length, candidates: allPosts.length }),
        row_count: fetched.length,
        tags: "reddit,research," + topic.replace(/\s+/g, "-").toLowerCase()
      });
    } catch (e) {
      result._persist_error = String(e);
    }
    return result;
  }
```

