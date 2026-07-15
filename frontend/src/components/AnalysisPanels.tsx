import {
  CheckSquare,
  Copy,
  FileText,
  Lightbulb,
  ListChecks,
  MessageSquareText,
  Pin,
  PinOff,
  X,
} from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";

import type {
  State,
  Suggestion,
  Topic,
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
  action,
  className,
  children,
}: {
  title: string;
  icon: ReactNode;
  action?: ReactNode;
  className?: string;
  children: ReactNode;
}) {
  return (
    <Card className={cn("min-h-0 overflow-hidden", className)}>
      <CardHeader className="px-3 pt-3 pb-2">
        <div className="flex items-center gap-2">
          <CardTitle className="min-w-0 flex-1 text-sm">
            {icon}
            {title}
          </CardTitle>
          {action}
        </div>
      </CardHeader>
      <CardContent className="flex-1 min-h-0 p-0">{children}</CardContent>
    </Card>
  );
}

function PanelScroll({ children }: { children: ReactNode }) {
  return <ScrollArea className="h-full px-3 pb-3">{children}</ScrollArea>;
}

function Empty({ children }: { children: ReactNode }) {
  return (
    <div className="flex h-full min-h-24 items-center justify-center px-4 py-6 text-center text-xs text-muted-foreground/50">
      {children}
    </div>
  );
}

async function copyText(text: string) {
  try {
    await navigator.clipboard.writeText(text);
  } catch {
    // Clipboard access can be temporarily unavailable in WebView2.
  }
}

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
      <Button variant="ghost" size="icon" className="h-6 w-6" title="Copy" aria-label="Copy" onClick={onCopy}>
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
        <Button variant="ghost" size="icon" className="h-6 w-6" title="Dismiss" aria-label="Dismiss suggestion" onClick={onDismiss}>
          <X className="h-3.5 w-3.5" />
        </Button>
      )}
    </div>
  );
}

