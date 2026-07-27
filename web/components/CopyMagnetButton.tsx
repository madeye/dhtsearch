"use client";

import { useState } from "react";

export default function CopyMagnetButton({ magnet }: { magnet: string }) {
  const [copied, setCopied] = useState(false);

  async function copy() {
    try {
      await navigator.clipboard.writeText(magnet);
    } catch {
      // Fallback for older browsers / non-secure contexts
      const ta = document.createElement("textarea");
      ta.value = magnet;
      document.body.appendChild(ta);
      ta.select();
      document.execCommand("copy");
      document.body.removeChild(ta);
    }
    setCopied(true);
    setTimeout(() => setCopied(false), 1500);
  }

  const label = copied ? "已复制磁力链接" : "复制磁力链接";

  return (
    // The full label costs a quarter of a phone's width and leaves the torrent
    // name almost no room, so below sm: this collapses to a square icon button.
    <button
      onClick={copy}
      title={label}
      aria-label={label}
      className={`flex h-10 w-10 shrink-0 items-center justify-center rounded-md border text-xs font-medium transition-colors sm:h-auto sm:w-auto sm:px-3 sm:py-1.5 ${
        copied
          ? "border-emerald-600 bg-emerald-600/20 text-emerald-400"
          : "border-zinc-700 bg-zinc-800 text-zinc-300 hover:border-emerald-600 hover:text-emerald-400"
      }`}
    >
      <span aria-hidden className="sm:hidden">
        {copied ? <CheckIcon /> : <LinkIcon />}
      </span>
      <span className="hidden sm:inline">{copied ? "✓ 已复制" : "复制磁力链接"}</span>
    </button>
  );
}

function LinkIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="h-4 w-4"
    >
      <path d="M10 13a5 5 0 0 0 7.54.54l3-3a5 5 0 0 0-7.07-7.07l-1.72 1.71" />
      <path d="M14 11a5 5 0 0 0-7.54-.54l-3 3a5 5 0 0 0 7.07 7.07l1.71-1.71" />
    </svg>
  );
}

function CheckIcon() {
  return (
    <svg
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="2.5"
      strokeLinecap="round"
      strokeLinejoin="round"
      className="h-4 w-4"
    >
      <path d="M20 6 9 17l-5-5" />
    </svg>
  );
}
