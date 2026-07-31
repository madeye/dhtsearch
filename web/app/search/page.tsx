import type { Metadata } from "next";
import Link from "next/link";
import { fetchSearch, type ContentFilter, type SearchResponse } from "@/lib/api";
import { formatCount } from "@/lib/format";
import SearchBox from "@/components/SearchBox";
import ResultList from "@/components/ResultList";
import Pagination from "@/components/Pagination";

interface PageProps {
  searchParams: Promise<{ q?: string; page?: string; include_adult?: string; category?: string }>;
}

export async function generateMetadata({ searchParams }: PageProps): Promise<Metadata> {
  const { q } = await searchParams;
  const query = (q ?? "").trim();
  return {
    title: query ? `${query} 的搜索结果` : "最新收录",
    description: query
      ? `在 DHTSearch 中搜索「${query}」的磁力链接，结果已过滤成人内容与垃圾信息。`
      : "浏览 DHT 网络最新收录的磁力链接，已过滤成人内容与垃圾信息。",
  };
}

export default async function SearchPage({ searchParams }: PageProps) {
  const params = await searchParams;
  const q = (params.q ?? "").trim();
  const page = Math.max(1, parseInt(params.page ?? "1", 10) || 1);
  const pageSize = 20;
  const contentFilter: ContentFilter =
    params.category === "adult"
      ? "adult"
      : params.include_adult === "true"
        ? "all"
        : "safe";

  let data: SearchResponse | null = null;
  let error: string | null = null;
  try {
    data = await fetchSearch(q, page, pageSize, contentFilter);
  } catch (e) {
    error = e instanceof Error ? e.message : "unknown error";
  }

  return (
    <main className="mx-auto flex w-full max-w-3xl flex-1 flex-col px-4 py-6">
      {/* Header: logo + search box */}
      <header className="flex items-center gap-4">
        <Link
          href="/"
          className="shrink-0 text-xl font-bold tracking-tight hover:text-emerald-400"
        >
          DHT<span className="text-emerald-500">Search</span>
        </Link>
        <div className="flex-1">
          <SearchBox initialQuery={q} contentFilter={contentFilter} />
        </div>
      </header>

      <ContentFilterTabs q={q} selected={contentFilter} />

      <div className={`mt-3 rounded-md border px-3 py-2 text-xs ${
        contentFilter === "safe"
          ? "border-emerald-900/50 bg-emerald-950/30 text-emerald-300/80"
          : "border-amber-900/50 bg-amber-950/30 text-amber-200/80"
      }`}>
        {contentFilter === "safe"
          ? "🛡️ 安全模式已隐藏成人内容与垃圾信息。"
          : contentFilter === "adult"
            ? "🔞 当前仅显示标记为成人内容的结果。"
            : "🔞 当前结果包含成人内容；垃圾信息仍然隐藏。"}
      </div>

      {error ? (
        <BackendError q={q} contentFilter={contentFilter} detail={error} />
      ) : data && data.results && data.results.length > 0 ? (
        <>
          <p className="mt-4 text-sm text-zinc-400">
            {q ? (
              <>
                「<span className="text-zinc-200">{q}</span>」共找到{" "}
                {formatCount(data.total)} 条结果
              </>
            ) : (
              <>最新收录 · 共 {formatCount(data.total)} 条</>
            )}
          </p>
          <ResultList results={data.results} />
          <Pagination q={q} page={data.page ?? page} total={data.total ?? 0} pageSize={data.page_size ?? pageSize} contentFilter={contentFilter} />
        </>
      ) : (
        <div className="mt-16 text-center">
          <p className="text-lg text-zinc-300">没有找到相关结果</p>
          <p className="mt-2 text-sm text-zinc-500">
            {q
              ? "换个关键词试试，或者稍后再来——爬虫正在持续收录新资源。"
              : "索引暂时为空，爬虫正在收录中，请稍后再来。"}
          </p>
        </div>
      )}

      <footer className="mt-auto pt-10 text-center text-xs text-zinc-600">
        DHTSearch · 干净、无广告的磁力搜索
      </footer>
    </main>
  );
}

function ContentFilterTabs({ q, selected }: { q: string; selected: ContentFilter }) {
  const filters: Array<{ value: ContentFilter; label: string }> = [
    { value: "safe", label: "安全" },
    { value: "all", label: "全部" },
    { value: "adult", label: "成人" },
  ];
  const hrefFor = (value: ContentFilter) => {
    const params = new URLSearchParams();
    if (q) params.set("q", q);
    if (value === "all") params.set("include_adult", "true");
    if (value === "adult") params.set("category", "adult");
    const suffix = params.toString();
    return suffix ? `/search?${suffix}` : "/search";
  };

  return (
    <nav aria-label="内容筛选" className="mt-4 flex gap-1 rounded-lg bg-zinc-900 p-1">
      {filters.map((filter) => {
        const href = hrefFor(filter.value);
        return (
		  <a
			key={filter.value}
			href={href}
            className={`flex-1 rounded-md px-3 py-1.5 text-center text-sm transition-colors ${
              selected === filter.value
                ? "bg-zinc-700 text-zinc-100"
                : "text-zinc-400 hover:text-zinc-200"
            }`}
          >
            {filter.label}
		  </a>
        );
      })}
    </nav>
  );
}

function BackendError({ q, contentFilter, detail }: { q: string; contentFilter: ContentFilter; detail: string }) {
  const retryParams = new URLSearchParams();
  if (q) retryParams.set("q", q);
  if (contentFilter === "all") retryParams.set("include_adult", "true");
  if (contentFilter === "adult") retryParams.set("category", "adult");
  const retryQuery = retryParams.toString();
  return (
    <div className="mt-16 text-center">
      <p className="text-lg text-zinc-300">无法连接到搜索服务</p>
      <p className="mt-2 text-sm text-zinc-500">
        后端服务似乎未启动或暂时不可达，请确认 Go 后端已在运行
        （默认 <code className="rounded bg-zinc-800 px-1.5 py-0.5 font-mono text-xs">http://localhost:8080</code>
        ，可通过 <code className="rounded bg-zinc-800 px-1.5 py-0.5 font-mono text-xs">NEXT_PUBLIC_API_BASE</code> 修改）。
      </p>
      <p className="mt-1 font-mono text-xs text-zinc-600">{detail}</p>
      <Link
        href={retryQuery ? `/search?${retryQuery}` : "/search"}
        className="mt-6 inline-block rounded-md border border-zinc-700 bg-zinc-800 px-4 py-2 text-sm text-zinc-300 hover:border-emerald-600 hover:text-emerald-400"
      >
        重试
      </Link>
    </div>
  );
}
