import { useEffect, useRef, useState } from "react";
import { Events } from "@wailsio/runtime";
import {
  AudioLines,
  Loader2,
  Mic,
  MicOff,
  MessageSquareText,
  NotebookPen,
  Settings as SettingsIcon,
  SlidersHorizontal,
  Square,
} from "lucide-react";

import { MeetingService } from "../bindings/github.com/tomvokac/parley";
import type { StatusEvent } from "../bindings/github.com/tomvokac/parley";
import type { Segment } from "../bindings/github.com/tomvokac/parley/internal/stt/models";
import type { State as AnalysisState } from "../bindings/github.com/tomvokac/parley/internal/analysis/models";

import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { ScrollArea } from "@/components/ui/scroll-area";
import { cn } from "@/lib/utils";
import { AnalysisPanels } from "@/components/AnalysisPanels";
import { SettingsDialog } from "@/components/SettingsDialog";
import { ContextDialog } from "@/components/ContextDialog";
import { AudioDialog } from "@/components/AudioDialog";
import { speakerVariant } from "@/lib/speaker";

function fmtTime(ms: number): string {
  const s = Math.floor(ms / 1000);
  const m = Math.floor(s / 60);
  return `${m}:${String(s % 60).padStart(2, "0")}`;
}

const STATE_LABEL: Record<string, string> = {
  idle: "Idle",
  starting: "Starting…",
  listening: "Listening",
  error: "Error",
};

const EMPTY_ANALYSIS: AnalysisState = {
  current: { title: "", summary: "", assertions: [] },
  past: [],
  suggestions: [],
};

function StatusPill({ status }: { status: StatusEvent }) {
  const live = status.state === "listening";
  const starting = status.state === "starting";
  const error = status.state === "error";
  return (
    <div className="flex items-center gap-2 text-sm">
      <span className={cn("relative flex h-2.5 w-2.5")}>
        {live && (
          <span className="absolute inline-flex h-full w-full animate-ping rounded-full bg-emerald-400/70" />
        )}
        <span
          className={cn(
            "relative inline-flex h-2.5 w-2.5 rounded-full",
            live && "bg-emerald-400",
            starting && "bg-amber-400",
            error && "bg-destructive",
            status.state === "idle" && "bg-muted-foreground/50"
          )}
        />
      </span>
      <span className="text-muted-foreground">
        {STATE_LABEL[status.state] ?? status.state}
      </span>
    </div>
  );
}

