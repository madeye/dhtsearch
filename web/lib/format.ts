// Format bytes into a human-readable size string, e.g. 2.0 GB
export function formatSize(bytes: number | undefined | null): string {
  if (bytes === undefined || bytes === null || !Number.isFinite(bytes) || bytes < 0) {
    return "-";
  }
  if (bytes === 0) return "0 B";
  const units = ["B", "KB", "MB", "GB", "TB", "PB"];
  const i = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1);
  const value = bytes / Math.pow(1024, i);
  return `${value >= 100 ? Math.round(value) : value.toFixed(1)} ${units[i]}`;
}

// Format a unix timestamp (seconds) as YYYY-MM-DD
export function formatDate(ts: number | undefined | null): string {
  if (!ts || !Number.isFinite(ts)) return "-";
  const d = new Date(ts * 1000);
  if (Number.isNaN(d.getTime())) return "-";
  const y = d.getFullYear();
  const m = String(d.getMonth() + 1).padStart(2, "0");
  const day = String(d.getDate()).padStart(2, "0");
  return `${y}-${m}-${day}`;
}

// Format a unix timestamp (seconds) as relative time in Chinese, e.g. 3 天前
export function formatRelativeTime(ts: number | undefined | null): string {
  if (!ts || !Number.isFinite(ts)) return "-";
  const diff = Date.now() - ts * 1000;
  if (diff < 0) return formatDate(ts);
  const minutes = Math.floor(diff / 60000);
  if (minutes < 1) return "刚刚";
  if (minutes < 60) return `${minutes} 分钟前`;
  const hours = Math.floor(minutes / 60);
  if (hours < 24) return `${hours} 小时前`;
  const days = Math.floor(hours / 24);
  if (days < 30) return `${days} 天前`;
  return formatDate(ts);
}

// Format a count with thousands separators
export function formatCount(n: number | undefined | null): string {
  if (n === undefined || n === null || !Number.isFinite(n)) return "-";
  return n.toLocaleString("zh-CN");
}

// Longest directory prefix (ending in "/") shared by every path.
//
// Torrents almost always wrap their contents in one top-level folder named
// after the torrent itself. Rendering that prefix on every row pushes the only
// distinguishing part — the file name — past the right edge, making unrelated
// files look identical. Callers strip the prefix and show it once instead.
export function commonDirPrefix(paths: string[]): string {
  if (paths.length < 2) return "";
  let prefix = paths[0].slice(0, paths[0].lastIndexOf("/") + 1);
  for (const p of paths.slice(1)) {
    while (prefix && !p.startsWith(prefix)) {
      // Drop the trailing "/" then back up to the previous separator.
      prefix = prefix.slice(0, prefix.lastIndexOf("/", prefix.length - 2) + 1);
    }
    if (!prefix) return "";
  }
  return prefix;
}

// Shorten a directory path from the left, e.g. "A.Very.Long.Release/Subs/" ->
// ".../Subs/". The deepest segment says what the file is for, while the release
// folder at the front is both the longest and the least informative, so keep
// trailing segments while they fit in the budget and elide the rest. A single
// trailing segment is kept whatever its length — CSS truncates the overflow.
export function shortenDir(dir: string, maxChars = 48): string {
  if (dir.length <= maxChars) return dir;
  const segments = dir.split("/").filter(Boolean);
  const kept: string[] = [];
  let used = 0;
  for (let i = segments.length - 1; i >= 0; i--) {
    const cost = segments[i].length + 1;
    if (kept.length > 0 && used + cost > maxChars) break;
    kept.unshift(segments[i]);
    used += cost;
  }
  const elided = kept.length < segments.length ? "…/" : "";
  return `${elided}${kept.join("/")}/`;
}
