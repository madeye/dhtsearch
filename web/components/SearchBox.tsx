"use client";

import { FormEvent, useState } from "react";
import type { ContentFilter } from "@/lib/api";

interface SearchBoxProps {
  initialQuery?: string;
  large?: boolean;
  contentFilter?: ContentFilter;
}

export default function SearchBox({ initialQuery = "", large = false, contentFilter = "safe" }: SearchBoxProps) {
  const [q, setQ] = useState(initialQuery);

  function onSubmit(e: FormEvent) {
    e.preventDefault();
    const query = q.trim();
    // Cloudflare may return an interstitial Managed Challenge for /search.
    // A client-side router fetch cannot display that HTML challenge, so make
    // search a document navigation and let Cloudflare complete its normal
    // challenge -> cf_clearance -> page flow.
    const params = new URLSearchParams();
    if (query) params.set("q", query);
    if (contentFilter === "all") params.set("include_adult", "true");
    if (contentFilter === "adult") params.set("category", "adult");
    const suffix = params.toString();
    window.location.assign(suffix ? `/search?${suffix}` : "/search");
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
          // min-w-0 lets the input shrink past its intrinsic size; without it
          // the row overflows narrow viewports and pushes the submit button
          // off-screen.
          className={`min-w-0 flex-1 rounded-lg border border-zinc-700 bg-zinc-900 text-zinc-100 placeholder-zinc-500 outline-none focus:border-emerald-500 ${
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
