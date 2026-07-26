import Link from "next/link";

interface PaginationProps {
  q: string;
  page: number;
  total: number;
  pageSize: number;
}

export default function Pagination({ q, page, total, pageSize }: PaginationProps) {
  const totalPages = Math.max(1, Math.ceil(total / pageSize));
  const mkHref = (p: number) =>
    `/search?q=${encodeURIComponent(q)}&page=${p}`;

  const btnCls =
    "rounded-md border border-zinc-700 bg-zinc-800 px-4 py-2 text-sm text-zinc-300 transition-colors hover:border-emerald-600 hover:text-emerald-400";
  const disabledCls =
    "cursor-not-allowed rounded-md border border-zinc-800 bg-zinc-900 px-4 py-2 text-sm text-zinc-600";

  return (
    <nav className="mt-6 flex items-center justify-center gap-4">
      {page > 1 ? (
        <Link href={mkHref(page - 1)} className={btnCls}>
          ← 上一页
        </Link>
      ) : (
        <span className={disabledCls}>← 上一页</span>
      )}
      <span className="text-sm text-zinc-400">
        第 {page} / {totalPages} 页
      </span>
      {page < totalPages ? (
        <Link href={mkHref(page + 1)} className={btnCls}>
          下一页 →
        </Link>
      ) : (
        <span className={disabledCls}>下一页 →</span>
      )}
    </nav>
  );
}
