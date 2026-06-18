---
name: reddit
description: "Reddit fetch + search + research bundle — fetch single posts (with comments + links), search across subreddits with LLM classification, multi-subreddit topic research with synthesis."
version: 5
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
  
  const oldUrl = url.replace(/^https?:\/\/(www\.)?reddit\.com/, 'https://old.reddit.com');
  const key = 'url:' + url;
  
  const curlCmd = `curl -sL -A "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36" -H "Accept: text/html,application/xhtml+xml" -H "Accept-Language: en-US,en;q=0.9" --max-time 20 ${JSON.stringify(oldUrl)} | yesmem cap-blob-put --cap reddit --key ${JSON.stringify(key)}`;
  
  const putRes = await sh(curlCmd, 25000);
  if (!putRes || !putRes.includes('"status":"ok"')) return {error: 'cap-blob-put failed', detail: String(putRes).slice(0,400)};
  
  let rows = [];
  for (let i = 0; i < 50; i++) {
    const r = await mcp__yesmem__cap_store({capability: 'reddit', action: 'query', table: 'blobs', where: 'key=? AND chunk_idx=?', args: JSON.stringify([key, i]), limit: 1});
    const parsed = typeof r === 'string' ? JSON.parse(r) : r;
    const arr = Array.isArray(parsed) ? parsed : (parsed.rows || []);
    if (!arr.length) break;
    rows.push(arr[0]);
  }
  if (!rows.length) return {error: 'blob empty after put', key};
  const html = rows.map(r => r.data || '').join('');
  
  // Helper: extract data-* attribute value
  const getAttr = (str, name) => {
    const m = str.match(new RegExp('data-' + name + '="([^"]*)"'));
    return m ? m[1] : '';
  };
  
  // Helper: clean HTML entities and Reddit markdown escapes
  const cleanText = (text) => {
    return text
      .replace(/<[^>]+>/g, '')
      .replace(/&amp;/g, '&').replace(/&lt;/g, '<').replace(/&gt;/g, '>')
      .replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/&#x27;/g, "'")
      .replace(/&nbsp;/g, ' ')
      .replace(/\\([()_*\[\]#])/g, '$1')  // Reddit markdown escapes: \( \) \_ \* etc.
      .replace(/\\n/g, '\n').trim();
  };
  
  // === PARSE POST ===
  const t3Match = html.match(/<div class="[^"]*thing id-t3_(\w+)[^"]*"([^>]*)>/);
  if (!t3Match) return {error: 'could not find post (t3_ thing) in HTML'};
  const postId = t3Match[1];
  const postFullname = 't3_' + postId;
  const t3Attrs = t3Match[2];
  
  const postAuthor = getAttr(t3Attrs, 'author') || '[deleted]';
  const postScore = parseInt(getAttr(t3Attrs, 'score')) || 0;
  const postNumComments = parseInt(getAttr(t3Attrs, 'comments-count')) || 0;
  const postTimestamp = parseInt(getAttr(t3Attrs, 'timestamp')) || 0;
  const postSubreddit = (getAttr(t3Attrs, 'subreddit-prefixed') || '').replace('r/', '');
  const postPermalinkRaw = getAttr(t3Attrs, 'permalink');
  const postPermalink = 'https://reddit.com' + (postPermalinkRaw || '/r/' + postSubreddit + '/comments/' + postId + '/');
  
  const titleMatch = html.match(/<a class="title may-blank[^"]*"[^>]*>([^<]+)<\/a>/);
  const postTitle = titleMatch ? cleanText(titleMatch[1]) : (html.match(/<title>([^<]+)/) || ['',''])[1].replace(' : ' + postSubreddit, '').trim();
  
  const bodyRe = new RegExp('id="form-' + postFullname + '[^"]*"[^>]*>.*?<div class="md">([\\s\\S]*?)<\\/div>\\s*<\\/div>\\s*<\\/form>');
  const bodyMatch = html.match(bodyRe);
  let postBody = bodyMatch ? cleanText(bodyMatch[1]) : '';
  
  let finalScore = postScore;
  if (!finalScore) {
    const sf = html.match(/<span class="number">(\d+)<\/span>/);
    if (sf) finalScore = parseInt(sf[1]);
  }
  
  const fetchedAt = Math.floor(Date.now()/1000);
  
  // === PARSE COMMENTS ===
  const commentRegex = /<div class="[^"]*thing id-(t1_\w+)[^"]*"([^>]*)>/g;
  const commentData = [];
  let cm;
  while ((cm = commentRegex.exec(html)) !== null) {
    const fullname = cm[1];
    const cattrs = cm[2];
    const author = getAttr(cattrs, 'author') || '[deleted]';
    const pos = cm.index;
    
    const chunk = html.slice(pos, pos + 3000);
    const parentMatch = chunk.match(/<a href="#(\w+)"[^>]*data-event-action="parent"/);
    let parentShortId = parentMatch ? parentMatch[1] : '';
    
    const scoreMatch = chunk.match(/<span class="score likes" title="(-?\d+)"/);
    const score = scoreMatch ? parseInt(scoreMatch[1]) : 0;
    
    const timeMatch = chunk.match(/<time[^>]*datetime="([^"]+)"/);
    let createdUtc = timeMatch ? Math.floor(new Date(timeMatch[1]).getTime() / 1000) : 0;
    
    const mdMatch = chunk.match(/<div class="md">([\s\S]*?)<\/div>\s*<\/div>\s*<\/form>/);
    let body = mdMatch ? cleanText(mdMatch[1]) : '';
    
    commentData.push({fullname, author, score, body, created_utc: createdUtc, parentShortId});
  }
  
  // Build short_id → fullname map for depth calculation
  const shortToFull = new Map();
  for (const c of commentData) shortToFull.set(c.fullname.replace('t1_', ''), c.fullname);
  shortToFull.set(postId, postFullname);
  
  for (const c of commentData) {
    c.parent_id = (c.parentShortId && shortToFull.has(c.parentShortId)) ? shortToFull.get(c.parentShortId) : postFullname;
    let depth = 0, current = c.parentShortId;
    const visited = new Set();
    while (current && current !== postId && shortToFull.has(current) && !visited.has(current)) {
      visited.add(current);
      depth++;
      const pc = commentData.find(x => x.fullname === shortToFull.get(current));
      current = pc ? pc.parentShortId : null;
    }
    c.depth = depth;
  }
  
  // === CAP_STORE PERSISTENCE ===
  await mcp__yesmem__cap_store({capability:'reddit',action:'create_table',table:'posts',columns:JSON.stringify([{name:'permalink',type:'TEXT'},{name:'subreddit',type:'TEXT'},{name:'author',type:'TEXT'},{name:'title',type:'TEXT'},{name:'body',type:'TEXT'},{name:'score',type:'INTEGER'},{name:'num_comments',type:'INTEGER'},{name:'created_utc',type:'INTEGER'},{name:'external_url',type:'TEXT'},{name:'fetched_at',type:'INTEGER'}])});
  await mcp__yesmem__cap_store({capability:'reddit',action:'create_table',table:'comments',columns:JSON.stringify([{name:'post_permalink',type:'TEXT'},{name:'comment_id',type:'TEXT'},{name:'depth',type:'INTEGER'},{name:'author',type:'TEXT'},{name:'score',type:'INTEGER'},{name:'body',type:'TEXT'},{name:'created_utc',type:'INTEGER'},{name:'parent_id',type:'TEXT'},{name:'fetched_at',type:'INTEGER'}])});
  await mcp__yesmem__cap_store({capability:'reddit',action:'create_table',table:'links',columns:JSON.stringify([{name:'post_permalink',type:'TEXT'},{name:'target_url',type:'TEXT'},{name:'kind',type:'TEXT'},{name:'source_kind',type:'TEXT'},{name:'source_author',type:'TEXT'},{name:'source_comment_id',type:'TEXT'},{name:'fetched_at',type:'INTEGER'}])});
  await mcp__yesmem__cap_store({capability:'reddit',action:'delete',table:'posts',where:'permalink=?',args:JSON.stringify([postPermalink])});
  await mcp__yesmem__cap_store({capability:'reddit',action:'delete',table:'comments',where:'post_permalink=?',args:JSON.stringify([postPermalink])});
  await mcp__yesmem__cap_store({capability:'reddit',action:'delete',table:'links',where:'post_permalink=?',args:JSON.stringify([postPermalink])});
  
  const postCreatedUtc = Math.floor(postTimestamp / 1000);
  await mcp__yesmem__cap_store({capability:'reddit',action:'upsert',table:'posts',data:JSON.stringify({permalink:postPermalink,subreddit:postSubreddit,author:postAuthor,title:postTitle,body:postBody,score:finalScore,num_comments:postNumComments,created_utc:postCreatedUtc,external_url:'',fetched_at:fetchedAt})});
  
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
  const urlRe = /https?:\/\/[^\s\)\]\>"'<]+/g;
  const collect = (text, sourceKind, author, cid) => {
    if (!text) return;
    const m = text.match(urlRe);
    if (!m) return;
    for (const u of m) {
      const cleaned = u.replace(/[.,;:!?'")\]>]*$/, '').replace(/\\([()_*\[\]#])/g, '$1');
      if (linkSet.has(cleaned)) continue;
      linkSet.add(cleaned);
      linkRows.push({post_permalink:postPermalink,target_url:cleaned,kind:categorize(cleaned),source_kind:sourceKind,source_author:author||'',source_comment_id:cid||'',fetched_at:fetchedAt});
    }
  };
  collect(postBody, 'post_body', postAuthor, '');
  
  const cap = typeof max_comments === 'number' && max_comments > 0 ? max_comments : 0;
  const outputComments = [];
  const commentRows = [];
  for (const c of commentData) {
    if (cap && outputComments.length >= cap) break;
    if (!c.body) continue;
    outputComments.push({author:c.author,score:c.score,depth:c.depth,body:c.body});
    commentRows.push({post_permalink:postPermalink,comment_id:c.fullname,depth:c.depth,author:c.author,score:c.score,body:c.body,created_utc:c.created_utc,parent_id:c.parent_id,fetched_at:fetchedAt});
    collect(c.body, 'comment', c.author, c.fullname);
  }
  
  for (const row of commentRows) {
    await mcp__yesmem__cap_store({capability:'reddit',action:'upsert',table:'comments',data:JSON.stringify(row)});
  }
  for (const row of linkRows) {
    await mcp__yesmem__cap_store({capability:'reddit',action:'upsert',table:'links',data:JSON.stringify(row)});
  }
  
  return {
    post: {title:postTitle,author:postAuthor,score:finalScore,subreddit:postSubreddit,permalink:postPermalink,body:postBody},
    comments: outputComments,
    links: Array.from(linkSet),
    stats: {comment_count:outputComments.length,link_count:linkSet.size,reported_comments:postNumComments},
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
  const UA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36";

  if (mListing) {
    const type = mListing[2].toLowerCase();
    const tParam = (type==="top"||type==="controversial") ? `&t=${t}` : "";
    const afterParam = after ? `&after=${encodeURIComponent(after)}` : "";
    url = `https://old.reddit.com/r/${encodeURIComponent(sub)}/${type}/?limit=${limit}${tParam}${afterParam}`;
  } else if (mSubSearch) {
    const term = mSubSearch[2].trim();
    const afterParam = after ? `&after=${encodeURIComponent(after)}` : "";
    url = `https://old.reddit.com/r/${encodeURIComponent(sub)}/search?q=${encodeURIComponent(term)}&restrict_sr=1&limit=${limit}&sort=${sort}&t=${t}${afterParam}`;
  } else if (sub) {
    const afterParam = after ? `&after=${encodeURIComponent(after)}` : "";
    url = `https://old.reddit.com/r/${encodeURIComponent(sub)}/search?q=${encodeURIComponent(q)}&restrict_sr=1&limit=${limit}&sort=${sort}&t=${t}${afterParam}`;
  } else {
    const afterParam = after ? `&after=${encodeURIComponent(after)}` : "";
    url = `https://old.reddit.com/search?q=${encodeURIComponent(q)}&limit=${limit}&sort=${sort}&t=${t}${afterParam}`;
  }

  const fetchedAt = Math.floor(Date.now()/1000);
  const blobKey = `search:${fetchedAt}_${Math.random().toString(36).slice(2,8)}`;

  const curlCmd = `curl -sL -A ${JSON.stringify(UA)} -H "Accept: text/html,application/xhtml+xml" -H "Accept-Language: en-US,en;q=0.9" --max-time 20 ${JSON.stringify(url)} | yesmem cap-blob-put --cap reddit --key ${JSON.stringify(blobKey)}`;
  const putRes = await sh(curlCmd, 25000);
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

  const html = rows.map(r => r.data || "").join("");

  const cleanText = (text) => {
    return text
      .replace(/<[^>]+>/g, '')
      .replace(/&amp;/g, '&').replace(/&lt;/g, '<').replace(/&gt;/g, '>')
      .replace(/&quot;/g, '"').replace(/&#39;/g, "'").replace(/&#x27;/g, "'")
      .replace(/&nbsp;/g, ' ')
      .replace(/\\([()_*\[\]#])/g, '$1')
      .trim();
  };

  const posts = [];
  const seen = new Set();

  if (mListing) {
    // === LISTING MODE: parse "thing" divs with data-* attributes ===
    const getAttr = (str, name) => {
      const ma = str.match(new RegExp('data-' + name + '="([^"]*)"'));
      return ma ? ma[1] : '';
    };
    const thingRegex = /<div class="[^"]*thing id-t3_(\w+)[^"]*"([^>]*)>/g;
    let tm;
    while ((tm = thingRegex.exec(html)) !== null) {
      const id = tm[1];
      if (seen.has(id)) continue;
      seen.add(id);
      const attrs = tm[2];
      const author = getAttr(attrs, 'author') || '[deleted]';
      const score = parseInt(getAttr(attrs, 'score')) || 0;
      const num_comments = parseInt(getAttr(attrs, 'comments-count')) || 0;
      const created_utc = Math.floor((parseInt(getAttr(attrs, 'timestamp')) || 0) / 1000);
      const sr = (getAttr(attrs, 'subreddit-prefixed') || '').replace('r/', '');
      const permalinkRaw = getAttr(attrs, 'permalink');
      const permalink = permalinkRaw ? `https://reddit.com${permalinkRaw}` : `https://reddit.com/r/${sr}/comments/${id}/`;

      const pos = tm.index;
      const chunk = html.slice(pos, pos + 2000);
      const titleMatch = chunk.match(/<a[^>]*class="[^"]*\btitle\b[^"]*"[^>]*>([^<]+)<\/a>/);
      const title = titleMatch ? cleanText(titleMatch[1]) : '';

      const is_self = !chunk.match(/<a[^>]*class="[^"]*\bthumbnail\b[^"]*"[^>]*href="(https?:[^"]+)"/i);

      posts.push({permalink, title, subreddit:sr, author, score, num_comments,
        url: '', is_self: !!is_self, created_utc});
    }
  } else {
    // === SEARCH MODE: parse search-result divs (flexible attribute order) ===
    const resultRegex = /<div class="[^"]*search-result search-result-link[^"]*"[^>]*data-fullname="t3_(\w+)"[^>]*>/g;
    let sm;
    while ((sm = resultRegex.exec(html)) !== null) {
      const id = sm[1];
      if (seen.has(id)) continue;
      seen.add(id);
      const pos = sm.index;
      const chunk = html.slice(pos, pos + 4000);

      // Title: <a ... class="...search-title..." ...>TITLE</a>
      const titleMatch = chunk.match(/<a\s[^>]*class="[^"]*\bsearch-title\b[^"]*"[^>]*>([^<]+)<\/a>/i);
      const title = titleMatch ? cleanText(titleMatch[1]) : '';

      // Score: <span class="...search-score...">N points</span>
      const scoreMatch = chunk.match(/<span[^>]*class="[^"]*\bsearch-score\b[^"]*"[^>]*>([\d,]+)\s*points?<\/span>/i);
      const score = scoreMatch ? parseInt(scoreMatch[1].replace(/,/g, '')) : 0;

      // Comments: <a class="...search-comments...">N comments</a>
      const commentsMatch = chunk.match(/<a\s[^>]*class="[^"]*\bsearch-comments\b[^"]*"[^>]*>([\d,]+)\s*comments?<\/a>/i);
      const num_comments = commentsMatch ? parseInt(commentsMatch[1].replace(/,/g, '')) : 0;

      // Time: <time datetime="...">
      const timeMatch = chunk.match(/<time[^>]*datetime="([^"]+)"/);
      const created_utc = timeMatch ? Math.floor(new Date(timeMatch[1]).getTime() / 1000) : 0;

      // Author: within <span class="...search-author..."> ... <a ...>USER</a></span>
      const authorMatch = chunk.match(/<span[^>]*class="[^"]*\bsearch-author\b[^"]*"[^>]*>[\s\S]*?<a\s[^>]*>([^<]+)<\/a>/i);
      const author = authorMatch ? cleanText(authorMatch[1]) : '[deleted]';

      // Subreddit: <a class="...search-subreddit-link...">r/NAME</a>
      const srMatch = chunk.match(/<a\s[^>]*class="[^"]*\bsearch-subreddit-link\b[^"]*"[^>]*>r\/([^<]+)<\/a>/i);
      const sr = srMatch ? srMatch[1] : '';

      // Permalink: construct from href in search-title, or fall back to ID
      const hrefMatch = chunk.match(/<a\s[^>]*class="[^"]*\bsearch-title\b[^"]*"[^>]*href="([^"]+)"/i)
                      || chunk.match(/<a\s[^>]*href="([^"]+)"[^>]*class="[^"]*\bsearch-title\b[^"]*"/i);
      const href = hrefMatch ? hrefMatch[1] : '';
      const permalink = href ? (href.startsWith('https://') ? href : `https://reddit.com${href}`) : `https://reddit.com/r/${sr}/comments/${id}/`;

      // is_self: check for external thumbnail link
      const is_self = !chunk.match(/<a[^>]*class="[^"]*\bthumbnail\b[^"]*"[^>]*href="(https?:[^"]+)"/i);

      posts.push({permalink, title, subreddit:sr, author, score, num_comments,
        url: '', is_self: !!is_self, created_utc});
    }
  }

  await mcp__yesmem__cap_store({capability:"reddit", action:"create_table", table:"listings", columns: JSON.stringify([
    {name:"query",type:"TEXT"},{name:"mode",type:"TEXT"},{name:"permalink",type:"TEXT"},{name:"title",type:"TEXT"},
    {name:"subreddit",type:"TEXT"},{name:"author",type:"TEXT"},{name:"score",type:"INTEGER"},{name:"num_comments",type:"INTEGER"},
    {name:"url",type:"TEXT"},{name:"created_utc",type:"INTEGER"},{name:"fetched_at",type:"INTEGER"}
  ])});
  await mcp__yesmem__cap_store({capability:"reddit", action:"create_table", table:"categories", columns: JSON.stringify([
    {name:"permalink",type:"TEXT"},{name:"category",type:"TEXT"},{name:"confidence",type:"TEXT"},{name:"model",type:"TEXT"},{name:"classified_at",type:"INTEGER"}
  ])});

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

  let afterToken = after;
  const nextMatch = html.match(/<a[^>]*rel="nofollow next"[^>]*href="[^"]*after=(\w+)/);
  if (nextMatch) afterToken = nextMatch[1];

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

## Database

```sql
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
