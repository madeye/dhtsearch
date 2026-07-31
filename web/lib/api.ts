// API client for the DHTSearch Go backend.
// Base URL is configurable via NEXT_PUBLIC_API_BASE.

import { headers } from "next/headers";

export const API_BASE =
  process.env.NEXT_PUBLIC_API_BASE ?? "http://localhost:8080";

// Forward the caller's identity on server-side API requests. The Go backend
// rate-limits per client IP; without these headers every SSR fetch would
// arrive from the Next server's own address, so one abusive visitor could
// exhaust the shared bucket for everyone.
async function clientIPHeaders(): Promise<Record<string, string>> {
  try {
    const h = await headers();
    const out: Record<string, string> = {};
    const xff = h.get("x-forwarded-for");
    if (xff) out["x-forwarded-for"] = xff;
    const realIP = h.get("x-real-ip");
    if (realIP) out["x-real-ip"] = realIP;
    return out;
  } catch {
    // Outside a request scope (e.g. build time) there is nothing to forward.
    return {};
  }
}

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
    headers: await clientIPHeaders(),
  });
  if (res.status === 429) {
    throw new Error("请求过于频繁，请稍后重试");
  }
  if (res.status === 503) {
    throw new Error("服务器繁忙，请稍后重试");
  }
  if (!res.ok) {
    throw new Error(`search API returned ${res.status}`);
  }
  return (await res.json()) as SearchResponse;
}

export interface TrendingResponse {
  movies?: string[];
  tv?: string[];
  updated_at?: number;
}

export async function fetchTrending(): Promise<TrendingResponse | null> {
  try {
    // The backend refreshes hourly; revalidate keeps the homepage from
    // hitting the API per request. No per-client IP headers here — they
    // would fragment this shared cache into one entry per visitor.
    const res = await fetch(`${API_BASE}/api/trending`, {
      next: { revalidate: 300 },
    });
    if (!res.ok) return null;
    return (await res.json()) as TrendingResponse;
  } catch {
    // Trending is decorative — degrade to no section.
    return null;
  }
}

export async function fetchStats(): Promise<StatsResponse | null> {
  try {
    const res = await fetch(`${API_BASE}/api/stats`, {
      cache: "no-store",
      headers: await clientIPHeaders(),
    });
    if (!res.ok) return null;
    return (await res.json()) as StatsResponse;
  } catch {
    // Backend unreachable — stats are optional, degrade gracefully.
    return null;
  }
}
