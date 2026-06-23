package main

import (
	"embed"
	"log"

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
