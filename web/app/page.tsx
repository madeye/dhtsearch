import { fetchStats } from "@/lib/api";
import { formatCount } from "@/lib/format";
import SearchBox from "@/components/SearchBox";

export default async function Home() {
  const stats = await fetchStats();

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
		  基于 DHT 网络实时收录，成人内容默认隐藏、可按需查看
        </p>

        <div className="mt-10">
          <SearchBox large />
        </div>

        <p className="mt-4 text-center text-xs text-zinc-500">
          直接输入关键词搜索，留空则浏览最新收录的资源
        </p>
      </div>

      <footer className="mt-16 text-center text-xs text-zinc-500">
        {stats ? (
          <p>
			已收录 {formatCount(stats.torrents)} 条 · 已分类成人内容{" "}
			{formatCount(stats.adult_classified)} 条 · 已过滤垃圾信息{" "}
            {formatCount(stats.spam_filtered)} 条
            {stats.crawler_running === false && (
              <span className="ml-2 text-amber-500">（爬虫暂停中）</span>
            )}
          </p>
        ) : (
          <p>索引统计暂时不可用（后端未连接）</p>
        )}
      </footer>
    </main>
  );
}