function App() {
  const [status, setStatus] = useState<StatusEvent>({
    state: "idle",
    message: "Ready",
    micAvailable: false,
    activeSources: [],
  });
  const [segments, setSegments] = useState<Segment[]>([]);
  const [analysis, setAnalysis] = useState<AnalysisState>(EMPTY_ANALYSIS);
  const [busy, setBusy] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [contextOpen, setContextOpen] = useState(false);
  const [audioOpen, setAudioOpen] = useState(false);
  const bottomRef = useRef<HTMLDivElement | null>(null);

  useEffect(() => {
    MeetingService.IsRunning()
      .then((r) => {
        if (r) setStatus((s) => ({ ...s, state: "listening" }));
      })
      .catch(() => {});

    const offStatus = Events.On("status", (e: { data: StatusEvent }) => {
      setStatus(e.data);
      setBusy(false);
    });
    const offTranscript = Events.On("transcript", (e: { data: Segment }) => {
      setSegments((prev) => [...prev, e.data].sort((a, b) => a.startMs - b.startMs));
    });
    const offAnalysis = Events.On("analysis", (e: { data: AnalysisState }) => {
      setAnalysis(e.data ?? EMPTY_ANALYSIS);
    });
    return () => {
      offStatus();
      offTranscript();
      offAnalysis();
    };
  }, []);

  useEffect(() => {
    bottomRef.current?.scrollIntoView({ behavior: "smooth" });
  }, [segments]);

  const running = status.state === "listening" || status.state === "starting";

  const toggle = async () => {
    setBusy(true);
    try {
      if (running) {
        await MeetingService.Stop();
      } else {
        setSegments([]);
        setAnalysis(EMPTY_ANALYSIS);
        await MeetingService.Start();
      }
    } catch (err) {
      console.error(err);
      setBusy(false);
    }
  };

  return (
    <div className="flex h-screen flex-col">
      <header className="flex items-center gap-4 border-b px-5 py-3">
        <div className="flex items-center gap-2">
          <div className="flex h-8 w-8 items-center justify-center rounded-lg bg-primary/15 text-primary">
            <AudioLines className="h-5 w-5" />
          </div>
          <div className="leading-tight">
            <div className="font-semibold tracking-tight">Parley</div>
            <div className="text-[11px] text-muted-foreground">Meeting assistant</div>
          </div>
        </div>

        <div className="ml-2">
          <StatusPill status={status} />
        </div>

        <div className="ml-auto flex items-center gap-2">
          <Badge variant={status.micAvailable ? "you" : "secondary"}>
            {status.micAvailable ? (
              <Mic className="h-3 w-3" />
            ) : (
              <MicOff className="h-3 w-3" />
            )}
            {status.micAvailable ? "Mic active" : "No mic"}
          </Badge>

          <Button
            variant="ghost"
            size="icon"
            title="Audio sources"
            onClick={() => setAudioOpen(true)}
          >
            <SlidersHorizontal className="h-4 w-4" />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            title="Meeting context"
            onClick={() => setContextOpen(true)}
          >
            <NotebookPen className="h-4 w-4" />
          </Button>
          <Button
            variant="ghost"
            size="icon"
            title="LLM settings"
            onClick={() => setSettingsOpen(true)}
          >
            <SettingsIcon className="h-4 w-4" />
          </Button>

          <Button
            onClick={toggle}
            disabled={busy}
            variant={running ? "destructive" : "default"}
            className="min-w-28"
          >
            {busy ? (
              <Loader2 className="h-4 w-4 animate-spin" />
            ) : running ? (
              <>
                <Square className="h-4 w-4" /> Stop
              </>
            ) : (
              <>
                <span className="inline-flex h-2.5 w-2.5 rounded-full bg-current" />
                Start listening
              </>
            )}
          </Button>
        </div>
      </header>

      <main className="grid min-h-0 flex-1 grid-cols-[minmax(360px,420px)_1fr] gap-4 p-4">
        <Card className="min-h-0">
          <CardHeader>
            <CardTitle>
              <MessageSquareText className="h-4 w-4 text-primary" />
              Live transcript
            </CardTitle>
          </CardHeader>
          <CardContent className="flex-1 min-h-0 p-0">
            <ScrollArea className="h-full px-4">
              {segments.length === 0 ? (
                <div className="flex h-full items-center justify-center py-16 text-center text-sm text-muted-foreground/60">
                  {running
                    ? "Listening… spoken audio will appear here."
                    : "Press Start listening to begin."}
                </div>
              ) : (
                <div className="flex flex-col gap-3 py-2">
                  {segments.map((seg, i) => (
                    <div key={i} className="flex flex-col gap-1">
                      <div className="flex items-center gap-2">
                        <Badge variant={speakerVariant(seg.source)}>
                          {seg.source}
                        </Badge>
                        <span className="text-[10px] tabular-nums text-muted-foreground/50">
                          {fmtTime(seg.startMs)}
                        </span>
                      </div>
                      <p className="text-sm leading-relaxed">{seg.text}</p>
                    </div>
                  ))}
                  <div ref={bottomRef} />
                </div>
              )}
            </ScrollArea>
          </CardContent>
        </Card>

        <AnalysisPanels state={analysis} active={running} />
      </main>

      <AudioDialog open={audioOpen} onOpenChange={setAudioOpen} />
      <SettingsDialog open={settingsOpen} onOpenChange={setSettingsOpen} />
      <ContextDialog open={contextOpen} onOpenChange={setContextOpen} />
    </div>
  );
}

export default App;
