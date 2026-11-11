// Command router-go runs the gateway and dashboard in one process.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"router-go/internal/api"
	"router-go/internal/config"
	"router-go/internal/consolelog"
	"router-go/internal/db"
	"router-go/internal/gateway"
	"router-go/web"
)

func main() {
	version := flag.Bool("version", false, "print version and exit")
	resetPW := flag.Bool("reset-password", false, "remove the stored dashboard password (falls back to INITIAL_PASSWORD) and exit")
	unlock := flag.Bool("remove-localhost-only", false, "turn off Settings → Local only (listen on all interfaces again) and exit; use it when Local only locked you out")
	port := flag.Int("port", 0, "listen port (default 20128, or the port saved in Settings); overrides the PORT env var and the saved setting")
	flag.Parse()
	if *version {
		fmt.Println("router-go", config.Version)
		return
	}

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	store, err := db.Open(cfg.DBPath)
	if err != nil {
		log.Fatalf("database: %v", err)
	}
	defer store.Close()

	if *resetPW {
		if err := store.UpdateSettings(map[string]any{"password": nil}); err != nil {
			log.Fatalf("reset-password: %v", err)
		}
		fmt.Println("dashboard password reset; log in with INITIAL_PASSWORD (default 123456)")
		return
	}

	if *unlock {
		if err := store.UpdateSettings(map[string]any{"localOnly": false}); err != nil {
			log.Fatalf("remove-localhost-only: %v", err)
		}
		fmt.Println("Local only is off; the next start listens on all interfaces (HOSTNAME_BIND / ROUTER_GO_ADDR still win if set)")
		return
	}

	saved := 0
	if s, err := store.Settings(); err == nil {
		if f, ok := s["port"].(float64); ok {
			saved = int(f)
		}
		lo, _ := s["localOnly"].(bool)
		cfg.SetLocalOnly(lo)
	}
	if err := cfg.ResolvePort(*port, saved); err != nil {
		log.Fatalf("config: %v", err)
	}

	if err := consolelog.Default.Persist(filepath.Join(cfg.DataDir, "logs", "console.log"), 5<<20); err != nil {
		log.Printf("console log is not saved to disk: %v", err)
	}
	handler := api.Handler(cfg, store, web.Handler())
	bg, stopBg := context.WithCancel(context.Background())
	defer stopBg()
	go gateway.New(store).RunBackgroundRefresh(bg) // renews OAuth sign-ins before they expire
	var mu sync.Mutex
	var current *http.Server
	var curLn net.Listener
	// start serves handler on ln; the previous server (if any) is drained in the background.
	start := func(ln net.Listener) {
		srv := &http.Server{
			Handler:           handler,
			ReadHeaderTimeout: 10 * time.Second,
			// no WriteTimeout: SSE responses are long-lived
		}
		mu.Lock()
		old := current
		current, curLn = srv, ln
		mu.Unlock()
		go func() {
			if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && !errors.Is(err, net.ErrClosed) {
				log.Fatalf("server: %v", err)
			}
		}()
		if old != nil {
			go func() { // let the response that triggered the move reach the browser, then finish in-flight requests
				time.Sleep(time.Second)
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				_ = old.Shutdown(ctx)
			}()
		}
	}
	// Rebind listens on cfg.BindHost():p, so it also applies a changed "Local only" setting.
	cfg.Rebind = func(p int) error {
		addr := net.JoinHostPort(cfg.BindHost(), strconv.Itoa(p))
		ln, err := net.Listen("tcp", addr)
		mu.Lock()
		prev := curLn
		mu.Unlock()
		if err != nil && prev != nil && prev.Addr().(*net.TCPAddr).Port == p {
			// same port on another interface: the old socket blocks it, so free it first (in-flight requests keep running)
			prevAddr := prev.Addr().String()
			_ = prev.Close()
			if ln, err = net.Listen("tcp", addr); err != nil {
				if back, e := net.Listen("tcp", prevAddr); e == nil {
					start(back)
				}
				return err
			}
		}
		if err != nil {
			return err
		}
		start(ln)
		consolelog.Logf("INFO", "SERVER", "moved to %s", ln.Addr())
		return nil
	}
	ln, err := net.Listen("tcp", cfg.Addr())
	if err != nil {
		log.Fatalf("listen %s: %v (pick another with --port)", cfg.Addr(), err)
	}
	_, src := cfg.PortInfo()
	consolelog.Logf("INFO", "SERVER", "router-go %s listening on %s [port from %s] (data: %s)", config.Version, ln.Addr(), src, cfg.DataDir)
	start(ln)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	mu.Lock()
	srv := current
	mu.Unlock()
	_ = srv.Shutdown(ctx)
}
