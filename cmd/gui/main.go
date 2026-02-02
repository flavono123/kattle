package main

import (
	"embed"
	"fmt"
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"path/filepath"
	"runtime"
	"runtime/pprof"
	"time"

	"github.com/wailsapp/wails/v2"
	"github.com/wailsapp/wails/v2/pkg/options"
	"github.com/wailsapp/wails/v2/pkg/options/assetserver"
)

//go:embed all:frontend/dist
var assets embed.FS

var dumpDir string
var dumpSeq int

func initMemoryDump() {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		log.Printf("Failed to get home directory: %v", err)
		return
	}
	dumpDir = filepath.Join(homeDir, "kattle-dumps")
	if err := os.MkdirAll(dumpDir, 0755); err != nil {
		log.Printf("Failed to create dump directory: %v", err)
		return
	}
	log.Printf("Memory dump enabled: dir=%s", dumpDir)
}

// DumpMemory saves a heap profile with the given label
func DumpMemory(label string) string {
	if dumpDir == "" {
		return ""
	}

	dumpSeq++
	timestamp := time.Now().Format("20060102-150405")
	filename := filepath.Join(dumpDir, fmt.Sprintf("heap_%s_%03d_%s.pb.gz", timestamp, dumpSeq, label))

	f, err := os.Create(filename)
	if err != nil {
		log.Printf("Failed to create heap dump file: %v", err)
		return ""
	}
	defer f.Close()

	runtime.GC()
	if err := pprof.WriteHeapProfile(f); err != nil {
		log.Printf("Failed to write heap profile: %v", err)
		return ""
	}

	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	log.Printf("[MEMDUMP] label=%s file=%s HeapAlloc=%dMB HeapInuse=%dMB Goroutines=%d",
		label, filepath.Base(filename), m.HeapAlloc/1024/1024, m.HeapInuse/1024/1024, runtime.NumGoroutine())

	return filename
}

func main() {
	// pprof 디버그 서버 및 메모리 덤프 (KATTLE_DEBUG=1 일 때만)
	if os.Getenv("KATTLE_DEBUG") == "1" {
		go func() {
			log.Println("pprof server started at http://localhost:6060/debug/pprof/")
			if err := http.ListenAndServe("localhost:6060", nil); err != nil {
				log.Printf("pprof server error: %v", err)
			}
		}()

		initMemoryDump()
		log.Println("Memory dump enabled. Use curl http://localhost:6060/debug/pprof/heap > dump.pb.gz")
	}

	// Create an instance of the app structure
	app := NewApp()

	// Create application with options
	err := wails.Run(&options.App{
		Title:  "gui",
		Width:  1024,
		Height: 768,
		AssetServer: &assetserver.Options{
			Assets: assets,
		},
		BackgroundColour: &options.RGBA{R: 27, G: 38, B: 54, A: 1},
		OnStartup:        app.startup,
		OnShutdown:       app.shutdown,
		Bind: []interface{}{
			app,
		},
	})

	if err != nil {
		println("Error:", err.Error())
	}
}
