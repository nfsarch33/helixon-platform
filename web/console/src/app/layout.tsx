import type { Metadata } from "next";
import "./globals.css";
import { Nav } from "../components/Nav";

export const metadata: Metadata = {
  title: "Helixon console",
  description: "Operator console for a Helixon agent: runs, evals, costs, memory.",
  robots: { index: false, follow: false },
};

// Fluid shell: the content column grows with the window up to 1536 px (max-w-screen-2xl) with responsive side
// gutters, instead of a fixed 1,152 px column that left a third of a wide screen empty. Prose blocks cap their own
// line length; tables and dashboards use the width.
const shell = "mx-auto w-full max-w-screen-2xl px-4 sm:px-6 lg:px-8";

export default function RootLayout({ children }: { children: React.ReactNode }) {
  return (
    <html lang="en">
      <body className="min-h-dvh bg-slate-50 text-slate-900 antialiased dark:bg-slate-950 dark:text-slate-100">
        <a href="#main" className="sr-only focus:not-sr-only focus:absolute focus:left-2 focus:top-2 focus:rounded focus:bg-white focus:p-2">Skip to content</a>
        <header className="border-b border-slate-200 bg-white dark:border-slate-800 dark:bg-slate-900">
          <div className={`${shell} flex flex-wrap items-center justify-between gap-3 py-3`}>
            <h1 className="text-lg font-semibold">Helixon console</h1>
            <Nav />
          </div>
        </header>
        <main id="main" className={`${shell} py-6`}>{children}</main>
      </body>
    </html>
  );
}
