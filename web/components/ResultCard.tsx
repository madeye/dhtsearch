"use client";

import { useState } from "react";
import type { SearchResult } from "@/lib/api";
import { formatRelativeTime, formatSize } from "@/lib/format";
import CopyMagnetButton from "./CopyMagnetButton";

const MAX_FILES_SHOWN = 10;

export default function ResultCard({ result }: { result: SearchResult }) {
  const [expanded, setExpanded] = useState(false);
  const files = result.files ?? [];
  const shownFiles = files.slice(0, MAX_FILES_SHOWN);

  return (
    <div className="rounded-lg border border-zinc-800 bg-zinc-900/60 p-4 transition-colors hover:border-zinc-700">
      <div className="flex items-start justify-between gap-3">
        <button
          onClick={() => setExpanded((v) => !v)}
          className="min-w-0 flex-1 text-left"
          title={result.name}
        >
          <h3 className="truncate text-sm font-medium text-emerald-400 hover:text-emerald-300 sm:text-base">
            {result.name || "(未命名)"}
          </h3>
          <p className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 text-xs text-zinc-400">
            <span>{formatSize(result.total_size)}</span>
            <span>{result.file_count ?? files.length} 个文件</span>
            <span>收录于 {formatRelativeTime(result.created_at)}</span>
          </p>
        </button>
        <CopyMagnetButton magnet={result.magnet} />
      </div>

      {expanded && (
        <div className="mt-3 rounded-md border border-zinc-800 bg-zinc-950/60 p-3">
          {files.length > 0 ? (
            <ul className="space-y-1 text-xs text-zinc-400">
              {shownFiles.map((f, i) => (
                <li key={i} className="flex items-baseline justify-between gap-3">
                  <span className="min-w-0 truncate font-mono">{f.path}</span>
                  <span className="shrink-0 text-zinc-500">{formatSize(f.size)}</span>
                </li>
              ))}
              {files.length > MAX_FILES_SHOWN && (
                <li className="pt-1 text-zinc-500">
                  … 其余 {files.length - MAX_FILES_SHOWN} 个文件未显示
                </li>
              )}
            </ul>
          ) : (
            <p className="text-xs text-zinc-500">暂无文件列表信息</p>
          )}
          <p className="mt-2 break-all font-mono text-[11px] leading-relaxed text-zinc-600">
            {result.magnet}
          </p>
        </div>
      )}
    </div>
  );
}
