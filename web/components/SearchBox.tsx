"use client";

import { useRouter } from "next/navigation";
import { FormEvent, useState } from "react";

interface SearchBoxProps {
  initialQuery?: string;
  large?: boolean;
}

export default function SearchBox({ initialQuery = "", large = false }: SearchBoxProps) {
  const router = useRouter();
  const [q, setQ] = useState(initialQuery);

  function onSubmit(e: FormEvent) {
    e.preventDefault();
    const query = q.trim();
    router.push(query ? `/search?q=${encodeURIComponent(query)}` : "/search");
  }

  return (
    <form onSubmit={onSubmit} className="w-full">
      <div className="flex w-full items-center gap-2">
        <input
          type="text"
          value={q}
          onChange={(e) => setQ(e.target.value)}
          placeholder="搜索磁力链接 / 资源名称…"
          autoFocus={large}
          className={`flex-1 rounded-lg border border-zinc-700 bg-zinc-900 text-zinc-100 placeholder-zinc-500 outline-none focus:border-emerald-500 ${
            large ? "px-5 py-3.5 text-lg" : "px-4 py-2 text-sm"
          }`}
        />
        <button
          type="submit"
          className={`shrink-0 rounded-lg bg-emerald-600 font-medium text-white transition-colors hover:bg-emerald-500 ${
            large ? "px-6 py-3.5 text-lg" : "px-4 py-2 text-sm"
          }`}
        >
          搜索
        </button>
      </div>
    </form>
  );
}
