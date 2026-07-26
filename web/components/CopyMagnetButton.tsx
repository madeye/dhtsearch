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

  return (
    <button
      onClick={copy}
      className={`shrink-0 rounded-md border px-3 py-1.5 text-xs font-medium transition-colors ${
        copied
          ? "border-emerald-600 bg-emerald-600/20 text-emerald-400"
          : "border-zinc-700 bg-zinc-800 text-zinc-300 hover:border-emerald-600 hover:text-emerald-400"
      }`}
    >
      {copied ? "✓ 已复制" : "复制磁力链接"}
    </button>
  );
}
