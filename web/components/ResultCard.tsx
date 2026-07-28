"use client";

import { useState } from "react";
import type { SearchResult } from "@/lib/api";
import {
  commonDirPrefix,
  formatCount,
  formatRelativeTime,
  formatSize,
  shortenDir,
} from "@/lib/format";
import CopyMagnetButton from "./CopyMagnetButton";

const MAX_FILES_SHOWN = 10;

export default function ResultCard({
  result,
  selected,
  onToggleSelect,
}: {
  result: SearchResult;
  selected: boolean;
  onToggleSelect: () => void;
}) {
  const [expanded, setExpanded] = useState(false);
  const files = result.files ?? [];
  const shownFiles = files.slice(0, MAX_FILES_SHOWN);
  // The crawler stores at most 50 file entries but records the real count, so
  // count what's hidden against file_count — otherwise a 3136-file torrent
  // claims only 40 files are missing.
  const totalFiles = Math.max(result.file_count ?? 0, files.length);
  const hiddenFiles = totalFiles - shownFiles.length;
  // Computed over every file, not just the shown ones, so the prefix doesn't
  // shift when the "+N more" tail is hidden.
  const root = commonDirPrefix(files.map((f) => f.path));

  return (
    <div
      className={`rounded-lg border bg-zinc-900/60 p-4 transition-colors ${
        selected
          ? "border-emerald-700/70 bg-emerald-950/20"
          : "border-zinc-800 hover:border-zinc-700"
      }`}
    >
      <div className="flex items-start justify-between gap-3">
        {/* Native checkbox for free keyboard/screen-reader semantics; mt-1
            lines it up with the first line of the title. */}
        <input
          type="checkbox"
          checked={selected}
          onChange={onToggleSelect}
          aria-label={`选择 ${result.name || "(未命名)"}`}
          className="mt-1 h-4 w-4 shrink-0 cursor-pointer accent-emerald-500"
        />
        <button
          onClick={() => setExpanded((v) => !v)}
          className="min-w-0 flex-1 text-left"
          title={result.name}
        >
          {/* Release names carry their resolution, codec and group at the very
              end, so clamp to two lines rather than truncating to one — and
              drop the clamp entirely once the card is open. Phone lines hold
              far fewer characters, so they get a third line to compensate. */}
          <h3
            className={`text-sm font-medium wrap-anywhere text-emerald-400 hover:text-emerald-300 sm:text-base ${
              expanded ? "" : "line-clamp-3 sm:line-clamp-2"
            }`}
          >
            {result.name || "(未命名)"}
          </h3>
          <p className="mt-1 flex flex-wrap gap-x-3 gap-y-0.5 text-xs text-zinc-400">
            <span>{formatSize(result.total_size)}</span>
            <span>{formatCount(totalFiles)} 个文件</span>
            <span>收录于 {formatRelativeTime(result.created_at)}</span>
          </p>
        </button>
        <CopyMagnetButton magnet={result.magnet} />
      </div>

      {expanded && (
        <div className="mt-3 rounded-md border border-zinc-800 bg-zinc-950/60 p-3">
          {files.length > 0 ? (
            <>
              {root && (
                <p
                  className="mb-2 line-clamp-2 border-b border-zinc-800/80 pb-2 font-mono text-[11px] wrap-anywhere text-zinc-500"
                  title={root}
                >
                  📁 {root}
                </p>
              )}
              <ul className="space-y-1.5 text-xs text-zinc-400">
                {shownFiles.map((f, i) => {
                  const rest = f.path.slice(root.length);
                  const cut = rest.lastIndexOf("/");
                  const dir = cut >= 0 ? rest.slice(0, cut + 1) : "";
                  const name = cut >= 0 ? rest.slice(cut + 1) : rest;
                  return (
                    <li key={i} className="flex items-baseline justify-between gap-3">
                      <span className="min-w-0 font-mono" title={f.path}>
                        {/* The directory is context, so it may truncate; the
                            file name is what tells two rows apart, so it wraps
                            in full on its own line. */}
                        {dir && (
                          <span className="block truncate text-zinc-600">
                            {shortenDir(dir)}
                          </span>
                        )}
                        <span className="block wrap-anywhere text-zinc-300">
                          {name}
                        </span>
                      </span>
                      <span className="shrink-0 tabular-nums text-zinc-500">
                        {formatSize(f.size)}
                      </span>
                    </li>
                  );
                })}
                {hiddenFiles > 0 && (
                  <li className="pt-1 text-zinc-500">
                    … 其余 {formatCount(hiddenFiles)} 个文件未显示
                  </li>
                )}
              </ul>
            </>
          ) : (
            <p className="text-xs text-zinc-500">暂无文件列表信息</p>
          )}
          <p
            className="mt-3 line-clamp-2 border-t border-zinc-800/80 pt-2 font-mono text-[11px] leading-relaxed break-all text-zinc-600"
            title={result.magnet}
          >
            {result.magnet}
          </p>
        </div>
      )}
    </div>
  );
}
