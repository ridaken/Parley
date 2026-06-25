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
import type { State as AnalysisState, Suggestion } from "../bindings/github.com/tomvokac/parley/internal/analysis/models";
import type { LiveNote, LLMConnection } from "../bindings/github.com/tomvokac/parley/internal/store/models";
import type { LoadedSession } from "../bindings/github.com/tomvokac/parley/models";

import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
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
import { normKey } from "@/lib/normalize";

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
  actionItems: [],
};

// How many suggestions / assertions to keep on screen before the oldest drop off
// the bottom (pinned items lead the list, so they're never trimmed in practice).
const SUGGESTION_CAP = 50;
const ASSERTION_CAP = 50;

type TextItem = { text: string };

// accumulateByText folds a fresh analysis pass into the running list instead of
// replacing it, so an item the model stops repeating doesn't disappear. Order:
// pinned first (priority), then items that are NEW this pass (newest on top), then
// the previously shown items in their existing order — all de-duplicated by
// normalized text and minus dismissed. Items the model merely repeats keep their
// place (only genuinely new ones jump to the top), which avoids reorder jitter.
function accumulateByText<T extends TextItem>(
  incoming: T[],
  previous: T[],
  opts: { pinned?: Map<string, T>; dismissed?: Set<string>; cap?: number } = {}
): T[] {
  const { pinned, dismissed, cap } = opts;
  const seen = new Set<string>();
  const out: T[] = [];
  const push = (s: T) => {
    const key = normKey(s.text);
    if (seen.has(key) || dismissed?.has(key)) return;
    seen.add(key);
    out.push(s);
  };
  if (pinned) for (const [, s] of pinned) push(s);
  const prevKeys = new Set(previous.map((s) => normKey(s.text)));
  for (const s of incoming) if (!prevKeys.has(normKey(s.text))) push(s);
  for (const s of previous) push(s);
  return cap ? out.slice(0, cap) : out;
}

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
  const [conns, setConns] = useState<LLMConnection[]>([]);
  const [activeConnId, setActiveConnId] = useState<number>(0);
  const [activeContext, setActiveContext] = useState<string | null>(null);
  // Session-scoped suggestion pins/dismissals, keyed by normalized text. The
  // `analysis` listener is registered once, so it reads these via refs to avoid a
  // stale closure.
  const [pinned, setPinned] = useState<Map<string, Suggestion>>(new Map());
  const [dismissed, setDismissed] = useState<Set<string>>(new Set());
  const pinnedRef = useRef(pinned);
  const dismissedRef = useRef(dismissed);
  pinnedRef.current = pinned;
  dismissedRef.current = dismissed;
  const bottomRef = useRef<HTMLDivElement | null>(null);

  // Forget pins/dismissals at meeting boundaries (new / cleared / loaded session).
  const resetSuggestionState = () => {
    setPinned(new Map());
    setDismissed(new Set());
  };

  // Name of the context profile that will ground the next meeting (null = none
  // selected), so the idle setup strip can show whether the LLM has background.
  const refreshContext = () => {
    Promise.all([LibraryService.ListProfiles(), LibraryService.GetSettings()])
      .then(([list, s]) => {
        const id = s?.activeProfileID ?? 0;
        const p = id ? (list ?? []).find((x) => x.id === id) : undefined;
        setActiveContext(p?.name ?? null);
      })
      .catch(() => {});
  };

  // Refresh on mount and whenever the context dialog closes (the active profile
  // or its name may have changed).
  useEffect(() => {
    if (!contextOpen) refreshContext();
  }, [contextOpen]);

  // Saved LLM connections + the active one, so the header switcher reflects the
  // current provider and changes apply to the next meeting started.
  const refreshConnections = () => {
    Promise.all([LibraryService.ListLLMConnections(), LibraryService.GetSettings()])
      .then(([c, s]) => {
        setConns(c ?? []);
        setActiveConnId(s?.activeLLMConnectionID ?? 0);
      })
      .catch(() => {});
  };

  // Refresh on mount and whenever Settings closes (connections may have changed).
  useEffect(() => {
    if (!settingsOpen) refreshConnections();
  }, [settingsOpen]);

  const pickConnection = (id: number) => {
    setActiveConnId(id);
    LibraryService.SetActiveLLMConnection(id).catch(() => {});
  };

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
      const raw = e.data ?? EMPTY_ANALYSIS;
      const title = raw.current?.title ?? "";
      // Accumulate against the previously displayed lists so suggestions and
      // assertions persist (newest on top) instead of being replaced each pass.
      // Assertions belong to the current topic, so they reset on a topic change;
      // suggestions are meeting-wide and honor pins/dismissals.
      setAnalysis((prev) => {
        const topicChanged = title !== (prev.current?.title ?? "");
        return {
          ...raw,
          current: {
            ...raw.current,
            assertions: accumulateByText(
              raw.current?.assertions ?? [],
              topicChanged ? [] : prev.current?.assertions ?? [],
              { cap: ASSERTION_CAP }
            ),
          },
          suggestions: accumulateByText(
            raw.suggestions ?? [],
            prev.suggestions ?? [],
            { pinned: pinnedRef.current, dismissed: dismissedRef.current, cap: SUGGESTION_CAP }
          ),
          actionItems: raw.actionItems ?? [],
        };
      });
      // Mirror the backend: topic-scoped notes expire when the topic changes.
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
        resetSuggestionState();
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

  // Toggle a suggestion's pin and re-order the visible list now (don't wait for the
  // next analysis pass) using the just-computed pin map.
  const pinSuggestion = (s: Suggestion) => {
    const key = normKey(s.text);
    const nextPinned = new Map(pinned);
    if (nextPinned.has(key)) nextPinned.delete(key);
    else nextPinned.set(key, s);
    setPinned(nextPinned);
    setAnalysis((a) => ({
      ...a,
      suggestions: accumulateByText([], a.suggestions ?? [], {
        pinned: nextPinned,
        dismissed: dismissedRef.current,
        cap: SUGGESTION_CAP,
      }),
    }));
  };

  // Dismiss a suggestion for the rest of the session (dismiss also clears any pin).
  const dismissSuggestion = (s: Suggestion) => {
    const key = normKey(s.text);
    setDismissed((d) => new Set(d).add(key));
    if (pinned.has(key)) {
      const nextPinned = new Map(pinned);
      nextPinned.delete(key);
      setPinned(nextPinned);
    }
    setAnalysis((a) => ({
      ...a,
      suggestions: (a.suggestions ?? []).filter((x) => normKey(x.text) !== key),
    }));
  };

  const viewSession = (s: LoadedSession) => {
    setSegments([...(s.segments ?? [])].sort((a, b) => a.startMs - b.startMs));
    setAnalysis(s.analysis ?? EMPTY_ANALYSIS);
    setNotes(s.liveNotes ?? []);
    resetSuggestionState();
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
    resetSuggestionState();
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
          {conns.length > 0 && (
            <Select
              value={activeConnId ? String(activeConnId) : undefined}
              onValueChange={(v) => pickConnection(Number(v))}
              disabled={running}
            >
              <SelectTrigger
                className="h-8 w-[150px] text-xs"
                title={
                  running
                    ? "Stop the meeting to switch LLM connection"
                    : "LLM connection used for analysis"
                }
              >
                <SelectValue placeholder="LLM connection" />
              </SelectTrigger>
              <SelectContent>
                {conns.map((c) => (
                  <SelectItem key={c.id} value={String(c.id)}>
                    {c.name}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          )}

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

      {!running && !loaded && (
        <div className="flex items-center gap-2 border-b border-amber-500/30 bg-amber-500/10 px-5 py-2 text-sm">
          <NotebookPen className="h-4 w-4 shrink-0 text-amber-600 dark:text-amber-400" />
          {activeContext ? (
            <span className="min-w-0 truncate text-muted-foreground">
              Meeting context:{" "}
              <span className="font-medium text-foreground">{activeContext}</span>{" "}
              — the assistant will use this as background.
            </span>
          ) : (
            <span className="min-w-0 truncate text-muted-foreground">
              No meeting context set. Give the assistant background — attendees,
              agenda, docs — before you start.
            </span>
          )}
          <Button
            size="sm"
            variant="outline"
            className="ml-auto border-amber-500/40 hover:bg-amber-500/15"
            onClick={() => setContextOpen(true)}
          >
            {activeContext ? "Change context" : "Set up context"}
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

        <AnalysisPanels
          state={analysis}
          active={running}
          highlight={running && !loaded}
          pinnedKeys={new Set(pinned.keys())}
          onPin={pinSuggestion}
          onDismiss={dismissSuggestion}
        />
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
