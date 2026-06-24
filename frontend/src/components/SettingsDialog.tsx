import { useEffect, useState } from "react";
import { CheckCircle2, Loader2, XCircle } from "lucide-react";

import { LibraryService } from "../../bindings/github.com/tomvokac/parley";
import type { Settings } from "../../bindings/github.com/tomvokac/parley/internal/store/models";

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

const DEFAULTS: Settings = {
  llmBaseURL: "http://127.0.0.1:8080/v1",
  llmModel: "local-model",
  analysisIntervalSec: 15,
  activeProfileID: 0,
  hasAPIKey: false,
  captureSources: [],
  sttBaseURL: "",
  whisperModel: "ggml-small.en-q5_1.bin",
};

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
  const [apiKey, setApiKey] = useState("");
  const [test, setTest] = useState<"idle" | "running" | "ok" | "fail">("idle");
  const [testMsg, setTestMsg] = useState("");

  useEffect(() => {
    if (!open) return;
    setApiKey("");
    setTest("idle");
    setTestMsg("");
    LibraryService.GetSettings()
      .then((s) => setSettings(s ?? DEFAULTS))
      .catch(() => setSettings(DEFAULTS));
  }, [open]);

  const persist = async () => {
    await LibraryService.SaveSettings(settings);
    if (apiKey !== "") await LibraryService.SetAPIKey(apiKey);
  };

  const save = async () => {
    await persist();
    onOpenChange(false);
  };

  const runTest = async () => {
    setTest("running");
    setTestMsg("");
    try {
      await persist();
      await LibraryService.TestConnection();
      setTest("ok");
      setTestMsg("Connected — the model responded.");
    } catch (e: any) {
      setTest("fail");
      setTestMsg(friendlyError(String(e?.message ?? e)));
    }
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[85vh] overflow-y-auto">
        <DialogHeader>
          <DialogTitle>Settings</DialogTitle>
          <DialogDescription>
            Point Parley at any OpenAI-compatible endpoint — a local llama-server /
            LM Studio / Ollama, or a cloud URL. Local stays private.
          </DialogDescription>
        </DialogHeader>

        <div className="flex flex-col gap-3">
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="baseurl">Base URL</Label>
            <Input
              id="baseurl"
              value={settings.llmBaseURL}
              placeholder="http://127.0.0.1:8080/v1"
              onChange={(e) =>
                setSettings({ ...settings, llmBaseURL: e.target.value })
              }
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="model">Model</Label>
            <Input
              id="model"
              value={settings.llmModel}
              placeholder="local-model"
              onChange={(e) =>
                setSettings({ ...settings, llmModel: e.target.value })
              }
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="apikey">
              API key {settings.hasAPIKey && "(saved — leave blank to keep)"}
            </Label>
            <Input
              id="apikey"
              type="password"
              value={apiKey}
              placeholder={settings.hasAPIKey ? "••••••••" : "optional for local"}
              onChange={(e) => setApiKey(e.target.value)}
            />
          </div>
          <div className="flex flex-col gap-1.5">
            <Label htmlFor="interval">Analysis interval (seconds)</Label>
            <Input
              id="interval"
              type="number"
              min={3}
              value={settings.analysisIntervalSec}
              onChange={(e) =>
                setSettings({
                  ...settings,
                  analysisIntervalSec: Number(e.target.value) || 15,
                })
              }
            />
          </div>

          <div className="mt-1 border-t pt-3">
            <div className="mb-2 text-sm font-medium">Transcription</div>
            <div className="flex flex-col gap-3">
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="stturl">Remote transcription URL (optional)</Label>
                <Input
                  id="stturl"
                  value={settings.sttBaseURL}
                  placeholder="leave blank to use the bundled engine"
                  onChange={(e) =>
                    setSettings({ ...settings, sttBaseURL: e.target.value })
                  }
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
                  onChange={(e) =>
                    setSettings({ ...settings, whisperModel: e.target.value })
                  }
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
              {test === "running" && (
                <Loader2 className="mt-0.5 h-3.5 w-3.5 shrink-0 animate-spin" />
              )}
              {test === "ok" && <CheckCircle2 className="mt-0.5 h-3.5 w-3.5 shrink-0" />}
              {test === "fail" && <XCircle className="mt-0.5 h-3.5 w-3.5 shrink-0" />}
              <span className="min-w-0 break-words">
                {test === "running" ? "Testing…" : testMsg}
              </span>
            </div>
          )}
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={runTest} disabled={test === "running"}>
            Test connection
          </Button>
          <Button onClick={save}>Save</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
