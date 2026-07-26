// API client for the DHTSearch Go backend.
// Base URL is configurable via NEXT_PUBLIC_API_BASE.

export const API_BASE =
  process.env.NEXT_PUBLIC_API_BASE ?? "http://localhost:8080";

export interface TorrentFile {
  path: string;
  size: number;
}

export interface SearchResult {
  info_hash: string;
  name: string;
  total_size: number;
  file_count: number;
  files?: TorrentFile[];
  magnet: string;
  created_at: number;
}

export interface SearchResponse {
  total: number;
  page: number;
  page_size: number;
  results: SearchResult[];
}

export interface StatsResponse {
  torrents?: number;
  seen?: number;
  fetched?: number;
  adult_filtered?: number;
  spam_filtered?: number;
  crawler_running?: boolean;
}

export async function fetchSearch(
  q: string,
  page: number,
  pageSize = 20
): Promise<SearchResponse> {
  const params = new URLSearchParams({
    q,
    page: String(page),
    page_size: String(pageSize),
  });
  const res = await fetch(`${API_BASE}/api/search?${params.toString()}`, {
    cache: "no-store",
  });
  if (!res.ok) {
    throw new Error(`search API returned ${res.status}`);
  }
  return (await res.json()) as SearchResponse;
}

export async function fetchStats(): Promise<StatsResponse | null> {
  try {
    const res = await fetch(`${API_BASE}/api/stats`, { cache: "no-store" });
    if (!res.ok) return null;
    return (await res.json()) as StatsResponse;
  } catch {
    // Backend unreachable — stats are optional, degrade gracefully.
    return null;
  }
}
