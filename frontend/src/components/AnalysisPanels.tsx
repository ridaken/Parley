import {
  History,
  Lightbulb,
  ListChecks,
  MessageSquareText,
} from "lucide-react";

import type { State } from "../../bindings/github.com/tomvokac/parley/internal/analysis/models";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { ScrollArea } from "@/components/ui/scroll-area";
import { speakerVariant } from "@/lib/speaker";
import type { ReactNode } from "react";

function Panel({
  title,
  icon,
  children,
}: {
  title: string;
  icon: ReactNode;
  children: ReactNode;
}) {
  return (
    <Card className="min-h-0">
      <CardHeader>
        <CardTitle className="text-sm">
          {icon}
          {title}
        </CardTitle>
      </CardHeader>
      <CardContent className="flex-1 min-h-0 p-0">
        <ScrollArea className="h-full px-4 pb-3">{children}</ScrollArea>
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

export function AnalysisPanels({
  state,
  active,
}: {
  state: State;
  active: boolean;
}) {
  const current = state.current;
  const assertions = current?.assertions ?? [];
  const suggestions = state.suggestions ?? [];
  const past = state.past ?? [];
  const hasTopic = !!current?.title;

  const idleHint = active
    ? "Analyzing… insight appears as the conversation develops."
    : "Start a session with an LLM endpoint configured.";

  return (
    <div className="grid min-h-0 grid-cols-2 grid-rows-2 gap-4">
      <Panel
        title="Current topic"
        icon={<MessageSquareText className="h-4 w-4 text-primary" />}
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
          <Empty>{idleHint}</Empty>
        )}
      </Panel>

      <Panel
        title="Suggested questions"
        icon={<Lightbulb className="h-4 w-4 text-amber-400" />}
      >
        {suggestions.length ? (
          <ul className="flex flex-col gap-2 py-1">
            {suggestions.map((s, i) => (
              <li key={i} className="flex flex-col gap-1 rounded-md bg-accent/40 p-2">
                <Badge
                  variant="outline"
                  className="w-fit text-[10px] uppercase tracking-wide"
                >
                  {s.kind}
                </Badge>
                <span className="text-sm leading-snug">{s.text}</span>
              </li>
            ))}
          </ul>
        ) : (
          <Empty>No suggestions yet.</Empty>
        )}
      </Panel>

      <Panel
        title="Assertions"
        icon={<ListChecks className="h-4 w-4 text-others" />}
      >
        {assertions.length ? (
          <ul className="flex flex-col gap-2 py-1">
            {assertions.map((a, i) => (
              <li key={i} className="flex gap-2">
                <Badge variant={speakerVariant(a.speaker)}>{a.speaker}</Badge>
                <span className="text-sm leading-snug">{a.text}</span>
              </li>
            ))}
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
                  {(t.assertions?.length ?? 0)} assertion
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
  );
}
