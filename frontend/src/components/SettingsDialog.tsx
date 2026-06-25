import { useEffect, useState } from "react";
import {
  CheckCircle2,
  Loader2,
  Pencil,
  Plus,
  Star,
  Trash2,
  XCircle,
} from "lucide-react";

import { LibraryService } from "../../bindings/github.com/tomvokac/parley";
import type {
  Settings,
  LLMConnection,
} from "../../bindings/github.com/tomvokac/parley/internal/store/models";

import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { cn } from "@/lib/utils";

const DEFAULTS: Settings = {
  llmBaseURL: "http://127.0.0.1:8080/v1",
  llmModel: "local-model",
  analysisIntervalSec: 15,
  analysisTimeoutSec: 30,
  activeProfileID: 0,
  hasAPIKey: false,
  captureSources: [],
  sttBaseURL: "",
  whisperModel: "ggml-small.en-q5_1.bin",
  activeLLMConnectionID: 0,
};

const blankConn = (): LLMConnection => ({
  id: 0,
  name: "",
  baseURL: "http://127.0.0.1:8080/v1",
  model: "local-model",
  hasAPIKey: false,
  updatedAt: "",
});

// friendlyError turns a raw backend/transport error into something actionable.
function friendlyError(raw: string): string {
  const lower = raw.toLowerCase();
  if (lower.includes("context deadline exceeded") || lower.includes("timeout")) {
    return "The endpoint didn't respond in time. Check the URL and port, make sure the server is running, and (for local servers) that the model has finished loading.";
  }
  if (lower.includes("connection refused") || lower.includes("no such host") || lower.includes("dial")) {
    return "Couldn't reach that address. Double-check the host/port and that the server is started.";
  }
  if (lower.includes("401") || lower.includes("unauthorized") || lower.includes("api key")) {
    return "The server rejected the request — the API key looks missing or wrong.";
  }
  if (lower.includes("404") || lower.includes("not found")) {
    return "The endpoint responded but the path/model wasn't found. Confirm the Base URL ends in /v1 and the model name is correct.";
  }
  return raw;
}

