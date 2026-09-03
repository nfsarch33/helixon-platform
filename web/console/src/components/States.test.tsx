import { describe, it, expect } from "vitest";
import { render, screen } from "@testing-library/react";
import { Panel, EmptyState, ErrorState, Loading } from "./States";
import { ApiError } from "../lib/api";

describe("Panel", () => {
  // A DOM id cannot contain whitespace. Building aria-labelledby from a raw
  // multi-word title pointed it at no element at all, leaving the section
  // with no accessible name -- the assertion below is on the resolution, not
  // on the string, so a different slug is fine and a dangling one is not.
  it("gives a multi-word title an id that actually resolves", () => {
    const { container } = render(<Panel title="Cycle ledger (newest first)">body</Panel>);
    const section = container.querySelector("section")!;
    const id = section.getAttribute("aria-labelledby")!;
    expect(id).not.toMatch(/\s/);
    expect(container.querySelector(`#${CSS.escape(id)}`)).not.toBeNull();
    expect(container.querySelector(`#${CSS.escape(id)}`)!.textContent).toBe("Cycle ledger (newest first)");
  });
  it("renders its actions and children", () => {
    render(<Panel title="Runs" actions={<button>refresh</button>}>the body</Panel>);
    expect(screen.getByText("the body")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "refresh" })).toBeInTheDocument();
  });
});

describe("states", () => {
  it("an empty state shows its hint", () => {
    render(<EmptyState title="No runs yet" hint="start one" />);
    expect(screen.getByText("No runs yet")).toBeInTheDocument();
    expect(screen.getByText("start one")).toBeInTheDocument();
  });
  it("an error state is an alert carrying the server's reason", () => {
    render(<ErrorState error={new ApiError(503, "run store not initialized", "/api/v1/runs")} />);
    expect(screen.getByRole("alert")).toHaveTextContent("run store not initialized");
  });
  it("a loading state is a live status, not silence", () => {
    render(<Loading label="Loading run abc" />);
    expect(screen.getByRole("status")).toHaveTextContent("Loading run abc");
  });
});
