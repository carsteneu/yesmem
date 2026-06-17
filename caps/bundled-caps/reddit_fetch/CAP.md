---
name: reddit_fetch
description: "Fetch Reddit post + nested comments + links via old.reddit.com HTML scraping. Persists to cap_store tables."
version: 1
tags: [reddit, fetch, research]
requires: [store]
scope: user
tested: true
auto_active: true
---

## Purpose

Fetch Reddit post + nested comments + links via old.reddit.com HTML scraping. Persists to cap_store tables.

## Scripts

### fetch
kind: tool

```javascript
async ({url, max_comments}) => {
  if (!url || typeof url !== 'string') return {error: 'url required (string)'};
  url = url.replace(/^reddit:/i, '').trim().replace(/\/$/, '');
  if (!/^https?:\/\/(www\.|old\.)?reddit\.com\//i.test(url)) return {error: 'not a reddit URL', given: url};
  
  const oldUrl = url.replace(/^https?:\/\/(www\.)?reddit\.com/, 'https://old.reddit.com');
  const key = 'url:' + url;
  
  const curlCmd = `curl -sL -A "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36" -H "Accept: text/html,application/xhtml+xml" -H "Accept-Language: en-US,en;q=0.9" --max-time 20 ${JSON.stringify(oldUrl)} | yesmem cap-blob-put --cap reddit_fetch --key ${JSON.stringify(key)}`;
  
  const putRes = await sh(curlCmd, 25000);
  if (!putRes || !putRes.includes('"status":"ok"')) return {error: 'cap-blob-put failed', detail: String(putRes).slice(0,400)};
  
  let rows = [];
  for (let i = 0; i < 50; i++) {
    const r = await mcp__yesmem__cap_store({capability: 'reddit_fetch', action: 'query', table: 'blobs', where: 'key=? AND chunk_idx=?', args: JSON.stringify([key, i]), limit: 1});
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
  
  const bodyRe = new RegExp('id="form-' + postFullname + '[^"]*"[^>]*>.*?<div class="md">([\\s\\S]*?)<\/div>\\s*<\/div>\\s*<\/form>');
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
  await mcp__yesmem__cap_store({capability:'reddit_fetch',action:'create_table',table:'posts',columns:JSON.stringify([{name:'permalink',type:'TEXT'},{name:'subreddit',type:'TEXT'},{name:'author',type:'TEXT'},{name:'title',type:'TEXT'},{name:'body',type:'TEXT'},{name:'score',type:'INTEGER'},{name:'num_comments',type:'INTEGER'},{name:'created_utc',type:'INTEGER'},{name:'external_url',type:'TEXT'},{name:'fetched_at',type:'INTEGER'}])});
  await mcp__yesmem__cap_store({capability:'reddit_fetch',action:'create_table',table:'comments',columns:JSON.stringify([{name:'post_permalink',type:'TEXT'},{name:'comment_id',type:'TEXT'},{name:'depth',type:'INTEGER'},{name:'author',type:'TEXT'},{name:'score',type:'INTEGER'},{name:'body',type:'TEXT'},{name:'created_utc',type:'INTEGER'},{name:'parent_id',type:'TEXT'},{name:'fetched_at',type:'INTEGER'}])});
  await mcp__yesmem__cap_store({capability:'reddit_fetch',action:'create_table',table:'links',columns:JSON.stringify([{name:'post_permalink',type:'TEXT'},{name:'target_url',type:'TEXT'},{name:'kind',type:'TEXT'},{name:'source_kind',type:'TEXT'},{name:'source_author',type:'TEXT'},{name:'source_comment_id',type:'TEXT'},{name:'fetched_at',type:'INTEGER'}])});
  await mcp__yesmem__cap_store({capability:'reddit_fetch',action:'delete',table:'posts',where:'permalink=?',args:JSON.stringify([postPermalink])});
  await mcp__yesmem__cap_store({capability:'reddit_fetch',action:'delete',table:'comments',where:'post_permalink=?',args:JSON.stringify([postPermalink])});
  await mcp__yesmem__cap_store({capability:'reddit_fetch',action:'delete',table:'links',where:'post_permalink=?',args:JSON.stringify([postPermalink])});
  
  const postCreatedUtc = Math.floor(postTimestamp / 1000);
  await mcp__yesmem__cap_store({capability:'reddit_fetch',action:'upsert',table:'posts',data:JSON.stringify({permalink:postPermalink,subreddit:postSubreddit,author:postAuthor,title:postTitle,body:postBody,score:finalScore,num_comments:postNumComments,created_utc:postCreatedUtc,external_url:'',fetched_at:fetchedAt})});
  
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
    await mcp__yesmem__cap_store({capability:'reddit_fetch',action:'upsert',table:'comments',data:JSON.stringify(row)});
  }
  for (const row of linkRows) {
    await mcp__yesmem__cap_store({capability:'reddit_fetch',action:'upsert',table:'links',data:JSON.stringify(row)});
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
```
