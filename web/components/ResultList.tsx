"use client";

import { useState } from "react";
import type { SearchResult } from "@/lib/api";
import { copyText } from "@/lib/clipboard";
import ResultCard from "./ResultCard";

export default function ResultList({ results }: { results: SearchResult[] }) {
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());
  const [copied, setCopied] = useState(false);

  // Results can repeat an info_hash across pages but not within one, so the
  // hash is a safe selection key for this list.
  function toggle(infoHash: string) {
    setSelected((prev) => {
      const next = new Set(prev);
      if (next.has(infoHash)) {
        next.delete(infoHash);
      } else {
        next.add(infoHash);
      }
      return next;
    });
    setCopied(false);
  }

  const allSelected = selected.size === results.length;

  function toggleAll() {
    setSelected(allSelected ? new Set() : new Set(results.map((r) => r.info_hash)));
    setCopied(false);
  }

  function clearSelection() {
    setSelected(new Set());
    setCopied(false);
  }

  async function copySelected() {
    const magnets = results
      .filter((r) => selected.has(r.info_hash))
      .map((r) => r.magnet);
    if (magnets.length === 0) return;
    await copyText(magnets.join("\n"));
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  }

  return (
    <>
      <div className="mt-3 space-y-3">
        {results.map((r) => (
          <ResultCard
            key={r.info_hash}
            result={r}
            selected={selected.has(r.info_hash)}
            onToggleSelect={() => toggle(r.info_hash)}
          />
        ))}
      </div>

      {selected.size > 0 && (
        // Sticky, not fixed: the bar rides the viewport bottom while the list
        // scrolls, but settles into the flow at the end of the list, so it can
        // never cover the last card, the pagination, or the footer.
        <div className="sticky bottom-4 z-10 mt-3">
          <div className="flex w-full items-center justify-between gap-2 rounded-lg border border-zinc-700 bg-zinc-900/95 px-3 py-2.5 shadow-lg shadow-black/40 backdrop-blur sm:px-4 sm:py-3">
            <span className="shrink-0 text-sm text-zinc-300">
              已选 <span className="font-medium text-emerald-400">{selected.size}</span> 条
            </span>
            <div className="flex items-center gap-2">
              {/* Phones get shorter labels and taller (easier-to-tap) buttons. */}
              <button
                onClick={toggleAll}
                className="rounded-md border border-zinc-700 bg-zinc-800 px-3 py-2 text-xs font-medium text-zinc-300 transition-colors hover:border-emerald-600 hover:text-emerald-400 sm:py-1.5"
              >
                <span className="sm:hidden">{allSelected ? "取消" : "全选"}</span>
                <span className="hidden sm:inline">
                  {allSelected ? "取消全选" : "全选本页"}
                </span>
              </button>
              <button
                onClick={copySelected}
                className={`rounded-md border px-3 py-2 text-xs font-medium transition-colors sm:py-1.5 ${
                  copied
                    ? "border-emerald-600 bg-emerald-600/20 text-emerald-400"
                    : "border-emerald-700 bg-emerald-600/10 text-emerald-400 hover:bg-emerald-600/20"
                }`}
              >
                {copied ? (
                  "✓ 已复制"
                ) : (
                  <>
                    <span className="sm:hidden">复制链接</span>
                    <span className="hidden sm:inline">复制磁力链接</span>
                  </>
                )}
              </button>
              <button
                onClick={clearSelection}
                title="取消选择"
                aria-label="取消选择"
                className="flex h-8 w-8 shrink-0 items-center justify-center rounded-md border border-zinc-700 bg-zinc-800 text-zinc-400 transition-colors hover:border-zinc-500 hover:text-zinc-200 sm:h-7 sm:w-7"
              >
                <svg
                  viewBox="0 0 24 24"
                  fill="none"
                  stroke="currentColor"
                  strokeWidth="2"
                  strokeLinecap="round"
                  className="h-4 w-4"
                  aria-hidden
                >
                  <path d="M18 6 6 18M6 6l12 12" />
                </svg>
              </button>
            </div>
          </div>
        </div>
      )}
    </>
  );
}
