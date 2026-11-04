// Command nvr-gui is the MyGo desktop entry point.
package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime/debug"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"nvrstatus/internal/config"
	"nvrstatus/internal/nvgui"
)

func main() {
	defer writeCrash()
	app := nvgui.New()
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { app.SaveSettings() })
	mygo.App.WhenReady(func() {
		win := mygo.NewWindow(mygo.WindowOptions{
			Title:                "NVR Status",
			Width:                1280,
			Height:               820,
			MinWidth:             1000,
			MinHeight:            700,
			StateKey:             "main",
			TitleBarStyle:        mygo.TitleBarHidden,
			TrafficLightPosition: &mygo.Point{X: 16, Y: 20},
			Content:              ui.View(app.View),
		})
		app.SetWindow(win)
	})
	if err := mygo.App.Run(); err != nil {
		log.Fatal(err)
	}
}

// writeCrash keeps the Python app's crash-log behaviour: a panic leaves a
// readable file under <app data>/logs/.
func writeCrash() {
	r := recover()
	if r == nil {
		return
	}
	dir := filepath.Join(config.AppDataDir(), "logs")
	_ = os.MkdirAll(dir, 0o755)
	name := filepath.Join(dir, "crash_"+time.Now().Format("20060102_150405")+".log")
	_ = os.WriteFile(name, []byte(fmt.Sprintf("panic: %v\n\n%s", r, debug.Stack())), 0o600)
	panic(r)
}
