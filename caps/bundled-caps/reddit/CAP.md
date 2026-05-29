---
name: reddit
description: "Reddit fetch + search + research bundle — fetch single posts (with comments + links), search across subreddits with LLM classification, multi-subreddit topic research with synthesis."
version: 13
tags: [reddit, fetch, search, research]
requires: [store]
scope: user
auto_active: true
---

## Purpose

Reddit fetch + search + research bundle — fetch single posts (with comments + links), search across subreddits with LLM classification, multi-subreddit topic research with synthesis.

## Scripts

### reddit_fetch
kind: tool

```js
async ({url, max_comments}) => {
  if (!url || typeof url !== 'string') return {error: 'url required (string)'};
  url = url.replace(/^reddit:/i, '').trim().replace(/\/$/, '');
  if (!/^https?:\/\/(www\.|old\.)?reddit\.com\//i.test(url)) return {error: 'not a reddit URL', given: url};
  const fetchUrl = url + '.json?limit=500&raw_json=1';
  const key = 'url:' + url;
  const putRes = await sh(`curl -sL -A "YesMem/1.0" ${JSON.stringify(fetchUrl)} --max-time 15 | yesmem cap-blob-put --cap reddit --key ${JSON.stringify(key)}`, 20000);
  if (!putRes || !putRes.includes('"status":"ok"')) return {error: 'cap-blob-put failed', detail: String(putRes).slice(0,400)};
  let rows = [];
  for (let i = 0; i < 50; i++) {
    const r = await mcp__yesmem__cap_store({capability:'reddit', action: 'query', table: 'blobs', where: 'key=? AND chunk_idx=?', args: JSON.stringify([key, i]), limit: 1});
    const parsed = typeof r === 'string' ? JSON.parse(r) : r;
    const arr = Array.isArray(parsed) ? parsed : (parsed.rows || []);
    if (!arr.length) break;
    rows.push(arr[0]);
  }
  if (!rows.length) return {error: 'blob empty after put', key};
  const raw = rows.map(r => r.data || '').join('');
  let data;
  try { data = JSON.parse(raw); } catch (e) { return {error: 'invalid json', preview: raw.slice(0,300), size: raw.length}; }
  if (!Array.isArray(data) || data.length < 2) return {error: 'unexpected shape'};
  const postData = data[0]?.data?.children?.[0]?.data;
  const commentsRaw = data[1]?.data?.children ?? [];
  if (!postData) return {error: 'no post in response'};
  const permalink = `https://reddit.com${postData.permalink}`;
  const fetchedAt = Math.floor(Date.now()/1000);
  await mcp__yesmem__cap_store({capability:'reddit',action:'create_table',table:'posts',columns:JSON.stringify([{name:'permalink',type:'TEXT'},{name:'subreddit',type:'TEXT'},{name:'author',type:'TEXT'},{name:'title',type:'TEXT'},{name:'body',type:'TEXT'},{name:'score',type:'INTEGER'},{name:'num_comments',type:'INTEGER'},{name:'created_utc',type:'INTEGER'},{name:'external_url',type:'TEXT'},{name:'fetched_at',type:'INTEGER'}])});
  await mcp__yesmem__cap_store({capability:'reddit',action:'create_table',table:'comments',columns:JSON.stringify([{name:'post_permalink',type:'TEXT'},{name:'comment_id',type:'TEXT'},{name:'depth',type:'INTEGER'},{name:'author',type:'TEXT'},{name:'score',type:'INTEGER'},{name:'body',type:'TEXT'},{name:'created_utc',type:'INTEGER'},{name:'parent_id',type:'TEXT'},{name:'fetched_at',type:'INTEGER'}])});
  await mcp__yesmem__cap_store({capability:'reddit',action:'create_table',table:'links',columns:JSON.stringify([{name:'post_permalink',type:'TEXT'},{name:'target_url',type:'TEXT'},{name:'kind',type:'TEXT'},{name:'source_kind',type:'TEXT'},{name:'source_author',type:'TEXT'},{name:'source_comment_id',type:'TEXT'},{name:'fetched_at',type:'INTEGER'}])});
  await mcp__yesmem__cap_store({capability:'reddit',action:'delete',table:'posts',where:'permalink=?',args:JSON.stringify([permalink])});
  await mcp__yesmem__cap_store({capability:'reddit',action:'delete',table:'comments',where:'post_permalink=?',args:JSON.stringify([permalink])});
  await mcp__yesmem__cap_store({capability:'reddit',action:'delete',table:'links',where:'post_permalink=?',args:JSON.stringify([permalink])});
  await mcp__yesmem__cap_store({capability:'reddit',action:'upsert',table:'posts',data:JSON.stringify({permalink,subreddit:postData.subreddit||'',author:postData.author||'[deleted]',title:postData.title||'',body:postData.selftext||'',score:postData.score||0,num_comments:postData.num_comments||0,created_utc:Math.floor(postData.created_utc||0),external_url:(postData.url&&!postData.is_self)?postData.url:'',fetched_at:fetchedAt})});
  const categorize = (u) => {
    const m = u.match(/^https?:\/\/([^/?#:]+)/i);
    if (!m) return 'external';
    const host = m[1].toLowerCase();
    if (host === 'github.com' || host.endsWith('.github.com') || host === 'gist.github.com') return 'github';
    if (host === 'reddit.com' || host.endsWith('.reddit.com') || host === 'redd.it') return 'reddit';
    return 'external';
  };
  const linkSet = new Set();
  const linkRows = [];
  const urlRe = /https?:\/\/[^\s\)\]\>"'<]+/g;
  const collect = (text, sourceKind, author, cid) => {
    if (!text) return;
    const m = text.match(urlRe);
    if (!m) return;
    for (const u of m) {
      if (linkSet.has(u)) continue;
      linkSet.add(u);
      linkRows.push({post_permalink:permalink,target_url:u,kind:categorize(u),source_kind:sourceKind,source_author:author||'',source_comment_id:cid||'',fetched_at:fetchedAt});
    }
  };
  collect(postData.selftext, 'post_body', postData.author, '');
  if (postData.url && /^https?:/.test(postData.url) && !/^https?:\/\/(www\.|old\.)?reddit\.com\//.test(postData.url)) {
    if (!linkSet.has(postData.url)) {
      linkSet.add(postData.url);
      linkRows.push({post_permalink:permalink,target_url:postData.url,kind:categorize(postData.url),source_kind:'post_link',source_author:postData.author||'',source_comment_id:'',fetched_at:fetchedAt});
    }
  }
  const comments = [];
  const commentRows = [];
  const cap = typeof max_comments === 'number' && max_comments > 0 ? max_comments : 0;
  const walk = (children, depth) => {
    for (const c of children) {
      if (cap && comments.length >= cap) return;
      if (!c?.data || c.kind === 'more') continue;
      const d = c.data;
      if (!d.body) continue;
      comments.push({author:d.author,score:d.score,depth,body:d.body});
      commentRows.push({post_permalink:permalink,comment_id:d.id||'',depth,author:d.author||'[deleted]',score:d.score||0,body:d.body,created_utc:Math.floor(d.created_utc||0),parent_id:d.parent_id||'',fetched_at:fetchedAt});
      collect(d.body, 'comment', d.author, d.id);
      const rc = d.replies?.data?.children;
      if (Array.isArray(rc)) walk(rc, depth + 1);
    }
  };
  walk(commentsRaw, 0);
  for (const row of commentRows) {
    await mcp__yesmem__cap_store({capability:'reddit',action:'upsert',table:'comments',data:JSON.stringify(row)});
  }
  for (const row of linkRows) {
    await mcp__yesmem__cap_store({capability:'reddit',action:'upsert',table:'links',data:JSON.stringify(row)});
  }
  return {
    post: {title:postData.title,author:postData.author,score:postData.score,subreddit:postData.subreddit,url:postData.url,permalink,body:postData.selftext||''},
    comments,
    links: Array.from(linkSet),
    stats: {comment_count:comments.length,link_count:linkSet.size,reported_comments:postData.num_comments},
    stored: {posts:1, comments:commentRows.length, links:linkRows.length}
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
  const VALID_SORT = ["relevance","top","new","comments","hot"];
  const VALID_T = ["hour","day","week","month","year","all"];
  if (!VALID_SORT.includes(sort)) return { error: `invalid sort '${sort}'` };
  if (!VALID_T.includes(t)) return { error: `invalid t '${t}'` };
  limit = Math.max(1, Math.min(100, (limit|0) || 25));
  const q = query.trim();
  const mListing = q.match(/^r\/([A-Za-z0-9_]+)\/(hot|top|new|rising|best|controversial)$/i);
  const mSubSearch = !mListing ? q.match(/^r\/([A-Za-z0-9_]+)\s*:\s*(.+)$/i) : null;
  let mode, sub = subreddit || "";
  if (mListing) { mode = "listing"; sub = mListing[1]; }
  else if (mSubSearch) { mode = "subreddit_search"; sub = mSubSearch[1]; }
  else if (sub) { mode = "subreddit_search"; }
  else { mode = "global_search"; }
  let url;
  if (mListing) {
    const type = mListing[2].toLowerCase();
    const tParam = (type==="top"||type==="controversial") ? `&t=${t}` : "";
    const afterParam = after ? `&after=${encodeURIComponent(after)}` : "";
    url = `https://www.reddit.com/r/${encodeURIComponent(sub)}/${type}.json?limit=${limit}&raw_json=1${tParam}${afterParam}`;
  } else if (mSubSearch) {
    const term = mSubSearch[2].trim();
    const afterParam = after ? `&after=${encodeURIComponent(after)}` : "";
    url = `https://www.reddit.com/r/${encodeURIComponent(sub)}/search.json?q=${encodeURIComponent(term)}&restrict_sr=1&limit=${limit}&sort=${sort}&t=${t}&raw_json=1${afterParam}`;
  } else if (sub) {
    const afterParam = after ? `&after=${encodeURIComponent(after)}` : "";
    url = `https://www.reddit.com/r/${encodeURIComponent(sub)}/search.json?q=${encodeURIComponent(q)}&restrict_sr=1&limit=${limit}&sort=${sort}&t=${t}&raw_json=1${afterParam}`;
  } else {
    const afterParam = after ? `&after=${encodeURIComponent(after)}` : "";
    url = `https://www.reddit.com/search.json?q=${encodeURIComponent(q)}&limit=${limit}&sort=${sort}&t=${t}&raw_json=1${afterParam}`;
  }
  const fetchedAt = Math.floor(Date.now()/1000);
  const blobKey = `search:${fetchedAt}_${Math.random().toString(36).slice(2,8)}`;
  const putRes = await sh(`curl -sL -A "YesMem/1.0 (+reddit_search)" ${JSON.stringify(url)} --max-time 15 | yesmem cap-blob-put --cap reddit --key ${JSON.stringify(blobKey)}`, 20000);
  if (!putRes || !putRes.includes('"status":"ok"')) return {error:"cap-blob-put failed", detail:String(putRes).slice(0,200), url};
  let rows = [];
  for (let i = 0; i < 50; i++) {
    const r = await mcp__yesmem__cap_store({capability:"reddit", action:"query", table:"blobs", where:"key=? AND chunk_idx=?", args: JSON.stringify([blobKey, i]), limit: 1});
    if (typeof r === "string" && /^Error/i.test(r)) return {error:"cap_store chunk read error", detail:r.slice(0,200), chunk:i};
    const parsed = typeof r === "string" ? JSON.parse(r) : r;
    const arr = Array.isArray(parsed) ? parsed : (parsed.rows || []);
    if (!arr.length) break;
    rows.push(arr[0]);
  }
  await mcp__yesmem__cap_store({capability:"reddit", action:"delete", table:"blobs", where:"key=?", args: JSON.stringify([blobKey])});
  if (!rows.length) return {error:"blob empty"};
  const raw = rows.map(r => r.data || "").join("");
  let data;
  try { data = JSON.parse(raw); } catch(e) { return {error:"invalid reddit json", size:raw.length, preview:raw.slice(0,200)}; }
  const children = data?.data?.children;
  if (!Array.isArray(children)) return {error:"unexpected shape", keys:Object.keys(data||{})};
  const afterToken = data?.data?.after || "";
  await mcp__yesmem__cap_store({capability:"reddit", action:"create_table", table:"listings", columns: JSON.stringify([
    {name:"query",type:"TEXT"},{name:"mode",type:"TEXT"},{name:"permalink",type:"TEXT"},{name:"title",type:"TEXT"},
    {name:"subreddit",type:"TEXT"},{name:"author",type:"TEXT"},{name:"score",type:"INTEGER"},{name:"num_comments",type:"INTEGER"},
    {name:"url",type:"TEXT"},{name:"created_utc",type:"INTEGER"},{name:"fetched_at",type:"INTEGER"}
  ])});
  await mcp__yesmem__cap_store({capability:"reddit", action:"create_table", table:"categories", columns: JSON.stringify([
    {name:"permalink",type:"TEXT"},{name:"category",type:"TEXT"},{name:"confidence",type:"TEXT"},{name:"model",type:"TEXT"},{name:"classified_at",type:"INTEGER"}
  ])});
  const posts = [];
  for (const c of children) {
    if (c?.kind !== "t3" || !c.data) continue;
    const d = c.data;
    const permalink = d.permalink ? `https://reddit.com${d.permalink}` : "";
    posts.push({permalink, title:d.title||"", subreddit:d.subreddit||"", author:d.author||"[deleted]",
      score:d.score||0, num_comments:d.num_comments||0, url:(d.url && !d.is_self)?d.url:"", is_self:!!d.is_self,
      created_utc:Math.floor(d.created_utc||0)});
  }
  let classifications = {};
  let modelUsed = "", classifyErr = null;
  if (classify && posts.length > 0) {
    try {
      const instruction = `Classify each Reddit post into exactly one category. Taxonomy:\n${TAXONOMY}\n\nReturn STRICT JSON array only, no prose: [{"permalink":"<url>","category":"<name>","confidence":"high|med|low"}]. One entry per input post, same order.`;
      const postList = posts.map(p => `[${p.permalink}] (r/${p.subreddit}) ${p.title}`).join('\n');
      const resp = await haiku(instruction + '\n\nPosts:\n' + postList);
      const m = resp.match(/\[[\s\S]*\]/);
      if (m) {
        const arr = JSON.parse(m[0]);
        for (const c of arr) {
          if (c?.permalink && c?.category) classifications[c.permalink] = {category:c.category, confidence:c.confidence||'med'};
        }
        modelUsed = "haiku";
      } else { classifyErr = 'no json in haiku response'; }
    } catch(e) { classifyErr = 'haiku call fail: ' + String(e).slice(0,100); }
  }
  const outPosts = [];
  for (const p of posts) {
    const row = {
      query:q, mode, permalink:p.permalink, title:p.title, subreddit:p.subreddit, author:p.author,
      score:p.score, num_comments:p.num_comments, url:p.url, created_utc:p.created_utc, fetched_at:fetchedAt
    };
    await mcp__yesmem__cap_store({capability:"reddit", action:"upsert", table:"listings", data: JSON.stringify(row)});
    const cls = classifications[p.permalink];
    if (cls) {
      await mcp__yesmem__cap_store({capability:"reddit", action:"upsert", table:"categories",
        data: JSON.stringify({permalink:p.permalink, category:cls.category, confidence:cls.confidence, model:modelUsed, classified_at:fetchedAt})});
    }
    outPosts.push({...p, category: cls?.category || null, confidence: cls?.confidence || null});
  }
  return {query:q, mode, count:outPosts.length, posts:outPosts, stored:outPosts.length, classified:Object.keys(classifications).length, classify_error:classifyErr, after:afterToken, source_url:url};
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
          const r = await reddit_search({ query: q, subreddit: sub, sort: "relevance", time: "month", limit: Math.ceil(limit / subs.length) });
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

## Database

```sql
CREATE TABLE cap_reddit_fetch__blobs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  key TEXT,
  chunk_idx INTEGER,
  data TEXT,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE cap_reddit_fetch__posts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  permalink TEXT,
  subreddit TEXT,
  author TEXT,
  title TEXT,
  body TEXT,
  score INTEGER,
  num_comments INTEGER,
  created_utc INTEGER,
  external_url TEXT,
  fetched_at INTEGER,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE cap_reddit_fetch__comments (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  post_permalink TEXT,
  comment_id TEXT,
  depth INTEGER,
  author TEXT,
  score INTEGER,
  body TEXT,
  created_utc INTEGER,
  parent_id TEXT,
  fetched_at INTEGER,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE cap_reddit_fetch__links (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  post_permalink TEXT,
  target_url TEXT,
  kind TEXT,
  source_kind TEXT,
  source_author TEXT,
  source_comment_id TEXT,
  fetched_at INTEGER,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE cap_reddit_search__blobs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  key TEXT,
  chunk_idx INTEGER,
  data TEXT,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE cap_reddit_search__listings (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  query TEXT,
  mode TEXT,
  permalink TEXT,
  title TEXT,
  subreddit TEXT,
  author TEXT,
  score INTEGER,
  num_comments INTEGER,
  url TEXT,
  created_utc INTEGER,
  fetched_at INTEGER,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE cap_reddit_search__categories (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  permalink TEXT,
  category TEXT,
  confidence TEXT,
  model TEXT,
  classified_at INTEGER,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE cap_reddit_research__analyses (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  source_table TEXT,
  filter_where TEXT,
  filter_args TEXT,
  instruction TEXT,
  summary TEXT,
  row_count INTEGER,
  model TEXT,
  tags TEXT,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE cap_reddit__blobs (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  key TEXT,
  chunk_idx INTEGER,
  data TEXT,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE cap_reddit__posts (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  permalink TEXT,
  subreddit TEXT,
  author TEXT,
  title TEXT,
  body TEXT,
  score INTEGER,
  num_comments INTEGER,
  created_utc INTEGER,
  external_url TEXT,
  fetched_at INTEGER,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE cap_reddit__comments (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  post_permalink TEXT,
  comment_id TEXT,
  depth INTEGER,
  author TEXT,
  score INTEGER,
  body TEXT,
  created_utc INTEGER,
  parent_id TEXT,
  fetched_at INTEGER,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE cap_reddit__links (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  post_permalink TEXT,
  target_url TEXT,
  kind TEXT,
  source_kind TEXT,
  source_author TEXT,
  source_comment_id TEXT,
  fetched_at INTEGER,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE cap_reddit__listings (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  query TEXT,
  mode TEXT,
  permalink TEXT,
  title TEXT,
  subreddit TEXT,
  author TEXT,
  score INTEGER,
  num_comments INTEGER,
  url TEXT,
  created_utc INTEGER,
  fetched_at INTEGER,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE cap_reddit__categories (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  permalink TEXT,
  category TEXT,
  confidence TEXT,
  model TEXT,
  classified_at INTEGER,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE cap_reddit__analyses (
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  source_table TEXT,
  filter_where TEXT,
  filter_args TEXT,
  instruction TEXT,
  summary TEXT,
  row_count INTEGER,
  model TEXT,
  tags TEXT,
  created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
  updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
);
```
