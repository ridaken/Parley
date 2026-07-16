import { useEffect, useState } from "react";
import {
  CheckCircle2,
  Loader2,
  Pencil,
  Play,
  Plus,
  RefreshCw,
  Square,
  Star,
  Trash2,
  XCircle,
} from "lucide-react";

import { LibraryService, MeetingService } from "../../bindings/github.com/tomvokac/parley";
import type {
  RuntimeInfo,
  TranscriptionModelOption,
} from "../../bindings/github.com/tomvokac/parley";
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
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { cn } from "@/lib/utils";

const DEFAULTS: Settings = {
  llmBaseURL: "http://127.0.0.1:8080/v1",
  llmModel: "local-model",
  analysisIntervalSec: 15,
  analysisTimeoutSec: 60,
  loggingLevel: "trace",
  activeProfileID: 0,
  hasAPIKey: false,
  captureSources: [],
  sttEngine: "auto",
  sttBaseURL: "",
  whisperModel: "ggml-small.en-q5_1.bin",
  activeLLMConnectionID: 0,
};

function transcriptionModelID(settings: Settings): string {
  if (settings.sttEngine === "nemotron" || settings.sttEngine === "external") {
    return settings.sttEngine;
  }
  if (settings.sttEngine === "whisper") {
    return `whisper:${settings.whisperModel}`;
  }
  return "auto";
}

