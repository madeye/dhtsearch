import type { Metadata } from "next";
import "./globals.css";

export const metadata: Metadata = {
  title: {
    default: "DHTSearch - 干净的磁力链接搜索",
    template: "%s - DHTSearch",
  },
  description:
    "干净、无广告的磁力链接搜索引擎。基于 DHT 网络实时收录，自动过滤成人内容与垃圾信息。",
};

export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html lang="zh-CN" className="h-full">
      <body className="flex min-h-full flex-col bg-zinc-950 text-zinc-100 antialiased">
        {children}
      </body>
    </html>
  );
}