export function SettingsDialog({
  open,
  onOpenChange,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
}) {
  const [settings, setSettings] = useState<Settings>(DEFAULTS);
  const [conns, setConns] = useState<LLMConnection[]>([]);
  // editing === null → list view; otherwise the connection being added/edited.
  const [editing, setEditing] = useState<LLMConnection | null>(null);
  const [apiKey, setApiKey] = useState("");
  const [test, setTest] = useState<"idle" | "running" | "ok" | "fail">("idle");
  const [testMsg, setTestMsg] = useState("");

  const loadConns = () => LibraryService.ListLLMConnections().then((c) => setConns(c ?? []));

  useEffect(() => {
    if (!open) return;
    setEditing(null);
    setApiKey("");
    setTest("idle");
    setTestMsg("");
    LibraryService.GetSettings()
      .then((s) => setSettings(s ?? DEFAULTS))
      .catch(() => setSettings(DEFAULTS));
    loadConns().catch(() => setConns([]));
  }, [open]);

  const saveOther = async () => {
    await LibraryService.SaveSettings(settings);
    onOpenChange(false);
  };

  const setActive = async (id: number) => {
    await LibraryService.SetActiveLLMConnection(id);
    setSettings((s) => ({ ...s, activeLLMConnectionID: id }));
  };

  const remove = async (c: LLMConnection) => {
    await LibraryService.DeleteLLMConnection(c.id);
    const [s] = await Promise.all([LibraryService.GetSettings(), loadConns()]);
    if (s) setSettings(s);
  };

  // persistEditing saves the editor's connection (and key) and returns its id.
  const persistEditing = async (): Promise<number> => {
    const saved = await LibraryService.SaveLLMConnection(editing!);
    if (apiKey !== "") await LibraryService.SetConnectionAPIKey(saved.id, apiKey);
    setEditing((e) => (e ? { ...e, id: saved.id } : e));
    return saved.id;
  };

  const saveConn = async () => {
    await persistEditing();
    const [s] = await Promise.all([LibraryService.GetSettings(), loadConns()]);
    if (s) setSettings(s);
    setEditing(null);
    setApiKey("");
    setTest("idle");
  };

  const testConn = async () => {
    setTest("running");
    setTestMsg("");
    try {
      const id = await persistEditing();
      await loadConns();
      await LibraryService.TestLLMConnection(id);
      setTest("ok");
      setTestMsg("Connected — the model responded.");
    } catch (e: any) {
      setTest("fail");
      setTestMsg(friendlyError(String(e?.message ?? e)));
    }
  };

  const canSaveConn = !!editing?.name.trim() && !!editing?.baseURL.trim();

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>Settings</DialogTitle>
          <DialogDescription>
            Save one connection per provider — a local llama-server / LM Studio /
            Ollama, or a cloud URL — and switch between them per meeting. Local
            stays private.
          </DialogDescription>
        </DialogHeader>

        <div className="flex flex-col gap-3">
          {/* ---- LLM connections ------------------------------------------ */}
          <div className="flex items-center justify-between">
            <div className="text-sm font-medium">LLM connections</div>
            {!editing && (
              <Button size="sm" variant="outline" onClick={() => { setEditing(blankConn()); setApiKey(""); setTest("idle"); }}>
                <Plus className="h-4 w-4" /> Add
              </Button>
            )}
          </div>

          {!editing && (
            <div className="flex flex-col gap-1.5">
              {conns.length === 0 && (
                <p className="rounded-md border border-dashed p-3 text-xs text-muted-foreground">
                  No connections yet. Add one to enable live analysis.
                </p>
              )}
              {conns.map((c) => {
                const active = c.id === settings.activeLLMConnectionID;
                return (
                  <div
                    key={c.id}
                    className={cn(
                      "flex items-center gap-2 rounded-md border p-2",
                      active ? "border-primary/50 bg-primary/5" : "border-border"
                    )}
                  >
                    <button
                      type="button"
                      onClick={() => setActive(c.id)}
                      title={active ? "Active connection" : "Use this connection"}
                      className={cn(
                        "shrink-0 rounded p-1 transition-colors",
                        active ? "text-primary" : "text-muted-foreground hover:text-foreground"
                      )}
                    >
                      <Star className={cn("h-4 w-4", active && "fill-current")} />
                    </button>
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2 text-sm font-medium">
                        <span className="truncate">{c.name}</span>
                        {active && (
                          <span className="rounded bg-primary/15 px-1.5 py-0.5 text-[10px] font-medium text-primary">
                            Active
                          </span>
                        )}
                      </div>
                      <div className="truncate text-[11px] text-muted-foreground">
                        {c.model} · {c.baseURL}
                        {c.hasAPIKey && " · 🔑"}
                      </div>
                    </div>
                    <Button
                      size="icon"
                      variant="ghost"
                      className="h-7 w-7"
                      title="Edit"
                      onClick={() => { setEditing({ ...c }); setApiKey(""); setTest("idle"); }}
                    >
                      <Pencil className="h-3.5 w-3.5" />
                    </Button>
                    <Button
                      size="icon"
                      variant="ghost"
                      className="h-7 w-7 text-destructive hover:text-destructive"
                      title="Delete"
                      onClick={() => remove(c)}
                    >
                      <Trash2 className="h-3.5 w-3.5" />
                    </Button>
                  </div>
                );
              })}
            </div>
          )}

          {editing && (
            <div className="flex flex-col gap-3 rounded-md border p-3">
              <div className="text-xs font-medium text-muted-foreground">
                {editing.id ? "Edit connection" : "New connection"}
              </div>
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="conn-name">Name</Label>
                <Input
                  id="conn-name"
                  value={editing.name}
                  placeholder="e.g. Local llama-server, OpenAI"
                  onChange={(e) => setEditing({ ...editing, name: e.target.value })}
                />
              </div>
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="conn-url">Base URL</Label>
                <Input
                  id="conn-url"
                  value={editing.baseURL}
                  placeholder="http://127.0.0.1:8080/v1"
                  onChange={(e) => setEditing({ ...editing, baseURL: e.target.value })}
                />
              </div>
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="conn-model">Model</Label>
                <Input
                  id="conn-model"
                  value={editing.model}
                  placeholder="local-model"
                  onChange={(e) => setEditing({ ...editing, model: e.target.value })}
                />
              </div>
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="conn-key">
                  API key {editing.hasAPIKey && "(saved — leave blank to keep)"}
                </Label>
                <Input
                  id="conn-key"
                  type="password"
                  value={apiKey}
                  placeholder={editing.hasAPIKey ? "••••••••" : "optional for local"}
                  onChange={(e) => setApiKey(e.target.value)}
                />
              </div>

              {test !== "idle" && (
                <div
                  className={
                    "flex items-start gap-2 rounded-md border p-2 text-xs " +
                    (test === "ok"
                      ? "border-emerald-500/30 text-emerald-400"
                      : test === "fail"
                      ? "border-destructive/30 text-destructive"
                      : "border-border text-muted-foreground")
                  }
                >
                  {test === "running" && <Loader2 className="mt-0.5 h-3.5 w-3.5 shrink-0 animate-spin" />}
                  {test === "ok" && <CheckCircle2 className="mt-0.5 h-3.5 w-3.5 shrink-0" />}
                  {test === "fail" && <XCircle className="mt-0.5 h-3.5 w-3.5 shrink-0" />}
                  <span className="min-w-0 break-words">{test === "running" ? "Testing…" : testMsg}</span>
                </div>
              )}

              <div className="flex items-center justify-between gap-2">
                <Button variant="ghost" onClick={() => { setEditing(null); setApiKey(""); }}>
                  Cancel
                </Button>
                <div className="flex gap-2">
                  <Button variant="outline" disabled={!canSaveConn || test === "running"} onClick={testConn}>
                    Test
                  </Button>
                  <Button disabled={!canSaveConn} onClick={saveConn}>
                    Save connection
                  </Button>
                </div>
              </div>
            </div>
          )}

          {/* ---- Analysis -------------------------------------------------- */}
          <div className="mt-1 border-t pt-3">
            <div className="mb-2 text-sm font-medium">Analysis</div>
            <div className="flex flex-col gap-1.5">
              <Label htmlFor="interval">Next analysis after response (seconds)</Label>
              <Input
                id="interval"
                type="number"
                min={3}
                value={settings.analysisIntervalSec}
                onChange={(e) =>
                  setSettings({ ...settings, analysisIntervalSec: Number(e.target.value) || 15 })
                }
              />
            </div>
            <div className="mt-3 flex flex-col gap-1.5">
              <Label htmlFor="analysis-timeout">Analysis request timeout (seconds)</Label>
              <Input
                id="analysis-timeout"
                type="number"
                min={5}
                value={settings.analysisTimeoutSec}
                onChange={(e) =>
                  setSettings({ ...settings, analysisTimeoutSec: Number(e.target.value) || 30 })
                }
              />
            </div>
          </div>

          {/* ---- Transcription -------------------------------------------- */}
          <div className="mt-1 border-t pt-3">
            <div className="mb-2 text-sm font-medium">Transcription</div>
            <div className="flex flex-col gap-3">
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="stturl">Remote transcription URL (optional)</Label>
                <Input
                  id="stturl"
                  value={settings.sttBaseURL}
                  placeholder="leave blank to use the bundled engine"
                  onChange={(e) => setSettings({ ...settings, sttBaseURL: e.target.value })}
                />
                <p className="text-[11px] leading-snug text-muted-foreground">
                  Blank = transcribe locally with the bundled Whisper model (private,
                  no setup). Set this to a whisper.cpp server URL (e.g.
                  http://192.168.1.10:8765) to offload transcription to another machine.
                </p>
              </div>
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="whispermodel">Bundled model file</Label>
                <Input
                  id="whispermodel"
                  value={settings.whisperModel}
                  placeholder="ggml-small.en-q5_1.bin"
                  disabled={!!settings.sttBaseURL.trim()}
                  onChange={(e) => setSettings({ ...settings, whisperModel: e.target.value })}
                />
                <p className="text-[11px] leading-snug text-muted-foreground">
                  Filename under resources/whisper/models. Defaults to
                  ggml-small.en-q5_1.bin — quantized, accurate on names/jargon, and
                  light enough to leave your laptop free for other work. Drop in
                  ggml-base.en.bin for a lighter load, or ggml-large-v3-turbo-q5_0.bin
                  for top accuracy if you have CPU headroom.
                </p>
              </div>
            </div>
          </div>
        </div>

        <DialogFooter>
          <Button onClick={saveOther}>Save</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