function useNewKeys(keys: string[], enabled: boolean): Set<string> {
  const prev = useRef<Set<string>>(new Set());
  const [fresh, setFresh] = useState<Set<string>>(new Set());
  const joined = keys.join("\u0001");

  useEffect(() => {
    const now = new Set(keys);
    if (!enabled) {
      prev.current = now;
      setFresh(new Set());
      return;
    }
    const added = new Set<string>();
    for (const key of now) if (!prev.current.has(key)) added.add(key);
    prev.current = now;
    if (added.size === 0) return;
    setFresh(added);
    const timer = setTimeout(() => setFresh(new Set()), 2000);
    return () => clearTimeout(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [joined, enabled]);

  return fresh;
}

const NEW_HIGHLIGHT = "animate-in fade-in slide-in-from-top-2 duration-300 ring-1 ring-primary/40";

function topicPoints(topic: Topic): string[] {
  const points = (topic.points ?? []).map((point) => point.trim()).filter(Boolean);
  if (points.length) return points;
  return topic.summary?.trim() ? [topic.summary.trim()] : [];
}

function legacySummaryPoints(summary: string): string[] {
  return summary
    .split(/\r?\n/)
    .map((line) => line.trim().replace(/^[-*]\s+/, ""))
    .filter(Boolean);
}

function DiscussionOutline({ state }: { state: State }) {
  const viewportRef = useRef<HTMLDivElement | null>(null);
  const [following, setFollowing] = useState(true);
  const topics = useMemo(
    () => [...(state.past ?? []), ...(state.current?.title ? [state.current] : [])],
    [state.past, state.current]
  );
  const usableTopics = topics.filter((topic) => topic.title || topicPoints(topic).length);
  const legacyPoints = usableTopics.length ? [] : legacySummaryPoints(state.summary ?? "");
  const outlineSignature = JSON.stringify(
    usableTopics.map((topic) => [topic.title, topic.summary, topic.points])
  ) + JSON.stringify(legacyPoints);

  useEffect(() => {
    const viewport = viewportRef.current;
    if (!viewport) return;
    const onScroll = () => {
      const distance = viewport.scrollHeight - viewport.scrollTop - viewport.clientHeight;
      setFollowing(distance <= 48);
    };
    viewport.addEventListener("scroll", onScroll, { passive: true });
    onScroll();
    return () => viewport.removeEventListener("scroll", onScroll);
  }, []);

  useEffect(() => {
    const viewport = viewportRef.current;
    if (following && viewport) {
      viewport.scrollTop = viewport.scrollHeight;
    }
  }, [outlineSignature, following]);

  const jumpToLatest = () => {
    const viewport = viewportRef.current;
    if (!viewport) return;
    setFollowing(true);
    viewport.scrollTop = viewport.scrollHeight;
  };

  return (
    <Panel
      title="Discussion outline"
      icon={<FileText className="h-4 w-4 text-primary" />}
      action={
        !following && (usableTopics.length > 0 || legacyPoints.length > 0) ? (
          <Button variant="ghost" size="sm" className="h-6 px-2 text-[11px]" onClick={jumpToLatest}>
            Jump to latest
          </Button>
        ) : null
      }
    >
      <ScrollArea viewportRef={viewportRef} className="h-full px-3 pb-3">
        {usableTopics.length > 0 ? (
          <div className="flex flex-col gap-3 py-1">
            {usableTopics.map((topic, index) => {
              const current = index === usableTopics.length - 1 && topic === state.current;
              const points = topicPoints(topic);
              return (
                <section
                  key={`${index}-${topic.title}`}
                  className={cn("rounded-lg border border-border/60 p-2.5", current && "border-primary/40 bg-primary/5")}
                >
                  <div className="flex items-center gap-2">
                    <span className={cn("h-2 w-2 shrink-0 rounded-full", current ? "bg-primary" : "bg-muted-foreground/40")} />
                    <h3 className="text-sm font-semibold leading-snug">{topic.title || "Untitled topic"}</h3>
                    {current && <Badge variant="outline" className="ml-auto text-[9px] uppercase tracking-wide">Current</Badge>}
                  </div>
                  {points.length > 0 && (
                    <ul className="mt-2 flex flex-col gap-1.5 pl-4">
                      {points.map((point, pointIndex) => (
                        <li key={`${pointIndex}-${point}`} className="flex gap-2 text-sm leading-snug text-muted-foreground">
                          <span className="mt-1.5 h-1 w-1 shrink-0 rounded-full bg-muted-foreground/60" />
                          <span>{point}</span>
                        </li>
                      ))}
                    </ul>
                  )}
                </section>
              );
            })}
          </div>
        ) : legacyPoints.length > 0 ? (
          <ul className="flex flex-col gap-2 py-1">
            {legacyPoints.map((point, index) => (
              <li key={`${index}-${point}`} className="flex gap-2 text-sm leading-snug">
                <span className="mt-1.5 h-1.5 w-1.5 shrink-0 rounded-full bg-primary/70" />
                <span>{point}</span>
              </li>
            ))}
          </ul>
        ) : (
          <Empty>The meeting outline will build as meaningful points emerge.</Empty>
        )}
      </ScrollArea>
    </Panel>
  );
}

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
  highlight: boolean;
  pinnedKeys: Set<string>;
  onPin: (suggestion: Suggestion) => void;
  onDismiss: (suggestion: Suggestion) => void;
}) {
  const current = state.current;
  const assertions = current?.assertions ?? [];
  const suggestions = state.suggestions ?? [];
  const actionItems = state.actionItems ?? [];
  const hasTopic = !!current?.title;
  const newSuggestions = useNewKeys(suggestions.map((item) => normKey(item.text)), highlight);
  const newAssertions = useNewKeys(assertions.map((item) => normKey(item.text)), highlight);
  const newActions = useNewKeys(actionItems.map((item) => normKey(item.text)), highlight);
  const idleHint = active ? "Analyzing the discussion…" : "Start a session with an LLM endpoint configured.";

  return (
    <div className="flex min-h-0 flex-col gap-3">
      <Card className="shrink-0 overflow-hidden">
        <div className="flex min-h-20 items-start gap-3 px-3 py-3">
          <div className="mt-0.5 flex h-8 w-8 shrink-0 items-center justify-center rounded-lg bg-primary/12 text-primary">
            <MessageSquareText className="h-4 w-4" />
          </div>
          {hasTopic ? (
            <div className="min-w-0">
              <div className="text-[10px] font-medium uppercase tracking-wider text-muted-foreground">Current topic</div>
              <div className="mt-0.5 font-semibold leading-snug">{current.title}</div>
              {current.summary && <p className="mt-1 line-clamp-2 text-sm leading-snug text-muted-foreground">{current.summary}</p>}
            </div>
          ) : (
            <div className="self-center text-sm text-muted-foreground/60">{idleHint}</div>
          )}
        </div>
      </Card>

      <div className="grid min-h-0 flex-1 grid-cols-2 grid-rows-2 gap-3">
        <DiscussionOutline state={state} />

        <Panel title="Assertions" icon={<ListChecks className="h-4 w-4 text-others" />}>
          <PanelScroll>
            {assertions.length ? (
              <ul className="flex flex-col gap-1.5 py-1">
                {assertions.map((assertion) => {
                  const key = normKey(assertion.text);
                  return (
                    <li key={key} className={cn("flex gap-2 rounded-md p-1", newAssertions.has(key) && NEW_HIGHLIGHT)}>
                      <Badge variant={speakerVariant(assertion.speaker)}>{assertion.speaker}</Badge>
                      <span className="text-sm leading-snug">{assertion.text}</span>
                    </li>
                  );
                })}
              </ul>
            ) : (
              <Empty>Key points stated on the current topic will appear here.</Empty>
            )}
          </PanelScroll>
        </Panel>

        <Panel title="Action items" icon={<CheckSquare className="h-4 w-4 text-primary" />}>
          <PanelScroll>
            {actionItems.length ? (
              <ul className="flex flex-col gap-1.5 py-1">
                {actionItems.map((item) => {
                  const key = normKey(item.text);
                  return (
                    <li key={key} className={cn("group flex items-start gap-2 rounded-md bg-accent/40 p-2", newActions.has(key) && NEW_HIGHLIGHT)}>
                      <span className="min-w-0 flex-1 text-sm leading-snug">{item.text}</span>
                      <Badge variant={item.owner ? "you" : "outline"} className="shrink-0">{item.owner || "Unassigned"}</Badge>
                      <ItemActions onCopy={() => copyText(item.text)} />
                    </li>
                  );
                })}
              </ul>
            ) : (
              <Empty>Tasks and commitments will collect here.</Empty>
            )}
          </PanelScroll>
        </Panel>

        <Panel title="Suggested questions" icon={<Lightbulb className="h-4 w-4 text-amber-400" />}>
          <PanelScroll>
            {suggestions.length ? (
              <ul className="flex flex-col gap-1.5 py-1">
                {suggestions.map((suggestion) => {
                  const key = normKey(suggestion.text);
                  const pinned = pinnedKeys.has(key);
                  return (
                    <li key={key} className={cn("group flex items-start gap-2 rounded-md bg-accent/40 p-2", pinned && "ring-1 ring-primary/40", newSuggestions.has(key) && NEW_HIGHLIGHT)}>
                      <div className="flex min-w-0 flex-1 flex-col gap-1">
                        <Badge variant="outline" className="w-fit text-[9px] uppercase tracking-wide">{suggestion.kind}</Badge>
                        <span className="text-sm leading-snug">{suggestion.text}</span>
                      </div>
                      <ItemActions
                        onCopy={() => copyText(suggestion.text)}
                        onPin={() => onPin(suggestion)}
                        pinned={pinned}
                        onDismiss={() => onDismiss(suggestion)}
                      />
                    </li>
                  );
                })}
              </ul>
            ) : (
              <Empty>No high-value question is warranted right now.</Empty>
            )}
          </PanelScroll>
        </Panel>
      </div>
    </div>
  );
}
