// YesMem Agent Dialog Injector
// Preloaded via NODE_OPTIONS="--require ~/.claude/yesmem/injector.js"
// Opens a Unix socket that accepts text to inject as stdin input into Claude Code.

const net = require('net');
const fs = require('fs');
const path = require('path');

// Only activate inside Claude Code
if (!process.argv.some(a => a.includes('claude'))) return;

const sockDir = path.join(process.env.HOME, '.claude', 'yesmem');
const sockPath = path.join(sockDir, `inject-${process.pid}.sock`);

// Clean up stale socket
try { fs.unlinkSync(sockPath); } catch {}

const server = net.createServer(conn => {
  let buf = '';
  conn.on('data', chunk => {
    buf += chunk.toString();
  });
  conn.on('end', () => {
    if (buf.length > 0) {
      // Emit as keyboard input — Ink's useInput picks this up
      process.stdin.emit('data', buf);
    }
  });
});

server.listen(sockPath, () => {
  // Make socket accessible
  try { fs.chmodSync(sockPath, 0o600); } catch {}
});

server.on('error', () => {}); // Silently fail if socket can't bind

// Cleanup on exit
const cleanup = () => {
  try { server.close(); } catch {}
  try { fs.unlinkSync(sockPath); } catch {}
};
process.on('exit', cleanup);
process.on('SIGINT', cleanup);
process.on('SIGTERM', cleanup);
