import {
  CheckSquare,
  Copy,
  FileText,
  History,
  Lightbulb,
  ListChecks,
  MessageSquareText,
  Pin,
  PinOff,
  X,
} from "lucide-react";
import { useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";

import type {
  State,
  Suggestion,
} from "../../bindings/github.com/tomvokac/parley/internal/analysis/models";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { ScrollArea } from "@/components/ui/scroll-area";
import { speakerVariant } from "@/lib/speaker";
import { normKey } from "@/lib/normalize";
import { cn } from "@/lib/utils";

function Panel({
  title,
  icon,
  scroll = true,
  className,
  children,
}: {
  title: string;
  icon: ReactNode;
  scroll?: boolean;
  className?: string;
  children: ReactNode;
}) {
  return (
    <Card className={cn("min-h-0", className)}>
      <CardHeader>
        <CardTitle className="text-sm">
          {icon}
          {title}
        </CardTitle>
      </CardHeader>
      <CardContent className="flex-1 min-h-0 p-0">
        {scroll ? (
          <ScrollArea className="h-full px-4 pb-3">{children}</ScrollArea>
        ) : (
          <div className="px-4 pb-3">{children}</div>
        )}
      </CardContent>
    </Card>
  );
}

function Empty({ children }: { children: ReactNode }) {
  return (
    <div className="flex h-full items-center justify-center py-10 text-center text-xs text-muted-foreground/50">
      {children}
    </div>
  );
}

function SummaryContent({ summary }: { summary: string }) {
  const lines = summary
    .split(/\r?\n/)
    .map((line) => line.trim())
    .filter(Boolean);

  if (lines.length === 0) {
    return <Empty>The live summary will build as the discussion develops.</Empty>;
  }

  return (
    <ul className="flex flex-col gap-2 py-1">
      {lines.map((line, i) => {
        const text = line.replace(/^[-*]\s+/, "");
        return (
          <li key={`${i}-${text}`} className="flex gap-2 text-sm leading-relaxed">
            <span className="mt-2 h-1.5 w-1.5 shrink-0 rounded-full bg-primary/70" />
            <span>{text}</span>
          </li>
        );
      })}
    </ul>
  );
}

// Copy text to the clipboard, swallowing the rejection that WebView2 can throw if
// focus/permission is momentarily unavailable — a failed copy must never surface.
async function copyText(text: string) {
  try {
    await navigator.clipboard.writeText(text);
  } catch {
    /* clipboard unavailable — no-op */
  }
}

// ItemActions is the hover/focus-revealed control row on a list item. Copy is
// always available; pin/dismiss only when the parent passes the handlers.
function ItemActions({
  onCopy,
  onPin,
  pinned,
  onDismiss,
}: {
  onCopy: () => void;
  onPin?: () => void;
  pinned?: boolean;
  onDismiss?: () => void;
}) {
  return (
    <div className="flex shrink-0 items-center gap-0.5 opacity-0 transition-opacity group-hover:opacity-100 focus-within:opacity-100">
      <Button
        variant="ghost"
        size="icon"
        className="h-6 w-6"
        title="Copy to clipboard"
        aria-label="Copy to clipboard"
        onClick={onCopy}
      >
        <Copy className="h-3.5 w-3.5" />
      </Button>
      {onPin && (
        <Button
          variant="ghost"
          size="icon"
          className="h-6 w-6"
          title={pinned ? "Unpin" : "Pin to top"}
          aria-label={pinned ? "Unpin suggestion" : "Pin suggestion"}
          aria-pressed={pinned}
          onClick={onPin}
        >
          {pinned ? <PinOff className="h-3.5 w-3.5" /> : <Pin className="h-3.5 w-3.5" />}
        </Button>
      )}
      {onDismiss && (
        <Button
          variant="ghost"
          size="icon"
          className="h-6 w-6"
          title="Dismiss"
          aria-label="Dismiss suggestion"
          onClick={onDismiss}
        >
          <X className="h-3.5 w-3.5" />
        </Button>
      )}
    </div>
  );
}

// useNewKeys returns the subset of keys that are new since the previous render.
// It depends on the content of the key list (not identity), so unrelated
// re-renders (hover, copy state) don't re-trigger; the fresh set self-clears after
// ~2s. When enabled is false (e.g. viewing/resuming a saved meeting) it simply
// seeds the baseline so a bulk load doesn't strobe every row as "new".
function useNewKeys(keys: string[], enabled: boolean): Set<string> {
  const prev = useRef<Set<string>>(new Set());
  const [fresh, setFresh] = useState<Set<string>>(new Set());
  const joined = keys.join("");

  useEffect(() => {
    const now = new Set(keys);
    if (!enabled) {
      prev.current = now;
      setFresh(new Set());
      return;
    }
    const added = new Set<string>();
    for (const k of now) if (!prev.current.has(k)) added.add(k);
    prev.current = now;
    if (added.size === 0) return;
    setFresh(added);
    const t = setTimeout(() => setFresh(new Set()), 2000);
    return () => clearTimeout(t);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [joined, enabled]);

  return fresh;
}

// Newly arrived items slide in from the top (pushing the rest down) and briefly
// ring, so an incoming suggestion/assertion reads as landing on top of the stack.
const NEW_HIGHLIGHT =
  "animate-in fade-in slide-in-from-top-2 duration-300 ring-1 ring-primary/40";

export function AnalysisPanels({
  state,
  active,
  highlight,
  pinnedKeys,
  onPin,
  onDismiss,
}: {
  state: State;
  active: boolean;
  // Highlight newly arrived items — only during a live meeting, never on a
  // saved-session view/resume (where everything would look "new").
  highlight: boolean;
  pinnedKeys: Set<string>;
  onPin: (s: Suggestion) => void;
  onDismiss: (s: Suggestion) => void;
}) {
  const current = state.current;
  const assertions = current?.assertions ?? [];
  const suggestions = state.suggestions ?? [];
  const past = state.past ?? [];
  const actionItems = state.actionItems ?? [];
  const summary = state.summary ?? "";
  const hasTopic = !!current?.title;

  const newSuggestions = useNewKeys(
    suggestions.map((s) => normKey(s.text)),
    highlight
  );
  const newAssertions = useNewKeys(
    assertions.map((a) => normKey(a.text)),
    highlight
  );
  const newActions = useNewKeys(
    actionItems.map((a) => normKey(a.text)),
    highlight
  );

  const idleHint = active
    ? "Analyzing… insight appears as the conversation develops."
    : "Start a session with an LLM endpoint configured.";

  return (
    <div className="flex min-h-0 flex-col gap-4">
      {/* Current topic — full-width banner, sized to its content. */}
      <Panel
        title="Current topic"
        icon={<MessageSquareText className="h-4 w-4 text-primary" />}
        scroll={false}
        className="shrink-0"
      >
        {hasTopic ? (
          <div className="flex flex-col gap-2 py-1">
            <div className="text-base font-semibold leading-snug">
              {current.title}
            </div>
            {current.summary && (
              <p className="text-sm text-muted-foreground leading-relaxed">
                {current.summary}
              </p>
            )}
          </div>
        ) : (
          <div className="py-6 text-center text-xs text-muted-foreground/50">
            {idleHint}
          </div>
        )}
      </Panel>

      <Panel
        title="Live summary"
        icon={<FileText className="h-4 w-4 text-primary" />}
        className="h-40 shrink-0"
      >
        <SummaryContent summary={summary} />
      </Panel>

      {/* The four list panels share the remaining height. */}
      <div className="grid min-h-0 flex-1 grid-cols-2 grid-rows-2 gap-4">
        <Panel
          title="Suggested questions"
          icon={<Lightbulb className="h-4 w-4 text-amber-400" />}
        >
          {suggestions.length ? (
            <ul className="flex flex-col gap-2 py-1">
              {suggestions.map((s) => {
                const key = normKey(s.text);
                const pinned = pinnedKeys.has(key);
                return (
                  <li
                    key={key}
                    className={cn(
                      "group flex items-start gap-2 rounded-md bg-accent/40 p-2",
                      pinned && "ring-1 ring-primary/40",
                      newSuggestions.has(key) && NEW_HIGHLIGHT
                    )}
                  >
                    <div className="flex min-w-0 flex-1 flex-col gap-1">
                      <Badge
                        variant="outline"
                        className="w-fit text-[10px] uppercase tracking-wide"
                      >
                        {s.kind}
                      </Badge>
                      <span className="text-sm leading-snug">{s.text}</span>
                    </div>
                    <ItemActions
                      onCopy={() => copyText(s.text)}
                      onPin={() => onPin(s)}
                      pinned={pinned}
                      onDismiss={() => onDismiss(s)}
                    />
                  </li>
                );
              })}
            </ul>
          ) : (
            <Empty>No suggestions yet.</Empty>
          )}
        </Panel>

        <Panel
          title="Action items"
          icon={<CheckSquare className="h-4 w-4 text-primary" />}
        >
          {actionItems.length ? (
            <ul className="flex flex-col gap-2 py-1">
              {actionItems.map((a) => {
                const key = normKey(a.text);
                return (
                  <li
                    key={key}
                    className={cn(
                      "group flex items-start gap-2 rounded-md bg-accent/40 p-2",
                      newActions.has(key) && NEW_HIGHLIGHT
                    )}
                  >
                    <span className="min-w-0 flex-1 text-sm leading-snug">
                      {a.text}
                    </span>
                    <Badge
                      variant={a.owner ? "you" : "outline"}
                      className="shrink-0"
                    >
                      {a.owner || "Unassigned"}
                    </Badge>
                    <ItemActions onCopy={() => copyText(a.text)} />
                  </li>
                );
              })}
            </ul>
          ) : (
            <Empty>Action items will collect here as the meeting progresses.</Empty>
          )}
        </Panel>

        <Panel
          title="Assertions"
          icon={<ListChecks className="h-4 w-4 text-others" />}
        >
          {assertions.length ? (
            <ul className="flex flex-col gap-2 py-1">
              {assertions.map((a) => {
                const key = normKey(a.text);
                return (
                  <li
                    key={key}
                    className={cn(
                      "flex gap-2 rounded-md p-1",
                      newAssertions.has(key) && NEW_HIGHLIGHT
                    )}
                  >
                    <Badge variant={speakerVariant(a.speaker)}>{a.speaker}</Badge>
                    <span className="text-sm leading-snug">{a.text}</span>
                  </li>
                );
              })}
            </ul>
          ) : (
            <Empty>Points made on the current topic will appear here.</Empty>
          )}
        </Panel>

        <Panel title="Past topics" icon={<History className="h-4 w-4" />}>
          {past.length ? (
            <ul className="flex flex-col gap-2 py-1">
              {[...past].reverse().map((t, i) => (
                <li key={i} className="rounded-md border border-border/60 p-2">
                  <div className="text-sm font-medium">{t.title}</div>
                  {t.summary && (
                    <div className="mt-0.5 text-xs text-muted-foreground">
                      {t.summary}
                    </div>
                  )}
                  <div className="mt-1 text-[10px] text-muted-foreground/60">
                    {t.assertions?.length ?? 0} assertion
                    {(t.assertions?.length ?? 0) === 1 ? "" : "s"}
                  </div>
                </li>
              ))}
            </ul>
          ) : (
            <Empty>Earlier topics will be archived here.</Empty>
          )}
        </Panel>
      </div>
    </div>
  );
}
