import type { Metadata } from "next";
import "./globals.css";
import { Nav } from "../components/Nav";

export const metadata: Metadata = {
  title: "Helixon console",
  description: "Operator console for a Helixon agent: runs, evals, costs, memory.",
  robots: { index: false, follow: false },
};

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body className="min-h-screen bg-slate-50 text-slate-900 antialiased dark:bg-slate-950 dark:text-slate-100">
        <a href="#main" className="sr-only focus:not-sr-only focus:absolute focus:left-2 focus:top-2 focus:rounded focus:bg-white focus:p-2">Skip to content</a>
        <header className="border-b border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
          <div className="mx-auto flex max-w-6xl flex-wrap items-center justify-between gap-3 px-4 py-3">
            <h1 className="text-lg font-semibold">Helixon console</h1>
            <Nav />
          </div>
        </header>
        <main id="main" className="mx-auto max-w-6xl px-4 py-6">{children}</main>
      </body>
    </html>
  );
}
