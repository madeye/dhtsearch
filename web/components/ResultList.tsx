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
        <div className="fixed inset-x-0 bottom-4 z-10 px-4">
          <div className="mx-auto flex w-full max-w-3xl items-center justify-between gap-3 rounded-lg border border-zinc-700 bg-zinc-900/95 px-4 py-3 shadow-lg shadow-black/40 backdrop-blur">
            <span className="text-sm text-zinc-300">
              已选 <span className="font-medium text-emerald-400">{selected.size}</span> 条
            </span>
            <div className="flex items-center gap-2">
              <button
                onClick={toggleAll}
                className="rounded-md border border-zinc-700 bg-zinc-800 px-3 py-1.5 text-xs font-medium text-zinc-300 transition-colors hover:border-emerald-600 hover:text-emerald-400"
              >
                {allSelected ? "取消全选" : "全选本页"}
              </button>
              <button
                onClick={copySelected}
                className={`rounded-md border px-3 py-1.5 text-xs font-medium transition-colors ${
                  copied
                    ? "border-emerald-600 bg-emerald-600/20 text-emerald-400"
                    : "border-emerald-700 bg-emerald-600/10 text-emerald-400 hover:bg-emerald-600/20"
                }`}
              >
                {copied ? "✓ 已复制" : "复制磁力链接"}
              </button>
            </div>
          </div>
        </div>
      )}
    </>
  );
}
