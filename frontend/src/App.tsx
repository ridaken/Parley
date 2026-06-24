import { useEffect, useRef, useState } from "react";
import { Events } from "@wailsio/runtime";
import {
  AlertTriangle,
  AudioLines,
  History,
  Loader2,
  MessageSquareText,
  NotebookPen,
  Settings as SettingsIcon,
  Square,
  X,
} from "lucide-react";

import { MeetingService, LibraryService } from "../bindings/github.com/tomvokac/parley";
import type { StatusEvent } from "../bindings/github.com/tomvokac/parley";
import type { Segment } from "../bindings/github.com/tomvokac/parley/internal/stt/models";
import type { State as AnalysisState } from "../bindings/github.com/tomvokac/parley/internal/analysis/models";
import type { LiveNote } from "../bindings/github.com/tomvokac/parley/internal/store/models";
import type { LoadedSession } from "../bindings/github.com/tomvokac/parley/models";

import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { ScrollArea } from "@/components/ui/scroll-area";
import { cn } from "@/lib/utils";
import { AnalysisPanels } from "@/components/AnalysisPanels";
import { SettingsDialog } from "@/components/SettingsDialog";
import { ContextDialog } from "@/components/ContextDialog";
import { AudioDialog } from "@/components/AudioDialog";
import { AudioSourceButton } from "@/components/AudioSourceButton";
import { SessionsDialog } from "@/components/SessionsDialog";
import { LiveContextBar, type NoteScope } from "@/components/LiveContextBar";
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
  const [notes, setNotes] = useState<LiveNote[]>([]);
  const [busy, setBusy] = useState(false);
  const [settingsOpen, setSettingsOpen] = useState(false);
  const [contextOpen, setContextOpen] = useState(false);
  const [audioOpen, setAudioOpen] = useState(false);
  const [sessionsOpen, setSessionsOpen] = useState(false);
  const [loaded, setLoaded] = useState<{ id: number; title: string } | null>(null);
  const [micConfigured, setMicConfigured] = useState(true);
  const bottomRef = useRef<HTMLDivElement | null>(null);

  // Reflect whether a microphone is configured (or defaulted) so the header is
  // meaningful before a session starts. Empty config = default mic + system audio.
  const refreshMicConfig = () => {
    LibraryService.GetSettings()
      .then((s) => {
        const srcs = s?.captureSources ?? [];
        setMicConfigured(srcs.length === 0 || srcs.some((c) => c.kind === "input"));
      })
      .catch(() => {});
  };

  // Refresh on mount and whenever the audio dialog closes (selection may have changed).
  useEffect(() => {
    if (!audioOpen) refreshMicConfig();
  }, [audioOpen]);

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
      const next = e.data ?? EMPTY_ANALYSIS;
      setAnalysis(next);
      // Mirror the backend: topic-scoped notes expire when the topic changes.
      const title = next.current?.title ?? "";
      setNotes((prev) =>
        prev.filter((n) => n.scope === "meeting" || n.topicTitle === title)
      );
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
      } else if (loaded) {
        // A saved meeting is open — continue it.
        await MeetingService.Resume(loaded.id);
      } else {
        setSegments([]);
        setAnalysis(EMPTY_ANALYSIS);
        setNotes([]);
        await MeetingService.Start();
      }
    } catch (err) {
      console.error(err);
      setBusy(false);
    }
  };

  const sendNote = async (scope: NoteScope, text: string) => {
    try {
      const note = await MeetingService.AddLiveNote(scope, text);
      if (note?.text) setNotes((prev) => [...prev, note]);
    } catch (err) {
      console.error(err);
    }
  };

  const viewSession = (s: LoadedSession) => {
    setSegments([...(s.segments ?? [])].sort((a, b) => a.startMs - b.startMs));
    setAnalysis(s.analysis ?? EMPTY_ANALYSIS);
    setNotes(s.liveNotes ?? []);
    setLoaded({ id: s.session.id, title: s.session.title });
  };

  const resumeSession = async (id: number) => {
    setBusy(true);
    try {
      const s = await MeetingService.LoadSession(id);
      viewSession(s);
      await MeetingService.Resume(id);
    } catch (err) {
      console.error(err);
      setBusy(false);
    }
  };

  const clearLoaded = () => {
    setLoaded(null);
    setSegments([]);
    setAnalysis(EMPTY_ANALYSIS);
    setNotes([]);
  };

  const startLabel = loaded ? "Resume meeting" : "Start listening";

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
          <AudioSourceButton
            status={status}
            micConfigured={micConfigured}
            running={running}
            onOpen={() => setAudioOpen(true)}
          />

          <Button
            variant="ghost"
            size="icon"
            title="Saved meetings"
            onClick={() => setSessionsOpen(true)}
          >
            <History className="h-4 w-4" />
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
                {startLabel}
              </>
            )}
          </Button>
        </div>
      </header>

      {status.state === "error" && status.message && (
        <div className="flex items-start gap-2 border-b border-destructive/30 bg-destructive/10 px-5 py-2 text-sm text-destructive">
          <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" />
          <span className="min-w-0 break-words">
            {status.message}{" "}
            <span className="text-destructive/70">
              Details were written to the log file (parley.log in your app data
              folder).
            </span>
          </span>
        </div>
      )}

      {loaded && !running && (
        <div className="flex items-center gap-2 border-b bg-accent/30 px-5 py-2 text-sm">
          <History className="h-4 w-4 text-muted-foreground" />
          <span className="min-w-0 truncate">
            Viewing saved meeting{" "}
            <span className="font-medium">{loaded.title}</span> — press{" "}
            <span className="font-medium">Resume meeting</span> to continue it.
          </span>
          <Button
            size="sm"
            variant="ghost"
            className="ml-auto"
            onClick={clearLoaded}
          >
            <X className="h-4 w-4" /> New meeting
          </Button>
        </div>
      )}

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

          {notes.length > 0 && (
            <div className="flex flex-wrap gap-1.5 px-4 pb-1 pt-2">
              {notes.map((n, i) => (
                <Badge
                  key={i}
                  variant={n.scope === "meeting" ? "you" : "secondary"}
                  className="max-w-full"
                  title={n.scope === "meeting" ? "Applies all meeting" : "Applies to current topic"}
                >
                  <span className="truncate">{n.text}</span>
                </Badge>
              ))}
            </div>
          )}

          <LiveContextBar disabled={!running} onSend={sendNote} />
        </Card>

        <AnalysisPanels state={analysis} active={running} />
      </main>

      <AudioDialog open={audioOpen} onOpenChange={setAudioOpen} />
      <SettingsDialog open={settingsOpen} onOpenChange={setSettingsOpen} />
      <ContextDialog open={contextOpen} onOpenChange={setContextOpen} />
      <SessionsDialog
        open={sessionsOpen}
        onOpenChange={setSessionsOpen}
        onView={viewSession}
        onResume={resumeSession}
        disabled={running}
      />
    </div>
  );
}

export default App;
