package main

import (
	"embed"
	"io"
	"log"
	"os"
	"runtime/debug"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/tomvokac/parley/internal/analysis"
	"github.com/tomvokac/parley/internal/store"
	"github.com/tomvokac/parley/internal/stt"
)

//go:embed all:frontend/dist
var assets embed.FS

func init() {
	// Register events so the binding generator produces typed JS/TS APIs.
	application.RegisterEvent[StatusEvent]("status")
	application.RegisterEvent[stt.Segment]("transcript")
	application.RegisterEvent[analysis.State]("analysis")
}

func main() {
	// Immediately drop any console window Windows allocated for us (e.g. a
	// console-subsystem build or a double-click launch) before anything renders,
	// so a stray terminal never lingers at launch. No-op for a GUI-subsystem
	// build or when launched from the user's own shell.
	hideOwnedConsole()

	logFile := setupLogging()
	if logFile != nil {
		defer logFile.Close()
	}
	// Any panic that unwinds to the main goroutine (e.g. inside the Wails event
	// loop) is written to the log with a full stack before the process dies, so a
	// reproducible crash — like the multi-monitor drag — leaves a diagnosable
	// trail instead of vanishing silently in a windowless GUI build.
	defer logPanic()

	db, err := store.Open(dbPath())
	if err != nil {
		log.Fatalf("open database: %v", err)
	}
	defer db.Close()

	app := application.New(application.Options{
		Name:        "Parley",
		Description: "Local-first meeting assistant",
		Services: []application.Service{
			application.NewService(NewMeetingService(db)),
			application.NewService(NewLibraryService(db)),
		},
		Assets: application.AssetOptions{
			Handler: application.AssetFileServerFS(assets),
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	app.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:            "Parley",
		Width:            1280,
		Height:           820,
		MinWidth:         960,
		MinHeight:        640,
		BackgroundColour: application.NewRGB(20, 18, 28),
		URL:              "/",
		Mac: application.MacWindow{
			InvisibleTitleBarHeight: 50,
			Backdrop:                application.MacBackdropTranslucent,
			TitleBar:                application.MacTitleBarHiddenInset,
		},
	})

	if err := app.Run(); err != nil {
		log.Fatal(err)
	}
}

// setupLogging tees the standard logger to a file in the app data dir so that a
// packaged (windowless) build still records diagnostics the user can share. It
// returns the open log file (or nil) so the caller can keep it for the process
// lifetime. Native runtime crash output (fd 2) is also routed to the file where
// the OS allows it (see redirectStderr).
func setupLogging() *os.File {
	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("parley: ")
	debug.SetTraceback("all") // include all goroutines in any crash dump
	f, err := os.OpenFile(logPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		log.Printf("could not open log file %s: %v", logPath(), err)
		return nil
	}
	log.SetOutput(io.MultiWriter(os.Stderr, f))
	redirectStderr(f)
	log.Printf("---- Parley starting; log at %s ----", logPath())
	return f
}

// logPanic records a panic (with a full stack trace) to the log before letting
// it continue to crash the process, preserving the original failure semantics.
func logPanic() {
	if r := recover(); r != nil {
		log.Printf("FATAL panic: %v\n%s", r, debug.Stack())
		panic(r)
	}
}
