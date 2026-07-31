import Link from "next/link";
import { fetchStats, fetchTrending } from "@/lib/api";
import { formatCount } from "@/lib/format";
import SearchBox from "@/components/SearchBox";
import DigitalOceanBadge from "@/components/DigitalOceanBadge";

function TrendingRow({ label, titles }: { label: string; titles: string[] }) {
  return (
    <div className="mt-3 flex flex-wrap items-center justify-center gap-2">
      <span className="text-xs text-zinc-500">{label}</span>
      {titles.map((title) => (
        <Link
          key={title}
          href={`/search?q=${encodeURIComponent(title)}`}
          className="rounded-full border border-zinc-800 bg-zinc-900 px-3 py-1 text-xs text-zinc-300 transition-colors hover:border-emerald-700 hover:text-emerald-400"
        >
          {title}
        </Link>
      ))}
    </div>
  );
}

export default async function Home() {
  const [stats, trending] = await Promise.all([fetchStats(), fetchTrending()]);
  const rows = [
    { label: "热门电影", titles: trending?.movies ?? [] },
    { label: "热门美剧", titles: trending?.tv ?? [] },
    { label: "热门日剧", titles: trending?.tv_jp ?? [] },
    { label: "热门韩剧", titles: trending?.tv_kr ?? [] },
  ].filter((r) => r.titles.length > 0);

  return (
    <main className="flex flex-1 flex-col items-center justify-center px-4 py-16">
      <div className="w-full max-w-2xl">
        <h1 className="text-center text-4xl font-bold tracking-tight sm:text-5xl">
          DHT<span className="text-emerald-500">Search</span>
        </h1>
        <p className="mt-4 text-center text-sm text-zinc-400 sm:text-base">
          干净、无广告的磁力链接搜索引擎
          <br className="sm:hidden" />
          <span className="hidden sm:inline"> · </span>
          基于 DHT 网络实时收录，自动过滤成人内容与垃圾信息
        </p>

        <div className="mt-10">
          <SearchBox large />
        </div>

        <p className="mt-4 text-center text-xs text-zinc-500">
          直接输入关键词搜索，留空则浏览最新收录的资源
        </p>

        {rows.length > 0 && (
          <div className="mt-8">
            {rows.map((r) => (
              <TrendingRow key={r.label} label={r.label} titles={r.titles} />
            ))}
            <p className="mt-2 text-center text-[10px] text-zinc-600">
              热门影视数据来自豆瓣
            </p>
          </div>
        )}
      </div>

      <footer className="mt-16 text-center text-xs text-zinc-500">
        {stats ? (
          <p>
            已收录 {formatCount(stats.torrents)} 条 · 已过滤成人内容{" "}
            {formatCount(stats.adult_filtered)} 条 · 垃圾信息{" "}
            {formatCount(stats.spam_filtered)} 条
            {stats.crawler_running === false && (
              <span className="ml-2 text-amber-500">（爬虫暂停中）</span>
            )}
          </p>
        ) : (
          <p>索引统计暂时不可用（后端未连接）</p>
        )}
        <div className="mt-6 flex justify-center">
          <DigitalOceanBadge />
        </div>
      </footer>
    </main>
  );
}