function selectTranscriptionModel(settings: Settings, modelID: string): Settings {
  if (modelID === "auto" || modelID === "nemotron" || modelID === "external") {
    return { ...settings, sttEngine: modelID };
  }
  if (modelID.startsWith("whisper:")) {
    return {
      ...settings,
      sttEngine: "whisper",
      whisperModel: modelID.slice("whisper:".length),
    };
  }
  return settings;
}

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
  meetingActive,
  runtimeInfo,
}: {
  open: boolean;
  onOpenChange: (v: boolean) => void;
  meetingActive: boolean;
  runtimeInfo: RuntimeInfo;
}) {
  const [settings, setSettings] = useState<Settings>(DEFAULTS);
  const [conns, setConns] = useState<LLMConnection[]>([]);
  // editing === null → list view; otherwise the connection being added/edited.
  const [editing, setEditing] = useState<LLMConnection | null>(null);
  const [apiKey, setApiKey] = useState("");
  const [test, setTest] = useState<"idle" | "running" | "ok" | "fail">("idle");
  const [testMsg, setTestMsg] = useState("");
  const [transcriptionModels, setTranscriptionModels] = useState<TranscriptionModelOption[]>([]);
  const [savedTranscription, setSavedTranscription] = useState({ modelID: "auto", externalURL: "" });
  const [modelAction, setModelAction] = useState<"" | "saving" | "starting" | "stopping" | "restarting" | "testing">("");
  const [modelMessage, setModelMessage] = useState("");
  const [modelTestOK, setModelTestOK] = useState(false);

  const loadConns = () => LibraryService.ListLLMConnections().then((c) => setConns(c ?? []));

  useEffect(() => {
    if (!open) return;
    setEditing(null);
    setApiKey("");
    setTest("idle");
    setTestMsg("");
    setModelAction("");
    setModelMessage("");
    setModelTestOK(false);
    setTranscriptionModels([]);
    LibraryService.GetSettings()
      .then((s) => {
        const loaded = s ?? DEFAULTS;
        setSettings(loaded);
        setSavedTranscription({
          modelID: transcriptionModelID(loaded),
          externalURL: loaded.sttBaseURL.trim().replace(/\/+$/, ""),
        });
      })
      .catch(() => {
        setSettings(DEFAULTS);
        setSavedTranscription({ modelID: "auto", externalURL: "" });
      });

    MeetingService.ListTranscriptionModels()
      .then((models) => setTranscriptionModels(models ?? []))
      .catch(() => setTranscriptionModels([]));
    loadConns().catch(() => setConns([]));
  }, [open]);

  const saveOther = async () => {
    setModelAction("saving");
    setModelMessage("");
    try {
      const modelID = transcriptionModelID(settings);
      const externalURL = settings.sttBaseURL.trim().replace(/\/+$/, "");
      await MeetingService.ConfigureTranscription({ modelID, externalURL });
      await LibraryService.SaveSettings({ ...settings, sttBaseURL: externalURL });
      onOpenChange(false);
    } catch (e: any) {
      setModelMessage(friendlyError(String(e?.message ?? e)));
    } finally {
      setModelAction("");
    }
  };

  const runModelAction = async (action: "starting" | "stopping" | "restarting") => {
    setModelAction(action);
    setModelMessage("");
    setModelTestOK(false);
    try {
      if (action === "starting") await MeetingService.StartTranscriptionModel();
      if (action === "stopping") await MeetingService.StopTranscriptionModel();
      if (action === "restarting") await MeetingService.RestartTranscriptionModel();
    } catch (e: any) {
      setModelMessage(friendlyError(String(e?.message ?? e)));
    } finally {
      setModelAction("");
    }
  };

  const testExternal = async () => {
    setModelAction("testing");
    setModelMessage("");
    setModelTestOK(false);
    try {
      await MeetingService.TestExternalTranscription(settings.sttBaseURL);
      setModelTestOK(true);
      setModelMessage("Connected — the external transcription server responded.");
    } catch (e: any) {
      setModelMessage(friendlyError(String(e?.message ?? e)));
    } finally {
      setModelAction("");
    }
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
  const selectedModelID = transcriptionModelID(settings);
  const selectedModel = transcriptionModels.find((model) => model.id === selectedModelID);
  const normalizedExternalURL = settings.sttBaseURL.trim().replace(/\/+$/, "");
  const modelDirty =
    selectedModelID !== savedTranscription.modelID ||
    (selectedModelID === "external" && normalizedExternalURL !== savedTranscription.externalURL);
  const lifecycleDisabled = meetingActive || modelDirty || modelAction !== "";
  const isLocalRuntime = runtimeInfo.transcriptionKind !== "external";

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
                  setSettings({ ...settings, analysisTimeoutSec: Number(e.target.value) || 60 })
                }
              />
            </div>
            <div className="mt-3 flex flex-col gap-1.5">
              <Label htmlFor="logging-level">Diagnostic logging</Label>
              <Select
                value={settings.loggingLevel || "trace"}
                onValueChange={(v) => setSettings({ ...settings, loggingLevel: v })}
              >
                <SelectTrigger id="logging-level">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="trace">Trace</SelectItem>
                  <SelectItem value="error">Error</SelectItem>
                  <SelectItem value="none">None</SelectItem>
                </SelectContent>
              </Select>
            </div>
          </div>

          {/* ---- Transcription -------------------------------------------- */}
          <div className="mt-1 border-t pt-3">
            <div className="mb-2 text-sm font-medium">Transcription</div>
            <div className="flex flex-col gap-3">
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="transcription-model">Model</Label>
                <Select
                  value={selectedModelID}
                  disabled={meetingActive || modelAction !== ""}
                  onValueChange={(modelID) => {
                    setSettings((current) => selectTranscriptionModel(current, modelID));
                    setModelMessage("");
                    setModelTestOK(false);
                  }}
                >
                  <SelectTrigger id="transcription-model">
                    <SelectValue placeholder="Choose a transcription model" />
                  </SelectTrigger>
                  <SelectContent>
                    {transcriptionModels.map((model) => (
                      <SelectItem key={model.id} value={model.id} disabled={!model.available}>
                        {model.label}{!model.available ? " — unavailable" : ""}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                <p className="text-[11px] leading-snug text-muted-foreground">
                  {selectedModel?.unavailableReason || selectedModel?.detail ||
                    "Installed local models and compatible external servers appear here."}
                </p>
              </div>

              {selectedModelID === "external" && (
                <div className="flex flex-col gap-1.5">
                  <Label htmlFor="stturl">External server URL</Label>
                  <div className="flex gap-2">
                    <Input
                      id="stturl"
                      value={settings.sttBaseURL}
                      placeholder="http://192.168.1.10:8765"
                      disabled={meetingActive || modelAction === "saving"}
                      onChange={(e) => {
                        setSettings({ ...settings, sttBaseURL: e.target.value });
                        setModelMessage("");
                        setModelTestOK(false);
                      }}
                    />
                    <Button
                      type="button"
                      variant="outline"
                      disabled={!settings.sttBaseURL.trim() || modelAction !== ""}
                      onClick={testExternal}
                    >
                      {modelAction === "testing" ? <Loader2 className="animate-spin" /> : null}
                      Test
                    </Button>
                  </div>
                  <p className="text-[11px] leading-snug text-muted-foreground">
                    Parley sends audio to the server's /inference endpoint but does not
                    start, stop, or restart the remote process.
                  </p>
                </div>
              )}

              <div className="rounded-md border bg-muted/20 p-3">
                <div className="flex items-start gap-2">
                  {runtimeInfo.transcriptionStatus === "loading" ? (
                    <Loader2 className="mt-0.5 h-4 w-4 shrink-0 animate-spin text-primary" />
                  ) : runtimeInfo.transcriptionStatus === "ready" || runtimeInfo.transcriptionStatus === "configured" ? (
                    <CheckCircle2 className="mt-0.5 h-4 w-4 shrink-0 text-emerald-400" />
                  ) : runtimeInfo.transcriptionStatus === "error" ? (
                    <XCircle className="mt-0.5 h-4 w-4 shrink-0 text-destructive" />
                  ) : (
                    <Square className="mt-0.5 h-4 w-4 shrink-0 text-muted-foreground" />
                  )}
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-xs font-medium">
                      {runtimeInfo.transcriptionModel || "Transcription model"}
                    </div>
                    <div className="mt-0.5 text-[11px] leading-snug text-muted-foreground">
                      {modelDirty
                        ? "Save these settings to apply the selected model."
                        : runtimeInfo.transcriptionMessage || "Model status unavailable."}
                    </div>
                  </div>
                </div>

                {isLocalRuntime && (
                  <div className="mt-3 flex flex-wrap gap-2">
                    <Button
                      type="button"
                      size="sm"
                      variant="outline"
                      disabled={lifecycleDisabled || !["stopped", "error"].includes(runtimeInfo.transcriptionStatus)}
                      onClick={() => runModelAction("starting")}
                    >
                      {modelAction === "starting" ? <Loader2 className="animate-spin" /> : <Play />}
                      Start
                    </Button>
                    <Button
                      type="button"
                      size="sm"
                      variant="outline"
                      disabled={lifecycleDisabled || !["loading", "ready"].includes(runtimeInfo.transcriptionStatus)}
                      onClick={() => runModelAction("stopping")}
                    >
                      {modelAction === "stopping" ? <Loader2 className="animate-spin" /> : <Square />}
                      Stop
                    </Button>
                    <Button
                      type="button"
                      size="sm"
                      variant="outline"
                      disabled={lifecycleDisabled || !["loading", "ready", "error"].includes(runtimeInfo.transcriptionStatus)}
                      onClick={() => runModelAction("restarting")}
                    >
                      {modelAction === "restarting" ? <Loader2 className="animate-spin" /> : <RefreshCw />}
                      Restart
                    </Button>
                  </div>
                )}

                {meetingActive && (
                  <p className="mt-2 text-[11px] text-amber-400">
                    Stop the active meeting before changing or restarting transcription.
                  </p>
                )}
              </div>

              {modelMessage && (
                <div
                  className={cn(
                    "flex items-start gap-2 rounded-md border p-2 text-xs",
                    modelTestOK
                      ? "border-emerald-500/30 text-emerald-400"
                      : "border-destructive/30 text-destructive"
                  )}
                >
                  {modelTestOK ? (
                    <CheckCircle2 className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                  ) : (
                    <XCircle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                  )}
                  <span className="min-w-0 break-words">{modelMessage}</span>
                </div>
              )}
            </div>
          </div>
        </div>

        <DialogFooter>
          <Button disabled={modelAction !== ""} onClick={saveOther}>
            {modelAction === "saving" && <Loader2 className="animate-spin" />}
            Save
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
